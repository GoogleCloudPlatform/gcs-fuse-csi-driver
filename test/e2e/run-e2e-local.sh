#!/bin/bash

# Copyright 2019 The Kubernetes Authors.
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

set -o xtrace
set -o nounset
set -o errexit

# To run in a dev project, run
# export GOPATH=$HOME/go /
# export PATH=$PATH:$GOPATH/bin /
# ~/go/src/GoogleCloudPlatform/gcs-fuse-csi-driver$ GKE_CLUSTER_VERSION=1.27  USE_GKE_MANAGED_DRIVER=true PROJECT_ID=$USER-gke-dev  ./test/e2e/run-e2e-local.sh

readonly PKGDIR=${GOPATH}/src/GoogleCloudPlatform/gcs-fuse-csi-driver
readonly overlay_name="${GCS_FUSE_OVERLAY_NAME:-stable}"
readonly gke_cluster_version=${GKE_CLUSTER_VERSION:-latest} 
readonly gke_node_version=${GKE_NODE_VERSION:-}
readonly use_gke_autopilot=${USE_GKE_AUTOPILOT:-false}
readonly gce_region=${GCE_CLUSTER_REGION:-us-central1}
readonly use_gke_managed_driver=${USE_GKE_MANAGED_DRIVER:-false}
readonly test_focus="${TEST_FOCUS:-}"
readonly test_skip="${TEST_SKIP:-}"

# To run on an existing cluster, set PROJECT_ID, CLUSTER_NAME, and GCE_CLUSTER_REGION, and set BRING_UP_CLUSTER=false. 
# If BRING_UP_CLUSTER is not set, a new cluster will be created.
readonly gke_cluster_name=${CLUSTER_NAME:-}
readonly test_project_id=${PROJECT_ID:-}
readonly bring_up_cluster=${BRING_UP_CLUSTER:-true}
readonly tear_down_cluster=${TEAR_DOWN_CLUSTER:-false}
readonly cloudsdk_api_endpoint_overrides_container=${CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER:-https://container.googleapis.com/}


# Make e2e-test will initialize ginkgo.
make -C "${PKGDIR}" init-ginkgo 

make -C "${PKGDIR}" e2e-test-ci

# Note that for all tests:
# - a regional cluster will be created (GCS does not support zonal buckets)
# - run-in-prow false
# - image-type is cos_containerd
# - number-nodes is 3  
base_cmd="${PKGDIR}/bin/e2e-test-ci \
            --run-in-prow=false \
            --image-type=cos_containerd --number-nodes=3 \
            --region=${gce_region} \
            --use-gke-autopilot=${use_gke_autopilot} \
            --gke-cluster-version=${gke_cluster_version} \
            --gke-node-version=${gke_node_version} \
            --test-focus=${test_focus} \
            --test-skip=${test_skip} \
            --gke-cluster-name=${gke_cluster_name} --test-project-id=${test_project_id} \
            --bring-up-cluster=${bring_up_cluster} --tear-down-cluster=${tear_down_cluster} \
            --api-endpoint-override=${cloudsdk_api_endpoint_overrides_container}"

# do-driver-build must be false when using GKE managed driver. Otherwise, do-driver-build must be true and deploy-overlay-name should be set.
if [ "$use_gke_managed_driver" = true ]; then
  base_cmd="${base_cmd} --do-driver-build=false --use-gke-managed-driver=true" 
else
  base_cmd="${base_cmd} --do-driver-build=true --deploy-overlay-name=${overlay_name}"
fi

eval "$base_cmd"