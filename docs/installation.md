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

# Cloud Storage FUSE CSI Driver Manual Installation

> WARNING: This documentation describes how to manually install the driver to your GKE clusters. The manual installation should only be used for test purposes. To get official support from Google, users should use GKE to automatically deploy and manage the CSI driver as an add-on feature. See the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver).
> NOTE: The manual installation only works on GKE Standard clusters. To enable the CSI driver on GKE Autopilot clusters, see the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver).

## Prerequisites

- Clone the repo by running the following command.

  ```bash
  git clone https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver.git
  cd gcs-fuse-csi-driver
  ```

- Install `jq` utility.

  ```bash
  sudo apt-get update
  sudo apt-get install jq
  ```

- Create a standard GKE cluster with [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) enabled. Autopilot clusters are not supported for manual installation.
- Run the following commands to create a n2-standard-8 GKE cluster with Workload Identity enabled. Note other machine types may experience out of memory failures when running e2e tests.

  ```bash
  CLUSTER_PROJECT_ID=<cluster-project-id>
  CLUSTER_NAME=<cluster-name>
  gcloud container clusters create ${CLUSTER_NAME} --workload-pool=${CLUSTER_PROJECT_ID}.svc.id.goog --machine-type=n2-standard-8
  ```

- For an existing cluster, run the following commands to enable Workload Identity. Make sure machine type has enough memory if running e2e tests. Consider using machine type n2-standard-8.

  ```bash
  CLUSTER_PROJECT_ID=<cluster-project-id>
  CLUSTER_NAME=<cluster-name>
  gcloud container clusters update ${CLUSTER_NAME} --workload-pool=${CLUSTER_PROJECT_ID}.svc.id.goog
  ```

- Run the following command to ensure the kubectl context is set up correctly.

  ```bash
  gcloud container clusters get-credentials ${CLUSTER_NAME}

  # check the current context
  kubectl config current-context
  ```

## Install

