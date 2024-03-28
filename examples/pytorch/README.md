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

# PyTorch Application Example

This example is inspired by the PyTorch example in [Cloud Storage FUSE repo](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/perfmetrics/scripts/ml_tests/pytorch/README-usage.md).

## Prerequisites

If you are using a GKE Autopilot cluster, you do not need to do anything in this step.

```bash
# when you are using a Standard cluster, add a new node pool with GPU:
CLUSTER_NAME=test-cluster-us-central1
REGION=us-central1
gcloud container node-pools create gpu-test-pool \
    --accelerator type=nvidia-tesla-a100,count=2 \
    --region ${REGION} --cluster ${CLUSTER_NAME} \
    --ephemeral-storage-local-ssd count=2 \
    --num-nodes 1 \
    --machine-type a2-highgpu-2g

# install the nvidia driver
# see the GKE doc for details: https://cloud.google.com/kubernetes-engine/docs/how-to/gpus#installing_drivers
kubectl apply -f https://raw.githubusercontent.com/GoogleCloudPlatform/container-engine-accelerators/master/nvidia-driver-installer/cos/daemonset-preloaded.yaml

# create a namespace and service account
kubectl create namespace gcs-csi-example
kubectl create serviceaccount gcs-csi --namespace gcs-csi-example
```

## Set up GCS bucket

Create a GCS bucket using `Location type`: `Region`, and select the same region where your cluster runs. Follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#authentication) to configure the access. This example uses the default Kubernetes service account in the default Kubernetes namespace.

## Prepare the training dataset

Follow the following steps to download the dataset from Kaggle, then unzip and upload the dataset to a GCS bucket. You only need to do this step once.

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/data-loader-job.yaml

# replace <kaggle-key> with your kaggle API key
# Go to https://www.kaggle.com to create a kaggle account if necessary, then read the "Authentication" section [here](https://www.kaggle.com/docs/api) for how to get your Kaggle API key. The format is {"username":"xxx","key":"xxx"}.
KAGGLE_KEY=your-kaggle-key
sed -i "s/<kaggle-key>/$KAGGLE_KEY/g" ./examples/pytorch/data-loader-job.yaml

# prepare the data
kubectl apply -f ./examples/pytorch/data-loader-job.yaml

# clean up
kubectl delete -f ./examples/pytorch/data-loader-job.yaml
```

## PyTorch training job

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/train-job-pytorch.yaml

# start the pytorch training job
kubectl apply -f ./examples/pytorch/train-job-pytorch.yaml

# clean up
kubectl delete -f ./examples/pytorch/train-job-pytorch.yaml
```

## PyTorch training job in Deep Learning Container (DLC)

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/train-job-pytorch-dlc.yaml

# start the pytorch training job
kubectl apply -f ./examples/pytorch/train-job-pytorch-dlc.yaml

# clean up
kubectl delete -f ./examples/pytorch/train-job-pytorch-dlc.yaml
```