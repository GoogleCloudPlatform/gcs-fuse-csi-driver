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

In order to let the Google Cloud Storage FUSE CSI Driver authenticate with GCP APIs, you need to do the following steps to make your Cloud Storage buckets accessible by your GKE cluster. See [GKE Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) for more information.

1. Create a GCP Service Account

   ```bash
    # Cloud Storage bucket project ID.
    GCS_BUCKET_PROJECT_ID=<gcs-bucket-project-id>
    # GCP service account name.
    GCP_SA_NAME=<gcp-service-account-name>
    # Create a GCP service account in the Cloud Storage bucket project.
    gcloud iam service-accounts create ${GCP_SA_NAME} --project=${GCS_BUCKET_PROJECT_ID}
    ```

2. Grant the Cloud Storage permissions to the GCP Service Account.

   The CSI driver requires the `storage.objects.list` permission to successfully mount Cloud Storage buckets. Select proper IAM role according to the doc [IAM roles for Cloud Storage](https://cloud.google.com/storage/docs/access-control/iam-roles) that are associated with proper permissions. Here are some recommendations for the IAM role selection:
   
   - For read-only workloads: `roles/storage.objectViewer`.
   - For read-write workloads: `roles/storage.objectAdmin`.
    
    ```bash
    # Specify a proper IAM role for Cloud Storage.
    STORAGE_ROLE=<cloud-storage-role>
    
    # Run the following command to apply permissions to a specific bucket.
    BUCKET_NAME=<gcs-bucket-name>
    gcloud storage buckets add-iam-policy-binding gs://${BUCKET_NAME} \
        --member "serviceAccount:${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com" \
        --role "${STORAGE_ROLE}"

    # Optionally, run the following command if you want to apply the permissions to all the Cloud Storage buckets in the project.
    # Choose "[2] None" for the binding condition.
    gcloud projects add-iam-policy-binding ${GCS_BUCKET_PROJECT_ID} \
        --member "serviceAccount:${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com" \
        --role "${STORAGE_ROLE}"
    ```

3. Create a Kubernetes Service Account.
    
    ```bash
    # Kubernetes namespace where your workload runs.
    K8S_NAMESPACE=<k8s-namespace>
    # Kubernetes service account name.
    K8S_SA_NAME=<k8s-sa-name>
    # Create a Kubernetes namespace and a service account.
    kubectl create namespace ${K8S_NAMESPACE}
    kubectl create serviceaccount ${K8S_SA_NAME} --namespace ${K8S_NAMESPACE}
    ```

    > Note: The Kubernetes Service Account needs to be in the same namespace where your workload runs.

4. Bind the the Kubernetes Service Account with the GCP Service Account.
    
    ```bash
    # GKE cluster project ID.
    CLUSTER_PROJECT_ID=<cluster-project-id>
    
    gcloud iam service-accounts add-iam-policy-binding ${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com \
        --role roles/iam.workloadIdentityUser \
        --member "serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SA_NAME}]"

    kubectl annotate serviceaccount ${K8S_SA_NAME} \
        --namespace ${K8S_NAMESPACE} \
        iam.gke.io/gcp-service-account=${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
    ```

5. Validate the service account setup (optional)

    - Check whether the GCP Service Account was created:
        
        ```bash
        gcloud iam service-accounts describe ${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
        ```
        
        The output should be like:

        ```
        email: ${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
        name: projects/${GCS_BUCKET_PROJECT_ID}/serviceAccounts/${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
        projectId: ${GCS_BUCKET_PROJECT_ID}
        ...
        ```
       
    - Check whether the GCP Service Account has correct IAM policy bindings:

        ```
        gcloud iam service-accounts get-iam-policy ${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
        ```

        The output should be like:

        ```
        bindings:
        - members:
            - serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SA_NAME}]
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
            - serviceAccount:${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
            role: roles/storage.objectViewer
       
       OR
       
       bindings:
       - members:
            - serviceAccount:${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
            role: roles/storage.objectAdmin
        ...
        ```

    - Check whether the Kubernetes Service Account was configured correctly:

        ```
        kubectl get serviceaccount ${K8S_SA_NAME} --namespace ${K8S_NAMESPACE} -o yaml
        ```

        The output should be like:

        ```yaml
        apiVersion: v1
        kind: ServiceAccount
        metadata:
        annotations:
            iam.gke.io/gcp-service-account: ${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
        name: ${K8S_SA_NAME}
        namespace: ${K8S_NAMESPACE}
        ```