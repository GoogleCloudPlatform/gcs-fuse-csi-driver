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

# GCSFuse Profiles Example

## Grant the GCSFuse CSI Controller access to your bucket:
```bash
GCS_PROJECT=your-bucket-project-id
BUCKET_NAME=your-bucket-name
PROJECT_ID=$(gcloud config get project)
PROJECT_NUMBER=$(gcloud projects describe ${PROJECT_ID} --format="value(projectNumber)")

# 1. Create a custom role in the bucket project for bucket-specific permissions
gcloud iam roles create gke.gcsfuse.profileUser \
  --project=${GCS_PROJECT} \
  --title="GCSFuse CSI Profile User" \
  --description="Allows scanning GCS buckets for objects and retrieving bucket metadata and the creation of Anywhere Caches." \
  --permissions="storage.objects.list,\
storage.buckets.get,\
storage.anywhereCaches.create,\
storage.anywhereCaches.get,\
storage.anywhereCaches.list,\
storage.anywhereCaches.update\
"

# 2. Bind the custom role to the GCSFuse CSI controller on the specific bucket
gcloud storage buckets add-iam-policy-binding gs://${BUCKET_NAME} \
  --project=${GCS_PROJECT} \
  --member="principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/gcs-fuse-csi-driver/sa/gcs-fuse-csi-controller-sa" \
  --role="projects/${GCS_PROJECT}/roles/gke.gcsfuse.profileUser"

# 3. Grant access needed to determine zones for anywherecaches:
gcloud projects add-iam-policy-binding ${PROJECT_ID} \
	    --member "principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/gcs-fuse-csi-driver/sa/gcs-fuse-csi-controller-sa" \
	    --role "roles/compute.viewer"
```

## Create the workload KSA and grant it permissions to your bucket:
```bash
kubectl create serviceaccount my-ksa

gcloud storage buckets add-iam-policy-binding gs://${BUCKET_NAME} \
    --member "principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/default/sa/my-ksa" \
    --role "roles/storage.objectUser"
```

## Apply the PV / PVC / Deployment:
```bash
kubectl apply -f examples/gcsfuse-profiles/pv-pvc-deployment.yaml
```