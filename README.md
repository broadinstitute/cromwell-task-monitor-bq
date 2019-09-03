# Cromwell Task Monitoring with BigQuery

This repo aims to enable massively parallel collection
of Cromwell task monitoring data into BigQuery.

**Please note** that the code is still in an "**alpha**" state,
and may work incorrectly or introduce some breaking changes.

## Deployment

[![Open in Cloud Shell](https://gstatic.com/cloudssh/images/open-btn.svg)](https://console.cloud.google.com/cloudshell/editor?cloudshell_git_repo=https://github.com/broadinstitute/cromwell-task-monitor-bq&cloudshell_working_dir=deploy&cloudshell_tutorial=tutorial.md)

## Motivation

### Monitoring image

We provide a container for `monitoring_image`
[workflow option](https://cromwell.readthedocs.io/en/stable/wf_options/Google/)
used by Google Pipelines API v2 in Cromwell.

For each Pipelines operation, the monitoring container
runs in parallel to all other containers on that GCE instance.
This container collects information about
the task call attempt running on that instance.

First, it reports the _static_ information into `runtime` table in BigQuery:

| project_id | zone | instance_id | instance_name | preemptible | workflow_id  | task_call_name | shard | attempt | cpu_count | cpu_platform | mem_total_gb | disk_mounts | disk_total_gb | start_time |
| ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- |
| sample-project | us-east1-b | 1234567890123456789 | gce-instance-1234 | true | 11910a69-aaf5-428a-aae0-0b3b41ef396c | Task_Hello | 1 | 2 | 2 | Intel Haswell | 7.5 | [/cromwell_root, /mnt/disk2] | [50.5, 25.2] | 2019-01-01 01:00:00.123456 UTC |

Next, it collects runtime _metrics_ at a regular interval (e.g. 1 second)
and reports them into `metrics` table in BigQuery at another interval (e.g. 1 minute):

| timestamp | instance_id | cpu_used_percent | mem_used_gb | disk_used_gb | disk_read_iops | disk_write_iops |
| ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- |
| 2019-01-01 01:00:00.123456 UTC | 1234567890123456789 | [75.2, 25.3] | 65.3 | [20.4, 10.2] | [100.5, 50.1] | [200.6, 0.1] |

Here, distinct values are reported in nested fields for all vCPU cores and disk mounts.

The last time point is reported when the task _terminates_ (on a success, failure or preemption).

The monitoring image obtains all of the details for the tables above from the internal
[instance metadata endpoint](https://cloud.google.com/compute/docs/storing-retrieving-metadata),
standard Unix calls, and the
[environment variables](https://github.com/broadinstitute/cromwell/blob/develop/supportedBackends/google/pipelines/v2alpha1/src/main/scala/cromwell/backend/google/pipelines/v2alpha1/api/MonitoringAction.scala)
assigned to the monitoring action by Cromwell.

This approach carefully avoids making many external API calls other than
[tabledata.insertAll](https://cloud.google.com/bigquery/docs/reference/rest/v2/tabledata/insertAll),
which becomes the only rate-limiting factor.

This approach carefully avoids making external API calls other than
[streaming inserts for BigQuery](https://cloud.google.com/bigquery/streaming-data-into-bigquery),
which were designed specifically for high-throughput loading
of data. Such inserts tolerate rates as high as
[100K RPS](https://cloud.google.com/bigquery/quotas#streaming_inserts)
(6M RPM) per table/project, and are available for queries in near-realtime (typically,
[seconds](https://cloud.google.com/bigquery/streaming-data-into-bigquery#dataavailability)).

To use streaming inserts,
the Pipelines jobs have to be started with `bigquery`
[OAuth scope](https://cloud.google.com/bigquery/docs/reference/rest/v2/tabledata/insertAll#authorization-scopes),
which is implemented in [**Cromwell 43+**](https://github.com/broadinstitute/cromwell/releases/tag/43).

The BigQuery tables are [partitioned on the date](https://cloud.google.com/bigquery/docs/querying-partitioned-tables)
from the `timestamp` or `start_time` columns.
Users then query a range of dates -
today, this month, last quarter etc,
and are billed only for that range.
BigQuery allows up to 4,000
[partitions per table](https://cloud.google.com/bigquery/quotas#partitioned_tables),
i.e. one could hold up to ~11 years (!) worth of monitoring data.

### Metadata upload

Optionally, we also provide a Cloud Function that gets triggered
when a workflow completes or fails, thanks to `final_workflow_log_dir`
[workflow option](https://cromwell.readthedocs.io/en/stable/Logging/#workflow-logs)
in Cromwell.

This option specifies a GCS "directory" where, upon termination of a workflow,
Cromwell uploads a log file named `workflow.<workflow_id>.log`.
The upload "event" then triggers our Cloud Function,
which parses `workflow_id` from the log name,
and asks Cromwell for task-level workflow metadata with that `workflow_id`.

Finally, the function records this information into `metadata` table in BigQuery:

| project_id | zone | instance_name | preemptible | workflow_name | workflow_id | task_call_name | shard | attempt | start_time | end_time | execution_status | cpu_count | mem_total_gb | disk_mounts | disk_total_gb | disk_types | docker_image | inputs.key | inputs.type | inputs.value |
| ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- |
| sample-project | us-east1-b | gce-instance-1234 | true | SampleWorkflow | 11910a69-aaf5-428a-aae0-0b3b41ef396c | Task_Hello | 1 | 2 | 2019-01-01 00:01:00.123789 UTC | 2019-01-01 02:00:00.789456 UTC | Done | 2 | 7.5 | [/cromwell_root, /mnt/disk2] | [51, 25] | [HDD, SSD] | example/image@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 | [bam] | [file] | [23.5] |

Notice that some information is stored redundantly, when compared to the `runtime` table from above.
This is done intentionally, to make each table easier to query and make it useful even on its own
(in addition, some numbers may be reported differently by Cromwell and its task instances).

However, the true power of this table comes from the `inputs` structure,
which stores information about task call-level inputs, in a way that
enables building _predictive models_ for how much of each resource type
(cpu, disk, memory) we should allocate, given certain inputs.

For inputs of `file` type, it reports only the _size_ of the file in GB.
This approach enables building simple yet accurate models that could then
be used to scale WDL Task resources with formulas, based on File input sizes, e.g.
```wdl
disks: 'local-disk ~{ceil(2.2 * size(bam, 'G') + 1.1)} HDD'
```

### Cost analysis

The amount of data stored in the format described above
is miniscule (~200KB / hour, when reporting one metric row per second).

The cost of [streaming inserts](https://cloud.google.com/bigquery/pricing#streaming_pricing)
in this case would be ~$0.0002, while
incremental monthly cost of storage ~$0.000004 (or $0.000002 after 3 months),
and querying it ~$0.000001 (it may even fall within the
[free tier](https://cloud.google.com/bigquery/pricing#free-tier)).

So, even though streaming inserts appear to be the biggest
cost factor initially, they're still very cheap overall,
compared to the amount spent on compute
(~$0.01 for just 1 preemptible core-hour).
As such, we expect monitoring to add only ~2%
to the overall cost of compute.
