#!/bin/bash

set -e

# GCP region for the deployment
REGION=${REGION}

# Name of the deployment for Deployment Manager
DEPLOYMENT_NAME=${DEPLOYMENT_NAME:-"cromwell-monitoring"}

# Cromwell API base URL, e.g. https://cromwell.example.org
CROMWELL_BASEURL=${CROMWELL_BASEURL}

# Cromwell Sam API base URL, e.g. https://sam.example.org
CROMWELL_SAM_BASEURL=${CROMWELL_SAM_BASEURL}

# Cromwell final_workflow_log_dir bucket (must belong to the same project)
CROMWELL_LOGS_BUCKET=${CROMWELL_LOGS_BUCKET}

# Email of the Service Account used by your Cromwell task instances
CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL=${CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL}

# BigQuery dataset name for monitoring tables
DATASET_ID=${DATASET_ID}

# Name of the Service Account (to be created) for the Cloud Function (see below)
CROMWELL_METADATA_SERVICE_ACCOUNT_NAME=${CROMWELL_METADATA_SERVICE_ACCOUNT_NAME:-"cromwell-metadata-uploader"}

# Name of the Cloud Function (to be created)
# that will query Cromwell workflow metadata and store it in BigQuery
FUNCTION_NAME=${FUNCTION_NAME:-"cromwell-metadata-uploader"}

### Enable GCP APIs

gcloud services enable deploymentmanager iam

### Deploy the template

DEPLOYMENT_TEMPLATE="monitoring.jinja"

deployment() {
  local action="$1" && shift

  local props="cromwellMetadataServiceAccountName:'${CROMWELL_METADATA_SERVICE_ACCOUNT_NAME}'"
  props="${props},cromwellTaskServiceAccountEmail:'${CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL}'"
  props="${props},datasetID:'${DATASET_ID}'"

  gcloud deployment-manager deployments "${action}" "${DEPLOYMENT_NAME}" \
    --template "${DEPLOYMENT_TEMPLATE}" \
    --properties "${props}"
}

deployment create || deployment update

output() {
  gcloud deployment-manager deployments describe "${DEPLOYMENT_NAME}" \
    --format "value(outputs.filter(name:$1).extract(finalValue).flatten())"
}

metadata_sa_email=$(output metadataServiceAccountEmail)
dataset_console_url=$(output monitoringDatasetConsoleURL)

### Deploy the Cloud Function

(
  cd ../metadata
  gcloud functions deploy ${FUNCTION_NAME} \
    --region ${REGION} \
    --trigger-bucket ${CROMWELL_LOGS_BUCKET} \
    --service-account ${metadata_sa_email} \
    --set-env-vars DATASET_ID=${DATASET_ID},CROMWELL_BASEURL=${CROMWELL_BASEURL} \
    --runtime go111 \
    --memory 128MB
)

### Register CROMWELL_METADATA_SERVICE_ACCOUNT in Sam

get_token() {
  curl -sH "Authorization: Bearer $(gcloud auth print-access-token)" \
    "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/$1:generateAccessToken" \
    -H "Content-Type: application/json" \
    -d "{
      \"scope\": [
          \"https://www.googleapis.com/auth/userinfo.email\",
          \"https://www.googleapis.com/auth/userinfo.profile\"
      ]
    }" \
    | python -c 'import json,sys; print(json.load(sys.stdin)["accessToken"])'
}

token=$(get_token ${metadata_sa_email})
curl -sH "Authorization: Bearer ${token}" "${CROMWELL_SAM_BASEURL}/register/user/v1" -d ""

### Final instructions

echo "
  Deployment is complete.

  ${metadata_sa_email} has been registered in Sam,
  however you need to grant it READER role
  on the Cromwell collection(s) to be accessed.

  Additionally, please grant ${metadata_sa_email}
  Storage Object Viewer role on all buckets
  that are used in your Cromwell workflows.

  After you run the first workflow,
  the monitoring tables will appear in
  ${dataset_console_url}
"
