#!/bin/bash

set -e

# GCP project name for the deployment
PROJECT_ID=${PROJECT_ID}

# Google Container Registry location
REGISTRY=${REGISTRY:-"us.gcr.io"}

# Docker image name
IMAGE_NAME=${IMAGE_NAME:-"cromwell-task-monitor-bq"}

# BigQuery dataset name for monitoring tables
DATASET_ID=${DATASET_ID}

### Deploy monitoring_image

IMAGE=${REGISTRY}/${PROJECT_ID}/${IMAGE_NAME}

docker build -t ${IMAGE} \
  --build-arg DATASET_ID=${DATASET_ID} ../monitor

docker push ${IMAGE}
