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

## Prerequisites

The following software are required for local development.

- [Go Programming Language](https://go.dev/doc/install)
- [Docker](https://docs.docker.com/get-docker/)
- [Python](https://docs.python-guide.org/starting/installation/)
- Run the following command to install GCC Compiler on Linux:

    ```bash
    sudo apt-get update && sudo apt-get install build-essential -y
    ```

Create a registry to host a custom driver image. This is needed if you want to build and install a custom GCSFuse CSI Driver.  Here is how you would create an artifact registry:

```bash
export REGION='us-central1'
export PROJECT_ID=$(gcloud config get project)
gcloud artifacts repositories create csi-dev \
--repository-format=docker \
--location=$REGION  --project=$PROJECT_ID \
--description="Docker repository"
```

## Build

You have two options for building a custom image for the Cloud Storage FUSE CSI Driver. You can use [Cloud build](#cloud-build), or [Makefile commands](#makefile-commands). Please note that building the image will take several minutes.

### Prerequisites

### Cloud Build

Run the following command to build and push the images using cloud build. If you created an artifact registry according to the [Prerequisites](#prerequisites), your REGISTRY would be as shown below. The `_REGISTRY` substitution is currently required for Cloud Build. If you would like to override `_STAGINGVERSION`, which is the version tag for the image, you can append `_STAGINGVERSION=<staging-version>` to the `--substitutions`. The default is `v999.999.999`. Please see the `cloudbuild-build-image.yaml` file for information on additional substitutions.

#### Cloud Build on Google Internal projects

For running cloud build from a Google Internal project, you can use the following command. This will use the gcsfuse version present in `cmd/sidecar_mounter/gcsfuse_binary` as the gcsfuse binary. You can change this to a different version, or use `_BUILD_GCSFUSE_FROM_SOURCE=true` to build gcsfuse from HEAD. Setting `GCSFUSE_BINARY_GCS_PATH` to the `gke-release-staging` bucket in `cmd/sidecar_mounter/gcsfuse_binary` is only allowed for Google Internal projects because artifacts in `gke-release-staging` are not publicly accessible.

Note that this currently doesn't work for _GCSFUSE_BINARY_GCS_PATH (fails because gcloud isn't in the prepare-gcsfuse-binary step image ), so use the make install command if you want to run at the GCSFuse version in _GCSFUSE_BINARY_GCS_PATH.

```bash
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
export GCSFUSE_BINARY_GCS_PATH=$(cat cmd/sidecar_mounter/gcsfuse_binary)
gcloud builds submit . --config=cloudbuild-build-image.yaml --substitutions=_REGISTRY=$REGISTRY,_GCSFUSE_BINARY_GCS_PATH=$GCSFUSE_BINARY_GCS_PATH
```

#### Cloud Build on NON Google Internal projects

If you are running this from a non-google internal project, you must set `_BUILD_GCSFUSE_FROM_SOURCE=true`, because without this, gcsfuse is loaded from the binary in `gke-release-staging`, which is not publicly accessible.

```bash
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
gcloud builds submit . --config=cloudbuild-build-image.yaml --substitutions=_REGISTRY=$REGISTRY,_BUILD_GCSFUSE_FROM_SOURCE=true
```

### Makefile Commands

Run the following command to build and push the images using the Makefile:

```bash
# REGISTRY=<your-container-registry>: Required. Define your container registry. Make sure you have logged in your registry so that you have image pull/push permissions.
# STAGINGVERSION=<staging-version>: Optional. Define a build version. If not defined, a staging version will be generated based on the commit hash.
# BUILD_GCSFUSE_FROM_SOURCE=true: Optional. You only have to build the gcsfuse binary from source when any CSI change depend on an unreleased GCSFuse enhancement.
make build-image-and-push-multi-arch REGISTRY=<your-container-registry> STAGINGVERSION=<staging-version>
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
