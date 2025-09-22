<!--
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

# OIDC Authentication for GCS FUSE CSI Driver

This document describes how to configure OIDC (OpenID Connect) authentication for the Google Cloud Storage FUSE CSI driver using Workload Identity Federation. This approach provides enhanced security by eliminating the need for service account key files and enables fine-grained access control to GCS buckets.

## Overview

The OIDC authentication feature allows Kubernetes pods to authenticate to Google Cloud Storage using projected service account tokens and credential configuration files stored in ConfigMaps. The GCS FUSE CSI driver webhook automatically injects the necessary credentials and configuration into the gcsfuse sidecar container.

## Prerequisites

- Kubernetes cluster with the GCS FUSE CSI driver installed
- Google Cloud project with appropriate APIs enabled
- `gcloud` CLI tool installed and configured
- Cluster admin permissions to configure Workload Identity Federation

## Step 1: Configure GCP IAM for Workload Identity Federation

### 1.1 Create a Workload Identity Pool

First, create a workload identity pool that will trust your Kubernetes cluster:

```bash
export PROJECT_ID="your-project-id"
export PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")
export LOCATION="global"
export POOL_ID="gcs-fuse-pool" # or any custom name
export PROVIDER_ID="gcs-fuse-provider" # or any custom name
export CLUSTER_NAME="your-cluster-name"
export CLUSTER_LOCATION="your-cluster-location"

# Create workload identity pool
gcloud iam workload-identity-pools create $POOL_ID \
    --project=$PROJECT_ID \
    --location=$LOCATION \
    --display-name="GCS FUSE Pool" 
```

### 1.2 Create a Workload Identity Provider

Create a provider within the pool that trusts your Kubernetes cluster:

```bash
# Get the cluster's OIDC issuer URL
export CLUSTER_ISSUER_URL=$(gcloud container clusters describe $CLUSTER_NAME \
    --location=$CLUSTER_LOCATION \
    --project=$PROJECT_ID \
    --format="value(workloadIdentityConfig.workloadPool)")

# Create workload identity provider
gcloud iam workload-identity-pools providers create-oidc $PROVIDER_ID \
    --project=$PROJECT_ID \
    --location=$LOCATION \
    --workload-identity-pool=$POOL_ID \
    --display-name="GCS FUSE Provider" \
    --attribute-mapping="google.subject=assertion.sub" \
    --issuer-uri=$CLUSTER_ISSUER_URL
```

### 1.3 Download the Credential Configuration File

Generate the credential configuration file that contains the workload identity federation configuration:

```bash
# Download credential configuration
gcloud iam workload-identity-pools create-cred-config \
    projects/$PROJECT_NUMBER/locations/$LOCATION/workloadIdentityPools/$POOL_ID/providers/$PROVIDER_ID \
    --output-file=credential-configuration.json \
    --credential-source-file=/var/run/service-account/token \
    --credential-source-type=text
```

