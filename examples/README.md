# Example Applications

## Install the CSI driver
See the documentation [GCS FUSE CSI Driver Installation](../docs/installation.md).

## Setup Service Accounts
In order to authenticate with GCP, you will need to do the following steps:
1. Setup a GCP Service Account.
2. Grant the GCS permissions to the GCP Service Account.
3. Setup a Kubernetes Service Account.
4. Bind the the Kubernetes Service Account with the GCP Service Account.

### Define the variables
```bash
# Relace <gcs-bucket-project-id> with the id of the project where your GCS bucket lives.
# Relace <gcp-service-account-name> with the name of the GCP service account. The service account should in the GCS bucket project.
# Relace <cluster-project-id> with the id of the project where your GKE cluster lives.
GCS_BUCKET_PROJECT_ID=<gcs-bucket-project-id>
GSA_NAME=<gcp-service-account-name>
CLUSTER_PROJECT_ID=<cluster-project-id>
```

### Create GCP Service Account
```bash
gcloud iam service-accounts create ${GSA_NAME} \
    --project=${GCS_BUCKET_PROJECT_ID}
```

### Grant GCS Permissions to the GCP Service Account
```bash
# Choose "[2] None" for binding condition if you want to apply the permissions to all the GCS buckets in the project.
gcloud projects add-iam-policy-binding ${CLUSTER_PROJECT_ID} \
    --member "serviceAccount:${GSA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com" \
    --role "roles/storage.admin"

# Optionally, run the following command if you only want to apply permissions to a specific bucket.
BUCKET_NAME=<gcs-bucket-name>
gcloud projects add-iam-policy-binding ${CLUSTER_PROJECT_ID} \
    --member "serviceAccount:${GSA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com" \
    --role "roles/storage.admin" \
    --condition="expression=resource.name.startsWith(\"projects/$GCS_BUCKET_PROJECT_ID/buckets/$BUCKET_NAME\"),title=access-to-$BUCKET_NAME"
```

### Create Kubernetes Service Account
```bash
kubectl create namespace gcs-csi-example
kubectl create serviceaccount gcs-csi --namespace gcs-csi-example
```

### Bind the Kubernetes Service Account with the GCP Service Account
```bash
gcloud iam service-accounts add-iam-policy-binding ${GSA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com \
    --role roles/iam.workloadIdentityUser \
    --member "serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[gcs-csi-example/gcs-csi]"

kubectl annotate serviceaccount gcs-csi \
    --namespace gcs-csi-example \
    iam.gke.io/gcp-service-account=${GSA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
```

## Install Example Applications
### Dynamic Provisioning Example
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