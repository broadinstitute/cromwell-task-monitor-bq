package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	gcpMetadata "cloud.google.com/go/compute/metadata"
	funcMetadata "cloud.google.com/go/functions/metadata"
	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

const minTokenLifetimeSec = 60

var (
	cromwellBaseURL = os.Getenv("CROMWELL_BASEURL")
	projectID       = os.Getenv("GCP_PROJECT")
	datasetID       = os.Getenv("DATASET_ID")
	tableID         = getEnv("TABLE_ID", "metadata")
)

var httpClient *http.Client
var metaClient *gcpMetadata.Client
var storageClient *storage.Client
var bqInserter *bigquery.Inserter
var cachedToken CachedToken
var gcsFetchBufferSize int

func init() {
	httpClient = &http.Client{Timeout: 20 * time.Second,}
	metaClient = gcpMetadata.NewClient(httpClient)

	var err error
	ctx := context.Background()

	storageClient, err = storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	bqInserter, err = getInserter(ctx)
	if err != nil {
		log.Fatal(err)
	}

	gcsFetchBufferSize, err = strconv.Atoi(getEnv("GCS_QUERY_THROTTLE", "500"))
	if err != nil {
		log.Fatal(err)
	}
}

func getEnv(key, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

// Upload is a Cloud Function that triggers on a Storage Object event
// for Cromwell output log, parses workflow ID from the object name,
// queries Cromwell metadata for that workflow ID,
// and loads monitoring-related metadata into BigQuery
func Upload(ctx context.Context, event StorageEvent) (err error) {
	err = checkEventType(ctx)
	if err != nil {
		return
	}
	id, err := parseWorkflowID(event.Path)
	if err != nil {
		return
	} else {
		fmt.Fprintf(os.Stderr, "Workflow ID %s parsed.\n", id)
	}
	workflow, err := getWorkflow(id)
	if err != nil {
		return
	} else {
		fmt.Fprintln(os.Stderr, "API call to cromwell server finished.")
	}
	rows, err := parseRows(ctx, workflow)
	if err != nil {
		return
	} else {
		fmt.Fprintln(os.Stderr, "All metadata have been parsed.")
	}
	return uploadRows(ctx, rows)
}

// StorageEvent is the payload of a Google Cloud Storage event.
type StorageEvent struct {
	Path string `json:"name"`
}

const (
	objFinalize   = "google.storage.object.finalize"
	objMetaUpdate = "google.storage.object.metadataUpdate"
)

func checkEventType(ctx context.Context) (err error) {
	m, err := funcMetadata.FromContext(ctx)
	if err != nil {
		return
	}
	switch m.EventType {
	case objFinalize, objMetaUpdate:
	default:
		err = fmt.Errorf("Skipping %s event", m.EventType)
	}
	return
}

var pathRe = regexp.MustCompile(`\.([a-f0-9-]{36})\.`)

func parseWorkflowID(path string) (id string, err error) {
	matches := pathRe.FindStringSubmatch(path)
	if matches == nil {
		err = fmt.Errorf("Unknown path pattern: '%s'", path)
		return
	}
	id = matches[1]
	return
}

func getWorkflow(id string) (workflow *Workflow, err error) {
	token, err := getAccessToken()
	if err != nil {
		return
	}

	uri := fmt.Sprintf("%s/api/workflows/v1/%s/metadata", cromwellBaseURL, id)
	req, err := http.NewRequest("GET", uri, nil)

	req.Header.Add("Authorization", "Bearer "+token)

	q := req.URL.Query()
	for _, key := range excludeKeys {
		q.Add("excludeKey", key)
	}
	// TODO Use includeKey instead of excludeKey
	// when Cromwell fixes a bug for combining
	// expandSubWorkflows with includeKey:
	//
	// call := reflect.TypeOf(Call{})
	// for i := 0; i < call.NumField(); i++ {
	// 	q.Add("includeKey", call.Field(i).Tag.Get("json"))
	// }
	// q.Add("includeKey", "workflowName")
	//
	q.Add("expandSubWorkflows", "true")
	req.URL.RawQuery = q.Encode()

	res, err := httpClient.Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(res.Body)
		err = fmt.Errorf(
			"Request for workflow %s failed with %s: %s", id, res.Status, body,
		)
		return
	}

	workflow = &Workflow{}
	err = json.NewDecoder(res.Body).Decode(workflow)
	return
}

var excludeKeys = []string{
	"submittedFiles", "workflowProcessingEvents", "executionEvents",
	"workflowRoot", "callRoot", "callCaching", "outputs",
	"commandLine", "failures", "returnCode", "stderr", "stdout",
	"monitoringLog", "backend", "compressedDockerSize", "labels", "jobId",
}

