# Cloud Storage FUSE CSI Driver Installation

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
- Install Kustomize by following the [official Kustomize documentation](https://kubectl.docs.kubernetes.io/installation/kustomize/).
- Create a standard GKE cluster with [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) enabled. Autopilot clusters are not supported for manual installation.
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
- Run the following command to ensure the kubectl context is set up correctly.
  ```bash
  kubectl config set-context ${CLUSTER_NAME} --cluster=${CLUSTER_NAME}

  # check the current context
  kubectl config current-context
  ```

## Install
- Run the following command to install the latest driver with version `v0.1.2`. The driver will be installed under a new namespace `gcs-fuse-csi-driver`. The installation may take a few minutes.
  ```bash
  # Replace <cluster-project-id> with your cluster project ID.
  make install STAGINGVERSION=v0.1.2 PROJECT=<cluster-project-id>
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
```
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