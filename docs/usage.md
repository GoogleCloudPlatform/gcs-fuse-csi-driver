# GCS FUSE CSI Driver Usage

## Before you begin

### Install the CSI driver

Refer to the documentation [GCS FUSE CSI Driver Installation](./installation.md) to install the CSI driver.

### Create GCS buckets

Refer to the documentation [Create GCS buckets](https://cloud.google.com/storage/docs/creating-buckets).

In order to have better performance and higher throughput, please set the `Location type` as `Region`, and select a region where your GKE cluster is running.

### Set up access to GCS buckets via GKE Workload Identity

In order to let the CSI driver authenticate with GCP APIs, you will need to do the following steps to make your GCS buckets accessible by your GKE cluster. See [GKE Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) for more information.

1. Create a GCP Service Account

   ```bash
    # The ID of the project where your GCS bucket lives.
    GCS_BUCKET_PROJECT_ID=<gcs-bucket-project-id>
    # The name of the GCP service account. The service account should in the GCS bucket project.
    GCP_SA_NAME=<gcp-service-account-name>
    # The ID of the project where your GKE cluster lives.
    CLUSTER_PROJECT_ID=<cluster-project-id>
    
    gcloud iam service-accounts create ${GCP_SA_NAME} --project=${GCS_BUCKET_PROJECT_ID}
    ```

2. Grant the GCS permissions to the GCP Service Account. Select a proper IAM role according to the doc [IAM roles for Cloud Storage](https://cloud.google.com/storage/docs/access-control/iam-roles).
    
    ```bash
    # Specify a proper IAM role for Cloud Storage.
    STORAGE_ROLE=<cloud-storage-role>
    
    # Choose "[2] None" for binding condition if you want to apply the permissions to all the GCS buckets in the project.
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

4. Bind the the Kubernetes Service Account with the GCP Service Account.
    
    ```bash
    gcloud iam service-accounts add-iam-policy-binding ${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com \
        --role roles/iam.workloadIdentityUser \
        --member "serviceAccount:${CLUSTER_PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SA_NAME}]"

    kubectl annotate serviceaccount ${K8S_SA_NAME} \
        --namespace ${K8S_NAMESPACE} \
        iam.gke.io/gcp-service-account=${GCP_SA_NAME}@${GCS_BUCKET_PROJECT_ID}.iam.gserviceaccount.com
    ```

## Using the GCS Fuse CSI driver

The GCS Fuse CSI driver allows developers to use standard Kubernetes API to consume pre-exising GCS buckets. There are two types of volume configuration supported:
1. [Static Provisioning](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#static) using a PersistentVolumeClaim bound to the PersistentVolume
2. Using [CSI Ephemeral Inline volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#csi-ephemeral-volumes)

The GCS Fuse CSI driver natively supports the above volume configuration methods. Currently, the [Dynamic Provisioning](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#dynamic) is under development and not officially supported.

No matter which configuration method you choose, the CSI driver webhook depends on Pod annotations to inject and configure a sidecar contianer containing gcsfuse. Specifically, the annotation `gke-gcsfuse/volumes: "true"` is required. By default, the sidecar contianer has **300m CPU, 100Mi memory, and 1Gi ephemeral storage** allocated. You can overwrite these values by specifing the annotation `gke-gcsfuse/[cpu-limit | memory-limit | ephemeral-storage-limit]`. For example:

```yaml
apiVersion: v1
kind: Pod
metadata:
 annotations:
   gke-gcsfuse/volumes: "true" # required
   gke-gcsfuse/cpu-limit: 500m # optional
   gke-gcsfuse/memory-limit: 100Mi # optional
   gke-gcsfuse/local-storage-limit: 50Gi # optional
```

### Using a PersistentVolumeClaim bound to the PersistentVolume

There are two ways to bind a `PersistentVolumeClaim` to a specific `PersistentVolume`.

1. A `PersistentVolume` can be bound to a `PersistentVolumeClaim` using `claimRef` on the `PersistentVolume` spec.
2. A `PersistentVolumeClaim` can bind a `PersistentVolume` using `volumeName` on the `PersistentVolumeClaim` spec.

To bind a PersistentVolume to a PersistentVolumeClaim, the `storageClassName` of the two resources must match, as well as `capacity` and `accessModes`. You can omit the storageClassName, but you must specify "" to prevent Kubernetes from using the default StorageClass. The `storageClassName` does not need to refer to an existing StorageClass object. If all you need is to bind the claim to a volume, you can use any name you want. Since GCS buckets do not have size limits, you can put any number for `capacity`, but it cannot be empty.

1. Create a PersistentVolume using the following YAML manifest.
   
   - `spec.csi.driver`: use `gcsfuse.csi.storage.gke.io` as the csi driver name.
   - `spec.csi.volumeHandle`: specify your GCS bucket name.
   - `spec.mountOptions`: pass flags to gcsfuse.
   
    ```yaml
    apiVersion: v1
    kind: PersistentVolume
    metadata:
      name: gcp-cloud-storage-csi-pv
    spec:
      accessModes:
      - ReadWriteMany
      capacity:
        storage: 5Gi
      storageClassName: dummy-storage-class
      claimRef:
        namespace: my_namespace
        name: gcp-cloud-storage-csi-static-pvc
      mountOptions: # optional, if Pod does not use the root user
        - uid=1001
        - gid=3003
      csi:
        driver: gcsfuse.csi.storage.gke.io
        volumeHandle: my-bucket-name
    ```

2. Create a PersistentVolumeClaim using the following YAML manifest.
    
    ```yaml
    apiVersion: v1
    kind: PersistentVolumeClaim
    metadata:
      name: gcp-cloud-storage-csi-static-pvc
      namespace: my_namespace
    spec:
      accessModes:
      - ReadWriteMany
      resources:
        requests:
          storage: 5Gi
      volumeName: gcp-cloud-storage-csi-pv
      storageClassName: dummy-storage-class
    ```

3. Use the persistentVolumeClaim in a Pod.
  
   - `spec.serviceAccountName`: use the same Kubernetes service account in the [GCS bucket access setup step](#set-up-access-to-gcs-buckets-via-gke-workload-identity).
    
    ```yaml
    apiVersion: v1
    kind: Pod
    metadata:
      name: nginx
      namespace: my_namespace
      annotations:
        gke-gcsfuse/volumes: "true" # required
        gke-gcsfuse/cpu-limit: 500m # optional
        gke-gcsfuse/memory-limit: 100Mi # optional
        gke-gcsfuse/local-storage-limit: 50Gi # optional
    spec:
      securityContext: # optional, if Pod does not use the root user
        runAsUser: 1001
        runAsGroup: 2002
        fsGroup: 3003
      containers:
      - image: nginx
        name: nginx
        volumeMounts:
        - name: gcp-cloud-storage-pvc
          mountPath: /data
      serviceAccountName: my_k8s_sa
      volumes:
      - name: gcp-cloud-storage-pvc
        persistentVolumeClaim:
          claimName: gcp-cloud-storage-csi-static-pvc
    ```

### Using CSI Ephemeral Inline volumes

By using the CSI Ephemeral Inline volumes, the lifecycle of volumes backed by GCS buckets is tied with the Pod lifecycle. After the Pod termination, there is no need to maintain independent PV/PVC objects to represent GCS-backed volumes.

Use the following YAML manifest to specify the GCS bucket directly on the Pod spec.

- `spec.serviceAccountName`: use the same Kubernetes service account in the [GCS bucket access setup step](#set-up-access-to-gcs-buckets-via-gke-workload-identity).
- `spec.volumes[n].csi.driver`: use `gcsfuse.csi.storage.gke.io` as the csi driver name.
- `spec.volumes[n].csi.volumeAttributes.bucketName`: specify your GCS bucket name.
- `spec.volumes[n].csi.volumeAttributes.mountOptions`: pass flags to gcsfuse. Put all the flags in one string separated by a comma `,` without spaces.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: nginx
  namespace: my_namespace
  annotations:
    gke-gcsfuse/volumes: "true" # required
    gke-gcsfuse/cpu-limit: 500m # optional
    gke-gcsfuse/memory-limit: 100Mi # optional
    gke-gcsfuse/local-storage-limit: 50Gi # optional
spec:
  securityContext: # optional, if Pod does not use the root user
    runAsUser: 1001
    runAsGroup: 2002
    fsGroup: 3003
  containers:
  - image: nginx
    name: nginx
    volumeMounts:
    - name: gcp-cloud-storage-csi-ephemeral
      mountPath: /data
  serviceAccountName: my_k8s_sa
  volumes:
  - name: gcp-cloud-storage-csi-ephemeral
    csi:
      driver: gcsfuse.csi.storage.gke.io
      volumeAttributes:
        bucketName: my-bucket-name
        # optional, if Pod does not use the root user
        mountOptions: "uid=1001,gid=3003"
```

## GCS Fuse Mount Flags

When the GCS Fuse is initiated, the following flags are passed to the gcsfuse binary, and these flags cannot be overwritten by users:

- implicit-dirs
- app-name=gke-gcs-fuse-csi
- temp-dir=/tmp/.volumes/{volume-name}/temp-dir
- foreground
- log-file=/dev/fd/1
- log-format=text

The following flags are disallowed to be passed to the gcsfuse binary:

- key-file
- token-url
- reuse-token-from-url
- endpoint

All the other flags defined in the [GCS Fuse flags file](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/flags.go) are allowed. You can pass the flags via `spec.mountOptions` on a `PersistentVolume` manifest, or `spec.volumes[n].csi.volumeAttributes.mountOptions` if you are using the CSI Ephemeral Inline volumes.

Specifically, you may consider passing the following flags as needed:

 - If the Pod/container does not use the root user, pass the uid to gcsfuse using the flag `uid`.
 - If the Pod/container does not use the default fsGroup, pass the gid to gcsfuse using the flag `gid`.
 - In order to get higher throughput, increase the max number of TCP connections allowed per gcsfuse instance by using the flag `max-conns-per-host`. The default value is 10.
 - If you only want to mount a directory in the bucket instead of the entire bucket, pass the directory relative path via the flag `only-dir=relative/path/to/the/bucket/root`.

## More usage examples
See the documentation [Example Applications](../examples/README.md).

## Common Issues

1. Error `Transport endpoint is not connected` in workload Pods.
   
   This error is due to gcsfuse termination. In most cases, gcsfuse was terminated because of OOM. Please use the Pod annotations `gke-gcsfuse/[cpu-limit | memory-limit | ephemeral-storage-limit]` to allocate more resources to gcsfuse (the sidecar container). Note that the only way to fix this error is to restart your workload Pod.

2. Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = failed to get GCS bucket "xxx": googleapi: Error 403: Caller does not have storage.buckets.get access to the Google Cloud Storage bucket. Permission 'storage.buckets.get' denied on resource (or it may not exist)., forbidden`
   
    Please double check the [service account setup steps](#set-up-access-to-gcs-buckets-via-gke-workload-identity) to make sure your Kubernetes service account is set up correctly. Make sure your workload Pod is using the Kubernetes service account.

3. Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = failed to get GCS bucket "xxx": googleapi: Error 400: Invalid bucket name: 'xxx', invalid`
   
   The GCS bucket does not exist. Make sure the GCS bucket is created, and the GCS bucket name is specified correctly.

4. Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = failed to find the sidecar container in Pod spec`
   
   The gcsfuse sidecar container was not injected. Please check the Pod annotation `gke-gcsfuse/volumes: "true"` is set correctly.