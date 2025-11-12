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

| Feature                                                                                                | GKE version requirements                                                                    |
|--------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------|
| [Private image for sidecar container, custom write buffer volume, and sidecar container resource requests](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-sidecar) | 1.27.10-gke.1055000, 1.28.6-gke.1369000, 1.29.1-gke.1575000, or later.                      |
| [File cache, volume attributes](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf)      | 1.27.12-gke.1190000, 1.28.8-gke.1175000, 1.29.3-gke.1093000, or later.                      |
| Cloud Storage FUSE volumes in init containers                                                            | 1.29.3-gke.1093000 or later, with all nodes on GKE version 1.29 or later.                    |
| [Parallel download](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#parallel-download) | 1.29.6-gke.1254000, 1.30.2-gke.1394000, or later.                                            |
| [Cloud Storage FUSE metrics](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#cloud-storage-fuse-metrics) | 1.31.1-gke.1621000 or later, enabled by default on 1.33.0-gke.2248000 or later.              |
| [Metadata prefetch](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#metadata-prefetch) | 1.32.1-gke.1357001 or later.                                                                |
| Streaming writes                                                                                       | 1.32.1-gke.1753001 or later, enabled by default on 1.33.2-gke.4655000 or later.             |
| Configure kernel read ahead                                                                            | 1.32.2-gke.1297001 or later.                                                                |
| Node restart support                                                                                   | 1.33.1-gke.1959000 or later.                                                                |
| Support for mounting the same {{storage_name}} bucket to the same Pod using different PersistentVolumes   | 1.33.0-gke.1932000 or later.                                                                |
| [Host networking support](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-setup#access-for-pods-with-host-network) | 1.33.3-gke.1226000 or later, {{standard_mode}} clusters only.                                        |
| [Buffered read](https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-perf#buffered-read)       | 1.34.0-gke.2011000 or later.                                                                |

The new CSI driver version will be first available in GKE Rapid channel on its release date. For Regular and Stable channels, plan for a 4-week and 12-week wait respectively.

## Releases

Refer the [release section](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases) for more details on each GCS Fuse CSI driver release.