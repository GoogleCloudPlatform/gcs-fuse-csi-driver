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

readonly PKGDIR=$(realpath "$( dirname -- "$0"; )/../..")
readonly gke_cluster_region=${GKE_CLUSTER_REGION:-us-central1}
readonly gke_cluster_version=$(kubectl version | grep -Eo 'Server Version: v[0-9]+\.[0-9]+\.[0-9]+' | grep -Eo  '[0-9]+\.[0-9]+\.[0-9]+')
readonly gke_release_channel=${GKE_RELEASE_CHANNEL:-rapid}
readonly use_gke_autopilot=${E2E_TEST_USE_GKE_AUTOPILOT:-false}
readonly cloudsdk_api_endpoint_overrides_container=${CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER:-https://container.googleapis.com/}

readonly use_gke_managed_driver="${E2E_TEST_USE_GKE_MANAGED_DRIVER:-true}"
readonly build_gcs_fuse_csi_driver="${E2E_TEST_BUILD_DRIVER:-false}"
readonly build_gcsfuse_from_source="${BUILD_GCSFUSE_FROM_SOURCE:-false}"
readonly overlay="${OVERLAY:-stable}"

readonly ginkgo_focus="${E2E_TEST_FOCUS:-}"
readonly ginkgo_skip="${E2E_TEST_SKIP:-should.succeed.in.performance.test}"
readonly ginkgo_procs="${E2E_TEST_GINKGO_PROCS:-10}"
readonly ginkgo_timeout="${E2E_TEST_GINKGO_TIMEOUT:-4h}"
readonly ginkgo_flake_attempts="${E2E_TEST_GINKGO_FLAKE_ATTEMPTS:-2}"
readonly gcsfuse_client_protocol=${GCSFUSE_CLIENT_PROTOCOL:-http1}
readonly enable_zb=${ENABLE_ZB:-false}
readonly enable_sidecar_bucket_access_check=${ENABLE_SIDECAR_BUCKET_ACCESS_CHECK:-false}

# Initialize ginkgo.
export PATH=${PATH}:$(go env GOPATH)/bin
## Keep this up to date with ../go.mod.
go install github.com/onsi/ginkgo/v2/ginkgo@v2.23.0

cd "${PKGDIR}"

# Build e2e-test CLI
pushd ./test
go build -o ${PKGDIR}/bin/e2e-test-ci ./e2e
popd
chmod +x "${PKGDIR}/bin/e2e-test-ci"

# Prepare the test cmd
base_cmd="${PKGDIR}/bin/e2e-test-ci \
            --pkg-dir=${PKGDIR} \
            --run-in-prow=false \
            --gke-cluster-region=${gke_cluster_region} \
            --use-gke-autopilot=${use_gke_autopilot} \
            --api-endpoint-override=${cloudsdk_api_endpoint_overrides_container} \
            --image-registry=${REGISTRY} \
            --build-gcs-fuse-csi-driver=${build_gcs_fuse_csi_driver} \
            --build-gcs-fuse-from-source=${build_gcsfuse_from_source} \
            --deploy-overlay-name=${overlay} \
            --use-gke-managed-driver=${use_gke_managed_driver} \
            --gke-cluster-version=${gke_cluster_version} \
            --gke-release-channel=${gke_release_channel} \
            --ginkgo-focus=${ginkgo_focus} \
            --ginkgo-skip=${ginkgo_skip} \
            --ginkgo-procs=${ginkgo_procs} \
            --ginkgo-timeout=${ginkgo_timeout} \
            --gcsfuse-client-protocol=${gcsfuse_client_protocol} \
            --ginkgo-flake-attempts=${ginkgo_flake_attempts} \
            --gcsfuse-enable-zb=${enable_zb} \
            --enable-sidecar-bucket-access-check=${enable_sidecar_bucket_access_check}"
eval "$base_cmd"
