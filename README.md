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

# Google Cloud Storage FUSE CSI Driver
The Google Cloud Storage FUSE Container Storage Interface (CSI) Plugin.

> WARNING: Manual deployment of this driver to your GKE cluster is not recommended. Instead users should use GKE to automatically deploy and manage the CSI driver as an add-on feature. See the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver).

> DISCLAIMER: Manual deployment of the driver to your cluster is not officially supported by Google.

## Project Overview

[Cloud Storage FUSE](https://cloud.google.com/storage/docs/gcs-fuse) is an open source FUSE adapter that lets you mount Cloud Storage buckets buckets as file systems. Your applications can upload and download objects using [Cloud Storage FUSE file system semantics](https://github.com/googlecloudplatform/gcsfuse/blob/master/docs/semantics.md). The Cloud Storage FUSE CSI driver lets you use the Kubernetes API to consume pre-existing Cloud Storage buckets as volumes.

The driver natively supports the following ways for you to provision your Cloud Storage buckets-backed volumes:

* **CSI ephemeral volumes**: You specify the Cloud Storage buckets bucket in-line with the Pod specification. To learn more about this volume type, see the [CSI ephemeral volumes overview](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#csi-ephemeral-volumes) in the open source Kubernetes documentation.

* **Static provisioning**: You create a PersistentVolume resource that refers to the Cloud Storage buckets bucket. Your client Pod can then reference a PersistentVolumeClaim that is bound to this PersistentVolume. To learn more about this workflow, see [Configure a Pod to Use a PersistentVolume for Storage](https://kubernetes.io/docs/tasks/configure-pod-container/configure-persistent-volume-storage/).

Currently, the driver does not support [Dynamic Volume Provisioning](https://kubernetes.io/docs/concepts/storage/dynamic-provisioning/).

### Benefits

* The Cloud Storage FUSE CSI driver on your cluster turns on automatic deployment and management of the driver. The driver works on both GKE Standard and Autopilot clusters. To leverage this benefit, you need to use GKE to automatically deploy and manage the CSI driver as a add-on feature. See the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver).

* The Cloud Storage FUSE CSI driver does not need privileged access that is typically required by FUSE clients. This enables a better security posture.

* The Cloud Storage FUSE CSI driver supports the `ReadWriteMany`, `ReadOnlyMany`, and `ReadWriteOnce` [access modes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#access-modes).

* You can use [GKE Workload Identity](https://cloud.devsite.corp.google.com/kubernetes-engine/docs/concepts/workload-identity) to easily manage authentication while having granular control over how your Pods access Cloud Storage buckets objects.

## Project Status

Status: Public Preview

### GKE Compatibility
| Cloud Storage FUSE CSI Driver Version                                                    | Cloud Storage FUSE Version                                                     | Supported GKE Kubernetes Version |
| ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ | -------------------------------- |
| [v0.1.2](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.2) | [v0.42.3](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v0.42.3) | 1.26.3-gke.400 or later          |
| [v0.1.3](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.3) | [v0.42.4](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v0.42.4) | 1.26.3-gke.400 or later          |

## Get Started
- [Cloud Storage FUSE CSI Driver Installation](./docs/installation.md)
- [Cloud Storage FUSE CSI Driver Usage](./docs/usage.md)
- [Example Applications](./examples/README.md)

## Development and Contribution
Refer to the [Cloud Storage FUSE CSI Driver Development Guide](./docs/development.md).

## Attribution

This project is inspired by the following open source projects:

- [Google Cloud Filestore CSI Driver](https://github.com/kubernetes-sigs/gcp-filestore-csi-driver) by the Kubernetes authors
- [Azure Blob Storage CSI Driver](https://github.com/kubernetes-sigs/blob-csi-driver) by the Kubernetes authors
- [Kubernetes CSI driver for Google Cloud Storage](https://github.com/ofek/csi-gcs) by [Ofek Lev](https://github.com/ofek)

## References

- [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
- [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)
- [Cloud Storage FUSE](https://cloud.google.com/storage/docs/gcs-fuse)
- [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver)