# Cloud Storage FUSE CSI Driver Usage

## Before you begin

### Enable Workload Identity on GKE cluster

The CSI driver depends on the Workload Identity feature to authenticate with GCP APIs. Follow the doc to [Enable Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#enable) on your GKE cluster.

### Install the CSI driver

Refer to the documentation [Cloud Storage FUSE CSI Driver Installation](./installation.md) to manually install the CSI driver.

> Note: We are actively working on integrating the CSI driver with GKE service to make it a GKE managed add-on feature. After the integration work is done, manual installation will not be needed. The instruction will be provided afterwards.

### Create Cloud Storage buckets

Refer to the documentation [Create Cloud Storage buckets](https://cloud.google.com/storage/docs/creating-buckets).

In order to have better performance and higher throughput, please set the `Location type` as `Region`, and select a region where your GKE cluster is running.

### Set up access to Cloud Storage buckets via GKE Workload Identity

In order to let the CSI driver authenticate with GCP APIs, you will need to do the following steps to make your Cloud Storage buckets accessible by your GKE cluster. See [GKE Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) for more information.

1. Create a GCP Service Account

   ```bash
    # The ID of the project where your Cloud Storage bucket lives.
    GCS_BUCKET_PROJECT_ID=<gcs-bucket-project-id>
    # The name of the GCP service account. The service account should be in the Cloud Storage bucket project.
    GCP_SA_NAME=<gcp-service-account-name>
    # The ID of the project where your GKE cluster lives.
    CLUSTER_PROJECT_ID=<cluster-project-id>
    
    gcloud iam service-accounts create ${GCP_SA_NAME} --project=${GCS_BUCKET_PROJECT_ID}
    ```

2. Grant the Cloud Storage permissions to the GCP Service Account. Select a proper IAM role according to the doc [IAM roles for Cloud Storage](https://cloud.google.com/storage/docs/access-control/iam-roles).
    
    ```bash
    # Specify a proper IAM role for Cloud Storage.
    STORAGE_ROLE=<cloud-storage-role>
    
    # Choose "[2] None" for binding condition if you want to apply the permissions to all the Cloud Storage buckets in the project.
    gcloud projects add-iam-policy-binding ${GCS_BUCKET_PROJECT_ID} \
        --member "serviceAccount:${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com" \
        --role "${STORAGE_ROLE}"

    # Optionally, run the following command if you only want to apply permissions to a specific bucket.
    BUCKET_NAME=<gcs-bucket-name>
    gcloud projects add-iam-policy-binding ${GCS_BUCKET_PROJECT_ID} \
        --member "serviceAccount:${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com" \
        --role "${STORAGE_ROLE}" \
        --condition="expression=resource.name.startsWith(\"projects/_/buckets/$BUCKET_NAME\"),title=access-to-$BUCKET_NAME"
    ```

3. Create a Kubernetes Service Account.
    
    ```bash
    # a new Kubernetes namespace
    K8S_NAMESPACE=<k8s-namespace>
    # Kubernetes SA name
    K8S_SA_NAME=<k8s-sa-name>

    kubectl create namespace ${K8S_NAMESPACE}
    kubectl create serviceaccount ${K8S_SA_NAME} --namespace ${K8S_NAMESPACE}
    ```

    > Note: The Kubernetes Service Account needs to be in the same namespace where your workload runs.

4. Bind the the Kubernetes Service Account with the GCP Service Account.
    
    ```bash
    gcloud iam service-accounts add-iam-policy-binding ${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com \
        --role roles/iam.workloadIdentityUser \
        --member "serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SA_NAME}]"

    kubectl annotate serviceaccount ${K8S_SA_NAME} \
        --namespace ${K8S_NAMESPACE} \
        iam.gke.io/gcp-service-account=${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
    ```

## Using the Cloud Storage FUSE CSI driver

The Cloud Storage FUSE CSI driver allows developers to use standard Kubernetes API to consume pre-existing Cloud Storage buckets. There are two types of volume configuration supported:
1. [Static Provisioning](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#static) using a PersistentVolumeClaim bound to the PersistentVolume
2. Using [CSI Ephemeral Inline volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#csi-ephemeral-volumes)

The Cloud Storage FUSE CSI driver natively supports the above volume configuration methods. Currently, the [Dynamic Provisioning](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#dynamic) is under development and not officially supported.

### Pod annotations and sidecar container

No matter which configuration method you choose, the CSI driver relies on Pod annotations to identify if the Pod uses Cloud Storage backed volumes. The CSI driver employs a mutating admission webhook controller to monitor all the Pod specs. If the proper Pod annotations are found, the webhook will inject a sidecar container into the workload Pod. The CSI driver uses [Cloud Storage FUSE](https://cloud.google.com/storage/docs/gcs-fuse) to mount Cloud Storage buckets, and the fuse instances run inside the sidecar containers.

In order to let the webhook identify the Cloud Storage backed volumes, the annotation `gke-gcsfuse/volumes: "true"` is required. By default, the sidecar container has **250m CPU, 256Mi memory, and 10Gi ephemeral storage** allocated. You can overwrite these values by specifying the annotation `gke-gcsfuse/[cpu-limit|memory-limit|ephemeral-storage-limit]`. For example:

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

To decide how much resource is needed, here are some suggestions:

1. High throughput workloads require more CPU allocated to the sidecar container. Our benchmarking test results indicate that, with `10 core` CPU specified, Cloud Storage FUSE can provide approximately 3 GB/s bandwidth for sequential read and write on files larger than 3 MB with 40 threads. See the [FIO test configuration file](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/perfmetrics/scripts/job_files/seq_rand_read_write.fio) for more details.

2. Since Cloud Storage FUSE consumes memory for object meta data caching, when the workloads need to process a large amount of files, it is required to increase the sidecar container memory allocation. Our application test shows that, Cloud Storage FUSE consumes approximately 2.5 GB memory for caching 2 million files' meta data in memory. Note that the memory consumption is proportional to the file number but not file size, as Cloud Storage FUSE currently does not cache the file content. Users also have options to tune the caching behavior, see the [Cloud Storage FUSE caching documentation](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/semantics.md#caching) for more details. 

3. For write operations, Cloud Storage FUSE prepares the files in a local tmp directory before the files are uploaded to the Cloud Storage bucket. Users need to estimate how large files their workloads may write to the Cloud Storage bucket, and increase the sidecar container ephemeral storage limit accordingly.

> Note: The sidecar container resource configuration may be overridden on Autopilot clusters. See [Resource requests in Autopilot](https://cloud.google.com/kubernetes-engine/docs/concepts/autopilot-resource-requests) for more details.

> Note: Users need to put the annotations under Pod `metadata` field. If the volumes are consumed by other Kubernetes workload type, for instance, [Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/) or [Deployment](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/), please make sure the annotations are configured under `spec.template.metadata` field.

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

### Using CSI Ephemeral Inline volumes

By using the CSI Ephemeral Inline volumes, the lifecycle of volumes backed by Cloud Storage buckets is tied with the Pod lifecycle. After the Pod termination, there is no need to maintain independent PV/PVC objects to represent Cloud Storage backed volumes.

> Note: Using CSI Ephemeral Inline volumes does not mean the CSI driver will create or delete the Cloud Storage buckets. Users need to manually create the buckets before use, and manually delete the buckets after use if needed.

Use the following YAML manifest to specify the Cloud Storage bucket directly on the Pod spec.

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
  securityContext: # optional, if Pod does not use the root user
    runAsUser: 1001
    runAsGroup: 2002
    fsGroup: 3003
  containers:
  - image: nginx
    name: nginx
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
        mountOptions: "uid=1001,gid=3003,debug_fuse" # optional
```

### Using a PersistentVolumeClaim bound to the PersistentVolume

There are two ways to bind a `PersistentVolumeClaim` to a specific `PersistentVolume`.

1. A `PersistentVolume` can be bound to a `PersistentVolumeClaim` using `claimRef` on the `PersistentVolume` spec.
2. A `PersistentVolumeClaim` can bind a `PersistentVolume` using `volumeName` on the `PersistentVolumeClaim` spec.

To bind a PersistentVolume to a PersistentVolumeClaim, the `storageClassName` of the two resources must match, as well as `capacity` and `accessModes`. You can omit the storageClassName, but you must specify `""` to prevent Kubernetes from using the default StorageClass. The `storageClassName` does not need to refer to an existing StorageClass object. If all you need is to bind the claim to a volume, you can use any name you want. Since Cloud Storage buckets do not have size limits, you can put any number for `capacity`, but it cannot be empty.

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
        - uid=1001
        - gid=3003
        - debug_fuse
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
      - image: nginx
        name: nginx
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

When the Cloud Storage FUSE is initiated, the following flags are passed to the Cloud Storage FUSE binary, and these flags cannot be overwritten by users:

- app-name=gke-gcs-fuse-csi
- temp-dir=/gcsfuse-tmp/.volumes/{volume-name}/temp-dir
- foreground
- log-file=/dev/fd/1
- log-format=text

The following flags are disallowed to be passed to the Cloud Storage FUSE binary:

- key-file
- token-url
- reuse-token-from-url
- endpoint

All the other flags defined in the [Cloud Storage FUSE flags file](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/flags.go) are allowed. You can pass the flags via `spec.mountOptions` on a `PersistentVolume` manifest, or `spec.volumes[n].csi.volumeAttributes.mountOptions` if you are using the CSI Ephemeral Inline volumes.

Specifically, you may consider passing the following flags as needed:

- If you use some other tools, such as [gsutil](https://cloud.google.com/storage/docs/gsutil), to upload objects to the bucket, pass the flag `implicit-dirs`. See Cloud Storage FUSE [implicit directories documentation](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/semantics.md#implicit-directories) for details.
- If the Pod/container does not use the root user, pass the uid to Cloud Storage FUSE using the flag `uid`.
- If the Pod/container does not use the default fsGroup, pass the gid to Cloud Storage FUSE using the flag `gid`.
- If you only want to mount a directory in the bucket instead of the entire bucket, pass the directory relative path via the flag `only-dir=relative/path/to/the/bucket/root`.
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

1. Error `Transport endpoint is not connected` in workload Pods.
   
    This error is due to Cloud Storage FUSE termination. In most cases, Cloud Storage FUSE was terminated because of OOM. Please use the Pod annotations `gke-gcsfuse/[cpu-limit | memory-limit | ephemeral-storage-limit]` to allocate more resources to Cloud Storage FUSE (the sidecar container). Note that the only way to fix this error is to restart your workload Pod.

2. Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = failed to get GCS bucket "xxx": googleapi: Error 403: Caller does not have storage.buckets.get access to the Google Cloud Storage bucket. Permission 'storage.buckets.get' denied on resource (or it may not exist)., forbidden`
   
    Please double check the [service account setup steps](#set-up-access-to-gcs-buckets-via-gke-workload-identity) to make sure your Kubernetes service account is set up correctly. Make sure your workload Pod is using the Kubernetes service account.

3. Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = failed to get GCS bucket "xxx": googleapi: Error 400: Invalid bucket name: 'xxx', invalid`
   
    The Cloud Storage bucket does not exist. Make sure the Cloud Storage bucket is created, and the Cloud Storage bucket name is specified correctly.

4. Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = failed to find the sidecar container in Pod spec`
   
    The Cloud Storage FUSE sidecar container was not injected. Please check the Pod annotation `gke-gcsfuse/volumes: "true"` is set correctly.

5. Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = the sidecar container failed with error: Incorrect Usage. flag provided but not defined: -xxx`

    Invalid mount flags are passed to Cloud Storage FUSE. Please check [Cloud Storage FUSE Mount Flags](#cloud-storage-fuse-mount-flags) for more details.