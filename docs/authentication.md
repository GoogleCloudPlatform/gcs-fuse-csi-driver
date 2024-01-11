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

# Configure access to Cloud Storage buckets using GKE Workload Identity

## Configure access

See the GKE documentation: [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#authentication)

## Validate the service account setup (optional)

- Make sure the Workload Identity feature is enabled on your cluster:

    ```bash
    gcloud container clusters describe ${CLUSTER_NAME} | grep workloadPool
    ```

    The output should be like:

    ```
    workloadPool: ${PROJECT_ID}.svc.id.goog
    ```

    If not, [have Workload Identity enabled](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#enable).

- Make sure the DaemonSet `gke-metadata-server` is running on your node pool:

    ```bash
    kubectl get daemonset gke-metadata-server -n kube-system
    ```

    The output should be like:

    ```
    NAME                  DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR                                                             AGE
    gke-metadata-server   3         3         3       3            3           beta.kubernetes.io/os=linux,iam.gke.io/gke-metadata-server-enabled=true   17d
    ```

    If not, [have GKE metadata server enabled on your node pool](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#migrate_applications_to).

- Check whether the GCP Service Account was created:
    
    ```bash
    gcloud iam service-accounts describe ${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    ```
    
    The output should be like:

    ```
    email: ${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    name: projects/${GSA_PROJECT}/serviceAccounts/${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    projectId: ${GSA_PROJECT}
    ...
    ```
    
- Check whether the GCP Service Account has correct IAM policy bindings:

    ```
    gcloud iam service-accounts get-iam-policy ${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    ```

    The output should be like:

    ```
    bindings:
    - members:
        - serviceAccount:${PROJECT_ID}.svc.id.goog[${NAMESPACE}/${KSA_NAME}]
        role: roles/iam.workloadIdentityUser
    ...
    ```

- Check whether the Cloud Storage bucket has correct IAM policy bindings:

    ```
    gcloud storage buckets get-iam-policy gs://${BUCKET_NAME}
    ```

    The output should be like:

    ```
    bindings:
    - members:
        - serviceAccount:${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
        role: roles/storage.objectViewer
    
    OR
    
    bindings:
    - members:
        - serviceAccount:${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
        role: roles/storage.objectAdmin
    ...
    ```

- Check whether the Kubernetes Service Account was configured correctly:

    ```
    kubectl get serviceaccount ${KSA_NAME} --namespace ${NAMESPACE} -o yaml
    ```

    The output should be like:

    ```yaml
    apiVersion: v1
    kind: ServiceAccount
    metadata:
    annotations:
        iam.gke.io/gcp-service-account: ${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    name: ${KSA_NAME}
    namespace: ${NAMESPACE}
    ```