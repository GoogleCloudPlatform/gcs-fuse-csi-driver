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

# Known Issues

## Issues due to the sidecar container design

### The sidecar container mode design

This section describes how the GCS FUSE sidecar container is injected and how a GCS bucket-backed volume is mounted. It helps you understand the restrictions of this sidecar container mode design.

All the Pod creation requests are monitored by a webhook controller. If the Pod annotation `gke-gcsfuse/volumes: "true"` is detected, the webhook will inject the sidecar container at **position 0** of the regular container array by modifying the Pod spec. The [Cloud Storage FUSE](https://cloud.google.com/storage/docs/gcs-fuse) processes run in the sidecar container.

After the Pod is scheduled onto a node, the GCS FUSE CSI Driver node server, which runs as a privileged container on each node, opens the `/dev/fuse` device on the node and obtains the file descriptor. Then the CSI driver calls [mount.fuse3(8)](https://man7.org/linux/man-pages/man8/mount.fuse3.8.html) passing the file descriptor via the mount option “fd=N” to create a mount point. In the end, the CSI driver calls [sendmsg(2)](https://man7.org/linux/man-pages/man2/sendmsg.2.html) to send the file descriptor to the sidecar container via [Unix Domain Socket (UDS) SCM_RIGHTS](https://man7.org/linux/man-pages/man7/unix.7.html).

After the CSI driver creates the mount point, it will inform kubelet to proceed with the Pod startup. The containers on the Pod spec will be started up in order, so the sidecar container will be started first.

In the sidecar container, which is an unprivileged container, a process connects to the UDS and calls [recvmsg(2)](https://man7.org/linux/man-pages/man2/recvmsg.2.html) to receive the file descriptor. Then the process calls Cloud Storage FUSE passing the file descriptor to start to serve the FUSE mount point. Instead of passing the actual mount point path, we pass the file descriptor to Cloud Storage FUSE as it supports the [magic /dev/fd/N syntax](https://github.com/GoogleCloudPlatform/gcsfuse/blob/8ab11cd07016a247f64023697383c6e88bc022b0/vendor/github.com/jacobsa/fuse/mount_linux.go#L128-L134). Before the Cloud Storage FUSE takes over the file descriptor, any operations against the mount point will hang.

### Implications of the sidecar container design

Until the Cloud Storage FUSE takes over the file descriptor, the mount point is not accessible. Any operations against the mount point will hang, including [stat(2)](https://man7.org/linux/man-pages/man2/lstat.2.html) that is used to check if the mount point exists.

The sidecar container, or more precisely, the Cloud Storage FUSE process that serves the mount point needs to remain running for the full duration of the Pod's lifecycle. If the Cloud Storage FUSE process is killed, the workload application will throw IO error `Transport endpoint is not connected`.

### Issues

- [The CSI driver does not support volumes for initContainers](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/38)
- [The sidecar container is at the spec.containers[0] position which may cause issues in some workloads](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/20)
- [subPath does not work when Anthos Service Mesh is enabled](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/47)
- ["Error: context deadline exceeded" when Anthos Service Mesh is enabled](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/46)
- [The sidecar container does not work well with istio-proxy sidecar container](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/53)

### Solutions

Unfortunately, there is no good short-term solution or workaround for the above issues due to the restrictions of the sidecar container mode design.

The [sidecar containers KEP](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/753-sidecar-containers) is implemented in this PR [Add SidecarContainers feature](https://github.com/kubernetes/kubernetes/pull/116429).

> The new feature gate "SidecarContainers" is now available. This feature introduces sidecar containers, a new type of init container that starts before other containers but remains running for the full duration of the pod's lifecycle and will not block pod termination.

This new feature is a good long-term solution. Instead of injecting the sidecar container as a regular container, we will leverage the new SidecarContainers feature to inject the container as an init container, so that other non-sidecar init container can also use the CSI driver.

We are currently testing the SidecarContainers feature, and will adopt the feature when it is available on GKE.

## Issues in Autopilot clusters

- [Resource limitation for the sidecar container on Autopilot using GPU: 2 CPU and 14GB Memory](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/35)
- [Cannot upload files larger than 10Gi in Autopilot clusters](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/21)

## Other issues

- [Multiple PVs referring to the same bucket does not work](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/48)
