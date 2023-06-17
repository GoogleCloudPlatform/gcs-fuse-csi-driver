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

# Example Applications

## Install the CSI driver

See the documentation [Cloud Storage FUSE CSI Driver Installation](../docs/installation.md).

## Set up access to GCS buckets

See the documentation [Cloud Storage FUSE CSI Driver Usage](../docs/usage.md#set-up-access-to-gcs-buckets-via-gke-workload-identity).

## Install Example Applications

### Static Provisioning Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/static/pv-pvc-deployment.yaml
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/static/pv-pvc-deploymen-non-root.yaml

# install PV/PVC and a Deployment
kubectl apply -f ./examples/static/pv-pvc-deployment.yaml
kubectl apply -f ./examples/static/pv-pvc-deploymen-non-root.yaml

# clean up
# the PV deletion will not delete your GCS bucket
kubectl delete -f ./examples/static/pv-pvc-deployment.yaml
kubectl delete -f ./examples/static/pv-pvc-deploymen-non-root.yaml
```

### Ephemeral Volume Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/ephemeral/deployment.yaml
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/ephemeral/deployment-non-root.yaml
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/ephemeral/deployment-two-vols.yaml

# install a Deployment using CSI Ephemeral Inline volume
kubectl apply -f ./examples/ephemeral/deployment.yaml
kubectl apply -f ./examples/ephemeral/deployment-non-root.yaml
kubectl apply -f ./examples/ephemeral/deployment-two-vols.yaml

# clean up
kubectl delete -f ./examples/ephemeral/deployment.yaml
kubectl delete -f ./examples/ephemeral/deployment-non-root.yaml
kubectl delete -f ./examples/ephemeral/deployment-two-vols.yaml
```

### Batch Job Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/batch-job/job.yaml

# install a Job using CSI Ephemeral Inline volume
kubectl apply -f ./examples/batch-job/job.yaml

# clean up
kubectl delete -f ./examples/batch-job/job.yaml
```

### PyTorch Application Example

This example is inspired by the TensorFlow example in [Cloud Storage FUSE repo](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/perfmetrics/scripts/ml_tests/pytorch/dino/README-usage.md). The training jobs in this repo run exactly the same code from the Cloud Storage FUSE repo with GKE settings.

#### Prerequisites

If you are using a GKE Autopilot cluster, you do not need to do anything in this step.

```bash
# when you are using a Standard cluster, add a new node pool with GPU:
CLUSTER_NAME=cluster-name
ZONE=node-pool-zone
gcloud container node-pools create pool-gpu-pytorch \
  --accelerator type=nvidia-tesla-a100,count=2 \
  --zone ${ZONE} --cluster ${CLUSTER_NAME} \
  --num-nodes 1 \
  --machine-type a2-highgpu-2g

# install the nvidia driver
# see the GKE doc for details: https://cloud.google.com/kubernetes-engine/docs/how-to/gpus#installing_drivers
kubectl apply -f https://raw.githubusercontent.com/GoogleCloudPlatform/container-engine-accelerators/master/nvidia-driver-installer/cos/daemonset-preloaded.yaml
```

#### Prepare the training dataset

Follow the following steps to download the dataset from Kaggle, then unzip and upload the dataset to a GCS bucket. You only need to do this step once.

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/data-loader-job.yaml

# replace <kaggle-key> with your kaggle API key
# Go to https://www.kaggle.com/docs/api to get your kaggle API key. The format is {"username":"xxx","key":"xxx"}.
KAGGLE_KEY=your-kaggle-key
sed -i "s/<kaggle-key>/$KAGGLE_KEY/g" ./examples/pytorch/data-loader-pod.yaml

# prepare the data
kubectl apply -f ./examples/pytorch/data-loader-job.yaml

# clean up
kubectl delete -f ./examples/pytorch/data-loader-job.yaml
```

#### PyTorch training job

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/train-job-pytorch.yaml

# start the pytorch training job
kubectl apply -f ./examples/pytorch/train-job-pytorch.yaml

# clean up
kubectl delete -f ./examples/pytorch/train-job-pytorch.yaml
```

### PyTorch training job in Deep Learning Container (DLC)

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/train-job-pytorch-dlc.yaml

# start the pytorch training job
kubectl apply -f ./examples/pytorch/train-job-pytorch-dlc.yaml

# clean up
kubectl delete -f ./examples/pytorch/train-job-pytorch-dlc.yaml
```

### TensorFlow Application Example

This example is inspired by the TensorFlow example in [Cloud Storage FUSE repo](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/perfmetrics/scripts/ml_tests/tf/resnet/README.md). The training jobs in this repo run exactly the same code from the Cloud Storage FUSE repo with GKE settings.

#### Prerequisites

See [Prerequisites](#prerequisites) for PyTorch applications. The prerequisites are the same for Tensorflow applications.

#### Prepare the training dataset

Follow the training dataset [imagenet2012 documentation](https://www.tensorflow.org/datasets/catalog/imagenet2012) to download the dataset from [ImageNet](https://image-net.org/challenges/LSVRC/2012/2012-downloads.php#Images). You need to manually download the dataset to a local filesystem, unzip and upload the dataset to your bucket.

#### TensorFlow training job in Deep Learning Container (DLC)

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/train-job-tensorflow-dlc.yaml

# start the tensorflow training job
kubectl apply -f ./examples/tensorflow/train-job-tensorflow-dlc.yaml

# clean up
kubectl delete -f ./examples/tensorflow/train-job-tensorflow-dlc.yaml
```

### Jupyter Notebook Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/jupyter/jupyter-notebook-server.yaml

# install a Jupyter Notebook server using CSI Ephemeral Inline volume
kubectl apply -f ./examples/jupyter/jupyter-notebook-server.yaml

# access the Jupyter Notebook via http://localhost:8888
kubectl port-forward jupyter-notebook-server 8888:8888

# clean up
kubectl delete -f ./examples/jupyter/jupyter-notebook-server.yaml
```
