package logs

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/functions/metadata"
	"cloud.google.com/go/storage"
)

var (
	projectID = os.Getenv("PROJECT_ID")
	datasetID = os.Getenv("DATASET_ID")
	tableID   = os.Getenv("TABLE_ID")
)

var storageClient *storage.Client
var bqInserter *bigquery.Inserter

func init() {
	ctx := context.Background()
	var err error

	storageClient, err = storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	bqInserter, err = GetInserter(ctx)
	if err != nil {
		log.Fatal(err)
	}
}

// StorageEvent is the payload of a Google Cloud Storage event.
type StorageEvent struct {
	Bucket   string          `json:"bucket"`
	Path     string          `json:"name"`
	Metadata StorageMetadata `json:"metadata"`
}

// StorageMetadata contains custom metadata for the Storage event
type StorageMetadata struct {
	Type LogType `json:"type"`
}

// LogType indicates the type of the log file
type LogType string

// enum for LogType
const (
	EPI LogType = "EPI"
	HCA LogType = "HCA"
)

// Upload receives a Storage event for monitoring.log,
// fetches the log, parses it, and uploads results to BigQuery.
func Upload(ctx context.Context, event StorageEvent) (err error) {
	fmt.Printf("%+v", event)

	call, err := ParseEvent(ctx, &event)
	if err != nil || call == nil {
		return
	}

	log, err := FetchLog(ctx, event.Bucket, event.Path)
	if err != nil {
		return
	}

	data, err := ParseLog(call, log)
	if err != nil {
		return
	}

	return UploadRows(ctx, data)
}

func parseRe(re *regexp.Regexp, s string) (matches map[string]string) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return
	}
	matches = map[string]string{}
	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" {
			matches[name] = m[i]
		}
	}
	return
}

var pathRe = regexp.MustCompile(
	`^(.*/)?(?P<workflowName>\w+)/(?P<workflowID>[0-9a-f\-]{36})/call-(?P<callName>\w+)(/shard-(?P<shard>\d+))?(/.*attempt-(?P<attempt>\d+))?/monitoring.log$`,
)

// TaskCall holds the parsed parameters of a Cromwell task call
type TaskCall struct {
	logType      LogType
	workflowName string
	workflowID   string
	name         string
	shard        int
	attempt      int
}

const (
	objFinalize   = "google.storage.object.finalize"
	objMetaUpdate = "google.storage.object.metadataUpdate"
)

// ParseEvent parses StorageEvent into TaskCall struct
func ParseEvent(
	ctx context.Context,
	event *StorageEvent,
) (
	call *TaskCall,
	err error,
) {
	meta, err := metadata.FromContext(ctx)
	if err != nil {
		return
	}
	switch meta.EventType {
	case objFinalize, objMetaUpdate:
	default:
		return
	}

	logType := event.Metadata.Type
	switch logType {
	case EPI, HCA:
	default:
		err = fmt.Errorf("Unknown log type: '%s'", logType)
		return
	}

	matches := parseRe(pathRe, event.Path)
	if matches == nil {
		err = fmt.Errorf("%s is not a valid path to monitoring.log", event.Path)
		return
	}

	call = &TaskCall{
		logType:      event.Metadata.Type,
		workflowName: matches["workflowName"],
		workflowID:   matches["workflowID"],
		name:         matches["callName"],
		shard:        -1,
		attempt:      1,
	}

	shard := matches["shard"]
	if len(shard) > 0 {
		call.shard, err = strconv.Atoi(shard)
		if err != nil {
			return
		}
	}

	attempt := matches["attempt"]
	if len(attempt) > 0 {
		call.attempt, err = strconv.Atoi(attempt)
	}

	return
}

// FetchLog reads the monitoring log from Cloud Storage
func FetchLog(
	ctx context.Context,
	bucket string,
	object string,
) (
	log string,
	err error,
) {
	reader, err := storageClient.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return
	}
	defer reader.Close()
	if bytes, err := ioutil.ReadAll(reader); err == nil {
		log = string(bytes)
	}
	return
}

const timeFormat = "Mon Jan 2 15:04:05 UTC 2006"

func parseTotal(matches map[string]string, key string) float64 {
	total, err := strconv.ParseFloat(matches[key], 64)
	if err != nil {
		fmt.Printf("Unable to parse total %s: %s\n", key, matches[key])
	}
	return total
}

