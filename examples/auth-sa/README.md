# Authenticate GCSFuse using GCP Service Account Keys

> WARNING: For testing purposes in non-Google Kubernetes Engine (GKE) clusters, you can authenticate with Google Cloud Platform (GCP) APIs using GCP service account keys. However, this approach is not recommended for production use.

## Background

The GKE managed Cloud Storage FUSE CSI driver only supports [GKE Workload Identity Federation](https://cloud.google.com/kubernetes-engine/docs/concepts/workload-identity) to access GCS buckets. However, the CSI driver does not support this authentication method on non-GKE clusters, such as on-premises clusters. This branch contains changes that allow users to mount a GCP Service Account key json file to the GCSFuse sidecar container, so that GCSFuse can use the exported service account key to authenticate with GCS API. Specifically, you need to manually build and install the CSI driver, and then create a GCP service account, export the service account key to a local file, setup GCS bucket permissions, create a Kubernetes Secret using the key, specify the Secret name via a new Pod annotation `gke-gcsfuse/gcp-sa-secret-name`. You also need to add the volume attribute `skipCSIBucketAccessCheck: "true"` to your volume. Below is an Pod example showing the new Pod annotation and volume attribute.

```yaml
apiVersion: v1
kind: Pod
metadata:
  annotations:
    gke-gcsfuse/volumes: "true"
    gke-gcsfuse/gcp-sa-secret-name: "my-sa-secret"
spec:
  restartPolicy: Never
  containers:
  ...
  volumes:
  - name: gcs-fuse-csi-ephemeral
    csi:
      driver: gcsfuse.csi.storage.gke.io
      volumeAttributes:
        bucketName: <bucket-name>
        skipCSIBucketAccessCheck: "true"
```

## Prerequisites: build and install the CSI driver

1. Clone the repository and switch to branch `auth-sa`:

```bash
git clone --branch auth-sa https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver.git
```

2. Follow [Cloud Storage FUSE CSI Driver Development Guide](../../docs/development.md) to build your private CSI driver images.

3. Manually install the CSI driver:

```bash
make install REGISTRY=<your-container-registry> STAGINGVERSION=<staging-version>
```

## Set up GCP service account and GCS bucket

1. Create a new GCP service account. Or you can use an existing GCP service account.

```bash
gcloud iam service-accounts create <YOUR_SERVICE_ACCOUNT_NAME> --project <YOUR_PROJECT_ID>
```

2. Export the GCP service account key to a local json file.

```bash
gcloud iam service-accounts keys create sa.json --iam-account <YOUR_SERVICE_ACCOUNT_NAME>@<YOUR_PROJECT_ID>.iam.gserviceaccount.com
```

3. Create a new GCS bucket. Or you can use an existing bucket.

```bash
gcloud storage buckets create gs://<YOUR_BUCKET_NAME>
```

4. Set up GCS bucket permission.

```bash
gcloud storage buckets add-iam-policy-binding gs://<YOUR_BUCKET_NAME> \
    --member "serviceAccount:<YOUR_SERVICE_ACCOUNT_NAME>@<YOUR_PROJECT_ID>.iam.gserviceaccount.com" \
    --role "roles/storage.objectUser"
```

## Deploy an example Pod

1. Create a new Kubernetes namespace. Or you can use an existing namespace.

```bash
kubectl create namespace <YOUR_K8S_NAMESPACE>
```

2. Create a Kubernetes Secret in **the same** namespace using the local GCP service account key json file.

```bash
kubectl create secret generic my-sa-secret --namespace <YOUR_K8S_NAMESPACE> --from-file=sa.json
```

3. Deploy an example Pod.

```bash
# replace <YOUR_K8S_NAMESPACE> with your Kubernetes namespace name
sed -i "s/<namespace-name>/<YOUR_K8S_NAMESPACE>/g" ./examples/auth-sa/pod.yaml
# replace <YOUR_BUCKET_NAME> with your GCS bucket name
sed -i "s/<bucket-name>/<YOUR_BUCKET_NAME>/g" ./examples/auth-sa/pod.yaml

kubectl apply -f ./examples/auth-sa/pod.yml
```
