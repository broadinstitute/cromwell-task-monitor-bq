#!/bin/bash

set -e

SUBSTITUTIONS=""
NA="N/A"

input() {
  local name="$1"
  local default="$2"

  local prompt="Enter ${name}"
  if [ -n "${default}" ]; then
    prompt="${prompt} (default: ${default})"
  fi

  while [ -z "${!name}" ]; do
    read -p "${prompt}: " value
    export ${name}=${value:-${default}}
  done
  if [ "${!name}" = "${NA}" ]; then
    export ${name}=
  fi

  SUBSTITUTIONS="_${name}=${!1},${SUBSTITUTIONS}"
}

PROJECT_ID=$(gcloud config list --format 'value(core.project)')
PROJECT_NUMBER=$(gcloud projects describe ${PROJECT_ID} --format 'value(projectNumber)')

input CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL "${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"
input CROMWELL_BASEURL ${NA}

if [ -n "${CROMWELL_BASEURL}" ]; then
  input CROMWELL_SAM_BASEURL ${NA}
  input CROMWELL_LOGS_BUCKET "${PROJECT_ID}-cromwell-logs"

  input CROMWELL_METADATA_FUNCTION_REGION "us-east1"
  input CROMWELL_METADATA_FUNCTION_NAME "cromwell-metadata-uploader"
  input CROMWELL_METADATA_SERVICE_ACCOUNT_NAME "${CROMWELL_METADATA_FUNCTION_NAME}"
fi

input GCR_REGISTRY "us.gcr.io"
input MONITORING_IMAGE_NAME "cromwell-task-monitor-bq"

input DATASET_ID "cromwell_monitoring"
input DATASET_LOCATION "US"

echo "
      -------------
      Deploying ...
"

### Enable GCP APIs

gcloud services enable \
  cloudbuild.googleapis.com \
  compute.googleapis.com \
  iam.googleapis.com

### Grant Cloud Build permissions for deployment

cloudbuild_sa_email="${PROJECT_NUMBER}@cloudbuild.gserviceaccount.com"

add_role() {
  gcloud projects add-iam-policy-binding ${PROJECT_ID} \
    --member "serviceAccount:${cloudbuild_sa_email}" \
    --role "roles/$1" >/dev/null
}

add_role bigquery.user
add_role cloudfunctions.developer
add_role iam.serviceAccountAdmin

### Create the bucket, if needed

if [ -n "${CROMWELL_BASEURL}" ]; then
  gsutil mb -l ${CROMWELL_METADATA_FUNCTION_REGION} \
    "gs://${CROMWELL_LOGS_BUCKET}" || true
fi

### Deploy through Cloud Build

gcloud builds submit --substitutions ${SUBSTITUTIONS} .
