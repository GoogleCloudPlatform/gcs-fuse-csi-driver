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

[Filesystem in Userspace (FUSE)](https://www.kernel.org/doc/html/next/filesystems/fuse.html) is an interface used to export a filesystem to the Linux kernel. [Cloud Storage FUSE](https://cloud.google.com/storage/docs/gcs-fuse) allows you to mount Cloud Storage buckets as a file system so that applications can access the objects in a bucket using common File IO operations (e.g. open, read, write, close) rather than using cloud-specific APIs. 

The Google Cloud Storage FUSE CSI Driver lets you use the Kubernetes API to mount pre-existing Cloud Storage buckets as volumes which are consumable from a Pod. Your applications can upload and download objects using [Cloud Storage FUSE file system semantics](https://github.com/googlecloudplatform/gcsfuse/blob/master/docs/semantics.md).

The driver natively supports the following ways for you to configure your Cloud Storage buckets-backed volumes:

* **CSI ephemeral volumes**: You specify the Cloud Storage buckets bucket in-line with the Pod specification. To learn more about this volume type, see the [CSI ephemeral volumes overview](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#csi-ephemeral-volumes) in the open source Kubernetes documentation.

* **Static provisioning**: You create a PersistentVolume resource that refers to the Cloud Storage buckets bucket. Your Pod can then reference a PersistentVolumeClaim that is bound to this PersistentVolume. To learn more about this workflow, see [Configure a Pod to Use a PersistentVolume for Storage](https://kubernetes.io/docs/tasks/configure-pod-container/configure-persistent-volume-storage/).

Currently, the driver does not support [Dynamic Volume Provisioning](https://kubernetes.io/docs/concepts/storage/dynamic-provisioning/).

### Benefits

* The Cloud Storage FUSE CSI driver on your cluster turns on automatic deployment and management of the driver. The driver works on both GKE Standard and Autopilot clusters. To leverage this benefit, you need to use GKE to automatically deploy and manage the CSI driver as a add-on feature. See the GKE documentation [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver).

* The Cloud Storage FUSE CSI driver does not need privileged access that is typically required by FUSE clients. This enables a better security posture.

* The Cloud Storage FUSE CSI driver allows applications to access data stored in Cloud Storage buckets using file system semantics.

* The Cloud Storage FUSE CSI driver supports the `ReadWriteMany`, `ReadOnlyMany`, and `ReadWriteOnce` [access modes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#access-modes).

* You can use [GKE Workload Identity](https://cloud.devsite.corp.google.com/kubernetes-engine/docs/concepts/workload-identity) to easily manage authentication while having granular control over how your Pods access Cloud Storage buckets objects.

* Many AI/ML/Batch workloads store data in Cloud Storage buckets. The Cloud Storage FUSE CSI driver enables GKE customers running ML training and serving workloads using frameworks like Ray, PyTorch, Spark, and TensorFlow to run their workloads directly on a GKE cluster without requiring any change to the code. This provides portability and simplicity with file semantics.

## Project Status

Status: General Availability

### GKE Compatibility
| CSI Driver Version                                                                         | Status     | Release Date | Cloud Storage FUSE Version                                                     | Earliest GKE 1.24   | Earliest GKE 1.25   | Earliest GKE 1.26   | Earliest GKE 1.27  | Earliest GKE 1.28  | Earliest GKE 1.29  |
| ------------------------------------------------------------------------------------------ | ---------- | ------------ | ------------------------------------------------------------------------------ | ------------------- | ------------------- | ------------------- | ------------------ | ------------------ | ------------------ |
| [v0.1.2](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.2)   | Deprecated | N/A          | [v0.42.3](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v0.42.3) | None                | None                | None                | None               | None               | None               |
| [v0.1.3](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.3)   | Deprecated | N/A          | [v0.42.4](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v0.42.4) | None                | None                | None                | None               | None               | None               |
| [v0.1.4](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.4)   | Released   | 2023-08-15   | [v1.0.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.0.0)   | 1.24.10-gke.2300    | 1.25.10-gke.1200    | 1.26.5-gke.2100     | 1.27.2-gke.1200    | 1.28.1-gke.200     | None               |
| [v0.1.5](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.5)   | Abandoned  | N/A          | [v1.2.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.0)   | None                | None                | None                | None               | None               | None               |
| [v0.1.6](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.6)   | Released   | 2023-10-25   | [v1.2.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.0)   | 1.24.17-gke.2144000 | 1.25.14-gke.1462000 | 1.26.9-gke.1483000  | 1.27.6-gke.1487000 | 1.28.2-gke.1078000 | None               |
| [v0.1.7](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.7)   | Released   | 2023-11-25   | [v1.2.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.1)   | 1.24.17-gke.2266000 | 1.25.15-gke.1144000 | 1.26.10-gke.1101000 | 1.27.7-gke.1121000 | 1.28.3-gke.1260000 | None               |
| [v0.1.8](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.8)   | Released   | 2023-12-08   | [v1.2.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.1)   | 1.24.17-gke.2339000 | 1.25.16-gke.1011000 | 1.26.10-gke.1227000 | 1.27.7-gke.1279000 | 1.28.3-gke.1430000 | 1.29.0-gke.1003000 |
| [v0.1.9](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.9)   | Abandoned  | N/A          | [v1.3.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.3.0)   | None                | None                | None                | None               | None               | None               |
| [v0.1.10](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.10) | Abandoned  | N/A          | [v1.3.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.3.0)   | None                | None                | None                | None               | None               | None               |
| [v0.1.11](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.11) | Abandoned  | N/A          | [v1.4.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.0)   | None                | None                | None                | None               | None               | None               |
| [v0.1.12](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.12) | Releasing  | 2024-01-25   | [v1.4.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.0)   | 1.24.17-gke.2477000 | 1.25.16-gke.1253000 | 1.26.12-gke.1107000 | 1.27.9-gke.1085000 | 1.28.5-gke.1242000 | 1.29.0-gke.1358000 |

> Note: The above GKE versions may not be valid any more, please follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/concepts/release-channels#what_versions_are_available_in_a_channel) to check what versions are available in a channel.

The new CSI driver version will be first available in GKE Rapid channel on its release date. For Regular and Stable channels, plan for a 4-week and 12-week wait respectively.

## Get Started
- GKE documentation: [Access Cloud Storage buckets with the Cloud Storage FUSE CSI driver](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver)
- [Configure access to Cloud Storage buckets using GKE Workload Identity](./docs/authentication.md)
- [Cloud Storage FUSE CSI Driver Enablement in Terraform](./docs/terraform.md)
- [Cloud Storage FUSE CSI Driver Manual Installation](./docs/installation.md)
- [Example Applications](./examples/README.md)
- [Troubleshooting](./docs/troubleshooting.md)
- [Known Issues](./docs/known-issues.md)

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