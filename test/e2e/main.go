/*
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
*/

package main

import (
	"flag"
	"os"
	"strings"

	"local/test/e2e/utils"

	"k8s.io/klog/v2"
)

var (
	pkgDir = flag.String("pkg-dir", "", "the package directory")

	// Kubernetes cluster flags.
	gkeClusterRegion               = flag.String("gke-cluster-region", "", "region that gke regional cluster should be created in")
	gkeClusterVersion              = flag.String("gke-cluster-version", "", "GKE cluster worker master and node version")
	gkeReleaseChannel              = flag.String("gke-release-channel", "rapid", "GKE cluster release channel")
	gkeNodeVersion                 = flag.String("gke-node-version", "", "GKE cluster worker node version")
	nodeMachineType                = flag.String("node-machine-type", "n2-standard-8", "GKE cluster worker node machine type")
	numNodes                       = flag.Int("number-nodes", 3, "number of nodes in the test cluster")
	useGKEAutopilot                = flag.Bool("use-gke-autopilot", false, "use GKE Autopilot cluster for the tests")
	apiEndpointOverride            = flag.String("api-endpoint-override", "https://container.googleapis.com/", "CloudSDK API endpoint override to use for the cluster environment")
	nodeImageType                  = flag.String("node-image-type", "cos_containerd", "image type to use for the cluster")
	istioVersion                   = flag.String("istio-version", "1.23.0", "istio version to install on the cluster")
	gcsfuseEnableZB                = flag.Bool("gcsfuse-enable-zb", false, "use GCS Zonal Buckets for the tests")
	gkeGcloudCommand               = flag.String("gke-gcloud-command", "gcloud", "(gke only) gcloud command used to create a cluster. Modify if you need to pass custom gcloud to create cluster.")
	gkeGcloudArgs                  = flag.String("gke-gcloud-args", "", "(gke only) Additional arguments to custom gcloud command.")
	enableSidecarBucketAccessCheck = flag.Bool("enable-sidecar-bucket-access-check", false, "enables bucket access check in sidecar")

	// Test infrastructure flags.
	inProw             = flag.Bool("run-in-prow", false, "whether or not to run the test in PROW")
	boskosResourceType = flag.String("boskos-resource-type", "gke-internal-project", "name of the boskos resource type to reserve")

	// Driver flags.
	imageRegistry          = flag.String("image-registry", "", "name of image to stage to")
	buildGcsFuseCsiDriver  = flag.Bool("build-gcs-fuse-csi-driver", false, "whether or not to build GCS FUSE CSI Driver images")
	buildGcsFuseFromSource = flag.Bool("build-gcs-fuse-from-source", false, "whether or not to build GCS FUSE from source code")
	buildArm               = flag.Bool("build-arm", false, "whether or not to build the image for Arm nodes")
	deployOverlayName      = flag.String("deploy-overlay-name", "stable", "which kustomize overlay to deploy the driver with")
	useGKEManagedDriver    = flag.Bool("use-gke-managed-driver", false, "use GKE managed GCS FUSE CSI driver for the tests")
	gcsfuseClientProtocol  = flag.String("gcsfuse-client-protocol", "http", "type of protocol gcsfuse uses to communicate with gcs")

	// Ginkgo flags.
	ginkgoFocus         = flag.String("ginkgo-focus", "", "pass to ginkgo run --focus flag")
	ginkgoSkip          = flag.String("ginkgo-skip", "", "pass to ginkgo run --skip flag")
	ginkgoProcs         = flag.String("ginkgo-procs", "10", "pass to ginkgo run --procs flag")
	ginkgoTimeout       = flag.String("ginkgo-timeout", "4h", "pass to ginkgo run --timeout flag")
	ginkgoFlakeAttempts = flag.String("ginkgo-flake-attempts", "2", "pass to ginkgo run --flake-attempts flag")
	ginkgoSkipGcpSaTest = flag.Bool("ginkgo-skip-gcp-sa-test", true, "skip GCP SA test")
)

func main() {
	klog.InitFlags(nil)
	if err := flag.Set("logtostderr", "true"); err != nil {
		klog.Warningf("Failed to set flags: %v", err)
	}
	flag.Parse()

	if *inProw {
		utils.EnsureVariable(boskosResourceType, true, "'boskos-resource-type' must be set when running in prow")
		utils.EnsureVariable(gkeClusterRegion, true, "'gke-cluster-region' must be set when running in prow")
		utils.EnsureVariable(gkeClusterVersion, true, "'gke-cluster-version' must be set when running in prow")
		utils.EnsureVariable(gkeReleaseChannel, true, "'gke-release-channel' must be set when running in prow")
		utils.EnsureVariable(apiEndpointOverride, true, "'api-endpoint-override' must be set")
		if err := os.Setenv("CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER", *apiEndpointOverride); err != nil {
			klog.Fatalf("failed to set CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER to %q: %v", *apiEndpointOverride, err.Error())
		}
	}

	testParams := &utils.TestParameters{
		PkgDir:                         *pkgDir,
		InProw:                         *inProw,
		BoskosResourceType:             *boskosResourceType,
		UseGKEManagedDriver:            *useGKEManagedDriver,
		NodeImageType:                  *nodeImageType,
		UseGKEAutopilot:                *useGKEAutopilot,
		APIEndpointOverride:            *apiEndpointOverride,
		GkeClusterRegion:               *gkeClusterRegion,
		GkeClusterVersion:              *gkeClusterVersion,
		GkeReleaseChannel:              *gkeReleaseChannel,
		GkeNodeVersion:                 *gkeNodeVersion,
		NodeMachineType:                *nodeMachineType,
		NumNodes:                       *numNodes,
		ImageRegistry:                  *imageRegistry,
		DeployOverlayName:              *deployOverlayName,
		BuildGcsFuseCsiDriver:          *buildGcsFuseCsiDriver,
		BuildGcsFuseFromSource:         *buildGcsFuseFromSource,
		BuildArm:                       *buildArm,
		GinkgoFocus:                    *ginkgoFocus,
		GinkgoSkip:                     *ginkgoSkip,
		GinkgoProcs:                    *ginkgoProcs,
		GinkgoTimeout:                  *ginkgoTimeout,
		GinkgoFlakeAttempts:            *ginkgoFlakeAttempts,
		GinkgoSkipGcpSaTest:            *ginkgoSkipGcpSaTest,
		IstioVersion:                   *istioVersion,
		GcsfuseClientProtocol:          *gcsfuseClientProtocol,
		EnableZB:                       *gcsfuseEnableZB,
		GkeGcloudCommand:               *gkeGcloudCommand,
		GkeGcloudArgs:                  *gkeGcloudArgs,
		EnableSidecarBucketAccessCheck: *enableSidecarBucketAccessCheck,
	}

	if strings.Contains(testParams.GinkgoFocus, "performance") {
		testParams.GinkgoSkip = ""
		testParams.GinkgoTimeout = "180m"
		testParams.NumNodes = 1
		testParams.NodeMachineType = "n2-standard-32"
	}

	if err := utils.Handle(testParams); err != nil {
		klog.Fatalf("Failed to run e2e test: %v", err)
	}
}
