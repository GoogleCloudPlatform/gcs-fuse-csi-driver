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

# TensorFlow Application Example

This example is inspired by the TensorFlow example in [Cloud Storage FUSE repo](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/perfmetrics/scripts/ml_tests/tf/resnet/README.md). The training jobs in this repo run exactly the same code from the Cloud Storage FUSE repo with GKE settings.

## Prerequisites

See [Prerequisites](#prerequisites) for PyTorch applications. The prerequisites are the same for Tensorflow applications.

## Prepare the training dataset

Follow the training dataset [imagenet2012 documentation](https://www.tensorflow.org/datasets/catalog/imagenet2012) to download the dataset from [ImageNet](https://image-net.org/challenges/LSVRC/2012/2012-downloads.php#Images). You need to manually download the dataset to a local filesystem, unzip and upload the dataset to your bucket.

## TensorFlow training job in Deep Learning Container (DLC)

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/pytorch/train-job-tensorflow-dlc.yaml

# start the tensorflow training job
kubectl apply -f ./examples/tensorflow/train-job-tensorflow-dlc.yaml

# clean up
kubectl delete -f ./examples/tensorflow/train-job-tensorflow-dlc.yaml
```