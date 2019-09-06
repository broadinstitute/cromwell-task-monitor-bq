# Deploy Cromwell Task Monitoring with BigQuery

## Let's get started!

This tutorial will help you set up
Cromwell task resource monitoring with BigQuery,
as described in more detail at
[broadinstitute/cromwell-task-monitor-bq](https://github.com/broadinstitute/cromwell-task-monitor-bq#motivation)

### First, please
<walkthrough-project-billing-setup/>

## Deploy

When you run our deployment script (see below),
it will ask for a few parameters:

- `CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL` is the email
  of the Service Account used by your Cromwell task instances.

- `CROMWELL_BASEURL` is the base URL of the Cromwell API,
  e.g. `https://cromwell.example.org`

  **Please Note:** if you _don't_ want to set up
  Cromwell Metadata monitoring in BigQuery,
  then keep this value as `N/A`.

The following values are **only** needed if you
provided a non-default `CROMWELL_BASEURL` above:

- `CROMWELL_SAM_BASEURL` is the base URL of the Cromwell Sam API,
  e.g. `https://sam.example.org`

  **Please Note:** if your Cromwell
  _doesn't_ use Sam to authorize requests,
  then keep this value as `N/A`.

- `CROMWELL_LOGS_BUCKET` is the bucket name corresponding
  to `final_workflow_log_dir` option in Cromwell.

  It **must** be in the same _project_
  as the one you selected above (**{{project-id}}**).

  If you didn't have one beforehand,
  just press Enter, and we will create it for you.

The following variables are **optional**, but
feel free to change them if you know what you're doing 😉:

- `CROMWELL_METADATA_FUNCTION_NAME`,
  `CROMWELL_METADATA_FUNCTION_REGION`,
  `CROMWELL_METADATA_SERVICE_ACCOUNT_NAME`
  are the Cloud Function and its Service Account
  names and region for Metadata deployment.

- `GCR_REGISTRY` and `MONITORING_IMAGE_NAME`
  are GCR registry and name for the monitoring image.

- `DATASET_ID` and `DATASET_LOCATION` are
  the name and location of BigQuery dataset
  where all monitoring tables will be stored.

Ready? Please run this command, and follow the prompts:
```sh
gcloud config set project {{project-id}} && ./interactive.sh
```

After the deployment is complete, the script will
instruct you what to do next.

## Happy monitoring!

<walkthrough-conclusion-trophy/>
