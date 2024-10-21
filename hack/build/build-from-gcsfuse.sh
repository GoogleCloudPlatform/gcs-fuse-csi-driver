#!/bin/bash

# Copyright 2018 The Kubernetes Authors.
# Copyright 2024 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Variables that must be defined by user.
# GCSFUSE_PATH

# Consts
BUCKET_LOCATION=us-central1        # e.g. us-central1
PROJECT_ID=$(gcloud config get project)  # e.g. jaimebz-gke-dev

# Make a bucket to temporarily store binary.
gsutil mb -l $BUCKET_LOCATION -p $PROJECT_ID gs://$BUCKET_NAME

echo "GCSFUSE path is ${GCSFUSE_PATH}"
if [[ $GCSFUSE_PATH == "" ]]; then
    echo "Please point to the location of gcsfuse repository by setting GCSFUSE_PATH"
fi

# Build binary.
echo "Building gcsfuse binary..."
mkdir ./bin/gcsfuse_bin/
GOOS=linux GOARCH=amd64 go run $GCSFUSE_PATH/tools/build_gcsfuse/main.go . ./bin/gcsfuse_bin/ v3

# Push binary to bucket.
echo "Pushing gcsfuse binary to bucket."
gsutil cp ./bin/gcsfuse_bin/gcsfuse gs://$BUCKET_NAME/linux/amd64/

cd 
# Build sidecar image.# Build sidecar image.# Build sidecar image.
make build-sidecar-and-push-multi-arch GCSFUSE_PATH=gs://$BUCKET_NAME 

# Delete bucket.
gsutil rm -r gs://$BUCKET_NAME