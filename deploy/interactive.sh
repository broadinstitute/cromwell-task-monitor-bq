#!/bin/bash

set -e

input() {
  local prompt="Enter $1"
  if [ -n "$2" ]; then
    prompt="${prompt} (default: $2)"
  fi
  while [ -z "${!1}" ]; do
    read -p "${prompt}: " $1 && echo
    $1=${1:-$2}
  done
  export $1
}

input REGION "us-east1"

input CROMWELL_BASEURL
input CROMWELL_SAM_BASEURL
input CROMWELL_LOGS_BUCKET "${PROJECT_ID}-cromwell-logs"
input CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL

input DATASET_ID "cromwell_monitoring"

gsutil mb -p ${PROJECT_ID} -l ${REGION} "${CROMWELL_LOGS_BUCKET}" || true

./docker.sh
./gcloud.sh
