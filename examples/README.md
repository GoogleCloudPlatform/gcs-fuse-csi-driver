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

## Jupyter Notebook Example

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