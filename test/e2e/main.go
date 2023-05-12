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

package e2etest

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
)

var (
	// Kubernetes cluster flags
	teardownCluster      = flag.Bool("teardown-cluster", true, "teardown the cluster after the e2e test")
	teardownDriver       = flag.Bool("teardown-driver", true, "teardown the driver after the e2e test")
	bringupCluster       = flag.Bool("bringup-cluster", true, "build kubernetes and bringup a cluster")
	platform             = flag.String("platform", "linux", "platform that the tests will be run, either linux or windows")
	gceZone              = flag.String("gce-zone", "", "zone that the gce k8s cluster is created/found in")
	gceRegion            = flag.String("gce-region", "", "region that gke regional cluster should be created in")
	testVersion          = flag.String("test-version", "", "version of Kubernetes to download and use for tests")
	localK8sDir          = flag.String("local-k8s-dir", "", "local prebuilt kubernetes/kubernetes directory to use for cluster and test binaries")
	deploymentStrat      = flag.String("deployment-strategy", "gce", "choose between deploying on gce or gke")
	gkeClusterVer        = flag.String("gke-cluster-version", "", "version of Kubernetes master and node for gke")
	numNodes             = flag.Int("num-nodes", 0, "the number of nodes in the test cluster")
	numWindowsNodes      = flag.Int("num-windows-nodes", 0, "the number of Windows nodes in the test cluster")
	gkeReleaseChannel    = flag.String("gke-release-channel", "", "GKE release channel to be used for cluster deploy. One of 'rapid', 'stable' or 'regular'")
	gkeTestClusterPrefix = flag.String("gke-cluster-prefix", "gcsfuse", "Prefix of GKE cluster names. A random suffix will be appended to form the full name.")
	gkeTestClusterName   = flag.String("gke-cluster-name", "", "Name of existing cluster")
	gkeNodeVersion       = flag.String("gke-node-version", "", "GKE cluster worker node version")
	isRegionalCluster    = flag.Bool("is-regional-cluster", false, "tell the test that a regional cluster is being used. Should be used for running on an existing regional cluster (ie, --bringup-cluster=false). The test will fail if a zonal GKE cluster is created when this flag is true")

	// Test infrastructure flags
	boskosResourceType = flag.String("boskos-resource-type", "gce-project", "name of the boskos resource type to reserve")
	storageClassFiles  = flag.String("storageclass-files", "", "name of storageclass yaml file to use for test relative to test/k8s-integration/config. This may be a comma-separated list to test multiple storage classes")
	inProw             = flag.Bool("run-in-prow", true, "is the test running in PROW")

	// Driver flags
	stagingImage      = flag.String("staging-image", "", "name of image to stage to")
	saFile            = flag.String("service-account-file", "", "path of service account file")
	deployOverlayName = flag.String("deploy-overlay-name", "", "which kustomize overlay to deploy the driver with")
	doDriverBuild     = flag.Bool("do-driver-build", true, "building the driver from source")
	useManagedDriver  = flag.Bool("use-gke-managed-driver", false, "use GKE managed PD CSI driver for the tests")
	useAutopilot      = flag.Bool("use-autopilot", false, "use GKE Autopilot cluster for the tests")

	// Test flags
	migrationTest = flag.Bool("migration-test", false, "sets the flag on the e2e binary signalling migration")

	useKubeTest2 = flag.Bool("use-kubetest2", false, "use kubetest2 to run e2e tests")
	parallel     = flag.Int("parallel", 4, "the number of parallel tests setting for ginkgo parallelism")
)

const (
	pdImagePlaceholder        = "gke.gcr.io/gcp-compute-persistent-disk-csi-driver"
	k8sInDockerBuildBinDir    = "_output/dockerized/bin/linux/amd64"
	k8sOutOfDockerBuildBinDir = "_output/bin"
	externalDriverNamespace   = "gce-pd-csi-driver"
	managedDriverNamespace    = "kube-system"
	regionalPDStorageClass    = "sc-regional.yaml"
	imageType                 = "cos_containerd"
)

type testParameters struct {
	platform             string
	stagingVersion       string
	goPath               string
	pkgDir               string
	testParentDir        string
	k8sSourceDir         string
	testSkip             string
	cloudProviderArgs    []string
	deploymentStrategy   string
	outputDir            string
	allowedNotReadyNodes int
	useManagedDriver     bool
	useAutopilot         bool
	clusterVersion       string
	nodeVersion          string
	projectID            string
	imageType            string
	parallel             int
}

func init() {
	flag.Set("logtostderr", "true")
}

func main() {
	klog.InitFlags(nil)
	flag.Set("logtostderr", "true")
	flag.Parse()

	if *useManagedDriver {
		*doDriverBuild = false
		*teardownDriver = false
	}

	if !*inProw && *doDriverBuild {
		ensureVariable(stagingImage, true, "staging-image is a required flag, please specify the name of image to stage to")
	}

	if *useManagedDriver {
		ensureFlag(doDriverBuild, false, "'do-driver-build' must be false when using GKE managed driver")
		ensureFlag(teardownDriver, false, "'teardown-driver' must be false when using GKE managed driver")
		ensureVariable(stagingImage, false, "'staging-image' must not be set when using GKE managed driver")
		ensureVariable(deployOverlayName, false, "'deploy-overlay-name' must not be set when using GKE managed driver")
	}

	if !*useManagedDriver {
		ensureVariable(deployOverlayName, true, "deploy-overlay-name is a required flag")
	}

	if len(*gceRegion) != 0 {
		ensureVariable(gceZone, false, "gce-zone and gce-region cannot both be set")
	} else {
		ensureVariable(gceZone, true, "One of gce-zone or gce-region must be set")
	}

	// TODO(amacaskill): make sure cluster version is greater than 1.26.3-gke.1000.
	ensureExactlyOneVariableSet([]*string{gkeClusterVer, gkeReleaseChannel},
		"For GKE cluster deployment, exactly one of 'gke-cluster-version' or 'gke-release-channel' must be set")
	if len(*localK8sDir) == 0 {
		ensureVariable(testVersion, true, "Must set either test-version or local k8s dir when deploying on 'gke'.")
	}

	if len(*gkeTestClusterName) == 0 {
		randSuffix := string(uuid.NewUUID())[0:4]
		*gkeTestClusterName = *gkeTestClusterPrefix + randSuffix
	}

	if len(*localK8sDir) != 0 {
		ensureVariable(testVersion, false, "Cannot set a test version when using a local k8s dir.")
	}

	if *numNodes == 0 && *bringupCluster {
		klog.Fatalf("num-nodes must be set to number of nodes in cluster")
	}
	if *numWindowsNodes == 0 && *bringupCluster && *platform == "windows" {
		klog.Fatalf("num-windows-nodes must be set if the platform is windows")
	}

	err := handle()
	if err != nil {
		klog.Fatalf("Failed to run integration test: %w", err)
	}
}

