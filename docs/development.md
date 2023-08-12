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

# Cloud Storage FUSE CSI Driver Development Guide

## Prerequisite

The following software are required for local development.
- [Go Programming Language](https://go.dev/doc/install)
- [Docker](https://docs.docker.com/get-docker/)
- [Python](https://docs.python-guide.org/starting/installation/)
- Run the following command to install GCC Compiler on Linux:
    ```bash
    sudo apt-get update && sudo apt-get install build-essential -y
    ```

## Build

Run the following command to build and push the images.

``` bash
# BUILD_GCSFUSE_FROM_SOURCE=true: Required. You have to build the gcsfuse binary from source code as well.
# REGISTRY=<your-container-registry>: Required. Define your container registry. Make sure you have logged in your registry so that you have image pull/push permissions.
# STAGINGVERSION=<staging-version>: Optional. Define a build version. If not defined, a staging version will be generated based on the commit hash.
make build-image-and-push-multi-arch BUILD_GCSFUSE_FROM_SOURCE=true REGISTRY=<your-container-registry> STAGINGVERSION=<staging-version>
```

## Manual installation

Refer to [Cloud Storage FUSE CSI Driver Manual Installation](./installation.md) documentation.

## Test

Refer to [Test](../test/README.md) documentation.

## Update go modules

Follow the following steps to update go modules:

1. Open the [go.mod](../go.mod) file in Visual Studio Code.
2. Click "Check for upgrades" above the first `require` block.
3. Hover over the module with available upgrade. Click "Quick Fix" to apply the upgrade.
4. If you upgraded any `k8s.io` modules, make sure the version is also updated in the `replace` block. You need to do this step because [lack of semantic versioning](https://github.com/kubernetes/kubernetes/issues/72638) prevents `go mod` from finding newer releases.
5. Run `go mod tidy` to ensure that the listed dependencies are really still needed.
6. Run `go mod vendor` to update vendor directory.
7. Resolve any issues that may be introduced by the new modules.

## Troubleshooting

Refer to [Troubleshooting](./troubleshooting.md) documentation.