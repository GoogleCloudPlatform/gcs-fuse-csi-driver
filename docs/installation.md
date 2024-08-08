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
- Run the following commands to create a n2-standard-4 GKE cluster with Workload Identity enabled. Note other machine types may experience out of memory failures when running e2e tests.

  ```bash
  CLUSTER_PROJECT_ID=<cluster-project-id>
  CLUSTER_NAME=<cluster-name>
  gcloud container clusters create ${CLUSTER_NAME} --workload-pool=${CLUSTER_PROJECT_ID}.svc.id.goog --machine-type=n2-standard-4
  ```

- For an existing cluster, run the following commands to enable Workload Identity. Make sure machine type has enough memory if running e2e tests. Consider using machine type n2-standard-4.

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

- Run the following command to install the latest driver version. The driver will be installed under a new namespace `gcs-fuse-csi-driver`. The installation may take a few minutes.

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

- Run the following command to uninstall the driver.

  ```bash
  make uninstall
  ````
