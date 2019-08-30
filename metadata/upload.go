package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	gcpMetadata "cloud.google.com/go/compute/metadata"
	funcMetadata "cloud.google.com/go/functions/metadata"
	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

const tokenLifetimeSec = 3000

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

func init() {
	httpClient = &http.Client{}
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
	}
	workflow, err := getWorkflow(id)
	if err != nil {
		return
	}
	rows, err := parseRows(ctx, workflow)
	if err != nil {
		return
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
	call := reflect.TypeOf(Call{})
	for i := 0; i < call.NumField(); i++ {
		q.Add("includeKey", call.Field(i).Tag.Get("json"))
	}
	q.Add("includeKey", "workflowName")
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
	Preemptible       bool   `json:"preemptible"`
	Shard             int    `json:"shardIndex"`
	Attempt           int    `json:"attempt"`
	ExecutionStatus   string `json:"executionStatus"`
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

	token = t.AccessToken
	SetCachedToken(token)
	return
}

// SetCachedToken sets up the token cache
func SetCachedToken(token string) {
	cachedToken.Value = token
	cachedToken.ExpiresAt = time.Now().Add(time.Second * tokenLifetimeSec)
}

// Token respresents response from instance metadata endpoint
// for service-accounts/default/token key
type Token struct {
	AccessToken string `json:"access_token"`
}

// CachedToken contains cached access token data
type CachedToken struct {
	Value     string
	ExpiresAt time.Time
}

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
		for _, call := range calls {
			if call.SubWorkflow.ID == "" {
				if len(matches[2]) == 0 {
					err = fmt.Errorf("Unable to parse task call name: '%s'", name)
					return
				}

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

				files := []*BQInput{}
				nonFiles := []*BQInput{}

				sizedFiles := make(chan *BQInput, len(inputs))
				errs := make(chan error, 1)

				for _, input := range inputs {
					if input.Type == "file" {
						go func(file BQInput) {
							f := &file.Value
							size, err := getGcsSize(ctx, f.StringVal)
							if err != nil {
								if getStatus(err) == http.StatusForbidden {
									fmt.Println(err)
									f.Valid = false
								} else if strings.Contains(err.Error(), "doesn't exist") {
									f.Valid = false
								} else {
									errs <- fmt.Errorf("%s: %s", f.StringVal, err.Error())
									return
								}
							}
							f.StringVal = fmt.Sprintf("%f", size)
							sizedFiles <- &file
						}(*input)
					} else {
						nonFiles = append(nonFiles, input)
					}
				}

				nFiles := len(inputs) - len(nonFiles)
				for len(files) < nFiles {
					select {
					case file := <-sizedFiles:
						files = append(files, file)
					case err = <-errs:
						return
					}
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
					CallName:        matches[2],
					Shard:           call.Shard,
					Attempt:         call.Attempt,
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
	err = table.Create(ctx, &bigquery.TableMetadata{
		Schema: schema,
	})
	if err != nil {
		if getStatus(err) != http.StatusConflict {
			return
		}
		err = nil
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
