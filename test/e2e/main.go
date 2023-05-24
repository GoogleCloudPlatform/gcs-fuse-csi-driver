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
	gceRegion          = flag.String("region", "", "region that gke regional cluster should be created in")
	gkeClusterVer      = flag.String("gke-cluster-version", "", "version of Kubernetes master and node for gke")
	gkeNodeVersion     = flag.String("gke-node-version", "", "GKE cluster worker node version")
	numNodes           = flag.Int("number-nodes", 3, "the number of nodes in the test cluster")
	gkeTestClusterName = flag.String("gke-cluster-name", "", "Name of test cluster")
	useGKEAutopilot    = flag.Bool("use-gke-autopilot", false, "use GKE Autopilot cluster for the tests")

	imageType = flag.String("image-type", "cos_containerd", "the image type to use for the cluster")

	// Test infrastructure flags
	boskosResourceType = flag.String("boskos-resource-type", "gce-project", "name of the boskos resource type to reserve")
	inProw             = flag.Bool("run-in-prow", true, "is the test running in PROW")

	// Driver flags
	stagingImage        = flag.String("staging-image", "", "name of image to stage to")
	saFile              = flag.String("service-account-file", "", "path of service account file")
	deployOverlayName   = flag.String("deploy-overlay-name", "", "which kustomize overlay to deploy the driver with")
	doDriverBuild       = flag.Bool("do-driver-build", true, "building the driver from source")
	useGKEManagedDriver = flag.Bool("use-gke-managed-driver", false, "use GKE managed PD CSI driver for the tests")

	// Test flags
	parallel = flag.Int("parallel", 4, "the number of parallel tests setting for ginkgo parallelism")
)

type testParameters struct {
	stagingVersion       string
	goPath               string
	pkgDir               string
	testParentDir        string
	k8sSourceDir         string
	testSkip             string
	cloudProviderArgs    []string
	outputDir            string
	allowedNotReadyNodes int
	useGKEManagedDriver  bool
	useGKEAutopilot      bool
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

	if *useGKEManagedDriver {
		ensureFlag(doDriverBuild, false, "'do-driver-build' must be false when using GKE managed driver")
		ensureVariable(stagingImage, false, "'staging-image' must not be set when using GKE managed driver")
		ensureVariable(deployOverlayName, false, "'deploy-overlay-name' must not be set when using GKE managed driver")
	}

	if !*useGKEManagedDriver {
		ensureVariable(deployOverlayName, true, "deploy-overlay-name is a required flag")
	}

	ensureVariable(gceRegion, true, "gce-region must be set")

	// TODO(amacaskill): make sure cluster version is greater than 1.26.3-gke.1000.
	ensureVariable(gkeClusterVer, true, "'gke-cluster-version' must be set")

	randSuffix := string(uuid.NewUUID())[0:4]
	*gkeTestClusterName = "gcsfuse" + randSuffix

	err := handle()
	if err != nil {
		klog.Fatalf("Failed to run integration test: %w", err)
	}
}

func handle() error {
	oldmask := syscall.Umask(0000)
	defer syscall.Umask(oldmask)

	testParams := &testParameters{
		stagingVersion:      string(uuid.NewUUID()),
		useGKEManagedDriver: *useGKEManagedDriver,
		imageType:           *imageType,
		useGKEAutopilot:     *useGKEAutopilot,
	}

	goPath, ok := os.LookupEnv("GOPATH")
	if !ok {
		return fmt.Errorf("Could not find env variable GOPATH")
	}
	testParams.goPath = goPath
	testParams.pkgDir = filepath.Join(goPath, "src", "GoogleCloudPlatform", "gcs-fuse-csi-driver")

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
			*stagingImage = fmt.Sprintf("gcr.io/%s/gcs-fuse-csi-driver", strings.TrimSpace(string(project)))
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
		err := pushImage(testParams.pkgDir, *stagingImage, testParams.stagingVersion)
		if err != nil {
			return fmt.Errorf("failed pushing image: %v", err.Error())
		}
		// Defer the cluster teardown.
		defer func() {
			err := deleteImage(*stagingImage, testParams.stagingVersion)
			if err != nil {
				klog.Errorf("failed to delete image: %w", err)
			}
		}()
	}

	// Create a cluster through GKE.
	if err := clusterUpGKE(testParams.projectID, *gceRegion, *numNodes, *imageType, testParams.useGKEManagedDriver, testParams.useGKEAutopilot); err != nil {
		return fmt.Errorf("failed to cluster up: %w", err)
	}

	// Defer the tear down of the cluster through GKE.
	defer func() {
		if err := clusterDownGKE(*gceRegion); err != nil {
			klog.Errorf("failed to cluster down: %w", err)
		}
	}()

	if !testParams.useGKEManagedDriver {
		// Install the driver and defer its teardown
		err := installDriver(testParams, *stagingImage, *deployOverlayName, *doDriverBuild)
		defer func() {
			if teardownErr := deleteDriver(testParams, *deployOverlayName); teardownErr != nil {
				klog.Errorf("failed to delete driver: %w", teardownErr)
			}
		}()
		if err != nil {
			return fmt.Errorf("failed to install CSI Driver: %w", err)
		}
	}

	// Kubernetes version of GKE deployments are expected to be of the pattern x.y.z-gke.k,
	// hence we use the main.Version utils to parse and compare GKE managed cluster versions.
	testParams.clusterVersion = mustGetKubeClusterVersion()
	klog.Infof("kubernetes cluster server version: %s", testParams.clusterVersion)
	testParams.nodeVersion = *gkeNodeVersion
	testParams.testSkip = generateTestSkip(testParams)

	// Now that clusters are running, we need to actually run the tests on the cluster with the following. We can probably just do exec.Command for this. Eventually I think it
	// would be good to change change the e2e-test make file function to work for manual test runs and this so that we can just call make run-e2e test so we don't have code copied,
	// but for the first iteration, I think its fine to just copy it into exec.command to see if it runs. It choses a cluster based on the current context (so we are good there)
	// TODO(amacaskill): make ginkgo procs and artifacts path variables configurable.
	e2eGinkgoProcs := "5"
	e2eArtifactsPath := "../../_artifacts"
	out, err := exec.Command("ginkgo", "run", "--procs", e2eGinkgoProcs, "-v", "--flake-attempts", "2", "--timeout", "20m", "--skip", testParams.testSkip, "./test/e2e/", "--", "-report-dir", e2eArtifactsPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to run tests with ginkgo: %s, err: %w", out, err)
	}
	return nil
}

func generateTestSkip(testParams *testParameters) string {
	skipString := "Dynamic.PV"
	if testParams.useGKEAutopilot {
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
