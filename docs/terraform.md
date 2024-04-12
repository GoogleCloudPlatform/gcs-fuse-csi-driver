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

# Cloud Storage FUSE CSI Driver Enablement in Terraform

If you are using Terraform to create GKE clusters, use `gcs_fuse_csi_driver_config` field in `addons_config` to enable the CSI driver. Meanwhile, make sure GKE Workload Identity and GKE Metadata Server are enabled. See the [Terraform documentation](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/container_cluster) for details.

The following example is a `.tf` file excerpt showing how to enable the CSI driver, GKE Workload Identity, and GKE Metadata Server:

```terraform
resource "google_container_cluster" "primary" {
    
    # Enable GKE Workload Identity.
    workload_identity_config {
        workload_pool = "${data.google_project.project.project_id}.svc.id.goog"
    }
    
    # Run the GKE Metadata Server on this node.
    node_config {
        workload_metadata_config {
            mode = "GKE_METADATA"
        }
    }

    # Enable GCS FUSE CSI driver.
    addons_config {
        gcs_fuse_csi_driver_config {
            enabled = true
        }
    }
}
```
