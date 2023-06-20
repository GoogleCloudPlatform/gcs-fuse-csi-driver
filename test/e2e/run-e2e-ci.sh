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

# NOTE: this script ONLY works on the GKE internal Prow test pipeline.

set -o xtrace
set -o nounset
set -o errexit

# TODO(songjiaxun): Change the Prow variable names to align with the local e2e test script.
readonly PKGDIR="$( dirname -- "$0"; )/../.."
readonly gke_cluster_region=${GCE_CLUSTER_REGION:-us-central1}
readonly use_gke_autopilot=${USE_GKE_AUTOPILOT:-false}
readonly cloudsdk_api_endpoint_overrides_container=${CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER:-https://container.googleapis.com/}
readonly use_gke_managed_driver="${USE_GKE_MANAGED_DRIVER:-true}"
readonly ginkgo_focus="${TEST_FOCUS:-}"
readonly ginkgo_skip="${TEST_SKIP:-}"
readonly boskos_resource_type="${GCE_PD_BOSKOS_RESOURCE_TYPE:-gke-internal-project}"
readonly gke_cluster_version=${GKE_CLUSTER_VERSION:-latest}
readonly gke_node_version=${GKE_NODE_VERSION:-}
readonly node_machine_type=${MACHINE_TYPE:-n1-standard-2}

# Initialize ginkgo.
export PATH=${PATH}:$(go env GOPATH)/bin
go install github.com/onsi/ginkgo/v2/ginkgo@v2.9.4

# Build e2e-test CLI
go build -mod=vendor -o ${PKGDIR}/bin/e2e-test-ci ./test/e2e
chmod +x ${PKGDIR}/bin/e2e-test-ci

# Prepare the test cmd
base_cmd="${PKGDIR}/bin/e2e-test-ci \
            --pkg-dir=${PKGDIR} \
            --run-in-prow=true \
            --gke-cluster-region=${gke_cluster_region} \
            --use-gke-autopilot=${use_gke_autopilot} \
            --api-endpoint-override=${cloudsdk_api_endpoint_overrides_container} \
            --build-gcs-fuse-csi-driver=true \
            --build-gcs-fuse-from-source=true \
            --deploy-overlay-name=stable \
            --use-gke-managed-driver=${use_gke_managed_driver} \
            --ginkgo-focus=${ginkgo_focus} \
            --ginkgo-skip=${ginkgo_skip} \
            --ginkgo-procs=5 \
            --ginkgo-timeout=30m \
            --ginkgo-flake-attempts=2 \
            --boskos-resource-type=${boskos_resource_type} \
            --gke-cluster-version=${gke_cluster_version} \
            --gke-node-version=${gke_node_version} \
            --node-machine-type=${node_machine_type} \
            --image-type=cos_containerd \
            --number-nodes=3"

eval "$base_cmd"