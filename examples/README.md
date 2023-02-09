# Example Applications

## Install the CSI driver
See the documentation [GCS FUSE CSI Driver Installation](../docs/installation.md).

## Set up access to GCS buckets
See the documentation [GCS FUSE CSI Driver Usage](../docs/usage.md#set-up-access-to-gcs-buckets-via-gke-workload-identity).

## Install Example Applications
### Dynamic Provisioning Example (Unstable)
```bash
# create a secret containing the Kubernetes Service Account information
kubectl create secret generic gcs-csi-secret --namespace gcs-csi-example \
    --from-literal=projectID=${GCS_BUCKET_PROJECT_ID} \
    --from-literal=serviceAccountName=gcs-csi \
    --from-literal=serviceAccountNamespace=gcs-csi-example

# deloy a StorageClass for non-root usage
kubectl apply -f ./examples/dynamic/storageclass-non-root.yaml

# deploy Deployment
kubectl apply -f ./examples/dynamic/deployment.yaml
kubectl apply -f ./examples/dynamic/deployment-non-root.yaml

# deploy StatefulSet
kubectl apply -f ./examples/dynamic/statefulset.yaml
kubectl apply -f ./examples/dynamic/statefulset-non-root.yaml

# clean up
kubectl delete -f ./examples/dynamic/deployment.yaml
kubectl delete -f ./examples/dynamic/deployment-non-root.yaml
kubectl delete -f ./examples/dynamic/statefulset.yaml
kubectl delete -f ./examples/dynamic/statefulset-non-root.yaml

# After the StatefulSet application get uninstalled,
# you will need to clean up the PVCs manually.
kubectl delete -n gcs-csi-example pvc gcs-bucket-gcp-gcs-csi-dynamic-statefulset-example-0 gcs-bucket-gcp-gcs-csi-dynamic-statefulset-example-1 gcs-bucket-gcp-gcs-csi-dynamic-statefulset-example-2
kubectl delete -n gcs-csi-example pvc gcs-bucket-gcp-gcs-csi-dynamic-statefulset-non-root-example-0 gcs-bucket-gcp-gcs-csi-dynamic-statefulset-non-root-example-1 gcs-bucket-gcp-gcs-csi-dynamic-statefulset-non-root-example-2

# clean up the secret and non-root StorageClass after all the PVs are deleted
kubectl delete -f ./examples/dynamic/storageclass-non-root.yaml
kubectl delete secret gcs-csi-secret --namespace gcs-csi-example
```

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

### Performance Testing
```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/perf-test/pod.yaml

# make sure the cluster have nodes using n2-standard-32 instance type
kubectl apply -f ./examples/perf-test/pod.yaml

# the FIO test may last ~2 hours
# when the test is done, you can find the result output in your bucket, e.g. <bucket-name>/fio-logs/output-2022-12-07.json
kubectl delete -f ./examples/perf-test/pod.yaml
```

### GCS Fuse E2E Test
```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/gcsfuse-e2e-test/pod.yaml

# create the test pod
kubectl apply -f ./examples/gcsfuse-e2e-test/pod.yaml

# the e2e test may last ~5 minutes
# when the test is done, you can find the result output in your bucket, e.g. <bucket-name>/e2e-logs/output-2022-12-07.json
kubectl delete -f ./examples/gcsfuse-e2e-test/pod.yaml
```

### Machine Learning Application Example
```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/machineLearning/jupyter-notebook-server.yaml

# install a Jupyter Notebook server using CSI Ephemeral Inline volume
kubectl apply -f ./examples/machineLearning/jupyter-notebook-server.yaml

# access the Jupyter Notebook via http://localhost:8888
kubectl port-forward jupyter-notebook-server 8888:8888

# clean up
kubectl delete -f ./examples/machineLearning/jupyter-notebook-server.yaml
```