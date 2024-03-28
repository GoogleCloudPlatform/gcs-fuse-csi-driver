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

## CSI Ephemeral Volume Example

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

## Static Provisioning Example

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

## Batch Job Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/batch-job/job.yaml

# install a Job using CSI Ephemeral Inline volume
kubectl apply -f ./examples/batch-job/job.yaml

# clean up
kubectl delete -f ./examples/batch-job/job.yaml
```

## TensorFlow Application Example

This example is inspired by the TensorFlow example in [Cloud Storage FUSE repo](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/perfmetrics/scripts/ml_tests/tf/resnet/README.md). The training jobs in this repo run exactly the same code from the Cloud Storage FUSE repo with GKE settings.

### Prerequisites

See [Prerequisites](#prerequisites) for PyTorch applications. The prerequisites are the same for Tensorflow applications.

### Prepare the training dataset

Follow the training dataset [imagenet2012 documentation](https://www.tensorflow.org/datasets/catalog/imagenet2012) to download the dataset from [ImageNet](https://image-net.org/challenges/LSVRC/2012/2012-downloads.php#Images). You need to manually download the dataset to a local filesystem, unzip and upload the dataset to your bucket.

### TensorFlow training job in Deep Learning Container (DLC)

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/train-job-tensorflow-dlc.yaml

# start the tensorflow training job
kubectl apply -f ./examples/tensorflow/train-job-tensorflow-dlc.yaml

# clean up
kubectl delete -f ./examples/tensorflow/train-job-tensorflow-dlc.yaml
```

## Jupyter Notebook Example (no experimental read cache)

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

## Jupyter Notebook Example (with experimental read cache)

#### Prerequisites

1. Your node pool must have created an ephemeral local ssds as described in
https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/local-ssd#node-pool

2. Your node pool must have a GPU accelerator. This example uses nvidia-tesla-t4,
but you can use another one (just make sure to update the nodeSelector in the
yaml below if so). See
[an example here](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/tree/main/examples#prerequisites)

#### Steps

```bash
# 1. replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/jupyter/jupyter-experimental-readcache.yaml

# 2. install a Jupyter Notebook server using experimental gcsfuse read cache
kubectl apply -f ./examples/jupyter/jupyter-experimental-readcache.yaml

# 3. get service IPs
kubectl get services -n example

# 4. Open jupyter
#  a. copy EXTERNAL-IP of tensorflow-jupyter server
#  b. open IP Address in a browser
#  c. input token "jupyter" (from yaml)

# 5. (optional) clean up
kubectl delete -f ./examples/jupyter/jupyter-notebook-server.yaml
```

