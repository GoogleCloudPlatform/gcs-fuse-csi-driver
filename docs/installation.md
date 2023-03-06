# GCS FUSE CSI Driver Installation

## Prerequisites
- Clone the repo by running the following command
  ```bash
  git clone https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver.git
  cd gcs-fuse-csi-driver
  ```
- Install `jq` utility
  ```bash
  sudo apt-get update
  sudo apt-get install jq
  ```
- A standard GKE cluster. Autopilot clusters are not supported for now.
- [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) is enabled on the cluster.
- Run the following commands to create a GKE cluster with Workload Identity enabled.
  ```bash
  CLUSTER_PROJECT_ID=<cluster-project-id>
  CLUSTER_NAME=<cluster-name>
  gcloud container clusters create ${CLUSTER_NAME} --workload-pool=${CLUSTER_PROJECT_ID}.svc.id.goog
  gcloud container clusters get-credentials ${CLUSTER_NAME}
  ```
- For an existing cluster, run the following commands to enable Workload Identity.
  ```bash
  CLUSTER_PROJECT_ID=<cluster-project-id>
  CLUSTER_NAME=<cluster-name>
  gcloud container clusters update ${CLUSTER_NAME} --workload-pool=${CLUSTER_PROJECT_ID}.svc.id.goog
  gcloud container clusters get-credentials ${CLUSTER_NAME}
  ```

## Install
- If you aren't using your own containers, you will need to checkout a release tag, otherwise the build will generate a "dirty" tag for a git commit version that does not exist. From the root of the gcs-fuse-csi-driver clone, that might look like this:
  
  ```bash
  # fetch tags to build a release version
  $ git fetch

  # View tags with "git tag" and then choose one
  $ git checkout v0.1.0
  ```

- Run the following command to install the driver. The driver will be installed under a new namespace `gcs-fuse-csi-driver`. The installation may take a few minutes.
  ```bash
  # Optionally, specify the image registry and image version if you have built the images from source code.
  # If you do not choose a custom registry and staging version you will need to fetch and checkout a tag, shown in the previous step.
  export REGISTRY=<your-container-registry>
  export STAGINGVERSION=<staging-version>
  # Optionally, specify the overlay if you want to try out features that are only available in the dev overlay.
  export OVERLAY=dev  
  make install
  ```

## Check the Driver Status
The output from the following command
```bash
kubectl get CSIDriver,Deployment,DaemonSet,Pods -n gcs-fuse-csi-driver
```
should contain the driver application information, something like
```
NAME                                                  ATTACHREQUIRED   PODINFOONMOUNT   STORAGECAPACITY   TOKENREQUESTS                    REQUIRESREPUBLISH   MODES                  AGE
csidriver.storage.k8s.io/gcsfuse.csi.storage.gke.io   false            true             false             <cluster-project-id>-gke-dev.svc.id.goog   true                Persistent,Ephemeral   3m49s

NAME                                          READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/gcs-fuse-csi-driver-webhook   1/1     1            1           3m49s

NAME                               DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
daemonset.apps/gcsfusecsi-node   3         3         3       3            3           kubernetes.io/os=linux   3m49s

NAME                                               READY   STATUS    RESTARTS   AGE
pod/gcs-fuse-csi-driver-webhook-565f85dcb9-pdlb9   1/1     Running   0          3m49s
pod/gcsfusecsi-node-b6rs2                        3/3     Running   0          3m49s
pod/gcsfusecsi-node-ng9xs                        3/3     Running   0          3m49s
pod/gcsfusecsi-node-t9zq5                        3/3     Running   0          3m49s
```

## Uninstall
- Run the following command to uninstall the driver.
  ```bash
  make uninstall
  ````