You have two options for installing the Cloud Storage FUSE CSI Driver. You can use [Cloud build](#cloud-build), or [Makefile commands](#makefile-commands). The installation may take a few minutes.

### Cloud Build

If you would like to build your own images, follow the [Cloud Storage FUSE CSI Driver Development Guide - Cloud Build](development.md#cloud-build) to build and push the images with Cloud Build. After your image is built and pushed to your registry, run the following command to install the driver. The driver will be installed under a new namespace `gcs-fuse-csi-driver`.  The following commands assume you have created your artifact registry according to the [development guide prerequisites](development.md#prerequisites).

#### Prerequisites

Run the following command to grant the Cloud Build service account the Kubernetes Engine Admin (`roles/container.admin`) role which is required for the cluster to create cluster-wide resources (ClusterRole, ClusterRoleBinding), which is an admin-level task. `roles/container.developer` is also required for Cloud Build to be able to install the driver on the cluster, but this is covered within the `roles/container.admin` role. Also ensure your node service account has the `roles/artifactregistry.reader` permission (or more permissive role that includes this role) to pull images from Artifact Registry.

```bash
export PROJECT_ID=$(gcloud config get project)
export PROJECT_NUMBER=$(gcloud projects describe ${PROJECT_ID} --format="value(projectNumber)")
gcloud projects add-iam-policy-binding $PROJECT_ID \
--member="serviceAccount:$PROJECT_NUMBER@cloudbuild.gserviceaccount.com" \
    --role="roles/container.admin"
```


- For `The policy contains bindings with conditions, so specifying a condition is required when adding a binding. Please specify a condition.:` You can enter: `2`(None).

#### Installing with Cloud Build on GKE Clusters

For GKE clusters, the cloud build script discovers the GKE cluster `_IDENTITY_PROVIDER` and `_IDENTITY_POOL` automatically, and these cannot be customized for GKE clusters. Note, the `_CLUSTER_NAME` and `_CLUSTER_LOCATION` are required to set up the kubectl config for the cloud build env. If you set a custom `_STAGINGVERSION` when you [built your custom image](development.md#cloud-build), you must set the same `_STAGINGVERSION` here via the `_STAGINGVERSION=<staging-version>` substitution. If you used the default when you [built your custom image](development.md#cloud-build), you should leave `_STAGING_VERSION` unset.


```bash
# Replace with your cluster name
export CLUSTER_NAME=<cluster-name>
# Replace with your cluster location. This can be a zone, or a region depending on if your cluster is zonal or regional.
export CLUSTER_LOCATION=<cluster-location>
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
gcloud builds submit . --config=cloudbuild-install.yaml \
  --substitutions=_REGISTRY=$REGISTRY,_CLUSTER_NAME=$CLUSTER_NAME,_CLUSTER_LOCATION=$CLUSTER_LOCATION
```

#### Installing with Cloud Build on self built K8s Clusters

For non-GKE clusters (installing the driver on a self built k8s cluster), the installation process requires you to provide connection credentials and Workload Identity information that cannot be discovered automatically. You must set up the following: 
1. Set ``_SELF_MANAGED_K8S` to `true`
2. Provide a Secret Manager secret with `_KUBECONFIG_SECRET` containing your kubeconfig file following the instructions in [Creating a KUBECONFIG_SECRET](#creating-a-kubeconfig_secret). 
3. Explicitly set the `_IDENTITY_PROVIDER` and `_IDENTITY_POOL` variables. 
   - Please note that custom overrides of `_IDENTITY_PROVIDER` and `_IDENTITY_POOL` , is not supported for pods with host network yet. 
   - `_IDENTITY_PROVIDER` should be the full URI (e.g  `//iam.googleapis.com/projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/WORKLOAD_IDENTITY_POOL/providers/WORKLOAD_IDENTITY_POOL_PROVIDER`). See [Configure Workload Identity Federation with other identity providers](https://cloud.google.com/iam/docs/workload-identity-federation-with-other-providers) for details. `WORKLOAD_IDENTITY_POOL` should be created with `gcloud iam workload-identity-pools create` and `WORKLOAD_IDENTITY_POOL_PROVIDER` should be created with `gcloud iam workload-identity-pools providers create-oidc`.
4. Create a private pool of Cloud Build workers that runs inside your network, following instructions in [Creating a private pool](#creating-a-private-pool). Pass this private pool to the `--worker-pool` `gcloud builds submit` flag.
   -  This is required for Cloud Build to access your cluster's API server. Failure to configure the private pool will cause the Cloud Build install job to fail with a `dial tcp <internal ip address>: i/o timeout` error because the Cloud Build Job can't access the self-managed Kubernetes cluster's API server at its private IP address.
5. If you set a custom `_STAGINGVERSION` when you [built your custom image](development.md#cloud-build), you must set the same `_STAGINGVERSION` here via the `_STAGINGVERSION=<staging-version>` substitution. 
   - If you used the default when you [built your custom image](development.md#cloud-build), you should leave `_STAGING_VERSION` unset.


```bash
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
export WORKLOAD_IDENTITY_POOL=<your identity pool>
# Note this should be the full URI (e.g. //iam.googleapis.com/projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/WORKLOAD_IDENTITY_POOL/providers/WORKLOAD_IDENTITY_POOL_PROVIDER)
export WORKLOAD_IDENTITY_PROVIDER=<your identity provider>
# The name of the secret you created in the "Creating a KUBECONFIG_SECRET section" below.
export KUBECONFIG_SECRET="gcsfuse-kubeconfig-secret"
gcloud builds submit . --config=cloudbuild-install.yaml \
  # The worker pool is created and the WORKER_POOL variable is exported in the "Creating a private pool" section.
  --worker-pool=$WORKER_POOL \
  # Region must match the region where your private pool was created in the "Creating a private pool" section. This should be the same as your cluster region.
  --region=$CLUSTER_LOCATION \
  --substitutions=_REGISTRY=$REGISTRY,_PROJECT_ID=$PROJECT_ID,_IDENTITY_POOL=$WORKLOAD_IDENTITY_POOL,_IDENTITY_PROVIDER=$WORKLOAD_IDENTITY_PROVIDER,_SELF_MANAGED_K8S=true,_KUBECONFIG_SECRET=$KUBECONFIG_SECRET
```

#### Creating a KUBECONFIG_SECRET

Before running the build, you must create a secret in Google Secret Manager to securely store your `kubeconfig` file.

1. Grant Permissions to Cloud Build: The Cloud Build service account needs permission to access secrets in your project.

```bash
export PROJECT_ID=$(gcloud config get project)
export PROJECT_NUMBER=$(gcloud projects describe ${PROJECT_ID} --format="value(projectNumber)")
gcloud projects add-iam-policy-binding $PROJECT_ID \
    --member="serviceAccount:$PROJECT_NUMBER@cloudbuild.gserviceaccount.com" \
    --role="roles/secretmanager.secretAccessor"
```
- For `The policy contains bindings with conditions, so specifying a condition is required when adding a binding. Please specify a condition.:` You can enter: `2`(None).

2. Create the Secret Container: This command creates an empty secret to hold your `kubeconfig`.

```bash
gcloud secrets create gcsfuse-kubeconfig-secret --replication-policy="automatic"
```

3. Upload Your `kubeconfig` as a New Version: This command reads your local `kubeconfig` file and uploads its contents to the secret you just created. Note, this uses the default `kubeconfig` location. If you are using a different location, replace `~/.kube/config` with the location of your `kubeconfig` file.

```bash
cat ~/.kube/config | gcloud secrets versions add gcsfuse-kubeconfig-secret --data-file=-
```

##### Troubleshooting: Kubeconfig File is Too Large

When following [Creating a KUBECONFIG_SECRET](#creating-a-kubeconfig_secret), if you see an error message like `The file [-] is larger than the maximum size of 65536 bytes`, it means your `kubeconfig` file contains connection details for multiple clusters and has exceeded Secret Manager's `64KiB` size limit.

To fix this, generate a minimal, self-contained `kubeconfig` file that only includes the details for your target cluster.

1. Set Your `kubectl` Context: Ensure `kubectl` is pointing to the correct self-managed cluster.

```bash
export CLUSTER_NAME=<cluster-name>
kubectl config use-context $CLUSTER_NAME
```

2. Generate a Minimal `kubeconfig`: This command creates a new, clean file with only the necessary information for the current context. The `--flatten` flag embeds credentials directly into the file.

```bash
kubectl config view --flatten --minify > minimal-kubeconfig.yaml
```

3. Upload the Minimal `kubeconfig`: Use this new, smaller file to add the secret version.

```bash
cat minimal-kubeconfig.yaml | gcloud secrets versions add gcsfuse-kubeconfig-secret --data-file=-
```


#### Creating a private pool

The following steps create a pool of Cloud Build workers that run inside your network, allowing them to connect to your cluster's private IP address. This process involves two major parts: setting up a private network connection and then creating the worker pool itself.


**Part 1: Set up the Private Connection**

These first three steps establish the private "bridge" between your VPC and Google's network. For the offical documentation and more details on setting parameters in the commands below, refer to [setting up a private connection](https://cloud.google.com/build/docs/private-pools/set-up-private-pool-to-use-in-vpc-network#setup-private-connection).

1.  **Enable the Service Networking API.** This is a one-time setup step that grants your project permission to create private connections to Google services, including Cloud Build.

    ```bash
    export PROJECT_ID=$(gcloud config get project)
    gcloud services enable servicenetworking.googleapis.com \
      --project=$PROJECT_ID
    ```

2.  **Reserve an IP Range for the Peering Connection.** The private pool workers need their own IP addresses inside your network. This command explicitly reserves a block of addresses for Google's services to use, preventing future conflicts with your own resources. Replace `VPC_NAME` with the name of your VPC network you created during VPC configuration of your K8s on GCE cluster. To get the permissions that you need to set up a private connection, ask your administrator to grant you the Compute Engine Network Admin (`roles/compute.networkAdmin`) IAM role on the Google Cloud project in which the VPC network resides. 

    A `/24` prefix provides 256 addresses, which is sufficient for most pools, but make sure to confirm the prefix-length is compatible with your VPC network.  

    ```bash
    export VPC_NAME=<your-vpc-name>
    gcloud compute addresses create reserved-range-$VPC_NAME \
        --global \
        --purpose=VPC_PEERING \
        --prefix-length=24 \
        --network=$VPC_NAME \
        --project=$PROJECT_ID
    ```

3.  **Create the VPC Peering Connection.** This command establishes the actual private bridge. It uses the IP range you reserved in the previous step as the on-ramp for the private pool workers. The IP range you specify here will be subject to firewall rules that are defined in the VPC network.

    ```bash
    # Replace YOUR_VPC_NAME with the name of your VPC network.
    gcloud services vpc-peerings connect \
        --service=servicenetworking.googleapis.com \
        --network=$VPC_NAME \
        --ranges=reserved-range-$VPC_NAME \
        --project=$PROJECT_ID
    ```


**Part 2: Create the Private Pool**

Now that the network bridge exists, this final step creates the pool of workers. For additional background, refer to the official documentation for [creating a new private pool](https://cloud.google.com/build/docs/private-pools/create-manage-private-pools#creating_a_new_private_pool).

4.  **Create the Private Pool.** This command builds the pool and connects it to your VPC via the peering you just created. We recommend creating the pool in the same region as your Kubernetes cluster to ensure low latency and avoid network data transfer costs. Replace `WORKER_POOL_NAME` with a name for your pool,  `CLUSTER_LOCATION` with the region of your K8s cluster, and `VPC_NAME` with the name of your VPC network. To have permissionsn to create the private pool, ask your administrator to grant you the Cloud Build WorkerPool Owner role (`roles/cloudbuild.workerPoolOwner`).

    ```bash
    export WORKER_POOL_NAME=<your pool name>
    export CLUSTER_LOCATION=<cluster-location>
    gcloud builds worker-pools create $WORKER_POOL_NAME \
      --project=$PROJECT_ID \
      --region=$CLUSTER_LOCATION \
      --peered-network=projects/$PROJECT_ID/global/networks/$VPC_NAME
    ```

5. **Save the Worker Pool Name for use in gcloud builds submit.** After completing these steps, you will use the `--worker-pool` flag with the resource name of the pool you just created for use in the `gcloud builds submit` command in [Installing with Cloud Build on self built K8s Clusters](#installing-with-cloud-build-on-self-built-k8s-clusters).

    ```bash
    export WORKER_POOL=projects/$PROJECT_ID/locations/$CLUSTER_LOCATION/workerPools/$WORKER_POOL_NAME
    ```

***

### Makefile Commands

- Run the following command to install the latest driver version. Note, the default registry is a GOOGLE internal project, so only Google Internal employees can currently run this. The driver will be installed under a new namespace `gcs-fuse-csi-driver`.

  ```bash
  latest_version=$(git tag --sort=-creatordate | head -n 1)
  # Replace <cluster-project-id> with your cluster project ID.
  make install STAGINGVERSION=$latest_version PROJECT=<cluster-project-id>
  ```

- If you would like to build your own images, follow the [Cloud Storage FUSE CSI Driver Development Guide](development.md) to build and push the images. Run the following command to install the driver.

  ```bash
  # Specify the image registry and image version if you have built the images from source code.
  make install REGISTRY=<your-container-registry> STAGINGVERSION=<staging-version> PROJECT=<cluster-project-id>
  ```

By default, the `Makefile` discovers the GKE cluster Workload Identity Provider and Workload Identity Pool automatically. To override them, pass the variables with the `make` command (note: this custom override does not work for pods with host network yet). For example:
```bash
make install REGISTRY=<your-container-registry> STAGINGVERSION=<staging-version> IDENTITY_PROVIDER=<your-identity-provider> IDENTITY_POOL=<your-identity-pool>
```

By default, the CSI driver performs a Workload Identity node label check during NodePublishVolume to ensure the GKE metadata server is available on the node. To disable this check, set the `WI_NODE_LABEL_CHECK` environment variable to `false`:

## Check the Driver Status

The output from the following command

```bash
kubectl get CSIDriver,Deployment,DaemonSet,Pods -n gcs-fuse-csi-driver
```

should contain the driver application information, something like

```text
NAME                                                  ATTACHREQUIRED   PODINFOONMOUNT   STORAGECAPACITY   TOKENREQUESTS                              REQUIRESREPUBLISH   MODES                  AGE
csidriver.storage.k8s.io/gcsfuse.csi.storage.gke.io   false            true             false             <cluster-project-id>-gke-dev.svc.id.goog   true                Persistent,Ephemeral   3m49s

NAME                                          READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/gcs-fuse-csi-driver-webhook   1/1     1            1           3m49s

NAME                               DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
daemonset.apps/gcsfusecsi-node     3         3         3       3            3           kubernetes.io/os=linux   3m49s

NAME                                               READY   STATUS    RESTARTS   AGE
pod/gcs-fuse-csi-driver-webhook-565f85dcb9-pdlb9   1/1     Running   0          3m49s
pod/gcsfusecsi-node-b6rs2                          2/2     Running   0          3m49s
pod/gcsfusecsi-node-ng9xs                          2/2     Running   0          3m49s
pod/gcsfusecsi-node-t9zq5                          2/2     Running   0          3m49s
```

## Uninstall

You have two options for un-installing the Cloud Storage FUSE CSI Driver. You can use cloud build, or run the makefile commands. 

### Cloud Build

You can use Cloud Build to uninstall the GCS FUSE CSI driver from your cluster by using the `cloudbuild-uninstall.yaml` configuration file. This process works by generating the exact same Kubernetes manifest that was used for installation and then using that manifest to delete all the associated resources.

Important: You must use the same substitution variables for the uninstall command that you used for the original installation. This is critical for ensuring that Cloud Build can correctly identify all the Kubernetes resources to be removed.

#### Uninstalling from GKE Clusters

The `_CLUSTER_NAME` and `_CLUSTER_LOCATION` are required to set up the kubectl config for the cloud build env. If you set a custom `_STAGINGVERSION` when you [built your custom image](development.md#cloud-build), you must set the same `_STAGINGVERSION` here via the `_STAGINGVERSION=<staging-version>` substitution. If you used the default when you [built your custom image](development.md#cloud-build), you should leave `_STAGING_VERSION` unset.

```bash
# Replace with your cluster name
export CLUSTER_NAME=<cluster-name>
# Replace with your cluster location. This can be a zone, or a region.
export CLUSTER_LOCATION=<cluster-location>
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
gcloud builds submit . --config=cloudbuild-uninstall.yaml \
  --substitutions=_REGISTRY=$REGISTRY,_CLUSTER_NAME=$CLUSTER_NAME,_CLUSTER_LOCATION=$CLUSTER_LOCATION
```


#### Uninstalling with Cloud Build on self built K8s Clusters

Uninstalling from a self-managed Kubernetes cluster requires providing credentials and configuration details so Cloud Build can connect to your cluster and generate the correct manifest for deletion. You must set `_SELF_MANAGED_K8S=true`. You must provide the same `_KUBECONFIG_SECRET`, `_IDENTITY_PROVIDER`, `_IDENTITY_POOL`, and `_STAGINGVERSION` variables that were used during installation. If your cluster is in a private network, you must use a Cloud Build private pool by specifying the `--worker-pool` and `--region` flags.

```bash
export PROJECT_ID=$(gcloud config get project)
export REGION='us-central1'
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
export WORKLOAD_IDENTITY_POOL=<your-identity-pool>
# Note this should be the full URI (e.g. //iam.googleapis.com/projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/WORKLOAD_IDENTITY_POOL/providers/WORKLOAD_IDENTITY_POOL_PROVIDER)
export WORKLOAD_IDENTITY_PROVIDER=<your-identity-provider>
# The name of the secret you created in the "Creating a KUBECONFIG_SECRET section" below.
export KUBECONFIG_SECRET="gcsfuse-kubeconfig-secret"
gcloud builds submit . --config=cloudbuild-uninstall.yaml \
  # The worker pool is created and the WORKER_POOL variable is exported in the "Creating a private pool" section.
  --worker-pool=$WORKER_POOL \
    # Region must match the region where your private pool was created in the "Creating a private pool" section. This should be the same as your cluster region.
  --region=$REGION \
  --substitutions=_REGISTRY=$REGISTRY,_PROJECT_ID=$PROJECT_ID,_IDENTITY_POOL=$WORKLOAD_IDENTITY_POOL,_IDENTITY_PROVIDER=$WORKLOAD_IDENTITY_PROVIDER,_SELF_MANAGED_K8S=true,_KUBECONFIG_SECRET=$KUBECONFIG_SECRET
```

### Makefile Commands
- Run the following command to uninstall the driver.

  ```bash
  make uninstall
  ```