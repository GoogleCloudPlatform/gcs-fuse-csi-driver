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

## Test
Refer to [Test](../test/README.md) documentation.

## Troubleshooting
Run the following queries on GCP Logs Explorer to check logs.
- Sidecar container and gcsfuse logs:
    ```
    resource.type="k8s_container"
    resource.labels.container_name="gke-gcsfuse-sidecar"
    ```
- Cloud Storage FUSE CSI Driver logs:
    ```
    resource.type="k8s_container"
    resource.labels.container_name="gcs-fuse-csi-driver"
    ```
- Cloud Storage FUSE CSI Driver Webhook logs:
    ```
    resource.type="k8s_container"
    resource.labels.container_name="gcs-fuse-csi-driver-webhook"
    ```