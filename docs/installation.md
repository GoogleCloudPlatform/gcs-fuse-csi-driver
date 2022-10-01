# GCS CSI Driver Installation

## Prerequisites
- Clone the repo by running the following command
  ```bash
  git clone https://github.com/songjiaxun/gcp-cloud-storage-csi-driver.git
  cd gcp-cloud-storage-csi-driver
  ```
- A standard GKE cluster using Ubuntu node images. Autopilot clusters are not supported. COS images are not supported.
- [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) is enabled on the cluster.
- Run the following commands to create a GKE cluster with Workload Identity enabled.
  ```bash
  CLUSTER_PROJECT_ID=<cluster-project-id>
  CLUSTER_NAME=<cluster-name>
  gcloud container clusters create ${CLUSTER_NAME} --image-type=ubuntu_containerd --workload-pool=${CLUSTER_PROJECT_ID}.svc.id.goog
  gcloud container clusters get-credentials ${CLUSTER_NAME}
  ```

## Install
- Run the following command to replace `<project-id>` with your GKE cluster project ID in file [csi_driver_audience.yaml](../deploy/overlays/stable/csi_driver_audience.yaml).
  ```bash
  sed -i "s/<project-id>/$CLUSTER_PROJECT_ID/g" ./deploy/overlays/stable/csi_driver_audience.yaml
  ```
- Run the following command to install the driver. The driver will be installed under a new namespace `gcp-cloud-storage-csi-driver`. The installation may take a few minutes.
  ```bash
  make install
  ```

## Check the Driver Status
The output from the following command
```bash
kubectl get CSIDriver,Deployment,DaemonSet,Pods -n gcp-cloud-storage-csi-driver
```
should contain the driver application information, something like
```
NAME                                                       ATTACHREQUIRED   PODINFOONMOUNT   STORAGECAPACITY   TOKENREQUESTS                      REQUIRESREPUBLISH   MODES                  AGE
csidriver.storage.k8s.io/cloudstorage.csi.storage.gke.io   false            true             false             <cluster-project-id>.svc.id.goog   true                Persistent,Ephemeral   116s

NAME                                               READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/gcp-cloud-storage-csi-controller   3/3     3            3           116s

NAME                                        DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
daemonset.apps/gcp-cloud-storage-csi-node   6         6         6       6            6           kubernetes.io/os=linux   116s

NAME                                                    READY   STATUS    RESTARTS   AGE
pod/gcp-cloud-storage-csi-controller-5bbb99dfdd-47csz   2/2     Running   0          116s
pod/gcp-cloud-storage-csi-controller-5bbb99dfdd-dg7hf   2/2     Running   0          116s
pod/gcp-cloud-storage-csi-controller-5bbb99dfdd-gg8mb   2/2     Running   0          116s
pod/gcp-cloud-storage-csi-node-bzm6n                    2/2     Running   0          116s
pod/gcp-cloud-storage-csi-node-cnp6v                    2/2     Running   0          116s
pod/gcp-cloud-storage-csi-node-kv2z7                    2/2     Running   0          116s
pod/gcp-cloud-storage-csi-node-trsn9                    2/2     Running   0          116s
pod/gcp-cloud-storage-csi-node-v28hb                    2/2     Running   0          116s
pod/gcp-cloud-storage-csi-node-xpdc5                    2/2     Running   0          116s
```

## Uninstall
- Run the following command to uninstall the driver.
  ```bash
  make uninstall
  ````