#!/bin/bash

set -e

input() {
  local prompt="Enter $1"
  if [ -n "$2" ]; then
    prompt="${prompt} (default: $2)"
  fi
  while [ -z "${!1}" ]; do
    read -p "${prompt}: " value && echo
    export $1=${value:-$2}
  done
}

PROJECT_ID=$(gcloud config list --format 'value(core.project)')
PROJECT_NUMBER=$(gcloud projects describe ${PROJECT_ID} --format 'value(projectNumber)')

input REGION "us-east1"

input CROMWELL_BASEURL
input CROMWELL_SAM_BASEURL
input CROMWELL_LOGS_BUCKET "${PROJECT_ID}-cromwell-logs"
input CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL

input DATASET_ID "cromwell_monitoring"

image_message=$(mktemp)

./docker.sh | tee "${image_message}"

gsutil mb -l ${REGION} "gs://${CROMWELL_LOGS_BUCKET}" || true

gcloud projects add-iam-policy-binding ${PROJECT_ID} \
  --member "serviceAccount:${PROJECT_NUMBER}@cloudservices.gserviceaccount.com" \
  --role "roles/owner" >/dev/null

./gcloud.sh

tail -1 "${image_message}"