**Important Links:**
- [Workload Identity Federation with Kubernetes documentation](https://cloud.google.com/iam/docs/workload-identity-federation-with-kubernetes)
- [Creating and managing workload identity pools](https://cloud.google.com/iam/docs/manage-workload-identity-pools-providers)

## Step 2: Grant Service Account Access to GCS Bucket

### 2.1 Grant Bucket-Level Permissions

Grant the service account the necessary permissions to access your GCS bucket:

```bash
export BUCKET_NAME="your-gcs-bucket"
epxort ROLE_NAME="roles/storage.objectUser" # it could be any role you wish to grant
export NAMESPACE="pod_namespace"
export KSA_NAME="pod_service_account"

# Grant a given role
gcloud storage buckets add-iam-policy-binding gs://$BUCKET_NAME --member "principal://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$PROJECT_ID.svc.id.goog/subject/ns/$NAMESPACE/sa/$KSA_NAME" --role "$ROLE_NAME"
```

**Important Links:**
- [Cloud Storage IAM documentation](https://cloud.google.com/storage/docs/access-control/iam)
- [Uniform bucket-level access](https://cloud.google.com/storage/docs/uniform-bucket-level-access)

## Step 3: Create Kubernetes Resources

### 3.1 Create Kubernetes Service Account

```bash
# Create Kubernetes service account
kubectl create serviceaccount $KSA_NAME -n $NAMESPACE
```

### 3.2 Create ConfigMap with Credential Configuration

Import the credential configuration file as a ConfigMap:

```bash
kubectl create configmap workload-identity-credentials \
    --from-file=credential-configuration.json \
    --namespace=$NAMESPACE
```

## Step 4: Configure Pod with OIDC Authentication

### 4.1 Add the Workload Identity Annotation

To enable OIDC authentication for your pod, add the following annotation to your pod specification:

```yaml
annotations:
  gke-gcsfuse/workload-identity-credential-configmap: "workload-identity-credentials"
```

### 4.2 Complete Pod Example

Here's a complete example of a pod using OIDC authentication with GCS FUSE:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gcs-fuse-oidc-example
  namespace: default
  annotations:
    gke-gcsfuse/volumes: "true"
    gke-gcsfuse/workload-identity-credential-configmap: "workload-identity-credentials"
spec:
  serviceAccountName: gcs-fuse-ksa
  containers:
  - name: app
    image: busybox
    command: ["/bin/sh", "-c", "echo 'Hello from GCS FUSE with OIDC!' > /mnt/gcs/hello.txt && cat /mnt/gcs/hello.txt && sleep 3600"]
    volumeMounts:
    - name: gcs-volume
      mountPath: /mnt/gcs
  volumes:
  - name: gcs-volume
    csi:
      driver: gcs.csi.storage.gke.io
      volumeAttributes:
        bucketName: "your-gcs-bucket"
        skipCSIBucketAccessCheck: "true"  # Important: Required for OIDC authentication
```

## Step 5: How It Works

### 5.1 Webhook Enhancement

When a pod with the `gke-gcsfuse/workload-identity-credential-configmap` annotation is created, the GCS FUSE CSI driver webhook:

1. **Parses the annotation** to identify the credential ConfigMap
2. **Reads the ConfigMap** to extract OIDC configuration details
3. **Injects volumes** for both the projected service account token and credential configuration
4. **Configures the gcsfuse sidecar** with:
   - Service account tokens mounted as volumes
   - The ConfigMap mounted as a volume
   - The `GOOGLE_APPLICATION_CREDENTIALS` environment variable set to the credential configuration file path

### 5.2 Sidecar Container Configuration

The injected gcsfuse sidecar container will have:

- **Environment Variables:**
  - `GOOGLE_APPLICATION_CREDENTIALS=/etc/workload-identity/credential-configuration.json`

- **Volume Mounts:**
  - Token volume mounted at the path specified in the credential configuration (e.g., `/var/run/service-account`)
  - ConfigMap volume mounted at `/etc/workload-identity`

- **Projected Token Volume:**
  - Audience set to match the workload identity pool provider
  - Token expiration of 3600 seconds
  - Automatic token refresh by Kubernetes

## Step 6: Verification and Troubleshooting

### 6.1 Verify Configuration

Check if the pod has been properly configured:

```bash
# Check pod annotations
kubectl get pod gcs-fuse-oidc-example -o yaml | grep annotations -A 5

# Check sidecar injection
kubectl get pod gcs-fuse-oidc-example -o yaml | grep -A 10 -B 5 "gke-gcsfuse-sidecar"

# Check volume mounts
kubectl describe pod gcs-fuse-oidc-example
```

### 6.2 Verify Token and Credentials

Exec into the sidecar container to verify credentials:

```bash
# Check if GOOGLE_APPLICATION_CREDENTIALS is set
kubectl exec gcs-fuse-oidc-example -c gke-gcsfuse-sidecar -- env | grep GOOGLE_APPLICATION_CREDENTIALS

# Check if credential files exist
kubectl exec gcs-fuse-oidc-example -c gke-gcsfuse-sidecar -- ls -la /etc/workload-identity/
kubectl exec gcs-fuse-oidc-example -c gke-gcsfuse-sidecar -- ls -la /var/run/service-account/

# Test authentication
kubectl exec gcs-fuse-oidc-example -c gke-gcsfuse-sidecar -- gcloud auth list
```

### 6.3 Check Logs

Monitor the webhook and sidecar logs for troubleshooting:

```bash
# Check webhook logs
kubectl logs -n kube-system deployment/gcs-fuse-csi-driver-webhook

# Check sidecar logs
kubectl logs gcs-fuse-oidc-example -c gke-gcsfuse-sidecar
```

### 6.4 Common Issues and Solutions

1. **ConfigMap not found:**
   - Ensure the ConfigMap exists in the same namespace as the pod
   - Verify the ConfigMap name in the annotation matches exactly

2. **Permission denied errors:**
   - Ensure the workload identity pool binding is correct

3. **Token authentication failures:**
   - Verify the audience in the credential configuration matches the workload identity pool provider
   - Check that the cluster OIDC issuer is properly configured
   - Ensure the token file path in the credential configuration is correct

4. **Sidecar not injected:**
   - Verify the webhook is running and healthy
   - Check that the pod has the required annotations

## Important Notes

- **Volume Attributes:** The `skipCSIBucketAccessCheck: "true"` volume attribute is required when using OIDC authentication
- **ConfigMap Security:** While credential configuration files don't contain private keys, they should still be treated as sensitive configuration
- **Token Refresh:** Kubernetes automatically handles token refresh for projected service account tokens

## Related Documentation

- [GCS FUSE CSI Driver Authentication](authentication.md)
- [Workload Identity Federation with Kubernetes](https://cloud.google.com/iam/docs/workload-identity-federation-with-kubernetes)
- [GCS FUSE CSI Driver Installation](installation.md)
- [Troubleshooting Guide](troubleshooting.md)

For additional support and troubleshooting, refer to the [troubleshooting documentation](troubleshooting.md) or create an issue in the [project repository](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver).
