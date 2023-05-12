#!/bin/bash


readonly PKGDIR=${GOPATH}/src/sigs.k8s.io/gcp-compute-persistent-disk-csi-driver

# TODO(amacaskill): figure out what variables we actually need here, or simplify and go straight from prow job config to make file.
readonly overlay_name="${GCE_PD_OVERLAY_NAME:-stable-master}"
readonly boskos_resource_type="${GCE_PD_BOSKOS_RESOURCE_TYPE:-gce-project}"
readonly do_driver_build="${GCE_PD_DO_DRIVER_BUILD:-true}"
readonly gke_cluster_version=${GKE_CLUSTER_VERSION:-latest}
readonly kube_version=${GCE_PD_KUBE_VERSION:-master}
readonly test_version=${TEST_VERSION:-master}
readonly gce_region=${GCE_CLUSTER_REGION:-us-central1}
readonly image_type=${IMAGE_TYPE:-cos_containerd}
readonly use_gke_managed_driver=${USE_GKE_MANAGED_DRIVER:-true}
readonly teardown_driver=${GCE_PD_TEARDOWN_DRIVER:-true}
readonly gke_node_version=${GKE_NODE_VERSION:-}
readonly gke_use_managed_driver=${USE_GKE_MANAGED_DRIVER:-true}

#  Pull in env variables, and forward to make file somehow. 
# TODO(amacaskill): move repo to correct dir readonly PKGDIR=${GOPATH}/src/GoogleCloudPlatform/gcs-fuse-csi-driver
readonly PKGDIR=${GOPATH}/src/GoogleCloudPlatform/gcs-fuse-csi-driver

# Make e2e-test will initialize ginko. We can't use e2e-test to build / install the driver /overlays because the registry depends on the boskos project. 
make -C "${PKGDIR}" init-ginkgo 

 
make -C "${PKGDIR}" e2e-test-ci
