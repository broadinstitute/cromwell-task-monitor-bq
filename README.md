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

It would then report the static information once:
| project_id | zone | instance_id | instance_type | workflow_id  | workflow_name | task_call_name | task_call_index | task_call_attempt | preemptible | cpu_count | mem_total_gb | disk_total_gb |
| ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- |
| sample-project  | us-east1-b | gce-instance-1234 | n1-standard-2 | 11910a69-aaf5-428a-aae0-0b3b41ef396c | ExampleWorkflow | Task_Hello | 1 | 2 | True | 2 | 7.5 | 25 |

Next, it will report aggregate runtime metrics at a regular interval (e.g. 1 minute):

| timestamp_start | timestamp_end | instance_id | cpu_usage_percent.p50 | cpu_usage_percent.p75 | cpu_usage_percent.p95 | cpu_usage_percent.p99 | mem_usage_percent.p50 | mem_usage_percent.p75 | mem_usage_percent.p95 | mem_usage_percent.p99 | disk_size_usage_percent.p50 | disk_size_usage_percent.p75 | disk_size_usage_percent.p95 | disk_size_usage_percent.p99 | disk_read_iops.p50 | disk_read_iops.p75 | disk_read_iops.p95 | disk_read_iops.p99 | disk_write_iops.p50 | disk_write_iops.p75 | disk_write_iops.p95 | disk_write_iops.p99 |
| ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- | ------------- |  ------------- |
| 2019-06-07T23:19:42.123456+00:00 | 2019-06-07T23:20:41.987654+00:00 | gce-instance-1234 |    25 | 60 | 75 | 80    |    60 | 70 | 85 | 90    |     20 | 25 | 30 | 35    |    100 | 100 | 150 | 175    |    200 | 200 | 250 | 300    |

Here, we follow
[SRE best practices](https://landing.google.com/sre/sre-book/chapters/monitoring-distributed-systems/)
to report percentiles instead of "regular" metrics,
to more accurately capture the aggregate metrics and outliers across the reporting interval.
The amount of data stored in this format is miniscule (~10KB / hour, when reporting one row per minute).

The monitoring image will be developed similarly to the
["official" monitoring image](https://github.com/broadinstitute/cromwell/blob/develop/supportedBackends/google/pipelines/v2alpha1/src/main/resources/cromwell-monitor/monitor.py) from Cromwell repo.

It would obtain all of the details for the tables above from the internal
[instance metadata endpoint](https://cloud.google.com/compute/docs/storing-retrieving-metadata),
Compute Engine API (instance labels), and
[environment variables](https://github.com/broadinstitute/cromwell/blob/develop/supportedBackends/google/pipelines/v2alpha1/src/main/scala/cromwell/backend/google/pipelines/v2alpha1/api/MonitoringAction.scala)
assigned to the monitoring action by Cromwell.

## Reporting to BigQuery

Ideally, we'd like to target 2 use cases by the proposed solution:

- querying historical data, with relaxed requirements
  on the "freshness" of the results (e.g. waiting a day is OK);
  however, it's crucial to retain all data
  (perhaps with reduced glanularity over time)

- near-realtime queries for interactive workflow development, where
  a user wants to get feedback on the runtime performance of their
  workflow right away (possibly even while the workflow is still running)

Additionaly, we'd like to report these metrics in a _scalable_ way.
Some cloud monitoring services enforce fairly
low request limits (e.g. 6K RPM for Stackdriver),
or keep only the most recent data (e.g. 6 weeks for Stackdriver),
which are not compatible with requirements for
high-throughput workflows used by some platforms at the Broad.

At the same time, we can withstand occasional duplication or
unavailability of recently loaded data,
because we're only interested in trends and bulk statistics.

Finally, the reported data should be easily exportable
to any of a number of analytics tools,
either from Google (BigQuery and now, Looker) or elsewhere.

To address all of these points, we propose to use
[streaming inserts for BigQuery](https://cloud.google.com/bigquery/streaming-data-into-bigquery),
which were designed specifically for high-throughput loading
of data. Such inserts tolerate rates as high as
[100K RPS](https://cloud.google.com/bigquery/quotas#streaming_inserts)
(6M RPM) per table/project, and are available for queries in near-realtime (typically,
[seconds](https://cloud.google.com/bigquery/streaming-data-into-bigquery#dataavailability)).

Using monitoring code, each Pipelines operation
will insert one data point per minute
(though we could make that configurable via a workflow option).
It could use jitter in the time of submission of the request
(not to be confused with the reported `timestamp`),
to reduce the potential of running against the RPS limit.

To be able to sumbit the `insertAll` request,
the Pipelines jobs will have to be started with at least `bigquery.insertdata`
[OAuth scope](https://cloud.google.com/bigquery/docs/reference/rest/v2/tabledata/insertAll#authorization-scopes).
This will have to be implemented in Cromwell,
either as a [static](https://github.com/broadinstitute/cromwell/blob/6d737b056aca1f3c56c0e7bc212267ea912812bc/supportedBackends/google/pipelines/v2alpha1/src/main/scala/cromwell/backend/google/pipelines/v2alpha1/GenomicsFactory.scala#L148-L156)
or a [configurable](https://github.com/broadinstitute/cromwell/issues/4638) option.

The BigQuery table will be [partitioned on the date](https://cloud.google.com/bigquery/docs/querying-partitioned-tables)
from the `timestamp` column,
to reduce costs of subsequent queries.
This way, users will be able to query only a range of dates -
today, this month, last quarter etc,
and would be billed only for that range.
BigQuery allows up to 4,000
[partitions per table](https://cloud.google.com/bigquery/quotas#partitioned_tables),
i.e. one could hold up to ~11 years (!) worth of monitoring data.

BigQuery dataset and table names
could be passed to the task through custom instance labels,
`monitoring-dataset` and `monitoring-table`, defined through `google_labels`
[workflow option](https://cromwell.readthedocs.io/en/stable/wf_options/Google/),
which is available in Cromwell 41+.

## Cost analysis

From the figure above, we anticipate this data collection to amount to
~1 GB of data per ~100K instance hours,
reported at 1 minute granularity.
The cost of [streaming inserts](https://cloud.google.com/bigquery/pricing#streaming_pricing)
in this example would be ~$0.3, while
incremental monthly cost of storage ~$0.02 (or $0.01 after 3 months),
and querying it ~$0.005 (it may even fall within the
[free tier](https://cloud.google.com/bigquery/pricing#free-tier)).

So even though streaming inserts appear to be the biggest
cost factor initially, they're still very cheap overall,
compared to the amount spent on compute
(~$0.01 for just 1 preemptible core-hour).
As such, we don't expect monitoring
to have any noticeable effect on the overall cloud bill.
