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

## GKE Compatibility

| CSI Driver Version                                                                         | Status     | Release Date | Cloud Storage FUSE Version                                                     | Sidecar Container Image                                                                                                                                | Earliest GKE 1.24   | Earliest GKE 1.25   | Earliest GKE 1.26   | Earliest GKE 1.27   | Earliest GKE 1.28  | Earliest GKE 1.29  |
| ------------------------------------------------------------------------------------------ | ---------- | ------------ | ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------- | ------------------- | ------------------- | ------------------- | ------------------ | ------------------ |
| [v0.1.2](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.2)   | Deprecated | N/A          | [v0.42.3](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v0.42.3) | N/A                                                                                                                                                    | None                | None                | None                | None                | None               | None               |
| [v0.1.3](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.3)   | Deprecated | N/A          | [v0.42.4](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v0.42.4) | N/A                                                                                                                                                    | None                | None                | None                | None                | None               | None               |
| [v0.1.4](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.4)   | Released   | 2023-08-15   | [v1.0.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.0.0)   | N/A                                                                                                                                                    | 1.24.10-gke.2300    | 1.25.10-gke.1200    | 1.26.5-gke.2100     | 1.27.2-gke.1200     | 1.28.1-gke.200     | None               |
| [v0.1.5](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.5)   | Abandoned  | N/A          | [v1.2.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.0)   | N/A                                                                                                                                                    | None                | None                | None                | None                | None               | None               |
| [v0.1.6](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.6)   | Released   | 2023-10-25   | [v1.2.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.0)   | N/A                                                                                                                                                    | 1.24.17-gke.2144000 | 1.25.14-gke.1462000 | 1.26.9-gke.1483000  | 1.27.6-gke.1487000  | 1.28.2-gke.1078000 | None               |
| [v0.1.7](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.7)   | Released   | 2023-11-25   | [v1.2.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.1)   | N/A                                                                                                                                                    | 1.24.17-gke.2266000 | 1.25.15-gke.1144000 | 1.26.10-gke.1101000 | 1.27.7-gke.1121000  | 1.28.3-gke.1260000 | None               |
| [v0.1.8](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.8)   | Released   | 2023-12-08   | [v1.2.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.1)   | N/A                                                                                                                                                    | 1.24.17-gke.2339000 | 1.25.16-gke.1011000 | 1.26.10-gke.1227000 | 1.27.7-gke.1279000  | 1.28.3-gke.1430000 | 1.29.0-gke.1003000 |
| [v0.1.9](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.9)   | Abandoned  | N/A          | [v1.3.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.3.0)   | N/A                                                                                                                                                    | None                | None                | None                | None                | None               | None               |
| [v0.1.10](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.10) | Abandoned  | N/A          | [v1.3.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.3.0)   | N/A                                                                                                                                                    | 1.24.17-gke.2472000 | None                | None                | None                | 1.28.5-gke.1217000 | None               |
| [v0.1.11](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.11) | Abandoned  | N/A          | [v1.4.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.0)   | N/A                                                                                                                                                    | None                | None                | None                | None                | None               | None               |
| [v0.1.12](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.12) | Released   | 2024-01-25   | [v1.4.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.0)   | [7898e40bf57f](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:7898e40bf57f159dc828511f4217cb42c08fa4df0c9ad732a0b0747b66e415c6) | None                | 1.25.16-gke.1268000 | 1.26.12-gke.1111000 | 1.27.9-gke.1092000  | None               | 1.29.0-gke.1381000 |
| [v0.1.13](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.13) | Released   | 2024-02-08   | [v1.4.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.1)   | [972699a4bf89](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:972699a4bf8973f7614f09908412a1fca24ea939eac2d3fcca599109f71fc162) | None                | 1.25.16-gke.1360000 | 1.26.13-gke.1052000 | 1.27.10-gke.1055000 | 1.28.6-gke.1095000 | 1.29.1-gke.1425000 |
| [v0.1.14](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.14) | Released   | 2024-02-20   | [v1.4.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.1)   | [c83609ecf50d](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:c83609ecf50d05a141167b8c6cf4dfe14ff07f01cd96a9790921db6748d40902) | None                | 1.25.16-gke.1537000 | 1.26.14-gke.1006000 | 1.27.11-gke.1018000 | 1.28.6-gke.1456000 | 1.29.2-gke.1060000 |

