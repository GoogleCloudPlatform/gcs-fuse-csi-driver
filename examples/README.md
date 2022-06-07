# Example Applications

## Install the CSI driver
See the documentation [GCS CSI Driver Installation](../docs/installation.md).

## Setup Service Accounts
In order to authenticate with GCP, you will need to setup a Kubernetes Service Account and grant the GCS permissions to the serice account.
### Create Kubernetes Service Account
```bash
kubectl create namespace gcs-csi-example
kubectl create serviceaccount gcs-csi --namespace gcs-csi-example
```

### Grant GCS Permissions to the Kubernetes Service Account
```bash
# Relace <gcs-bucket-project-id> with the id of the project where your GCS bucket lives.
# Relace <cluster-project-id> with the id of the project where your GKE cluster lives.
# Choose "[2] None" for binding condition.
GCS_BUCKET_PROJECT_ID=<gcs-bucket-project-id>
CLUSTER_PROJECT_ID=<cluster-project-id>
gcloud projects add-iam-policy-binding ${GCS_BUCKET_PROJECT_ID} \
    --member "serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[gcs-csi-example/gcs-csi]" \
    --role "roles/storage.admin"
```

## Install Example Applications
### Dynamic Provisioning Example
```bash
# create a secret containing the Kubernetes Service Account information
kubectl create secret generic gcs-csi-secret --namespace gcs-csi-example \
    --from-literal=projectID=${GCS_BUCKET_PROJECT_ID} \
    --from-literal=serviceAccountName=gcs-csi \
    --from-literal=serviceAccountNamespace=gcs-csi-example

# deploy a Deployment
kubectl apply -f ./examples/dynamic/deployment.yaml

# deploy a StatefulSet
kubectl apply -f ./examples/dynamic/statefulset.yaml

# clean up
kubectl delete -f ./examples/dynamic/deployment.yaml

# After the StatefulSet application get uninstalled,
# you will need to clean up the PVCs manually.
kubectl delete -f ./examples/dynamic/statefulset.yaml
kubectl delete -n gcs-csi-example pvc gcs-bucket-gcp-cloud-storage-csi-dynamic-statefulset-example-0 gcs-bucket-gcp-cloud-storage-csi-dynamic-statefulset-example-1 gcs-bucket-gcp-cloud-storage-csi-dynamic-statefulset-example-2

# You will need to firstly delete all the files in the GCS bucket
# so that the PV and the GCS bucket can be deleted.

# Do not delete the secret before you cleaned up all the PVs.
kubectl delete secret gcs-csi-secret --namespace gcs-csi-example
```

### Static Provisioning Example
```bash
# Open the file ./examples/static/pv.yaml and replace <bucket-name> with your pre-provisioned GCS bucket name.

# install PV/PVC and a Deployment
kubectl apply -f ./examples/static/pv.yaml
kubectl apply -f ./examples/static/pvc.yaml
kubectl apply -f ./examples/static/deployment.yaml

# clean up
kubectl delete -f ./examples/static/deployment.yaml
kubectl delete -f ./examples/static/pvc.yaml
# the PV deletion will not delete your GCS bucket
kubectl delete -f ./examples/static/pv.yaml
```

### Ephemeral Volume Example
```bash
# Open the file ./examples/ephemeral/deployment.yaml and replace <bucket-name> with your pre-provisioned GCS bucket name.

# install a Deployment using CSI Ephemeral Inline volume
kubectl apply -f ./examples/ephemeral/deployment.yaml

# clean up
kubectl delete -f ./examples/ephemeral/deployment.yaml
```