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

# Troubleshooting

## I/O errors in your workloads

- Error `Transport endpoint is not connected` in workload Pods.
  
  This error is due to Cloud Storage FUSE termination. In most cases, Cloud Storage FUSE was terminated because of OOM. Please use the Pod annotations `gke-gcsfuse/[cpu-limit|memory-limit|ephemeral-storage-limit]` to allocate more resources to Cloud Storage FUSE (the sidecar container). Note that the only way to fix this error is to restart your workload Pod.

- Error `Permission denied` in workload Pods.
  
  Cloud Storage FUSE does not have permission to access the file system.
  
  Please double check your container user and fsGroup. Make sure you pass `uid` and `gid` flags correctly. See [Configure how Cloud Storage FUSE buckets are mounted](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#mounting-flags) for more details.
  
  Please double check your service account setup. See [Configure access to Cloud Storage buckets using GKE Workload Identity](./authentication.md) for more details.

## Pod event warnings

If your workload Pods cannot start up, please run `kubectl describe pod <your-pod-name> -n <your-namespace>` to check the Pod events. Find the troubleshooting guide below according to the Pod event.

- Pod event warning: `MountVolume.MountDevice failed for volume "xxx" : kubernetes.io/csi: attacher.MountDevice failed to create newCsiDriverClient: driver name gcsfuse.csi.storage.gke.io not found in the list of registered CSI drivers`, or Pod event warning: `MountVolume.SetUp failed for volume "xxx" : kubernetes.io/csi: mounter.SetUpAt failed to get CSI client: driver name gcsfuse.csi.storage.gke.io not found in the list of registered CSI drivers`

  This warning indicates that the CSI driver is not enabled, or the CSI driver is not up and running. Please double check if the CSI driver is enabled on your cluster. See [Enable the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#enable) for details. If the CSI is enabled, on each node you should see a Pod called `gcsfusecsi-node-xxxxx` up and running. If the cluster was just scaled, updated, or upgraded, this warning is normal and should be transient because it takes a few minutes for the CSI driver Pods to be functional after the cluster operations.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Unauthenticated desc = failed to prepare storage service: storage service manager failed to setup service: timed out waiting for the condition`

  After you follow the documentation [Configure access to Cloud Storage buckets using GKE Workload Identity](./authentication.md) to configure the Kubernetes service account, it usually takes a few minutes for the credentials being propagated. Whenever the credentials are propagated into the Kubernetes cluster, this warning will disappear, and your Pod scheduling should continue. If you still see this warning after 5 minutes, please double check the documentation [Configure access to Cloud Storage buckets using GKE Workload Identity](./authentication.md) to make sure your Kubernetes service account is set up correctly. Make sure your workload Pod is using the Kubernetes service account in the same namespace.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = PermissionDenied desc = failed to get GCS bucket "xxx": googleapi: Error 403: xxx@xxx.iam.gserviceaccount.com does not have storage.objects.list access to the Google Cloud Storage bucket. Permission 'storage.objects.list' denied on resource (or it may not exist)., forbidden`
   
  Please double check the documentation [Configure access to Cloud Storage buckets using GKE Workload Identity](./authentication.md) to make sure your Kubernetes service account is set up correctly. Make sure your workload Pod is using the Kubernetes service account in the same namespace.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = NotFound desc = failed to get GCS bucket "xxx": storage: bucket doesn't exist`
   
  The Cloud Storage bucket does not exist. Make sure the Cloud Storage bucket is created, and the Cloud Storage bucket name is specified correctly.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = FailedPrecondition desc = failed to find the sidecar container in Pod spec`
   
  The Cloud Storage FUSE sidecar container was not injected. Please check the Pod annotation `gke-gcsfuse/volumes: "true"` is set correctly.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = InvalidArgument desc = the sidecar container failed with error: Incorrect Usage. flag provided but not defined: -xxx`

  Invalid mount flags are passed to Cloud Storage FUSE. Please check [Configure how Cloud Storage FUSE buckets are mounted](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#mounting-flags) for more details.

- Pod event warning: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = ResourceExhausted desc = the sidecar container failed with error: signal: killed`

  The gcsfuse process was killed, which is usually caused by OOM. Please consider increasing the sidecar container memory limit by using the annotation `gke-gcsfuse/memory-limit`.

- Other Pod event warnings: `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = xxx` or `UnmountVolume.TearDown failed for volume "xxx" : rpc error: code = Internal desc = xxx`
  
  Warnings that are not listed above and include a rpc error code `Internal` mean that other unexpected issues occurred in the CSI driver, please create a [new issue](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/new) on the GitHub project page. Please include your workload information as detailed as possible, and the Pod event warning in the issue.
