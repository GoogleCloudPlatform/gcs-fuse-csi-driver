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

# Configure access to Cloud Storage buckets using GKE Workload Identity Federation

## Configure access

See the GKE documentation: [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#authentication)

## Troubleshooting Steps

If you run into permission problems, try these troubleshooting steps.

- [Uniform bucket-level access](https://cloud.google.com/storage/docs/uniform-bucket-level-access) is required for read-write workloads when using Workload Identity Federation. Make sure the bucket Permissions Access control is `Uniform`.
- Before v1.17.2 release, the Cloud Storage FUSE CSI driver does not support Pods running on the [host network](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-v1/#hosts-namespaces) (hostNetwork: true) due to [restrictions of Workload Identity Federation for GKE](https://cloud.google.com/kubernetes-engine/docs/concepts/workload-identity#restrictions). Make sure the `hostNetwork` is set to `false`.
- OSS driver users can opt-in to host network pod ksa support after v1.17.2 release. To do so, 
    1. Make sure your sidecar image is based on  v1.17.2+ (https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/tree/v1.17.2).
    2. Make sure your cluster is on or after gke version 1.29
    3. Granting the host network enabled pod's Kubernetes service account access to the bucket following: https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-setup#authentication.
    4. Provide the following volume attributes in hostnetwork enabled pods:
    ```
    volumeAttributes:
        bucketName: test-bucket
        hostNetworkPodKSA: "true"
	    identityProvider: "https://container.googleapis.com/v1/projects/<project-id>/locations/<location>/clusters/<cluster>"
    ```
- If you set `runAsUser` or `runAsGroup` in [Security Context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/) for your Pod or container, or if your container image uses a non-root user or group, you must set the `uid` and `gid` mount flags. You also need to use the `file-mode` and `dir-mode` mount flags to set the file system permissions. For example, set CSI inline volume `mountOptions` to `"uid=1001,gid=2002,file-mode=664,dir-mode=775"`.
- If you set `fsGroup` in [Security Context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/) for your Pod, you don't need to use the `file-mode` and `dir-mode` mount flags. These flags are automatically added by the [CSI fsGroup delegation feature](https://kubernetes-csi.github.io/docs/support-fsgroup.html#delegate-fsgroup-to-csi-driver).
- Double check the Workload Identity Federation setup following the below steps.

## Validate Workload Identity Federation and Kubernetes ServiceAccount setup

- Make sure the Workload Identity Federation feature is enabled on your cluster:

    ```bash
    gcloud container clusters describe ${CLUSTER_NAME} | grep workloadPool
    ```

    The output should be like:

    ```text
    workloadPool: ${PROJECT_ID}.svc.id.goog
    ```

    If not, [have Workload Identity Federation enabled](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#enable_on_clusters_and_node_pools).

- Make sure the DaemonSet `gke-metadata-server` is running on your node pool:

    ```bash
    kubectl get daemonset gke-metadata-server -n kube-system
    ```

    The output should be like:

    ```text
    NAME                  DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR                                                             AGE
    gke-metadata-server   3         3         3       3            3           beta.kubernetes.io/os=linux,iam.gke.io/gke-metadata-server-enabled=true   17d
    ```

    If not, [have GKE metadata server enabled on your node pool](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#migrate_applications_to).

- Check whether the Kubernetes ServiceAccount was created correctly:

    ```bash
    kubectl get serviceaccount ${KSA_NAME} --namespace ${NAMESPACE}
    ```

    The output should be like:

    ```text
    NAME          SECRETS   AGE
    ${KSA_NAME}   0         64m
    ```

    If not, create a namespace and Kubernetes ServiceAccount accordingly. Make sure your workload runs in the same Kubernetes namespace using the Kubernetes ServiceAccount.

- Check whether the Cloud Storage bucket has correct IAM policy bindings:

    ```bash
    gcloud storage buckets get-iam-policy gs://${BUCKET_NAME}
    ```

    The output should be like:

    ```text
    bindings:
    - members:
        - principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/${NAMESPACE}/sa/${KSA_NAME}
        role: roles/storage.objectViewer
    
    OR
    
    bindings:
    - members:
        - principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/${NAMESPACE}/sa/${KSA_NAME}
        role: roles/storage.objectUser
    ...
    ```

    If not, follow the GKE documentation to grant one of the [IAM roles for Cloud Storage](https://cloud.devsite.corp.google.com/storage/docs/access-control/iam-roles) to the Kubernetes ServiceAccount.

## Validate the GCP and Kubernetes ServiceAccount setup (deprecated)

> Note: Workload Identity Federation for GKE simplified configuration steps in GKE documentation: [Configure applications to use Workload Identity Federation for GKE](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#authenticating_to). Previously, the Workload Identity configuration involves extra steps to [link Kubernetes ServiceAccounts to IAM](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#kubernetes-sa-to-iam), such as GCP Service Account (GSA) creation and Kubernetes ServiceAccount (KSA) configuration. With the new Workload Identity Federation for GKE feature, these steps are no longer required. This section provides validation steps for the users who still use the old configurations.

- Make sure the Workload Identity Federation feature is enabled on the GKE cluster and node pools

    Follow the first two steps in the previous section.

- Check whether the GCP Service Account was created:

    ```bash
    gcloud iam service-accounts describe ${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    ```

    The output should be like:

    ```text
    email: ${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    name: projects/${GSA_PROJECT}/serviceAccounts/${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    projectId: ${GSA_PROJECT}
    ...
    ```

- Check whether the GCP Service Account has correct IAM policy bindings:

    ```bash
    gcloud iam service-accounts get-iam-policy ${GSA_NAME}@${GSA_PROJECT}.iam.gserviceaccount.com
    ```

    The output should be like:

    ```text
    bindings:
    - members:
        - serviceAccount:${PROJECT_ID}.svc.id.goog[${NAMESPACE}/${KSA_NAME}]
        role: roles/iam.workloadIdentityUser
    ...
    ```

- Check whether the Cloud Storage bucket has correct IAM policy bindings:

    ```bash
    gcloud storage buckets get-iam-policy gs://${BUCKET_NAME}
    ```

    The output should be like:

    ```text
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

- Check whether the Kubernetes ServiceAccount was configured correctly:

    ```bash
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
