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

| CSI Driver Version                                                                         | Status    | Release Date | Cloud Storage FUSE Version                                                   | Sidecar Container Image                                                                                                                                | Earliest GKE 1.27   | Earliest GKE 1.28   | Earliest GKE 1.29  | Earliest GKE 1.30  |
|--------------------------------------------------------------------------------------------|-----------|--------------|------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------|---------------------|--------------------|--------------------|
| [v0.1.4](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.4)   | Released  | 2023-08-15   | [v1.0.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.0.0) | N/A                                                                                                                                                    | 1.27.2-gke.1200     | 1.28.1-gke.200      | None               | None               |
| [v0.1.6](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.6)   | Released  | 2023-10-25   | [v1.2.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.0) | N/A                                                                                                                                                    | 1.27.6-gke.1487000  | 1.28.2-gke.1078000  | None               | None               |
| [v0.1.7](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.7)   | Released  | 2023-11-25   | [v1.2.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.1) | N/A                                                                                                                                                    | 1.27.7-gke.1121000  | 1.28.3-gke.1260000  | None               | None               |
| [v0.1.8](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.8)   | Released  | 2023-12-08   | [v1.2.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.2.1) | N/A                                                                                                                                                    | 1.27.7-gke.1279000  | 1.28.3-gke.1430000  | 1.29.0-gke.1003000 | None               |
| [v0.1.12](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.12) | Released  | 2024-01-25   | [v1.4.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.0) | [7898e40bf57f](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:7898e40bf57f159dc828511f4217cb42c08fa4df0c9ad732a0b0747b66e415c6) | 1.27.9-gke.1092000  | None                | 1.29.0-gke.1381000 | None               |
| [v0.1.13](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.13) | Released  | 2024-02-08   | [v1.4.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.1) | [972699a4bf89](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:972699a4bf8973f7614f09908412a1fca24ea939eac2d3fcca599109f71fc162) | 1.27.10-gke.1055000 | 1.28.6-gke.1095000  | 1.29.1-gke.1425000 | None               |
| [v0.1.14](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v0.1.14) | Released  | 2024-02-20   | [v1.4.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v1.4.1) | [c83609ecf50d](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:c83609ecf50d05a141167b8c6cf4dfe14ff07f01cd96a9790921db6748d40902) | 1.27.11-gke.1018000 | 1.28.6-gke.1456000  | 1.29.2-gke.1060000 | None               |
| [v1.2.0](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.2.0)   | Released  | 2024-04-04   | [v2.0.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.0.0) | [31880114306b](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:31880114306b1fb5d9e365ae7d4771815ea04eb56f0464a514a810df9470f88f) | 1.27.12-gke.1190000 | 1.28.8-gke.1175000  | 1.29.3-gke.1093000 | 1.30.0-gke.1167000 |
| [v1.3.0](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.3.0)   | Released  | 2024-05-05   | [v2.0.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.0.1) | [1f36463e0827](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:1f36463e0827619ba8af3f94ee21d404b635573f70ea3d6447b26f29fd97b9f0) | 1.27.13-gke.1166000 | 1.28.9-gke.1209000  | 1.29.4-gke.1447000 | None               |
| [v1.3.1](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.3.1)   | Released  | 2024-05-17   | [v2.0.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.0.1) | [17c8a585a1e0](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:17c8a585a1e088cb90827386958407d817d65595cd80be74bb48c0498eea7abd) | 1.27.14-gke.1011000 | 1.28.10-gke.1012000 | 1.29.5-gke.1010000 | 1.30.1-gke.1156000 |
| [v1.3.2](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.3.2)   | Released  | 2024-06-07   | [v2.1.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.1.0) | [46a32daec5df](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:46a32daec5df0688da1381876747c5f15b6b5d46dec01ea6cbfae1caf0a4366a) | None                | None                | 1.29.5-gke.1121000 | None               |
| [v1.4.1](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.4.1)   | Released  | 2024-06-07   | [v2.2.0](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.2.0) | [26aaa3ec5955](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:26aaa3ec5955506ce36c4d9c0bae909401168b9a0f52d36661f66ca0789bea0e) | None                | None                | 1.29.5-gke.1198000 | 1.30.1-gke.1329000 |
| [v1.4.2](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.4.2)   | Released  | 2024-06-28   | [v2.3.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.3.1) | [80c2a52aaa16](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:80c2a52aaa16ee7d9956a4e4afb7442893919300af84ae445ced32ac758c55ad) | None                | None                | 1.29.6-gke.1157000 | 1.30.2-gke.1266000 |
| [v1.4.3](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.4.3)   | Released  | 2024-07-11   | [v2.3.2](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.3.2) | [7c74e9ef7c49](https://gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:7c74e9ef7c49627c252087458fd65fa59811161134e5e9a6d3c16aaff0616174) | None                | None                | 1.29.6-gke.1342000 | 1.30.3-gke.1225000 |

> Note: The above GKE versions may not be valid any more, please follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/concepts/release-channels#what_versions_are_available_in_a_channel) to check what versions are available in a channel. The release versions that are not included in the above table are deprecated or abandoned.

The new CSI driver version will be first available in GKE Rapid channel on its release date. For Regular and Stable channels, plan for a 4-week and 12-week wait respectively.

## Releases

### v1.4.3

* update gcsfuse binary to 2.3.2 by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/312>

### v1.4.2

* Cherrypick #298 to release-1.4  by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/299>
* Cherrypick #288, #286, #285 to release-1.4 by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/300>
* Cherrypick #301 to release-1.4 by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/302>

### v1.4.1

* Update gcsfuse to v2.1.0 by @msau42 in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/265>
* Update gcsfuse to v2.2.0 by @msau42 in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/276>
* Introduce a new PV volume attribute to skip bucket access check in CSI by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/268>
* update failed_mount e2e testcases for skip bucket access knob by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/269>
* Remove skip access check from mount options by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/272>
* Add success mounts e2e tests for skip bucket access skip flag by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/273>
* fix metadataCacheTTLSeconds typo by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/274>

### v1.3.2

* Update gcsfuse to v2.1.0 by @msau42 in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/266>

### v1.4.0

* Remove Bucket access check from node csi drvier by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/255>
* Update prow tests to go1.22.3 by @hime in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/256>
* Bump golang from 1.22.2 to 1.22.3 in /cmd/csi_driver by @dependabot in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/254>
* Bump golang from 1.22.2 to 1.22.3 in /cmd/sidecar_mounter by @dependabot in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/253>
* Bump golang from 1.22.2 to 1.22.3 in /cmd/webhook by @dependabot in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/252>
* fix e2e test failures for failed mount test cases by @saikat-royc in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/259>

### v1.3.1

* Update debian base image to fix CVE-2024-2961 by @msau42 in <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/249>

### v1.3.0

* Update gcsfuse to [v2.0.1](https://github.com/GoogleCloudPlatform/gcsfuse/releases/tag/v2.0.1).
* Fix the issue: [Custom sidecar container image crashes the CSI driver](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/233)
* Improve observability by logging GCSFuse memory usage and cache volume usage.
* Update base image to debian 12.
* Update golang to go1.22.2.
* Update go modules.

### v1.2.0

* Update gcsfuse to v2.0.0.
* Update golang version to 1.22.2.
* Add GCSFuse file cache features.
* Add volume attributes supports.
* Adopt Kubernetes native sidecar container features in GKE 1.29 to support init container volume mounting. Fix the [issue](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/168) where the sidecar container does not respect terminationGracePeriodSeconds when the Pod restartPolicy is OnFailure or Always.
* Add a rate limiter to the CSI node server to avoid GCP API throttling errors.
* Refactor code to increase stability and readability.

### v0.1.14

* Fix sidecar container auto-termination logic for Pods with restart policy OnFailure.
* Update golang version to 1.22.0.

### v0.1.13

* Update gcsfuse to v1.4.1.
* Support custom buffer or cache volume. Attached PD or other storage medium can be used for the write buffering.
* Fix a sidecar container auto-termination issue.

### v0.1.12

* Update go modules.
* Update golang version to 1.21.5.
* Bump the GCSFuse version to v1.4.0.
* Remove the gracePeriod flag from the sidecar container.
* Fix memory leak by cleaning up the GCP storage client after each `NodePublishVolume` call.
* Support sidecar image hosted in a private registry.
* Allow users to specify sidecar container resource requests.

### v0.1.11

This release is abandoned.

### v0.1.10

This release is abandoned.

### v0.1.9

This release is abandoned.

### v0.1.8

* Updated go modules.
* Replace sidecar container emptyDir `gke-gcsfuse-cache` with `gke-gcsfuse-buffer`.

### v0.1.7

* Updated go modules.
* Updated gcsfuse version to v1.2.1-gke.0.
* Updated CSI driver golang builder version to go1.21.4.
* Allow users to override sidecar grace-period to fix <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/91>.
* Add CSI fsgroup delegation support to fix <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/16>.

### v0.1.6

* Updated go modules.
* Updated sidecar container versions.
* Updated CSI driver golang builder version to go1.21.2.
* Make the sidecar container follow the [Restricted Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted), setting securityContext.capabilities.drop=["ALL"] to fix the issue <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/52>
* Fixed the behavior when users pass "0" to the pod annotation to configure the sidecar container resources, allowing the sidecar container to consume unlimited resources on Standard clusters.
* Fixed sidecar container validation logic in webhook.

### v0.1.5

* Updated go modules.
* Updated sidecar container versions.
* Updated CSI driver golang builder version to go1.21.1.
* Updated gcsfuse binary to v1.2.0, using golang builder version go1.21.0.
* Increased unmount timeout to avoid errors.
* Make the sidecar container follow the [Restricted Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted).
* Added a secondary cache emptyDir volume to the sidecar container.
* Added more E2E test cases.
* Improved documentation.
* Fixed other issues.

### v0.1.4

* Fixed openssl CVEs: CVE-2023-2650, CVE-2023-0465, CVE-2023-0466, CVE-2023-0464.
* Fixed golang CVEs in go1.20.3: CVE-2023-29400, CVE-2023-24539, CVE-2023-29403.
* Updated go modules.
* Updated sidecar container versions.
* Updated golang builder version to go1.20.5.
* Updated gcsfuse binary to v1.0.0.
* Fixed the issue [Cannot parse gcsfuse bool flags with value](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/33).
* Fixed the issue [Enable -o options for gcsfuse](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/32).
* Fixed the issue [Remove the requirement of storage.buckets.get permission from the CSI driver](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/31).
* Fixed other issues.

### v0.1.3

* Updated go modules.
* Updated sidecar container versions.
* Updated golang builder version to go1.20.4.
* Updated gcsfuse binary to v0.42.4.
* Fixed copyright information.
* Updated documentation.
* Added ARM node support.
* Fixed issue <https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/23>.
* Fixed other issues.

### v0.1.2

* Update go module.
* Read the sidecar image from a configMap.
* Fix CSI mounter options parsing logic.
* Fix other minor issues.

### v0.1.1

* Update go module.
* Improve SIGTERM signal handling logic in sidecar container.
* Add webhook metrics endpoint to emit component version metric.
* Update gcsfuse version to v0.42.3-gke.0.
* Decrease the default sidecar container ephemeral storage limit to 5GiB.

### v0.1.0

* Initial alpha release of the Google Cloud Storage FUSE CSI Driver.
