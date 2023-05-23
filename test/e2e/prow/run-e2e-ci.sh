#!/bin/bash

# To execute manually run:
# GOPATH=$HOME/go GKE_CLUSTER_VERSION=1.26.3-gke.1000 GKE_NODE_VERSION=1.26.3-gke.1000 USE_GKE_MANAGED_DRIVER=true ./test/e2e/prow/run-e2e-ci.sh
readonly PKGDIR=${GOPATH}/src/GoogleCloudPlatform/gcs-fuse-csi-driver

readonly overlay_name="${GCS_FUSE_OVERLAY_NAME:-stable}"
readonly boskos_resource_type="${GCE_PD_BOSKOS_RESOURCE_TYPE:-gke-internal-project}"
readonly gke_cluster_version=${GKE_CLUSTER_VERSION:-1.26.3-gke.1000}
readonly gke_node_version=${GKE_NODE_VERSION:-1.26.3-gke.1000}
readonly use_gke_autopilot=${USE_GKE_AUTOPILOT:-false}
readonly gce_region=${GCE_CLUSTER_REGION:-us-central1}
readonly use_gke_managed_driver=${USE_GKE_MANAGED_DRIVER:-false}

readonly PKGDIR=${GOPATH}/src/GoogleCloudPlatform/gcs-fuse-csi-driver

# Make e2e-test will initialize ginko. We can't use e2e-test to build / install the driver /overlays because the registry depends on the boskos project. 
make -C "${PKGDIR}" init-ginkgo

make -C "${PKGDIR}" e2e-test-ci

kt2_version=0e09086b60c122e1084edd2368d3d27fe36f384f
go install sigs.k8s.io/kubetest2@${kt2_version}
go install sigs.k8s.io/kubetest2/kubetest2-gce@${kt2_version}
go install sigs.k8s.io/kubetest2/kubetest2-gke@${kt2_version}
go install sigs.k8s.io/kubetest2/kubetest2-tester-ginkgo@${kt2_version}

# Note that for all tests:
# - a regional cluster will be created (GCS does not support zonal buckets)
# - run-in-prow true
# - image-type is cos_containerd
# - num_nodes is 3
base_cmd="${PKGDIR}/bin/e2e-test-ci \
            --run-in-prow=true \
            --image-type=cos_containerd \
            --num-nodes=3 \
            --gce-region=${gce_region} \
            --use-gke-autopilot=${use_gke_autopilot} \
            --gke-cluster-version=${gke_cluster_version} \
            --boskos-resource-type=${boskos_resource_type}"

# do-driver-build must be false when using GKE managed driver. Otherwise, do-driver-build must be true and deploy-overlay-name should be set.
if [ "$use_gke_managed_driver" = true ]; then
  base_cmd="${base_cmd} --do-driver-build=false --use-gke-managed-driver=true"
else
  base_cmd="${base_cmd} --do-driver-build=true --deploy-overlay-name=${overlay_name}"
fi

eval "$base_cmd"
