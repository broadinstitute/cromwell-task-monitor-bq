package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/compute/metadata"
	"google.golang.org/api/googleapi"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/mem"
)

const (
	defaultMetricsTableID = "metrics"
	defaultRuntimeTableID = "runtime"

	defaultMeasureIntervalMs = 1000
	defaultReportIntervalMs  = 60000

	defaultDiskMount = "/cromwell_root"

	envInstanceID   = "INSTANCE_ID"
	envInstanceName = "INSTANCE_NAME"
	envZone         = "ZONE"
	envPreemptible  = "PREEMPTIBLE"
	envCPUPlatform  = "CPU_PLATFORM"

	envWorkflowID      = "WORKFLOW_ID"
	envTaskCallName    = "TASK_CALL_NAME"
	envTaskCallIndex   = "TASK_CALL_INDEX"
	envTaskCallAttempt = "TASK_CALL_ATTEMPT"

	envDiskMounts = "DISK_MOUNTS"

	envMeasureIntervalMs = "MEASURE_INTERVAL_MS"
	envReportIntervalMs  = "REPORT_INTERVAL_MS"

	envProjectID      = "PROJECT_ID"
	envDatasetID      = "DATASET_ID"
	envMetricsTableID = "METRICS_TABLE_ID"
	envRuntimeTableID = "RUNTIME_TABLE_ID"
)

var (
	bq                               *bigquery.Client
	metricsInserter, runtimeInserter *bigquery.Inserter

	projectID                       string
	instanceID                      int64
	instanceName, zone, cpuPlatform string
	preemptible                     bool

	workflowID, taskCallName       bigquery.NullString
	taskCallIndex, taskCallAttempt bigquery.NullInt64
	diskMounts                     []string

	measureInterval, reportInterval time.Duration
)

// init() parses environment variables
// and initializes BigQuery client
//
func init() {
	var err error
	var ok bool

	id, ok := os.LookupEnv(envInstanceID)
	if !ok {
		if id, err = metadata.InstanceID(); err != nil {
			log.Fatal(envInstanceID, err)
		}
	}
	if instanceID, err = strconv.ParseInt(id, 10, 64); err != nil {
		log.Fatal(envInstanceID, err)
	}

	if instanceName, ok = os.LookupEnv(envInstanceName); !ok {
		if instanceName, err = metadata.InstanceName(); err != nil {
			log.Fatal(envInstanceName, err)
		}
	}

	if zone, ok = os.LookupEnv(envZone); !ok {
		if zone, err = metadata.Zone(); err != nil {
			log.Fatal(envZone, err)
		}
	}

	preempt, ok := os.LookupEnv(envPreemptible)
	if !ok {
		if preempt, err = metadata.Get("instance/scheduling/preemptible"); err != nil {
			log.Fatal(envPreemptible, err)
		}
	}
	if preemptible, err = strconv.ParseBool(preempt); err != nil {
		log.Fatal(envPreemptible, err)
	}

	if cpuPlatform, ok = os.LookupEnv(envCPUPlatform); !ok {
		if cpuPlatform, err = metadata.Get("instance/cpu-platform"); err != nil {
			log.Fatal(envCPUPlatform, err)
		}
	}

	if id, ok := os.LookupEnv(envWorkflowID); ok {
		workflowID.StringVal = id
		workflowID.Valid = ok
	}

	if name, ok := os.LookupEnv(envTaskCallName); ok {
		taskCallName.StringVal = name
		taskCallName.Valid = ok
	}

	if index, ok := os.LookupEnv(envTaskCallIndex); ok {
		var parsedIndex int64
		if index == "NA" {
			parsedIndex = -1
		} else {
			parsedIndex, err = strconv.ParseInt(index, 10, 64)
			if err != nil {
				log.Fatal(envTaskCallIndex, err)
			}
		}
		taskCallIndex.Int64 = parsedIndex
		taskCallIndex.Valid = ok
	}

	if attempt, ok := os.LookupEnv(envTaskCallAttempt); ok {
		parsedAttempt, err := strconv.ParseInt(attempt, 10, 64)
		if err != nil {
			log.Fatal(envTaskCallAttempt, err)
		}
		taskCallAttempt.Int64 = parsedAttempt
		taskCallAttempt.Valid = ok
	}

	if mounts, ok := os.LookupEnv(envDiskMounts); ok {
		diskMounts = strings.Split(mounts, " ")
	} else {
		diskMounts = []string{defaultDiskMount}
	}

	measureInterval, err = parseIntervalMs(envMeasureIntervalMs, defaultMeasureIntervalMs)
	if err != nil {
		log.Fatal(envMeasureIntervalMs, err)
	}

	reportInterval, err = parseIntervalMs(envReportIntervalMs, defaultReportIntervalMs)
	if err != nil {
		log.Fatal(envReportIntervalMs, err)
	}

	projectID, ok = os.LookupEnv(envProjectID)
	if !ok {
		if projectID, err = metadata.ProjectID(); err != nil {
			log.Fatal(envProjectID, err)
		}
	}

	datasetID, ok := os.LookupEnv(envDatasetID)
	if !ok {
		log.Fatalf("%s is not defined", envDatasetID)
	}

	metricsTableID, ok := os.LookupEnv(envMetricsTableID)
	if !ok {
		metricsTableID = defaultMetricsTableID
	}

	runtimeTableID, ok := os.LookupEnv(envRuntimeTableID)
	if !ok {
		runtimeTableID = defaultRuntimeTableID
	}

	ctx := context.Background()

	bq, err = bigquery.NewClient(ctx, projectID)
	if err != nil {
		return
	}

	runtimeInserter, err = getInserter(ctx, bq, datasetID, runtimeTableID, Runtime{}, "start_time")
	if err != nil {
		log.Fatal(err)
	}

	metricsInserter, err = getInserter(ctx, bq, datasetID, metricsTableID, Metric{}, "timestamp")
	if err != nil {
		log.Fatal(err)
	}
}