func handle() error {
	oldmask := syscall.Umask(0000)
	defer syscall.Umask(oldmask)

	testParams := &testParameters{
		platform:         *platform,
		stagingVersion:   string(uuid.NewUUID()),
		useManagedDriver: *useManagedDriver,
		imageType:        imageType,
		useAutopilot:     *useAutopilot,
		parallel:         *parallel,
	}

	goPath, ok := os.LookupEnv("GOPATH")
	if !ok {
		return fmt.Errorf("Could not find env variable GOPATH")
	}
	testParams.goPath = goPath
	testParams.pkgDir = filepath.Join(goPath, "src", "sigs.k8s.io", "gcp-compute-persistent-disk-csi-driver")

	// If running in Prow, then acquire and set up a project through Boskos
	if *inProw {
		oldProject, err := exec.Command("gcloud", "config", "get-value", "project").CombinedOutput()
		project := strings.TrimSpace(string(oldProject))
		if err != nil {
			return fmt.Errorf("failed to get gcloud project: %s, err: %w", oldProject, err)
		}
		newproject, _ := SetupProwConfig(*boskosResourceType)
		err = setEnvProject(newproject)
		if err != nil {
			return fmt.Errorf("failed to set project environment to %s: %w", newproject, err)
		}
		testParams.projectID = newproject

		defer func() {
			err = setEnvProject(string(oldProject))
			if err != nil {
				klog.Errorf("failed to set project environment to %s: %w", oldProject, err.Error())
			}
		}()
		project = newproject
		if *doDriverBuild {
			*stagingImage = fmt.Sprintf("gcr.io/%s/gcp-persistent-disk-csi-driver", strings.TrimSpace(string(project)))
		}
		if _, ok := os.LookupEnv("USER"); !ok {
			err = os.Setenv("USER", "prow")
			if err != nil {
				return fmt.Errorf("failed to set user in prow to prow: %v", err.Error())
			}
		}
	}

	// Build and push the driver, if required. Defer the driver image deletion.
	if *doDriverBuild {
		klog.Infof("Building GCS Fuse CSI Driver")
		err := pushImage(testParams.pkgDir, *stagingImage, testParams.stagingVersion, testParams.platform)
		if err != nil {
			return fmt.Errorf("failed pushing image: %v", err.Error())
		}
		defer func() {
			if *teardownCluster {
				err := deleteImage(*stagingImage, testParams.stagingVersion)
				if err != nil {
					klog.Errorf("failed to delete image: %w", err)
				}
			}
		}()
	}

	// Either GKE or a prebuild local K8s dir is being used
	testParams.k8sSourceDir = *localK8sDir

	gkeRegional := isRegionalGKECluster(*gceZone, *gceRegion)
	if *isRegionalCluster && !gkeRegional {
		return fmt.Errorf("--is-regional-cluster set but deployed GKE cluster would be zonal")
	}
	*isRegionalCluster = gkeRegional

	// Create a cluster either through GKE.
	if *bringupCluster {
		if err := clusterUpGKE(*gceZone, *gceRegion, *numNodes, *numWindowsNodes, "cos_containerd", testParams.useManagedDriver, testParams.projectID); err != nil {
			return fmt.Errorf("failed to cluster up: %w", err)
		}
	}

	// Defer the tear down of the cluster through GKE.
	if *teardownCluster {
		defer func() {
			if err := clusterDownGKE(*gceZone, *gceRegion); err != nil {
				klog.Errorf("failed to cluster down: %w", err)
			}
		}()
	}

	// TODO(amacaskill): Install the driver and defer its teardown if !testParams.useManagedDriver.

	// Kubernetes version of GKE deployments are expected to be of the pattern x.y.z-gke.k,
	// hence we use the main.Version utils to parse and compare GKE managed cluster versions.
	testParams.clusterVersion = mustGetKubeClusterVersion()
	klog.Infof("kubernetes cluster server version: %s", testParams.clusterVersion)
	testParams.nodeVersion = *gkeNodeVersion
	testParams.testSkip = generateTestSkip(testParams)

	return nil
}

func generateTestSkip(testParams *testParameters) string {
	skipString := "Dynamic.PV"
	if testParams.useAutopilot {
		skipString = skipString + "|OOM|high.resource.usage"
	}
	return skipString
}

func setEnvProject(project string) error {
	out, err := exec.Command("gcloud", "config", "set", "project", project).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set gcloud project to %s: %s, err: %v", project, out, err.Error())
	}

	err = os.Setenv("PROJECT", project)
	if err != nil {
		return err
	}
	return nil
}
