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
# Choose "[2] None" for binding condition if you want to apply the permissions to all the GCS buckets in the project.
GCS_BUCKET_PROJECT_ID=<gcs-bucket-project-id>
CLUSTER_PROJECT_ID=<cluster-project-id>
gcloud projects add-iam-policy-binding ${GCS_BUCKET_PROJECT_ID} \
    --member "serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[gcs-csi-example/gcs-csi]" \
    --role "roles/storage.admin"

# Optionally, run the following command if you only want to apply permissions to a specific bucket.
GCS_BUCKET_PROJECT_ID=<gcs-bucket-project-id>
CLUSTER_PROJECT_ID=<cluster-project-id>
BUCKET_NAME=<gcs-bucket-name>
gcloud projects add-iam-policy-binding ${GCS_BUCKET_PROJECT_ID} \
    --member "serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[gcs-csi-example/gcs-csi]" \
    --role "roles/storage.admin" \
    --condition="expression=resource.name.startsWith(\"projects/$GCS_BUCKET_PROJECT_ID/buckets/$BUCKET_NAME\"),title=access-to-$BUCKET_NAME"
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

# install a Deployment using CSI Ephemeral Inline volume
kubectl apply -f ./examples/ephemeral/deployment.yaml
kubectl apply -f ./examples/ephemeral/deployment-non-root.yaml

# clean up
kubectl delete -f ./examples/ephemeral/deployment.yaml
kubectl delete -f ./examples/ephemeral/deployment-non-root.yaml
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