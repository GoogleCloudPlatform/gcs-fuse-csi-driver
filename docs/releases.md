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

# Google Cloud Storage FUSE CSI Driver Release Notes

## Requirements

To use specific features for the Cloud Storage FUSE CSI driver, you would need to meet the following version requirements:

| Feature                                                                                                | GKE version requirements                                                                    | GCS FUSE version |
|--------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------|------------------|
| [Private image for sidecar container, custom write buffer volume, and sidecar container resource requests](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-sidecar) | 1.27.10-gke.1055000, 1.28.6-gke.1369000, 1.29.1-gke.1575000, or later.                      | v1.4.1+         |
| [File cache, volume attributes](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf)      | 1.27.12-gke.1190000, 1.28.8-gke.1175000, 1.29.3-gke.1093000, or later.                      | v2.0.0         |
| Cloud Storage FUSE volumes in init containers                                                            | 1.29.3-gke.1093000 or later, with all nodes on GKE version 1.29 or later.                    | v2.0.0+       |
| [Parallel download](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#parallel-download) | 1.29.6-gke.1254000, 1.30.2-gke.1394000, or later.                                            | v2.3.1+         |
| [Cloud Storage FUSE metrics](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#cloud-storage-fuse-metrics) | enabled by default on 1.33.0-gke.2248000 or later.              | v3.2.0+      |
| [Metadata prefetch](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#metadata-prefetch) | 1.32.1-gke.1357001 or later.                                                                | v2.8.0+         |
| Streaming writes                                                                                       | 1.32.1-gke.1753001 or later, enabled by default on 1.33.2-gke.4655000 or later.             | v2.9.1+        |
| Configure kernel read ahead                                                                            | 1.32.2-gke.1297001 or later.                                                                | v2.10.0+         |
| Node restart support                                                                                   | 1.33.1-gke.1959000 or later on nodes without [Graceful Node Shutdown enabled](https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown)   | v2.12.2+         |
| Support for mounting the same GCS bucket to the same Pod using different PersistentVolumes   | 1.33.0-gke.1932000 or later.                                                                | v2.12.2+         |
| [Host networking support](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-setup#access-for-pods-with-host-network) | 1.33.3-gke.1226000 or later, standard mode clusters only.                  | v3.1.0+         |
| [Buffered read](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#buffered-read)       | 1.34.0-gke.2011000 or later.                                                                | v3.3.0+         |
| [Cloud Storage FUSE performance defaults](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#defaults-for-high-performance-machine-types)       | 1.33.2-gke.4655000 or later (except 1.34.1-gke.1431000 to 1.34.1-gke.3403001 due to [known issue](https://docs.cloud.google.com/kubernetes-engine/docs/release-notes#December_03_2025)  )                                                                | v3.1.0+         |

Note: For node restart support on clusters running earlier versions or node with [Graceful Node Shutdown enabled](https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown), Pods are unable to recover and you need to redeploy. Graceful Node Shutdown is only default enabled for Preemptible, Spot VMs, Confidential VMs, GPUs VMs, TPU VMs, and Z3, C4, C3 Metal machine families.

The new CSI driver version will be first available in GKE Rapid channel on its release date. For Regular and Stable channels, plan for a 4-week and 12-week wait respectively. 

## Releases

Refer the [release section](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases) for more details on each GCS Fuse CSI driver release.