// Workflow represents partial response to
// GET /api/workflows/v1/{id}/metadata call
type Workflow struct {
	ID    string `json:"id"`
	Name  string `json:"workflowName"`
	Calls `json:"calls"`
}

// Calls contains metadata for Call structs
type Calls map[string][]Call

// Call contains metadata for
// a Cromwell task call or subworkflow
type Call struct {
	Jes struct {
		GoogleProject string `json:"googleProject"`
		Zone          string `json:"zone"`
		InstanceName  string `json:"instanceName"`
		MachineType   string `json:"machineType"`
	} `json:"jes"`
	Preemptible       bool      `json:"preemptible"`
	Shard             int       `json:"shardIndex"`
	Attempt           int       `json:"attempt"`
	Start             time.Time `json:"start"`
	End               time.Time `json:"end"`
	ExecutionStatus   string    `json:"executionStatus"`
	RuntimeAttributes struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
		Disks  string `json:"disks"`
	} `json:"runtimeAttributes"`
	DockerImageUsed string `json:"dockerImageUsed"`
	Inputs          `json:"inputs"`
	SubWorkflow     Workflow `json:"subWorkflowMetadata"`
}

// Inputs contains task/subworkflow inputs
type Inputs map[string]interface{}

func getAccessToken() (token string, err error) {
	if cachedToken.ExpiresAt.Sub(time.Now()) > 0 {
		token = cachedToken.Value
		return
	}
	scopes := []string{
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
	}
	res, err := metaClient.Get(fmt.Sprintf(
		"instance/service-accounts/default/token?scopes=%s",
		strings.Join(scopes, ","),
	))
	if err != nil {
		return
	}
	var t Token
	err = json.Unmarshal([]byte(res), &t)
	if err != nil {
		return
	}
	token = SetCachedToken(&t)
	return
}

// SetCachedToken sets up the token cache
func SetCachedToken(t *Token) (token string) {
	token = t.AccessToken
	cachedToken.Value = token
	cachedToken.ExpiresAt = time.Now().Add(
		time.Second * time.Duration(t.ExpiresIn-minTokenLifetimeSec),
	)
	return
}

// Token respresents response from instance metadata endpoint
// for service-accounts/default/token key
type Token struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// CachedToken contains cached access token data
type CachedToken struct {
	Value     string
	ExpiresAt time.Time
}

// ######################################## //
// block to implement buffered query into GCS
type GcsQueryJob struct {
	id    int
	file *BQInput
}
type GcsQueryResult struct {
	err   error
	file *BQInput
}
func processGCSQuery(ctx context.Context, wg *sync.WaitGroup,
					 jobs chan GcsQueryJob, results chan GcsQueryResult) {
    for job := range jobs {
		file := job.file
		var e error
		if (file.Type == "file") {
			f := &file.Value
			size, err := getGcsSize(ctx, f.StringVal)
			if err == nil {
				f.StringVal = fmt.Sprintf("%f", size)
			} else {
				if getStatus(err) == http.StatusForbidden {
					f.Valid = false
					fmt.Fprintf(os.Stderr, "Cannot access: %s,\n  for reason: %s\n",
								f.StringVal, err.Error())
				} else if strings.Contains(err.Error(), "doesn't exist") {
					f.Valid = false
					fmt.Fprintf(os.Stderr, "%s: %s\n", err.Error(), f.StringVal)
				} else {
					e = err
					return
				}
			}
		}
		res := GcsQueryResult{e, file}
		results <- res
    }
    wg.Done()
}
func createWorkerPool(ctx context.Context,
					  numOfWorkers int,
					  callName string, attempt int, shard int,
					  jobs chan GcsQueryJob, results chan GcsQueryResult) {
    var wg sync.WaitGroup
    for i := 0; i < numOfWorkers; i++ {
        wg.Add(1)
        go processGCSQuery(ctx, &wg, jobs, results)
    }
    wg.Wait()
    close(results)
}
// get the files from the result channel, stop if there's any error
func collectGcsQueryResults(results chan GcsQueryResult,
							filesChan chan *BQInput,
							done chan bool) {
	for res := range results {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "Error on GCS path: %s,\n  for reason: %s\n",
						res.file.Value.StringVal, res.err.Error())
			return
		} else if res.file.Type == "file" {
			filesChan <- res.file
		}
	}
	done <- true
}
// ######################################## //

