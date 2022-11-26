# GCS FUSE CSI Driver Installation

## Prerequisites
- Clone the repo by running the following command
  ```bash
  git clone https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver.git
  cd gcs-fuse-csi-driver
  ```
- A standard GKE cluster. Autopilot clusters are not supported.
- [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) is enabled on the cluster.
- Run the following commands to create a GKE cluster with Workload Identity enabled.
  ```bash
  CLUSTER_PROJECT_ID=<cluster-project-id>
  CLUSTER_NAME=<cluster-name>
  gcloud container clusters create ${CLUSTER_NAME} --workload-pool=${CLUSTER_PROJECT_ID}.svc.id.goog
  gcloud container clusters get-credentials ${CLUSTER_NAME}
  ```

## Install
- Run the following command to replace `<project-id>` with your GKE cluster project ID in file [csi_driver_audience.yaml](../deploy/overlays/stable/csi_driver_audience.yaml).
  ```bash
  sed -i "s/<project-id>/$CLUSTER_PROJECT_ID/g" ./deploy/overlays/stable/csi_driver_audience.yaml
  ```
- Run the following command to patch the webhook CA bundl.
    ```bash
  ./deploy/base/webhook/patch-ca-bundle.sh
  ```
- Run the following command to install the driver. The driver will be installed under a new namespace `gcs-fuse-csi-driver`. The installation may take a few minutes.
  ```bash
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
deployment.apps/gcs-fuse-csi-controller       3/3     3            3           3m49s
deployment.apps/gcs-fuse-csi-driver-webhook   1/1     1            1           3m49s

NAME                               DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
daemonset.apps/gcs-fuse-csi-node   3         3         3       3            3           kubernetes.io/os=linux   3m49s

NAME                                               READY   STATUS    RESTARTS   AGE
pod/gcs-fuse-csi-controller-8947bbb9f-8spw6        3/3     Running   0          3m49s
pod/gcs-fuse-csi-controller-8947bbb9f-9w627        3/3     Running   0          3m49s
pod/gcs-fuse-csi-controller-8947bbb9f-hvpl9        3/3     Running   0          3m49s
pod/gcs-fuse-csi-driver-webhook-565f85dcb9-pdlb9   1/1     Running   0          3m49s
pod/gcs-fuse-csi-node-b6rs2                        3/3     Running   0          3m49s
pod/gcs-fuse-csi-node-ng9xs                        3/3     Running   0          3m49s
pod/gcs-fuse-csi-node-t9zq5                        3/3     Running   0          3m49s
```

## Uninstall
- Run the following command to uninstall the driver.
  ```bash
  make uninstall
  ````