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

# DLIO Unet3D Loading Tests

## Prerequisites

### Build DLIO docker container image

```bash
# Replace the docker registry.
git clone https://github.com/argonne-lcf/dlio_benchmark.git
cd dlio_benchmark/
docker build -t jiaxun/dlio:v1.1.0 .
docker image push jiaxun/dlio:v1.1.0
```

### Create a new node pool

For an existing GKE cluster, use the following command to create a new node pool. Make sure the cluster has the [Workload Identity feature enabled](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#enable).

> In this early stage test, the managed GCS FUSE CSI driver feature is disabled, and the driver is manually installed.

```bash
# Replace the cluster name and zone.
gcloud container node-pools create large-pool \
    --cluster test-cluster-us-west1-c \
    --ephemeral-storage-local-ssd count=16 \
    --machine-type n2-standard-96 \
    --zone us-west1-c \
    --num-nodes 3
```

### Set up GCS bucket

Create a GCS bucket using `Location type`: `Region`, and select the same region where your cluster runs. Follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#authentication) to configure the access. This example uses the default Kubernetes service account in the default Kubernetes namespace.

### Install Helm

The example uses Helm charts to manage the applications. Follow the [Helm documentation](https://helm.sh/docs/intro/install/#from-script) to install Helm.

## DLIO Unet3D Datasets Loading

Run the following commands to generate Unet3D datasets using DLIO, and upload to the bucket. You may need to `--set image=<your-registry>/dlio:v1.0.0` and `--set bucketName=<your-bucket-name>` to set your registry and bucket name.

```bash
cd ./examples/dlio

helm install dlio-unet3d-100kb-500k-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400

helm install dlio-unet3d-500kb-1m-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000

helm install dlio-unet3d-3mb-100k-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-3mb-100k \
--set dlio.numFilesTrain=100000 \
--set dlio.recordLength=3145728

helm install dlio-unet3d-150mb-5k-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-150mb-5k \
--set dlio.numFilesTrain=5000 \
--set dlio.recordLength=157286400

# Clean up
helm uninstall \
dlio-unet3d-100kb-500k-data-loader \
dlio-unet3d-500kb-1m-data-loader \
dlio-unet3d-3mb-100k-data-loader \
dlio-unet3d-150mb-5k-data-loader
```

## DLIO Unet3D Loading Tests

Change the directory to `./examples/dlio`. Run the following commands to run the loading tests.

```bash
python ./run_tests.py
```

### Delete the tests

```bash
python ./delete_tests.py
```

## Parsing the test results

Run the following python script to parse the logs. The results will be saved in `./examples/dlio/output.csv`.

```bash
cd ./examples/dlio
python ./parse_logs.py
```