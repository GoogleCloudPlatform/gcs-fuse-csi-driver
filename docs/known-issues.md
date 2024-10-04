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

All the Pod creation requests are monitored by a webhook controller. If the Pod annotation `gke-gcsfuse/volumes: "true"` is detected, the webhook will inject the sidecar container by modifying the Pod spec. The [Cloud Storage FUSE](https://cloud.google.com/storage/docs/gcs-fuse) processes run in the sidecar container.

Before GKE 1.29, the GCS FUSE sidecar container is injected at **position 0** of the regular container array. After GKE 1.29, the GCS FUSE sidecar container is injected as a [native sidecar container](https://kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/), which is an init container with `restartPolicy: Always`.

After the Pod is scheduled onto a node, the GCS FUSE CSI Driver node server, which runs as a privileged container on each node, opens the `/dev/fuse` device on the node and obtains the file descriptor. Then the CSI driver calls [mount.fuse3(8)](https://man7.org/linux/man-pages/man8/mount.fuse3.8.html) passing the file descriptor via the mount option “fd=N” to create a mount point. In the end, the CSI driver calls [sendmsg(2)](https://man7.org/linux/man-pages/man2/sendmsg.2.html) to send the file descriptor to the sidecar container via [Unix Domain Socket (UDS) SCM_RIGHTS](https://man7.org/linux/man-pages/man7/unix.7.html).

After the CSI driver creates the mount point, it will inform kubelet to proceed with the Pod startup. Before GKE 1.29, the regular containers on the Pod spec start up in order, so the sidecar container starts before the workload container. After GKE 1.29, the native sidecar container starts before any regular containers.

In the sidecar container, which is an unprivileged container, a process connects to the UDS and calls [recvmsg(2)](https://man7.org/linux/man-pages/man2/recvmsg.2.html) to receive the file descriptor. Then the process calls Cloud Storage FUSE passing the file descriptor to start to serve the FUSE mount point. Instead of passing the actual mount point path, we pass the file descriptor to Cloud Storage FUSE as it supports the [magic /dev/fd/N syntax](https://github.com/GoogleCloudPlatform/gcsfuse/blob/8ab11cd07016a247f64023697383c6e88bc022b0/vendor/github.com/jacobsa/fuse/mount_linux.go#L128-L134). Before the Cloud Storage FUSE takes over the file descriptor, any operations against the mount point will hang.

Since the CSI driver sets `requiresRepublish: true`, it periodically checks whether the GCSFuse volume is still needed by the containers. Before GKE 1.29, when the CSI driver detects all the main workload containers have terminated, it creates an exit file in a Pod emptyDir volume to notify the sidecar container to terminate. After GKE 1.29, the native sidecar container terminates after all the regular containers have terminated.

### Implications of the sidecar container design

Until the Cloud Storage FUSE takes over the file descriptor, the mount point is not accessible. Any operations against the mount point will hang, including [stat(2)](https://man7.org/linux/man-pages/man2/lstat.2.html) that is used to check if the mount point exists.

The sidecar container, or more precisely, the Cloud Storage FUSE process that serves the mount point needs to remain running for the full duration of the Pod's lifecycle. If the Cloud Storage FUSE process is killed, the workload application will throw IO error `Transport endpoint is not connected`.

Before GKE 1.29, the sidecar container auto-termination depends on Kubernetes API correctly reporting the Pod status. However, due to a [Kubernetes issue](https://github.com/kubernetes/kubernetes/issues/106896), container status is not updated after termination caused by Pod deletion. As a result, the sidecar container may not automatically terminate in some scenarios.

After GKE 1.29, because of the native sidecar container feature, the CSI driver does not need to manage the sidecar container lifecycle, thus many issues are solved.

### Issues

- [The CSI driver does not support volumes for initContainers](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/38)
- [The sidecar container is at the spec.containers[0] position which may cause issues in some workloads](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/20)
- [subPath does not work when Anthos Service Mesh is enabled](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/47)
- ["Error: context deadline exceeded" when Anthos Service Mesh is enabled](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/46)
- [The sidecar container does not work well with istio-proxy sidecar container](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/53)
- [The sidecar container does not respect terminationGracePeriodSeconds when the Pod restartPolicy is OnFailure or Always](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/168)

### Solutions

The GCS FUSE SCI Driver now utilizes the [Kubernetes native sidecar container feature](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/753-sidecar-containers), available in GKE versions 1.29.3-gke.1093000 or later.

The Kubernetes native sidecar container feature introduces sidecar containers, a new type of init container that starts before other containers but remains running for the full duration of the pod's lifecycle and will not block pod termination.

Instead of injecting the sidecar container as a regular container, the sidecar container is now injected as an init container, so that other non-sidecar init containers can also use the CSI driver. Moreover, the sidecar container lifecycle, such as auto-termination, is managed by Kubernetes.

## Issues in Autopilot clusters

- [Resource limitation for the sidecar container on Autopilot](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/35)

## Issues caused by incompatible mutating webhooks

- [Incompatible mutating webhook removes GCSFuse sidecar container restartPolicy field, causing Pod stuck in PodInitializing state](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/322)

After GKE 1.29, the CSI driver relies on the [Kubernetes native sidecar container feature](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/753-sidecar-containers) to run GCS FUSE sidecar container. If a third-party mutating webhook uses the package [k8s.io/api/core/v1](https://pkg.go.dev/k8s.io/api/core/v1) to deserialize Pod specs, and the package version is older than 1.28 that does not support [container restartPolicy](https://github.com/kubernetes/api/blob/v0.28.0/core/v1/types.go#L2526), the `restartPolicy: Always` field will be removed from the GCS FUSE sidecar container by the third-party webhook. As a result, the Pod will be stuck in `PodInitializing` state.

Here are some of the webhooks that may be affected:

- The webhook `pod-admission-controller.spiffe.gke.io` used by [Managed Workload Identities](https://cloud.google.com/iam/docs/managed-workload-identity).
- The webhook `istio-inject.webhook.crfa.internal.knative.dev` used by [Cloud Run for Anthos](https://cloud.google.com/anthos/run/archive/docs).
- The webhook `gtoken.doit-intl.com` bused by [gtoken](https://github.com/doitintl/gtoken/tree/master).
- The webhook `k8s-image-swapper.github.io` used by [k8s-image-swapper](https://github.com/estahn/k8s-image-swapper), with version smaller than `v1.5.10`.
- The webhook `logsidecar-injector.logging.kubesphere.io` used by [logsidecar-injector](https://github.com/kubesphere/logsidecar-injector).

### Workaround

Disable the automatic sidecar container injection, and manually inject the sidecar container. See [Sidecar Manual Injection](./sidecar-manual-injection.md) for details.

### Short-term Fixes

- Add a [`ValidatingAdmissionPolicy`](../deploy/base/webhook/validating_admission_policy.yaml) to validate the native sidecar container spec, and throws clear error message if the sidecar container is modified by incompatible webhooks. However, the `ValidatingAdmissionPolicy` does not fix the issue, it only gives users more informative error messages.
- Ask the incompatible webhook provider to upgrade the [k8s.io/api/core/v1](https://pkg.go.dev/k8s.io/api/core/v1) package version.

### Long-term Fix

- The CSI driver webhook should enable `reinvocationPolicy` to ensure the native sidecar container spec is not modified by other webhooks.

GKE is working on the long-term fix.
