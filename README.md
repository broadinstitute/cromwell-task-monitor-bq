# Bulk Cromwell Task Monitoring with BigQuery

This repo aims to enable bulk collection
of Cromwell task monitoring data into BigQuery.

**Please note** that any code is not yet
ready to be used in production.

## Monitoring image

This repo will provide code for `monitoring_image`
[workflow option](https://cromwell.readthedocs.io/en/stable/wf_options/Google/)
used by Google Pipelines API v2 in Cromwell.

For each Pipelines operation, the monitoring container
runs in parallel to all other containers on that GCE instance.
This container will collect information about
the task call attempt running on that instance.
It would then report the metrics at the end of the task run
in a format similar to the following:

| timestamp | project_id | zone | instance_id | instance_type | workflow_id  | task_call_name | task_call_index | task_call_attempt | preemptible | cpu_count | cpu_util_pct | mem_total_gb | mem_util_pct | disk_total_gb | disk_util_pct |
| ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- |
| 2019-06-07T23:19:42+00:00  | sample-project  | us-east1-b | gce-instance-1234 | n1-standard-2 | 11910a69-aaf5-428a-aae0-0b3b41ef396c | Task_Hello | 1 | 2 | True | 2 | 60 | 7.5 | 73 | 25 | 12 |
| 2019-06-07T23:20:43+00:00  | sample-project  | us-east1-b | gce-instance-1234 | n1-standard-2 | 11910a69-aaf5-428a-aae0-0b3b41ef396c | Task_Hello | 1 | 2 | True | 2 | 65 | 7.5 | 75 | 25 | 15 |

Some information is stored redundantly, but that's OK.
The actual amount of data stored in this format is miniscule (~ 10KB / hour, when reporting one row per minute),
and we'd like each row to be loaded as an independent data point in BigQuery,
to simplify both loading and querying.

The monitoring script will be developed similarly to the
["official" monitoring script](https://github.com/broadinstitute/cromwell/blob/develop/supportedBackends/google/pipelines/v2alpha1/src/main/resources/cromwell-monitor/monitor.py) from Cromwell repo.

It would obtain all of the details for the table above from the internal
[instance metadata endpoint](https://cloud.google.com/compute/docs/storing-retrieving-metadata),
as well as Compute Engine API and
[environment variables](https://github.com/broadinstitute/cromwell/blob/develop/supportedBackends/google/pipelines/v2alpha1/src/main/scala/cromwell/backend/google/pipelines/v2alpha1/api/MonitoringAction.scala)
assigned to the monitoring action by Cromwell.

## Loading to GCS

The monitoring code will periodically aggregate all info
into a file, and copy that file at the end of the operation
into a `gs://<monitoring-bucket>/<bucket-prefix>/<instance-id>.csv` object on GCS.

The bucket name and prefix (e.g. `example-cromwell-executions-bucket` and `monitoring`)
can be passed to the task through custom instance labels,
`monitoring-bucket` and `monitoring-prefix`, defined through `google_labels`
[workflow option](https://cromwell.readthedocs.io/en/stable/wf_options/Google/),
which is available in Cromwell 41+.

## Loading from GCS to BigQuery

[BigQuery Data Transfer Service for Cloud Storage](https://cloud.google.com/bigquery/docs/cloud-storage-transfer)
will run on a schedule
([API rate limit is 8h](https://cloud.google.com/bigquery/docs/reference/datatransfer/rest/v1/projects.locations.transferConfigs#TransferConfig)),
and load all files from that bucket into a table in BigQuery
(removing files from GCS afterwards to minimize costs). The table is
[partitioned on the date](https://cloud.google.com/bigquery/docs/querying-partitioned-tables),
to reduce costs of subsequent queries.

## Cost analysis

We anticipate this data collection to amount to
~1 GB data/day per ~100K Pipelines operations,
reported with 1 minute granularity.
Incremental monthly cost of storage in this example
would be ~$0.6, and querying 1 month worth of data ~$0.15.