func parseIntervalMs(varName string, defaultMs int) (interval time.Duration, err error) {
	ms := defaultMs
	if env, ok := os.LookupEnv(varName); ok {
		if ms, err = strconv.Atoi(env); err != nil {
			return
		}
	}
	interval = time.Duration(ms) * time.Millisecond
	return
}

func getInserter(
	ctx context.Context,
	bq *bigquery.Client,
	datasetID string,
	tableID string,
	dataType interface{},
	partitionField string,
) (
	inserter *bigquery.Inserter,
	err error,
) {
	dataset := bq.Dataset(datasetID)
	_, err = dataset.Metadata(ctx)
	if assertNotExists(err) {
		err = dataset.Create(ctx, &bigquery.DatasetMetadata{})
	}
	if err != nil {
		return
	}

	table := dataset.Table(tableID)
	schema, err := bigquery.InferSchema(dataType)
	if err != nil {
		return
	}
	_, err = table.Metadata(ctx)
	if assertNotExists(err) {
		err = table.Create(ctx, &bigquery.TableMetadata{
			Schema: schema,
			TimePartitioning: &bigquery.TimePartitioning{
				Field:                  partitionField,
				RequirePartitionFilter: true,
			},
		})
	}
	if err != nil {
		return
	}
	inserter = table.Inserter()
	return
}

func assertNotExists(err error) (result bool) {
	if err == nil {
		return
	}
	if e, ok := err.(*googleapi.Error); ok && e.Code == http.StatusNotFound {
		result = true
	}
	return
}

// main() starts 2 goroutines:
// one to measure runtime metrics on measureInterval,
// and another to report them on reportInterval
//
// initially, it also reports general runtime attributes
//
// finally, it traps any SIGINT/SIGTERM
// and exits after startReport() submits the final report
//
func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	measurements := make(chan *Measurement, 2*reportInterval/measureInterval)
	errs := make(chan error, 1)

	ctx := context.Background()
	defer bq.Close()

	if err := runtime(ctx); err != nil {
		log.Fatal(err)
	}

	go func() {
		if err := startMeasure(signals, measurements); err != nil {
			errs <- err
		}
	}()

	go func() {
		errs <- startReport(ctx, measurements)
	}()

	for err := range errs {
		if err == nil {
			os.Exit(0)
		} else {
			log.Fatal(err)
		}
	}
}

func runtime(ctx context.Context) (
	err error,
) {
	cpuCount, err := cpu.Counts(true)
	if err != nil {
		return
	}
	memStat, err := mem.VirtualMemory()
	if err != nil {
		return
	}
	diskTotal := make([]float64, len(diskMounts))
	for i, path := range diskMounts {
		stat, err := disk.Usage(path)
		if err != nil {
			return err
		}
		diskTotal[i] = toGB(stat.Total)
	}
	r := &Runtime{
		ProjectID:    projectID,
		Zone:         zone,
		InstanceID:   instanceID,
		InstanceName: instanceName,
		Preemptible:  preemptible,
		WorkflowID:   workflowID,
		TaskCallName: taskCallName,
		Shard:        taskCallIndex,
		Attempt:      taskCallAttempt,
		CPUCount:     cpuCount,
		CPUPlatform:  cpuPlatform,
		MemTotalGB:   toGB(memStat.Total),
		DiskMounts:   diskMounts,
		DiskTotalGB:  diskTotal,
		StartTime:    time.Now(),
	}
	return runtimeInserter.Put(ctx, []*Runtime{r})
}

func toGB(bytes uint64) float64 {
	return float64(bytes) * 1E-9
}

func startMeasure(
	signals chan os.Signal,
	measurements chan<- *Measurement,
) (
	err error,
) {
	disks, err := getDisks()
	if err != nil {
		return
	}

	var m *Measurement
	for {
		select {
		case <-time.After(measureInterval):
		case sig := <-signals:
			log.Println("Received signal:", sig)
			close(measurements)
			return
		}
		m, err = measure(disks)
		if err != nil {
			return
		}
		measurements <- m
	}
}

func getDisks() (disks []string, err error) {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return
	}
	mounts := map[string]string{}
	for _, p := range partitions {
		mounts[p.Mountpoint] = p.Device
	}
	disks = make([]string, len(diskMounts))
	for i, mount := range diskMounts {
		disks[i] = mounts[mount]
	}
	return
}

