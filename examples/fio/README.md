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

# FIO Loading Tests

## Prerequisites

### Create a new node pool

For an existing GKE cluster, use the following command to create a new node pool. Make sure the cluster has the [Workload Identity feature enabled](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#enable).

> In this early stage test, the managed GCS FUSE CSI driver feature is disabled, and the driver is manually installed.

```bash
# Replace the cluster name and zone.
gcloud container node-pools create large-pool \
    --cluster test-cluster-us-central1-c \
    --ephemeral-storage-local-ssd count=16 \
    --machine-type n2-standard-96 \
    --zone us-central1-c \
    --num-nodes 3
```

### Set up GCS bucket

Create a GCS bucket using `Location type`: `Region`, and select the same region where your cluster runs. Follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#authentication) to configure the access. This example uses the default Kubernetes service account in the default Kubernetes namespace.

### Install Helm

The example uses Helm charts to manage the applications. Follow the [Helm documentation](https://helm.sh/docs/intro/install/#from-script) to install Helm.

## FIO Loading Tests

Change the directory to `./examples/fio`. Run the following commands to run the loading tests. Each `helm install` command will deploy a Pod to run the test, and upload logs to the bucket.

### Run the tests

```bash
python ./run_tests.py
```

### Delete the tests

```bash
python ./delete_tests.py
```

## Parsing the test results

Run the following python script to parse the logs. The results will be saved in `./examples/fio/output.csv`.

```bash
cd ./examples/fio
python ./parse_logs.py
```