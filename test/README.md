<!--
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Test

The repo has created Make commands for all the tests. Please clone the repo and run the following commands from the root level of the repo.

## Unit test

```bash
make unit-test
```

## Sanity test

```bash
make sanity-test
```

## End-to-end test
### Prerequisites

1. Enable the following GCP API:
   - [Cloud Resource Manager API](https://cloud.google.com/resource-manager/reference/rest)
   - [Google Cloud Storage JSON API](https://cloud.google.com/storage/docs/json_api)

2. Configure your gcloud:

    ```bash
    gcloud auth login
    gcloud config set <your-project>
    gcloud auth application-default login
    ```

3. Create a GKE cluster:

    If you want to run the test against GKE pre-installed CSI driver, please follow the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver) to create a cluster with the Cloud Storage FUSE CSI driver enabled.

    If you want to create a cluster to manually install the CSI driver and test it, follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/concepts/kubernetes-engine-overview) to create a **Standard** cluster **without** enabling the `GcsFuseCsiDriver` add-on. Then follow the [installation instruction](../docs/installation.md) to manually install the CSI driver.

4. Make sure the current kubelet context uses the cluster

    The e2e test code will automatically parse the current kubelet context to get your cluster information. Before you run the test, run the following command to ensure the current kubelet context uses the right cluster:

    ```bash
    kubectl config current-context
    ```

    If the current kubelet context does not use the cluster you want to test against, run the following command to switch to the right cluster:

    ```bash
    # for regional clusters:
    gcloud container clusters get-credentials <your-cluster-name> --region <cluster-region>

    # for zonal clusters:
    gcloud container clusters get-credentials <your-cluster-name> --zone <cluster-zone>
    ```

    If your kubelet config file is not located in ~/.kube/config, please run the following command to set your kubelet config location:

    ```bash
    expoert KUBECONFIG=<your-kubelet-config-location>
    ```

### Run end-to-end test

You can control the test through the following parameters:

- `REGISTRY`: default value is `jiaxun`. Change the container registry to your own if you need to build your own CSI images. Make sure you have permission to push images to the container registry.
- `OVERLAY`: default value is `stable`. Change the kustomize overlay name for manual installation.
- `STAGINGVERSION`: default value is the current git commit hash. Pass the container image version if you need to build your own CSI images with a different version.
- `E2E_TEST_USE_GKE_AUTOPILOT`: default value is `false`. Change it to `true` if you will test the CSI driver on an Autopilot cluster.
- `CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER`: default value is `https://container.googleapis.com/`. **Only works for Google GKE developers.** Valid values are `https://staging-container.sandbox.googleapis.com/`, `https://staging2-container.sandbox.googleapis.com/`, and `https://test-container.sandbox.googleapis.com/`.
- `E2E_TEST_USE_GKE_MANAGED_DRIVER`: default value is `true`. Change it to `false` if you want to manually install the CSI driver.
- `E2E_TEST_BUILD_DRIVER`: default value is `false`. Change it to `true` if you want to build the CSI images from source code.
- `BUILD_GCSFUSE_FROM_SOURCE`: default value is `false`. Change it to `true` if you want to build the GCS FUSE binary from source code.
- `E2E_TEST_FOCUS`: default value is an empty string. The value will be passed to `ginkgo run --focus` flag.
- `E2E_TEST_SKIP`: default value is `should.succeed.in.performance.test`. The value will be passed to `ginkgo run --skip` flag.
- `E2E_TEST_GINKGO_PROCS`: default value is `5`. The value will be passed to `ginkgo run --procs` flag.
- `E2E_TEST_GINKGO_TIMEOUT`: default value is `1h`. The value will be passed to `ginkgo run --timeout` flag.
- `E2E_TEST_GINKGO_FLAKE_ATTEMPTS`: default value is `2`. The value will be passed to `ginkgo run --flake-attempts` flag.

```bash
# Run the test on an Autopilot cluster with the GcsFuseCsiDriver add-on enabled.
make e2e-test E2E_TEST_USE_GKE_MANAGED_DRIVER=true E2E_TEST_USE_GKE_AUTOPILOT=true

# Build the CSI driver and install it before the test. The GCS FUSE binary will be built from source code. The images will be using the version "v999.999.999", and will be pushed to your own container registry.
make e2e-test E2E_TEST_USE_GKE_MANAGED_DRIVER=false E2E_TEST_BUILD_DRIVER=true BUILD_GCSFUSE_FROM_SOURCE=true STAGINGVERSION=v999.999.999 REGISTRY=my-registry

# Run the test with customized Ginkgo flags.
make e2e-test E2E_TEST_FOCUS=gcsfuseIntegration E2E_TEST_SKIP=failedMount E2E_TEST_GINKGO_PROCS=3 E2E_TEST_GINKGO_TIMEOUT=20m E2E_TEST_GINKGO_FLAKE_ATTEMPTS=1
```

## Performance test

The performance test is a part of the e2e test suite. You can run the following shortcut to run the performance test on an existing cluster with the CSI driver installed.

Make sure the cluster has at least one `n2-standard-32` node. The test lasts about 2 hours.

```bash
make perf-test
```

You can also customize the performance test via `make e2e-test`:

```bash
# Build the CSI driver and install it before the test.
make e2e-test E2E_TEST_USE_MANAGED_DRIVER=false E2E_TEST_BUILD_DRIVER=true BUILD_GCSFUSE_FROM_SOURCE=false E2E_TEST_GINKGO_TIMEOUT=3h E2E_TEST_SKIP= E2E_TEST_FOCUS=should.succeed.in.performance.test E2E_TEST_GINKGO_FLAKE_ATTEMPTS=1
```