func measure(disks []string) (m *Measurement, err error) {
	// CPU
	cpuUsed, err := cpu.Percent(0, true)
	if err != nil {
		return
	}

	// Memory
	memStat, err := mem.VirtualMemory()
	if err != nil {
		return
	}

	// Disks
	nDisks := len(diskMounts)
	diskUsed := make([]uint64, nDisks)
	diskReads := make([]uint64, nDisks)
	diskWrites := make([]uint64, nDisks)

	for i, path := range diskMounts {
		stat, err := disk.Usage(path)
		if err != nil {
			return nil, err
		}
		diskUsed[i] = stat.Used
	}
	diskIO, err := disk.IOCounters(disks...)
	if err != nil {
		return
	}
	for i, d := range disks {
		stat := diskIO[d]
		diskReads[i] = stat.ReadCount
		diskWrites[i] = stat.WriteCount
	}

	m = &Measurement{
		time.Now(), cpuUsed, memStat.Used,
		diskUsed, diskReads, diskWrites,
	}
	return
}

// Measurement contains monitoring stats for a single time point
type Measurement struct {
	Time           time.Time
	CPUUsedPercent []float64
	MemUsedBytes   uint64
	DiskUsedBytes  []uint64
	DiskReadCount  []uint64
	DiskWriteCount []uint64
}

func startReport(
	ctx context.Context,
	measurements <-chan *Measurement,
) (
	err error,
) {
	points := []*Measurement{}
	end := time.Now().Add(reportInterval)

	var last *Measurement

	for m := range measurements {
		points = append(points, m)
		now := time.Now()
		if now.After(end) {
			err = report(ctx, points, last)
			if err != nil {
				return
			}
			end = now.Add(reportInterval)
			last = points[len(points)-1]
			points = []*Measurement{}
		}
	}
	err = report(ctx, points, last)
	return
}

func report(
	ctx context.Context,
	points []*Measurement,
	last *Measurement,
) (
	err error,
) {
	nPoints := len(points)
	if nPoints == 0 {
		return
	}

	metrics := make([]*Metric, nPoints)

	for i, p := range points {
		m := &Metric{
			Timestamp:  p.Time,
			InstanceID: instanceID,
		}

		m.CPUUsedPercent = make([]float64, len(p.CPUUsedPercent))
		for cpu, used := range p.CPUUsedPercent {
			m.CPUUsedPercent[cpu] = used
		}

		m.MemUsedGB = toGB(p.MemUsedBytes)

		nDisks := len(p.DiskUsedBytes)
		m.DiskUsedGB = make([]float64, nDisks)
		m.DiskReadIOPS = make([]float64, nDisks)
		m.DiskWriteIOPS = make([]float64, nDisks)

		for disk, usedBytes := range p.DiskUsedBytes {
			m.DiskUsedGB[disk] = toGB(usedBytes)
			if last != nil {
				dt := p.Time.Sub(last.Time).Seconds()
				m.DiskReadIOPS[disk] = float64(p.DiskReadCount[disk]-last.DiskReadCount[disk]) / dt
				m.DiskWriteIOPS[disk] = float64(p.DiskWriteCount[disk]-last.DiskWriteCount[disk]) / dt
			}
		}

		metrics[i] = m
		last = p
	}
	if err != nil {
		return
	}

	return metricsInserter.Put(ctx, metrics)
}

// Runtime contains a row of runtime attributes in BigQuery
type Runtime struct {
	ProjectID    string              `bigquery:"project_id"`
	Zone         string              `bigquery:"zone"`
	InstanceID   int64               `bigquery:"instance_id"`
	InstanceName string              `bigquery:"instance_name"`
	Preemptible  bool                `bigquery:"preemptible"`
	WorkflowID   bigquery.NullString `bigquery:"workflow_id"`
	TaskCallName bigquery.NullString `bigquery:"task_call_name"`
	Shard        bigquery.NullInt64  `bigquery:"shard"`
	Attempt      bigquery.NullInt64  `bigquery:"attempt"`
	CPUCount     int                 `bigquery:"cpu_count"`
	CPUPlatform  string              `bigquery:"cpu_platform"`
	MemTotalGB   float64             `bigquery:"mem_total_gb"`
	DiskMounts   []string            `bigquery:"disk_mounts"`
	DiskTotalGB  []float64           `bigquery:"disk_total_gb"`
	StartTime    time.Time           `bigquery:"start_time"`
}

// Metric contains a single row of stats reported to BigQuery
type Metric struct {
	Timestamp      time.Time `bigquery:"timestamp"`
	InstanceID     int64     `bigquery:"instance_id"`
	CPUUsedPercent []float64 `bigquery:"cpu_used_percent"`
	MemUsedGB      float64   `bigquery:"mem_used_gb"`
	DiskUsedGB     []float64 `bigquery:"disk_used_gb"`
	DiskReadIOPS   []float64 `bigquery:"disk_read_iops"`
	DiskWriteIOPS  []float64 `bigquery:"disk_write_iops"`
}