// ParseLog parses data from the monitoring log
func ParseLog(
	call *TaskCall,
	log string,
) (
	data []*MonitorPoint,
	err error,
) {
	// Parse the header first

	header := strings.Split(log, "Runtime Information")[0]
	headerRe := regexp.MustCompile(
		`(?s).*` +
			`#CPU:(\s(?P<cpu>\d+))?\n` +
			`Total Memory:(\s(?P<mem>\d+(\.\d+)?)(?P<memUnit>[MG]))?\n` +
			`Total Disk space:(\s(?P<disk>\d+(\.\d+)?)(?P<diskUnit>[MG]))?`,
	)
	matches := parseRe(headerRe, header)
	if matches == nil {
		err = fmt.Errorf("Unable to parse header: %s", header)
		return
	}
	totalCPU := parseTotal(matches, "cpu")
	totalMem := parseTotal(matches, "mem")
	if matches["memUnit"] == "M" {
		totalMem /= 1000
	}
	totalDisk := parseTotal(matches, "disk")
	if matches["diskUnit"] == "M" {
		totalDisk /= 1000
	}

	// Parse the timepoints

	delims := map[LogType]string{
		EPI: "\n",
		HCA: "[",
	}
	delim := delims[call.logType]
	lines := strings.Split(log, delim)

	regexes := map[LogType]string{
		EPI: `^(?P<cpu>\d+(\.\d+)?)\s+(?P<mem>\d+(\.\d+)?)\s+(?P<disk>\d+(\.\d+)?)(?P<diskUnit>[MG])\s+(?P<time>.*)$`,
		HCA: `^(?P<time>[^\]]+)\]\n.*\s(?P<cpu>\d+(\.\d+)?)%\n.*\s(?P<mem>\d+(\.\d+)?)%\n.*\s(?P<disk>\d+(\.\d+)?)(?P<diskUnit>%)\n$`,
	}
	re := regexp.MustCompile(regexes[call.logType])

	for _, line := range lines[1:] {

		var t time.Time
		var cpu, mem, disk float64

		matches := parseRe(re, line)
		if matches == nil {
			continue
		}

		t, err = time.Parse(timeFormat, matches["time"])
		timestamp := bigquery.NullTimestamp{
			Timestamp: t,
			Valid:     err == nil,
		}

		cpu, _ = strconv.ParseFloat(matches["cpu"], 64)
		cpu *= totalCPU / 100
		cpuFloat := bigquery.NullFloat64{
			Float64: cpu,
			Valid:   cpu > 0,
		}

		mem, _ = strconv.ParseFloat(matches["mem"], 64)
		mem *= totalMem / 100
		memFloat := bigquery.NullFloat64{
			Float64: mem,
			Valid:   mem > 0,
		}

		disk, _ = strconv.ParseFloat(matches["disk"], 64)
		switch matches["diskUnit"] {
		case "G":
		case "M":
			disk /= 1000
		case "%":
			disk *= totalDisk / 100
		}
		diskFloat := bigquery.NullFloat64{
			Float64: disk,
			Valid:   disk > 0,
		}

		data = append(data, &MonitorPoint{
			call.workflowName, call.workflowID,
			call.name, call.shard, call.attempt,
			cpuFloat, "#", memFloat, "G", diskFloat, "G",
			timestamp,
		})
	}
	return
}

// MonitorPoint contains resource monitoring data for a single time point
type MonitorPoint struct {
	WorkflowName string                 `bigquery:"workflow_name"`
	WorkflowID   string                 `bigquery:"workflow_id"`
	CallName     string                 `bigquery:"task_call_name"`
	Shard        int                    `bigquery:"shard"`
	Attempt      int                    `bigquery:"attempt"`
	CPU          bigquery.NullFloat64   `bigquery:"cpu"`
	CPUUnit      string                 `bigquery:"cpu_unit"`
	Mem          bigquery.NullFloat64   `bigquery:"mem"`
	MemUnit      string                 `bigquery:"mem_unit"`
	Disk         bigquery.NullFloat64   `bigquery:"disk"`
	DiskUnit     string                 `bigquery:"disk_unit"`
	Timestamp    bigquery.NullTimestamp `bigquery:"timestamp"`
}

// GetInserter configures BigQuery table and inserter
func GetInserter(
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
	schema, err := bigquery.InferSchema(MonitorPoint{})
	if err != nil {
		return
	}
	table.Create(ctx, &bigquery.TableMetadata{
		Schema: schema,
	})
	inserter = bq.Dataset(datasetID).Table(tableID).Inserter()
	return
}

const maxRows = 10000

// UploadRows uploads data points to BigQuery
func UploadRows(
	ctx context.Context,
	data []*MonitorPoint,
) (
	err error,
) {
	for maxRows < len(data) {
		batch := data[:maxRows]
		data = data[maxRows:]
		err = bqInserter.Put(ctx, batch)
		if err != nil {
			return
		}
	}
	return bqInserter.Put(ctx, data)
}
