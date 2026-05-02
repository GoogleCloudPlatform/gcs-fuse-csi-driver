#!/bin/bash

# Copyright 2018 The Kubernetes Authors.
# Copyright 2022 Google LLC
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

# Script to delete all GCS buckets with a given prefix

BUCKET_PREFIX="gcsfusecsi-testsuite-gen-"

# List all buckets
BUCKETS=$(gcloud storage ls)

# Array to store buckets to be deleted
DELETE_BUCKETS=()

# Identify buckets for deletion and print them
echo "The following buckets will be deleted:"
for BUCKET in $BUCKETS; do
  # Extract the bucket name (remove gs://)
  BUCKET_NAME=$(echo $BUCKET | sed 's/gs:\/\///')

  # Check if the bucket starts with the specified prefix
  if [[ "$BUCKET_NAME" == "$BUCKET_PREFIX"* ]]; then
    echo "- $BUCKET_NAME"
    DELETE_BUCKETS+=("$BUCKET_NAME") # Add to array
  fi
done

# Check if there are any buckets to delete
if [[ ${#DELETE_BUCKETS[@]} -eq 0 ]]; then
  echo "No buckets found with the prefix '$BUCKET_PREFIX'. Script exiting."
  exit 0
fi

# Prompt for confirmation
read -p "Are you sure you want to delete ALL the listed buckets? (y/n): " CONFIRM

# If the user confirms, delete the buckets
if [[ "$CONFIRM" == "y" || "$CONFIRM" == "Y" ]]; then
  echo "Deleting buckets..."
  for BUCKET_NAME in "${DELETE_BUCKETS[@]}"; do
    gcloud storage rm --recursive "gs://$BUCKET_NAME"
    echo "Deleted: $BUCKET_NAME"
  done
  echo "All specified buckets have been deleted."
else
  echo "Deletion cancelled."
  exit 0
fi

echo "Script completed."
