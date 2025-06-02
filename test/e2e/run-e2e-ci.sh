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

# Note: the environment variable names in Prow are different from the local e2e test script.
readonly PKGDIR=$(realpath "$( dirname -- "$0"; )/../..")
readonly gke_cluster_region=${GCE_CLUSTER_REGION:-us-central1}
readonly use_gke_autopilot=${USE_GKE_AUTOPILOT:-false}
readonly cloudsdk_api_endpoint_overrides_container=${CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER:-https://container.googleapis.com/}
readonly use_gke_managed_driver="${USE_GKE_MANAGED_DRIVER:-true}"
readonly ginkgo_focus="${TEST_FOCUS:-}"
readonly ginkgo_skip="${TEST_SKIP:-}"
readonly ginkgo_timeout="${E2E_TEST_GINKGO_TIMEOUT:-4h}"
readonly ginkgo_procs="${E2E_TEST_GINKGO_PROCS:-20}"
readonly boskos_resource_type="${GCE_PD_BOSKOS_RESOURCE_TYPE:-gke-internal-project}"
readonly gke_cluster_version=${GKE_CLUSTER_VERSION:-latest}
readonly gke_release_channel=${GKE_RELEASE_CHANNEL:-rapid}
readonly gke_node_version=${GKE_NODE_VERSION:-}
readonly node_machine_type=${MACHINE_TYPE:-n2-standard-4}
readonly number_nodes=${NUMBER_NODES:-3}
readonly gcsfuse_client_protocol=${GCSFUSE_CLIENT_PROTOCOL:-http1}
readonly build_gcsfuse_from_source=${BUILD_GCSFUSE_FROM_SOURCE:-false}
readonly enable_zb=${ENABLE_ZB:-false}

# Install golang
version=1.22.7
wget -O go_tar.tar.gz https://go.dev/dl/go${version}.linux-amd64.tar.gz -q
rm -rf /usr/local/go && tar -xzf go_tar.tar.gz -C /usr/local
export PATH=$PATH:/usr/local/go/bin && go version && rm go_tar.tar.gz

# Initialize ginkgo.
export PATH=${PATH}:$(go env GOPATH)/bin
go install github.com/onsi/ginkgo/v2/ginkgo@v2.19.1

cd "${PKGDIR}"

# Build e2e-test CLI
pushd test
go build -o ${PKGDIR}/bin/e2e-test-ci ./e2e
popd
chmod +x ${PKGDIR}/bin/e2e-test-ci

# Prepare the test cmd
base_cmd="${PKGDIR}/bin/e2e-test-ci \
            --pkg-dir=${PKGDIR} \
            --run-in-prow=true \
            --gke-cluster-region=${gke_cluster_region} \
            --use-gke-autopilot=${use_gke_autopilot} \
            --api-endpoint-override=${cloudsdk_api_endpoint_overrides_container} \
            --build-gcs-fuse-csi-driver=true \
            --build-gcs-fuse-from-source=${build_gcsfuse_from_source} \
            --use-gke-managed-driver=${use_gke_managed_driver} \
            --ginkgo-focus=${ginkgo_focus} \
            --ginkgo-skip=${ginkgo_skip} \
            --ginkgo-timeout=${ginkgo_timeout} \
            --ginkgo-procs=${ginkgo_procs} \
            --boskos-resource-type=${boskos_resource_type} \
            --gke-cluster-version=${gke_cluster_version} \
            --gke-release-channel=${gke_release_channel} \
            --gke-node-version=${gke_node_version} \
            --node-machine-type=${node_machine_type} \
            --gcsfuse-client-protocol=${gcsfuse_client_protocol} \
            --number-nodes=${number_nodes} \
            --enable-zb=${enable_zb} \"

eval "$base_cmd"