func parseRows(
	ctx context.Context,
	workflow *Workflow,
) (
	rows []*Row,
	err error,
) {
	for name, calls := range workflow.Calls {
		matches := callRe.FindStringSubmatch(name)
		if matches == nil {
			err = fmt.Errorf("Unable to parse call name: '%s'", name)
			return
		}
		callName := matches[2]
		if len(callName) == 0 {
			callName = matches[0]
		}
		for _, call := range calls {
			if call.SubWorkflow.ID == "" {
				var runtime *Runtime
				runtime, err = parseRuntime(&call)
				if err != nil {
					return
				} else if runtime == nil {
					continue
				}

				var kInputs, inputs []*BQInput
				for k, v := range call.Inputs {
					kInputs, err = parseInputs(ctx, &v, []string{k})
					if err != nil {
						return
					}
					inputs = append(inputs, kInputs...)
				}

				nonFiles := []*BQInput{}
				// create & stuff a channel holding jobs
				fileCnt := 0
				var jobs = make(chan GcsQueryJob, len(inputs))
				for i, input := range inputs {
					if (input.Type == "file") {
						fileCnt++
						job := GcsQueryJob{i, input}
						jobs <- job
					} else {
						nonFiles = append(nonFiles, input)
					}
				}
				close(jobs)

				var filesChan = make(chan *BQInput, len(inputs))
				done := make(chan bool)
				var results = make(chan GcsQueryResult, len(inputs))
				createWorkerPool(ctx, gcsFetchBufferSize, callName, call.Attempt, call.Shard, jobs, results)
				go collectGcsQueryResults(results, filesChan, done)
				<- done
				close(filesChan)

				files := []*BQInput{}
				for f := range filesChan {
					files = append(files, f)
				}

				inputs = append(files, nonFiles...)
				sort.Slice(inputs, func(i, j int) bool {
					return inputs[i].Key < inputs[j].Key
				})

				row := &Row{
					ProjectID:       call.Jes.GoogleProject,
					Zone:            call.Jes.Zone,
					InstanceName:    call.Jes.InstanceName,
					Preemptible:     call.Preemptible,
					WorkflowName:    workflow.Name,
					WorkflowID:      workflow.ID,
					CallName:        callName,
					Shard:           call.Shard,
					Attempt:         call.Attempt,
					StartTime:       call.Start,
					EndTime:         call.End,
					ExecutionStatus: call.ExecutionStatus,
					CPUCount:        runtime.CPUCount,
					MemTotalGB:      runtime.MemTotalGB,
					DiskMounts:      runtime.DiskMounts,
					DiskTotalGB:     runtime.DiskTotalGB,
					DiskTypes:       runtime.DiskTypes,
					DockerImage:     call.DockerImageUsed,
					Inputs:          inputs,
				}
				rows = append(rows, row)
			} else {
				var subRows []*Row
				subRows, err = parseRows(ctx, &call.SubWorkflow)
				if err != nil {
					return
				}
				rows = append(rows, subRows...)
			}
		}
	}
	return
}

var callRe = regexp.MustCompile(`^\w+(\.(\w+))?$`)

// Row is table row to be uploaded to BigQuery
type Row struct {
	ProjectID       string     `bigquery:"project_id"`
	Zone            string     `bigquery:"zone"`
	InstanceName    string     `bigquery:"instance_name"`
	Preemptible     bool       `bigquery:"preemptible"`
	WorkflowName    string     `bigquery:"workflow_name"`
	WorkflowID      string     `bigquery:"workflow_id"`
	CallName        string     `bigquery:"task_call_name"`
	Shard           int        `bigquery:"shard"`
	Attempt         int        `bigquery:"attempt"`
	StartTime       time.Time  `bigquery:"start_time"`
	EndTime         time.Time  `bigquery:"end_time"`
	ExecutionStatus string     `bigquery:"execution_status"`
	CPUCount        int        `bigquery:"cpu_count"`
	MemTotalGB      float64    `bigquery:"mem_total_gb"`
	DiskMounts      []string   `bigquery:"disk_mounts"`
	DiskTotalGB     []float64  `bigquery:"disk_total_gb"`
	DiskTypes       []string   `bigquery:"disk_types"`
	DockerImage     string     `bigquery:"docker_image"`
	Inputs          []*BQInput `bigquery:"inputs"`
}

// BQInput represents a single input, to be stored in BigQuery
type BQInput struct {
	Key   string              `bigquery:"key"`
	Type  string              `bigquery:"type"`
	Value bigquery.NullString `bigquery:"value"`
}

