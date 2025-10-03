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

Note that this currently isn't working because gcloud isn't installed the `prepare-gcsfuse-binary` step image, so for now, use the make install command if you want to run at the GCSFuse version in _GCSFUSE_BINARY_GCS_PATH on Google internal project. <!--TODO(amacaskill): fix this. -->

```bash
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
export GCSFUSE_BINARY_GCS_PATH=$(cat cmd/sidecar_mounter/gcsfuse_binary)
gcloud builds submit . --config=cloudbuild-build-image.yaml --substitutions=_REGISTRY=$REGISTRY,_GCSFUSE_BINARY_GCS_PATH=$GCSFUSE_BINARY_GCS_PATH
```

#### Cloud Build on NON Google Internal projects

If you are running this from a non-google internal project, you must set `_BUILD_GCSFUSE_FROM_SOURCE=true`, because without this, gcsfuse is loaded from the binary in `gke-release-staging`, which is not publicly accessible. By default, `_BUILD_GCSFUSE_FROM_SOURCE=true` will build GCSFuse from the master branch. If you would like to run from a particular GCSFuse release tag instead of master, run the following commands to find the latest GCSFuse release supported by our driver. Running from a GCSFuse release rather than master is recommended as GCSFuse master branch isn't always stable like releases, which have gone through extensive qualification testing.

```bash
export LATEST_TAG=$(curl -s "https://api.github.com/repos/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/latest" | jq -r .tag_name)
export GCSFUSE_TAG=$(curl -sL "https://raw.githubusercontent.com/GoogleCloudPlatform/gcs-fuse-csi-driver/$LATEST_TAG/cmd/sidecar_mounter/gcsfuse_binary" | cut -d'/' -f5 | cut -d'-' -f1)
echo "latest GCSFuse CSI driver release is $LATEST_TAG, which uses GCSFuse version $GCSFUSE_TAG"
```

 After you have found the latest `GCSFUSE_VERSION`, pass that to the cloudbuild script with the `_GCSFUSE_TAG` substitution. 

```bash
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
gcloud builds submit . --config=cloudbuild-build-image.yaml --substitutions=_REGISTRY=$REGISTRY,_BUILD_GCSFUSE_FROM_SOURCE=true,_GCSFUSE_TAG=$GCSFUSE_TAG
```

### Makefile Commands

Run the following command to build and push the images using the Makefile:

```bash
# REGISTRY=<your-container-registry>: Required. Define your container registry. Make sure you have logged in your registry so that you have image pull/push permissions.
# STAGINGVERSION=<staging-version>: Optional. Define a build version. If not defined, a staging version will be generated based on the commit hash.
# BUILD_GCSFUSE_FROM_SOURCE=<true|false>: Optional, default: false. Indicate if you want to build the gcsfuse binary from source. This is required for building images from non-google internal projects OR if you want to test any CSI change depend on an unreleased GCSFuse enhancement. If BUILD_GCSFUSE_FROM_SOURCE is true, by default it builts GCSFuse from head of the master branch. If you want to build GCSFuse from a particular tag, then set GCSFUSE_TAG as explained in cloudbuild sections above.
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
