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

# Cloud Storage FUSE CSI Driver Usage

> NOTE: This documentation is basically the same as the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver). In case of discrepancy, the GKE documentation shall prevail.

## Before you begin

### Enable the CSI driver

See the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#enable).

If you need to manually install the CSI driver, refer to the documentation [Cloud Storage FUSE CSI Driver Installation](./installation.md).

> WARNING: Manual deployment of this driver to your GKE cluster is not recommended. Instead users should use GKE to automatically deploy and manage the CSI driver as an add-on feature.

### Enable Workload Identity on GKE cluster

The CSI driver depends on the Workload Identity feature to authenticate with GCP APIs. Follow the doc to [Enable Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#enable) on your GKE cluster.

### Create Cloud Storage buckets

Refer to the documentation [Create Cloud Storage buckets](https://cloud.google.com/storage/docs/creating-buckets).

In order to have better performance and higher throughput, please set the `Location type` as `Region`, and select a region where your GKE cluster is running.

### Set up access to Cloud Storage buckets via GKE Workload Identity

In order to let the CSI driver authenticate with GCP APIs, you will need to do the following steps to make your Cloud Storage buckets accessible by your GKE cluster. See [GKE Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) for more information.

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

   The CSI driver requires `storage.objects.list` and `storage.buckets.get` permissions to successfully mount Cloud Storage buckets. Select proper IAM role according to the doc [IAM roles for Cloud Storage](https://cloud.google.com/storage/docs/access-control/iam-roles) that are associated with proper permissions. Here are some recommendations for the IAM role selection:
   
   - For read-only workloads: `roles/storage.insightsCollectorService` and `roles/storage.objectViewer`.
   - For read-write workloads: `roles/storage.insightsCollectorService` and `roles/storage.objectAdmin`.
   - Note that the following commands can be used multiple times to add IAM policy bindings for different roles to one Cloud Storage bucket or project.
    
    ```bash
    # Specify a proper IAM role for Cloud Storage.
    STORAGE_ROLE=<cloud-storage-role>
    
    # Run the following command to apply permissions to a specific bucket.
    BUCKET_NAME=<gcs-bucket-name>
    gcloud storage buckets add-iam-policy-binding gs://$BUCKET_NAME \
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

## Limitations

* The Cloud Storage FUSE file system has [differences in performance, availability, access authorization, and semantics](https://cloud.devsite.corp.google.com/storage/docs/gcs-fuse#differences-and-limitations) compared to a POSIX file system.

* The Cloud Storage FUSE CSI driver is not supported on [GKE Sandbox](https://cloud.google.com/kubernetes-engine/docs/concepts/sandbox-pods).

* The Cloud Storage FUSE CSI driver does not support volume snapshots, volume cloning, or volume expansions.

* See the [open issues](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues) in the Cloud Storage FUSE CSI driver GitHub project.

## Using the Cloud Storage FUSE CSI driver

The Cloud Storage FUSE CSI driver allows developers to use standard Kubernetes API to consume pre-existing Cloud Storage buckets. There are two types of volume configuration supported:
1. [Static Provisioning](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#static) using a PersistentVolumeClaim bound to the PersistentVolume
2. Using [CSI Ephemeral Inline volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#csi-ephemeral-volumes)

The Cloud Storage FUSE CSI driver natively supports the above volume configuration methods. Currently, the driver does not support [Dynamic Volume Provisioning](https://kubernetes.io/docs/concepts/storage/dynamic-provisioning/).

### Prepare to mount Cloud Storage FUSE buckets

No matter which configuration method you choose, the CSI driver relies on Pod annotations to identify if the Pod uses Cloud Storage backed volumes. The CSI driver employs a mutating admission webhook controller to monitor all the Pod specs. If the proper Pod annotations are found, the webhook will inject a sidecar container into the workload Pod. The CSI driver uses [Cloud Storage FUSE](https://cloud.google.com/storage/docs/gcs-fuse) to mount Cloud Storage buckets, and the fuse instances run inside the sidecar containers.

In order to let the webhook identify the Cloud Storage backed volumes, the annotation `gke-gcsfuse/volumes: "true"` is required. By default, the sidecar container is configured with the following resources:

* 250m CPU
* 256Mi memory
* 5Gi ephemeral storage
 
You can overwrite these values by specifying the annotation `gke-gcsfuse/[cpu-limit|memory-limit|ephemeral-storage-limit]`. For example:

```yaml
apiVersion: v1
kind: Pod
metadata:
  annotations:
    gke-gcsfuse/volumes: "true" # required
    gke-gcsfuse/cpu-limit: 500m # optional
    gke-gcsfuse/memory-limit: 100Mi # optional
    gke-gcsfuse/ephemeral-storage-limit: 50Gi # optional
```
Use the following considerations when deciding the amount of resources to allocate:

* For the CPU limit, you may want to allocate more CPU resource to the sidecar container if your workload requires high throughput. See [Cloud Storage FUSE Performance Benchmarks](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/benchmarks.md) for more details.

* For the memory limit, since Cloud Storage FUSE consumes memory for object meta data caching, when the workloads need to process a large amount of files, it is required to increase the sidecar container memory allocation. Note that the memory consumption is proportional to the file number but not file size, as Cloud Storage FUSE currently does not cache the file content. You also have options to tune the caching behavior, see the [Cloud Storage FUSE caching documentation](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/semantics.md#caching) for more details. 

* For the ephemeral storage limit, as Cloud Storage FUSE will stage files in a local temporary directory when write operation happens, you need to estimate how much free space needed to handle staged content when writing large files, and increase the sidecar container ephemeral storage limit accordingly. See [Cloud Storage FUSE documentation](https://github.com/GoogleCloudPlatform/gcsfuse#downloading-object-contents) for more details.

> Note: The sidecar container resource configuration may be overridden on Autopilot clusters. See [Resource requests in Autopilot](https://cloud.google.com/kubernetes-engine/docs/concepts/autopilot-resource-requests) for more details.

> Note: You need to put the annotations under Pod `metadata` field. If the volumes are consumed by other Kubernetes workload types (for instance, [Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/), [Deployment](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/), or [StatefulSet](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/)), please make sure the annotations are configured under the `spec.template.metadata.annotations` field.

After your workload Pod is scheduled successfully, you should see the following container is injected into your workload Pod spec.

```yaml
containers:
- name: gke-gcsfuse-sidecar
  image: my-registry/gcs-fuse-csi-driver-sidecar-mounter:xxx
  resources:
    limits:
      cpu: 500m
      ephemeral-storage: 50Gi
      memory: 100Mi
    requests:
      cpu: 500m
      ephemeral-storage: 50Gi
      memory: 100Mi
  securityContext:
    allowPrivilegeEscalation: false
    capabilities:
      drop:
      - all
    readOnlyRootFilesystem: true
    runAsGroup: 65534
    runAsNonRoot: true
    runAsUser: 65534
    seccompProfile:
      type: RuntimeDefault
  volumeMounts:
  - mountPath: /gcsfuse-tmp
    name: gke-gcsfuse-tmp
```

### Provision your volume as a CSI ephemeral volume

By using the CSI Ephemeral Inline volumes, the lifecycle of volumes backed by Cloud Storage buckets is tied with the Pod lifecycle. After the Pod termination, there is no need to maintain independent PV/PVC objects to represent Cloud Storage backed volumes.

> Note: Using CSI Ephemeral Inline volumes does not mean the CSI driver will create or delete the Cloud Storage buckets. You need to manually create the buckets before use, and manually delete the buckets after use if needed.

Use the following YAML manifest to specify the Cloud Storage bucket directly on the Pod spec.

- `spec.terminationGracePeriodSeconds`: if your application needs to write large files to the Cloud Storage bucket, increase the Pod termination grace period seconds to make sure the Cloud Storage FUSE has enough time to flush the data after your application exits. The sidecar container respects the Pod termination grace period and handles the `SIGTERM` signal. The Cloud Storage FUSE termination is delayed until the sidecar container receives the `SIGKILL` signal. See [Kubernetes best practices: terminating with grace](https://cloud.google.com/blog/products/containers-kubernetes/kubernetes-best-practices-terminating-with-grace) for more information.
- `spec.serviceAccountName`: use the same Kubernetes service account in the [Cloud Storage bucket access setup step](#set-up-access-to-cloud-storage-buckets-via-gke-workload-identity).
- `spec.volumes[n].csi.driver`: use `gcsfuse.csi.storage.gke.io` as the csi driver name.
- `spec.volumes[n].csi.volumeAttributes.bucketName`: specify your Cloud Storage bucket name. You can pass an underscore `_` to mount all the buckets that the service account is configured to have access to. See [Dynamic Mounting](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/mounting.md#dynamic-mounting) for more information.
- `spec.volumes[n].csi.volumeAttributes.mountOptions`: pass flags to Cloud Storage FUSE. Put all the flags in one string separated by a comma `,` without spaces. See [Cloud Storage FUSE Mount Flags](#cloud-storage-fuse-mount-flags) to learn about available mount flags.
- `spec.volumes[n].csi.readOnly`: pass `true` if all the volume mounts are read-only.
- `spec.containers[n].volumeMounts[m].readOnly`: pass `true` if only specific volume mount is read-only.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gcs-fuse-csi-example-ephemeral
  namespace: my_namespace
  annotations:
    gke-gcsfuse/volumes: "true" # required
    gke-gcsfuse/cpu-limit: 500m # optional
    gke-gcsfuse/memory-limit: 100Mi # optional
    gke-gcsfuse/ephemeral-storage-limit: 50Gi # optional
spec:
  terminationGracePeriodSeconds: 60 # optional, 30 by default
  securityContext: # optional, if Pod does not use the root user
    runAsUser: 1001
    runAsGroup: 2002
    fsGroup: 3003
  containers:
  - image: busybox
    name: busybox
    command: ["sleep"]
    args: ["infinity"]
    volumeMounts:
    - name: gcs-fuse-csi-ephemeral
      mountPath: /data
      readOnly: true # optional, if this specific volume mount is read-only
  serviceAccountName: my_k8s_sa
  volumes:
  - name: gcs-fuse-csi-ephemeral
    csi:
      driver: gcsfuse.csi.storage.gke.io
      readOnly: true # optional, if all the volume mounts are read-only
      volumeAttributes:
        bucketName: my-bucket-name
        mountOptions: "implicit-dirs,uid=1001,gid=3003" # optional
```

### Provision your volume using static provisioning

There are two ways to bind a `PersistentVolumeClaim` to a specific `PersistentVolume`.

1. A `PersistentVolume` can be bound to a `PersistentVolumeClaim` using `claimRef` on the `PersistentVolume` spec.
2. A `PersistentVolumeClaim` can bind a `PersistentVolume` using `volumeName` on the `PersistentVolumeClaim` spec.

To bind a PersistentVolume to a PersistentVolumeClaim, the `storageClassName` of the two resources must match, as well as `capacity` and `accessModes`. You can omit the storageClassName, but you must specify `""` to prevent Kubernetes from using the default StorageClass. The `storageClassName` does not need to refer to an existing StorageClass object. If all you need is to bind the claim to a volume, you can use any name you want. Since Cloud Storage buckets do not have size limits, you can put any number for `capacity`, but it cannot be empty.

The Cloud Storage FUSE CSI Driver supports `ReadWriteOnce`, `ReadOnlyMany`, and `ReadWriteMany` access modes. See [Kubernetes Access Modes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#access-modes) for more details.

1. Create a PersistentVolume using the following YAML manifest.
   
   - `spec.csi.driver`: use `gcsfuse.csi.storage.gke.io` as the csi driver name.
   - `spec.csi.volumeHandle`: specify your Cloud Storage bucket name. You can pass an underscore `_` to mount all the buckets that the service account is configured to have access to. See [Dynamic Mounting](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/mounting.md#dynamic-mounting) for more information.
   - `spec.mountOptions`: pass flags to Cloud Storage FUSE. See [Cloud Storage FUSE Mount Flags](#cloud-storage-fuse-mount-flags) to learn about available mount flags.
   - `spec.csi.readOnly`: pass `true` if all the volume mounts are read-only.
   
    ```yaml
    apiVersion: v1
    kind: PersistentVolume
    metadata:
      name: gcs-fuse-csi-pv
    spec:
      accessModes:
      - ReadWriteMany
      capacity:
        storage: 5Gi
      storageClassName: dummy-storage-class
      claimRef:
        namespace: my_namespace
        name: gcs-fuse-csi-static-pvc
      mountOptions: # optional
        - implicit-dirs
        - uid=1001
        - gid=3003
      csi:
        driver: gcsfuse.csi.storage.gke.io
        volumeHandle: my-bucket-name
        readOnly: true # optional, if all the volume mounts are read-only
    ```

2. Create a PersistentVolumeClaim using the following YAML manifest.
    
    ```yaml
    apiVersion: v1
    kind: PersistentVolumeClaim
    metadata:
      name: gcs-fuse-csi-static-pvc
      namespace: my_namespace
    spec:
      accessModes:
      - ReadWriteMany
      resources:
        requests:
          storage: 5Gi
      volumeName: gcs-fuse-csi-pv
      storageClassName: dummy-storage-class
    ```

3. Use the PersistentVolumeClaim in a Pod.
  
   - `spec.serviceAccountName`: use the same Kubernetes service account in the [Cloud Storage bucket access setup step](#set-up-access-to-cloud-storage-buckets-via-gke-workload-identity).
   - `spec.containers[n].volumeMounts[m].readOnly`: pass `true` if only specific volume mount is read-only.
    
    ```yaml
    apiVersion: v1
    kind: Pod
    metadata:
      name: gcs-fuse-csi-example-static-pvc
      namespace: my_namespace
      annotations:
        gke-gcsfuse/volumes: "true" # required
        gke-gcsfuse/cpu-limit: 500m # optional
        gke-gcsfuse/memory-limit: 100Mi # optional
        gke-gcsfuse/ephemeral-storage-limit: 50Gi # optional
    spec:
      securityContext: # optional, if Pod does not use the root user
        runAsUser: 1001
        runAsGroup: 2002
        fsGroup: 3003
      containers:
      - image: busybox
        name: busybox
        command: ["sleep"]
        args: ["infinity"]
        volumeMounts:
        - name: gcs-fuse-csi-static
          mountPath: /data
          readOnly: true # optional, if this specific volume mount is read-only
      serviceAccountName: my_k8s_sa
      volumes:
      - name: gcs-fuse-csi-static
        persistentVolumeClaim:
          claimName: gcs-fuse-csi-static-pvc
    ```

## Cloud Storage FUSE Mount Flags

When the Cloud Storage FUSE is initiated, the following flags are passed to the Cloud Storage FUSE binary, and these flags cannot be overwritten:

- app-name=gke-gcs-fuse-csi
- temp-dir=/gcsfuse-tmp/.volumes/{volume-name}/temp-dir
- foreground
- log-file=/dev/fd/1
- log-format=text

The following flags are disallowed to be passed to the Cloud Storage FUSE binary:

- key-file
- token-url
- reuse-token-from-url
- o

> Note: Passing flags via `-o` to Cloud Storage FUSE is not allowed. For read-only volumes, please follow the instructions above to configure the `readOnly` field.

All the other flags defined in the [Cloud Storage FUSE documentation](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/mounting.md#full-list-of-mount-options) are allowed. You can pass the flags via `spec.mountOptions` on a `PersistentVolume` manifest, or `spec.volumes[n].csi.volumeAttributes.mountOptions` if you are using the CSI Ephemeral Inline volumes.

Specifically, you may consider passing the following flags as needed:

- To use an existing directory, simulated by a "/" prefix, that was not originally created by Cloud Storage FUSE, pass the `implicit-dirs` flag. Otherwise, you will need to explicitly create the directory using `mkdir` before you see it in the local filesystem. See the Files and Directories section under the [Semantics documentation](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/semantics.md#implicit-directories) for more details.
- If you are using a [Security Context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/) for your Pod or container to use a non-root user and fsGroup, or your container image uses a non-root user and fsGroup, you will need to pass the uid and gid to Cloud Storage FUSE using the flag `uid` and `gid`.
- If you only want to mount a directory in the bucket instead of the entire bucket, pass the directory relative path via the flag `only-dir=relative/path/to/the/bucket/root`.
- To tune Cloud Storage FUSE caching behavior, refer to [Caching](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/semantics.md#caching) in the Cloud Storage FUSE GitHub documentation.
- If you need to troubleshoot Cloud Storage FUSE issues, pass the flags `debug_fuse`, `debug_fs`, and `debug_gcs`.

## Workload Examples

The following YAML manifest shows a Job example consuming a Cloud Storage bucket. Before you run the example workload, replace the namespace, service account name and bucket name.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: gcs-fuse-csi-job-example
  namespace: my_namespace
spec:
  template:
    metadata:
      annotations:
        gke-gcsfuse/volumes: "true"
    spec:
      serviceAccountName: my_k8s_sa
      containers:
      - name: writer
        image: busybox
        command:
          - "/bin/sh"
          - "-c"
          - touch /data/test && echo $(date) >> /data/test && sleep 10
        volumeMounts:
        - name: gcs-fuse-csi-ephemeral
          mountPath: /data
      - name: reader
        image: busybox
        command:
          - "/bin/sh"
          - "-c"
          - sleep 10 && cat /data/test
        volumeMounts:
        - name: gcs-fuse-csi-ephemeral
          mountPath: /data
          readOnly: true
      volumes:
      - name: gcs-fuse-csi-ephemeral
        csi:
          driver: gcsfuse.csi.storage.gke.io
          volumeAttributes:
            bucketName: my-bucket-name
      restartPolicy: Never
  backoffLimit: 1
```

After about 30 seconds, the Job will succeed. The container `reader` log should show a timestamp. The sidecar container `gke-gcsfuse-sidecar` log should include a line `all the other containers exited in the Job Pod, exiting the sidecar container` indicating that the sidecar container exited as expected. 

See the documentation [Example Applications](../examples/README.md) for more examples.

## Common Issues

### I/O errors in your workloads

- Error `Transport endpoint is not connected` in workload Pods.
  
  This error is due to Cloud Storage FUSE termination. In most cases, Cloud Storage FUSE was terminated because of OOM. Please use the Pod annotations `gke-gcsfuse/[cpu-limit|memory-limit|ephemeral-storage-limit]` to allocate more resources to Cloud Storage FUSE (the sidecar container). Note that the only way to fix this error is to restart your workload Pod.

- Error `Permission denied` in workload Pods.
  
  Cloud Storage FUSE does not have permission to access the file system.
  
  Please double check your container user and fsGroup. Make sure you pass `uid` and `gid` flags correctly. See [Cloud Storage FUSE Mount Flags](#cloud-storage-fuse-mount-flags) for more details.
  
  Please double check your service account setup. See [Set up access to Cloud Storage buckets via GKE Workload Identity](#set-up-access-to-cloud-storage-buckets-via-gke-workload-identity) for more details.

### Pod event warnings

If your workload Pods cannot start up, please run `kubectl describe pod <your-pod-name> -n <your-namespace>` to check the Pod events. Find the troubleshooting guide below according to the Pod event.

- Pod event warning: `MountVolume.MountDevice failed for volume "xxx" : kubernetes.io/csi: attacher.MountDevice failed to create newCsiDriverClient: driver name gcsfuse.csi.storage.gke.io not found in the list of registered CSI drivers`, or Pod event warning: `MountVolume.SetUp failed for volume "xxx" : kubernetes.io/csi: mounter.SetUpAt failed to get CSI client: driver name gcsfuse.csi.storage.gke.io not found in the list of registered CSI drivers`

  This warning indicates that the CSI driver is not enabled, or the CSI driver is not up and running. Please double check if the CSI driver is enabled on your cluster. See [Enable the CSI driver](#enable-the-csi-driver) for details. If the CSI is enabled, on each node you should see a Pod called `gcsfusecsi-node-xxxxx` up and running. If the cluster was just scaled, updated, or upgraded, this warning is normal and should be transient because it takes a few minutes for the CSI driver Pods to be functional after the cluster operations.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Unauthenticated desc = failed to prepare storage service: storage service manager failed to setup service: timed out waiting for the condition`

  After you follow the [service account setup steps](#set-up-access-to-gcs-buckets-via-gke-workload-identity) to configure the Kubernetes service account, it usually takes a few minutes for the credentials being propagated. Whenever the credentials are propagated into the Kubernetes cluster, this warning will disappear, and your Pod scheduling should continue. If you still see this warning after 5 minutes, please double check the [service account setup steps](#set-up-access-to-gcs-buckets-via-gke-workload-identity) to make sure your Kubernetes service account is set up correctly. Make sure your workload Pod is using the Kubernetes service account in the same namespace.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = PermissionDenied desc = failed to get GCS bucket "xxx": googleapi: Error 403: xxx@xxx.iam.gserviceaccount.com does not have storage.objects.list access to the Google Cloud Storage bucket. Permission 'storage.objects.list' denied on resource (or it may not exist)., forbidden`
   
  Please double check the [service account setup steps](#set-up-access-to-gcs-buckets-via-gke-workload-identity) to make sure your Kubernetes service account is set up correctly. Make sure your workload Pod is using the Kubernetes service account in the same namespace.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = NotFound desc = failed to get GCS bucket "xxx": storage: bucket doesn't exist`
   
  The Cloud Storage bucket does not exist. Make sure the Cloud Storage bucket is created, and the Cloud Storage bucket name is specified correctly.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = FailedPrecondition desc = failed to find the sidecar container in Pod spec`
   
  The Cloud Storage FUSE sidecar container was not injected. Please check the Pod annotation `gke-gcsfuse/volumes: "true"` is set correctly.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = InvalidArgument desc = the sidecar container failed with error: Incorrect Usage. flag provided but not defined: -xxx`

  Invalid mount flags are passed to Cloud Storage FUSE. Please check [Cloud Storage FUSE Mount Flags](#cloud-storage-fuse-mount-flags) for more details.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = ResourceExhausted desc = the sidecar container failed with error: signal: killed`

  The gcsfuse process was killed, which is usually caused by OOM. Please consider increasing the sidecar container memory limit by using the annotation `gke-gcsfuse/memory-limit`.

- Other Pod event warnings: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = xxx` or `UnmountVolume.TearDown failed for volume "xxx" : rpc error: code = Internal desc = xxx`
  
  Warnings that are not listed above and include a rpc error code `Internal` mean that other unexpected issues occurred in the CSI driver, please create a [new issue](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/new) on the GitHub project page. Please include your workload information as detailed as possible, and the Pod event warning in the issue.