> Note: The above GKE versions may not be valid any more, please follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/concepts/release-channels#what_versions_are_available_in_a_channel) to check what versions are available in a channel.

The new CSI driver version will be first available in GKE Rapid channel on its release date. For Regular and Stable channels, plan for a 4-week and 12-week wait respectively.

## Releases

### v0.1.14

- Fix sidecar container auto-termination logic for Pods with restart policy OnFailure.
- Update golang version to 1.22.0.

### v0.1.13

- Update gcsfuse to v1.4.1.
- Support custom buffer or cache volume. Attached PD or other storage medium can be used for the write buffering.
- Fix a sidecar container auto-termination issue.

### v0.1.12

- Update go modules.
- Update golang version to 1.21.5.
- Bump the GCSFuse version to v1.4.0.
- Remove the gracePeriod flag from the sidecar container.
- Fix memory leak by cleaning up the GCP storage client after each `NodePublishVolume` call.
- Support sidecar image hosted in a private registry.
- Allow users to specify sidecar container resource requests.

### v0.1.11

This release is abandoned.

### v0.1.10

This release is abandoned.

### v0.1.9

This release is abandoned.

### v0.1.8

- Updated go modules.
- Replace sidecar container emptyDir `gke-gcsfuse-cache` with `gke-gcsfuse-buffer`.

### v0.1.7

- Updated go modules.
- Updated gcsfuse version to v1.2.1-gke.0.
- Updated CSI driver golang builder version to go1.21.4.
- Allow users to override sidecar grace-period to fix https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/91.
- Add CSI fsgroup delegation support to fix https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/16.

### v0.1.6

- Updated go modules.
- Updated sidecar container versions.
- Updated CSI driver golang builder version to go1.21.2.
- Make the sidecar container follow the [Restricted Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted), setting securityContext.capabilities.drop=["ALL"] to fix the issue https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/52
- Fixed the behavior when users pass "0" to the pod annotation to configure the sidecar container resources, allowing the sidecar container to consume unlimited resources on Standard clusters.
- Fixed sidecar container validation logic in webhook.

### v0.1.5

- Updated go modules.
- Updated sidecar container versions.
- Updated CSI driver golang builder version to go1.21.1.
- Updated gcsfuse binary to v1.2.0, using golang builder version go1.21.0.
- Increased unmount timeout to avoid errors.
- Make the sidecar container follow the [Restricted Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted).
- Added a secondary cache emptyDir volume to the sidecar container.
- Added more E2E test cases.
- Improved documentation.
- Fixed other issues.

### v0.1.4

- Fixed openssl CVEs: CVE-2023-2650, CVE-2023-0465, CVE-2023-0466, CVE-2023-0464.
- Fixed golang CVEs in go1.20.3: CVE-2023-29400, CVE-2023-24539, CVE-2023-29403.
- Updated go modules.
- Updated sidecar container versions.
- Updated golang builder version to go1.20.5.
- Updated gcsfuse binary to v1.0.0.
- Fixed the issue [Cannot parse gcsfuse bool flags with value](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/33).
- Fixed the issue [Enable -o options for gcsfuse](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/32).
- Fixed the issue [Remove the requirement of storage.buckets.get permission from the CSI driver](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/31).
- Fixed other issues.

### v0.1.3

- Updated go modules.
- Updated sidecar container versions.
- Updated golang builder version to go1.20.4.
- Updated gcsfuse binary to v0.42.4.
- Fixed copyright information.
- Updated documentation.
- Added ARM node support.
- Fixed issue https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/23.
- Fixed other issues.

### v0.1.2

- Update go module.
- Read the sidecar image from a configMap.
- Fix CSI mounter options parsing logic.
- Fix other minor issues.

### v0.1.1

- Update go module.
- Improve SIGTERM signal handling logic in sidecar container.
- Add webhook metrics endpoint to emit component version metric.
- Update gcsfuse version to v0.42.3-gke.0.
- Decrease the default sidecar container ephemeral storage limit to 5GiB.

### v0.1.0

- Initial alpha release of the Google Cloud Storage FUSE CSI Driver.