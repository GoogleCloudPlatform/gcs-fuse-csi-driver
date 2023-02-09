# GCS FUSE CSI Driver Development Guide

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
1. Define the following variables.
   ```bash
   # Required. Define your container registry. Make sure you have logged in your registry so that you have image pull/push permissions.
   export REGISTRY=<your-container-registry>
   # Optional. Define a build version. If not defined, a staging version will be generated based on the commit hash.
   export STAGINGVERSION=<staging-version>
   # Optional. If you want to build the gcsfuse binary from source code. Otherwise, a pre-built gcsfuse binary will be downloaded automatically.
   export BUILD_GCSFUSE_FROM_SOURCE=true
   ```
2. Build and push the images.
   ``` bash
   make build-image-and-push-multi-arch
   ```

## Test
Refer to [Test](../test/README.md) documentation.

## Troubleshooting
Run the following queries on GCP Logs Explorer to check logs.
- Sidecar container and gcsfuse logs:
    ```
    resource.type="k8s_container"
    resource.labels.container_name="gke-gcsfuse-sidecar"
    ```
- GCS Fuse CSI Driver logs:
    ```
    resource.type="k8s_container"
    resource.labels.container_name="gcs-fuse-csi-driver"
    ```
- GCS Fuse CSI Driver Webhook logs:
    ```
    resource.type="k8s_container"
    resource.labels.container_name="gcs-fuse-csi-driver-webhook"
    ```