func parseRuntime(call *Call) (r *Runtime, err error) {
	machineType := call.Jes.MachineType
	if machineType == "" {
		return
	}
	var (
		cpu int
		mem float64
	)
	runtime := call.RuntimeAttributes
	machine := strings.Split(machineType, "-")
	if machine[0] == "custom" {
		cpu, err = strconv.Atoi(machine[1])
		if err != nil {
			return
		}
		mem, err = strconv.ParseFloat(machine[2], 64)
		if err != nil {
			return
		}
		mem /= 1024
	} else {
		cpu, err = strconv.Atoi(runtime.CPU)
		if err != nil {
			return
		}
		memory := strings.Split(runtime.Memory, " ")
		mem, err = strconv.ParseFloat(memory[0], 64)
		if err != nil {
			return
		}
	}

	disks := strings.Split(runtime.Disks, ", ")
	nDisks := len(disks)
	diskMounts := make([]string, nDisks)
	diskTotalGB := make([]float64, nDisks)
	diskTypes := make([]string, nDisks)

	for i, disk := range disks {
		d := strings.Split(disk, " ")
		if len(d) < 3 {
			return nil, fmt.Errorf("Unable to parse disk metadata from %s", disk)
		}

		diskType := d[2]
		diskTypes[i] = diskType

		diskMount := d[0]
		if diskMount == "local-disk" {
			diskMount = "/cromwell_root"
		}
		diskMounts[i] = diskMount

		diskSizeGB, err := strconv.ParseFloat(d[1], 64)
		if err != nil {
			return nil, err
		} else if diskType == "LOCAL" {
			diskSizeGB = 375
		}
		diskTotalGB[i] = diskSizeGB
	}

	r = &Runtime{cpu, mem, diskMounts, diskTotalGB, diskTypes}
	return
}

// Runtime represents task call runtime attributes
type Runtime struct {
	CPUCount    int
	MemTotalGB  float64
	DiskMounts  []string
	DiskTotalGB []float64
	DiskTypes   []string
}

func parseInputs(
	ctx context.Context,
	val *interface{},
	keys []string,
) (
	bqInputs []*BQInput,
	err error,
) {
	var (
		t      string
		value  string
		inputs []*BQInput
	)

	switch v := (*val).(type) {
	case nil:
		t = "null"
	case bool:
		t = "boolean"
		value = fmt.Sprintf("%t", v)
	case float64:
		t = "number"
		value = fmt.Sprintf("%f", v)
	case string:
		if isFile(v) {
			t = "file"
		} else {
			t = "string"
		}
		value = v
	case []interface{}:
		for i, vv := range v {
			k := fmt.Sprintf("[%d]", i)
			inputs, err = parseInputs(ctx, &vv, append(keys, k))
			bqInputs = append(bqInputs, inputs...)
		}
	case map[string]interface{}:
		for k, vv := range v {
			k = fmt.Sprintf("[\"%s\"]", k)
			inputs, err = parseInputs(ctx, &vv, append(keys, k))
			bqInputs = append(bqInputs, inputs...)
		}
	default:
		err = fmt.Errorf("Unknown type: %s", t)
	}
	if t == "" {
		return
	}
	bqInputs = append(bqInputs, &BQInput{
		Key:  strings.Join(keys, ""),
		Type: t,
		Value: bigquery.NullString{
			StringVal: value,
			Valid:     t != "null",
		},
	})
	return
}

var gcsRe = regexp.MustCompile(`^gs://([^/]+)/(.+)$`)

func isFile(uri string) bool {
	return gcsRe.Match([]byte(uri))
}

func getGcsSize(
	ctx context.Context,
	uri string,
) (
	sizeGB float64,
	err error,
) {
	matches := gcsRe.FindStringSubmatch(uri)
	bucket, object := matches[1], matches[2]
	attrs, err := storageClient.Bucket(bucket).Object(object).Attrs(ctx)
	if err != nil {
		return
	}
	sizeGB = float64(attrs.Size) * 1E-9
	return
}

func getInserter(
	ctx context.Context,
) (
	inserter *bigquery.Inserter,
	err error,
) {
	bq, err := bigquery.NewClient(ctx, projectID)
	if err != nil {
		return
	}
	table := bq.Dataset(datasetID).Table(tableID)
	schema, err := bigquery.InferSchema(Row{})
	if err != nil {
		return
	}
	_, err = table.Metadata(ctx)
	if getStatus(err) == http.StatusNotFound {
		err = table.Create(ctx, &bigquery.TableMetadata{
			Schema: schema,
			TimePartitioning: &bigquery.TimePartitioning{
				Field:                  "start_time",
				RequirePartitionFilter: true,
			},
		})
	}
	inserter = bq.Dataset(datasetID).Table(tableID).Inserter()
	return
}

func getStatus(err error) (status int) {
	if e, ok := err.(*googleapi.Error); ok {
		status = e.Code
	}
	return
}

const maxRows = 10000

func uploadRows(
	ctx context.Context,
	rows []*Row,
) (
	err error,
) {
	for maxRows < len(rows) {
		batch := rows[:maxRows]
		rows = rows[maxRows:]
		err = bqInserter.Put(ctx, batch)
		if err != nil {
			return
		}
	}
	return bqInserter.Put(ctx, rows)
}
