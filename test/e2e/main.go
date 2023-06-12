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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
)

var envAPIMap = map[string]string{
	"https://container.googleapis.com/":                  "prod",
	"https://staging-container.sandbox.googleapis.com/":  "staging",
	"https://staging2-container.sandbox.googleapis.com/": "staging2",
	"https://test-container.sandbox.googleapis.com/":     "test",
}

var (
	// Kubernetes cluster flags.
	gceRegion           = flag.String("region", "", "region that gke regional cluster should be created in")
	gkeClusterVer       = flag.String("gke-cluster-version", "", "version of Kubernetes master and node for gke")
	gkeNodeVersion      = flag.String("gke-node-version", "", "GKE cluster worker node version")
	nodeMachineType     = flag.String("node-machine-type", "n1-standard-2", "GKE cluster worker node machine type")
	numNodes            = flag.Int("number-nodes", 3, "the number of nodes in the test cluster")
	gkeTestClusterName  = flag.String("gke-cluster-name", "", "Name of test cluster")
	testProjectID       = flag.String("test-project-id", "", "Project ID of the test cluster")
	bringUpCluster      = flag.Bool("bring-up-cluster", true, "Whether or not to create a cluster")
	tearDownCluster     = flag.Bool("tear-down-cluster", true, "Whether or not to create a cluster")
	useGKEAutopilot     = flag.Bool("use-gke-autopilot", false, "use GKE Autopilot cluster for the tests")
	apiEndpointOverride = flag.String("api-endpoint-override", "", "The CloudSDK api endpoint override to use for the cluster environment.")

	imageType = flag.String("image-type", "cos_containerd", "the image type to use for the cluster")

	// Test infrastructure flags.
	boskosResourceType = flag.String("boskos-resource-type", "gke-internal-project", "name of the boskos resource type to reserve")
	inProw             = flag.Bool("run-in-prow", false, "is the test running in PROW")
	testFocus          = flag.String("test-focus", "*", "The ginkgo test focus.")

	// Driver flags.
	stagingImage        = flag.String("staging-image", "", "name of image to stage to")
	deployOverlayName   = flag.String("deploy-overlay-name", "", "which kustomize overlay to deploy the driver with")
	doDriverBuild       = flag.Bool("do-driver-build", true, "building the driver from source")
	useGKEManagedDriver = flag.Bool("use-gke-managed-driver", false, "use GKE managed PD CSI driver for the tests")
)

type testParameters struct {
	stagingVersion      string
	goPath              string
	pkgDir              string
	testSkip            string
	useGKEManagedDriver bool
	useGKEAutopilot     bool
	clusterVersion      string
	nodeVersion         string
	projectID           string
	imageType           string
}

func main() {
	klog.InitFlags(nil)
	if err := flag.Set("logtostderr", "true"); err != nil {
		klog.Warningf("Failed to set flags: %w", err)
	}
	flag.Parse()

	ensureVariable(gceRegion, true, "region must be set")

	if *useGKEManagedDriver {
		ensureFlag(doDriverBuild, false, "'do-driver-build' must be false when using GKE managed driver")
		ensureVariable(stagingImage, false, "'staging-image' must not be set when using GKE managed driver")
		ensureVariable(deployOverlayName, false, "'deploy-overlay-name' must not be set when using GKE managed driver")
	}

	if !*useGKEManagedDriver {
		ensureVariable(deployOverlayName, true, "deploy-overlay-name is a required flag")
	}

	if !*bringUpCluster {
		ensureVariable(gkeTestClusterName, true, "'gke-cluster-name' must be set when 'bring-up-cluster' is false")
	}
	if !*inProw {
		ensureVariable(testProjectID, true, "'test-project-id' must be set when not running in prow")
	}
	if len(*apiEndpointOverride) != 0 {
		if err := os.Setenv("CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER", *apiEndpointOverride); err != nil {
			klog.Fatalf("failed to set CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER to %q: %v", *apiEndpointOverride, err.Error())
		}
	}

	// TODO(amacaskill): make sure cluster version is greater than 1.26.3-gke.1000.
	ensureVariable(gkeClusterVer, true, "'gke-cluster-version' must be set")

	if len(*gkeTestClusterName) == 0 {
		randSuffix := string(uuid.NewUUID())[0:4]
		*gkeTestClusterName = "gcsfuse" + randSuffix
	}

	err := handle()
	if err != nil {
		klog.Fatalf("Failed to run integration test: %w", err)
	}
}

func handle() error {
	oldmask := syscall.Umask(0o000)
	defer syscall.Umask(oldmask)

	testParams := &testParameters{
		stagingVersion:      string(uuid.NewUUID()),
		useGKEManagedDriver: *useGKEManagedDriver,
		imageType:           *imageType,
		useGKEAutopilot:     *useGKEAutopilot,
	}

	goPath, ok := os.LookupEnv("GOPATH")
	if !ok {
		return fmt.Errorf("could not find env variable GOPATH")
	}
	testParams.goPath = goPath
	testParams.pkgDir = filepath.Join(goPath, "src", "GoogleCloudPlatform", "gcs-fuse-csi-driver")

	// TODO(amacaskill): Change e2e_test.go / Makefile to assign and use CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER directly.
	gkeEnv, ok := os.LookupEnv("CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER")
	if !ok {
		gkeEnv = "https://container.googleapis.com/"
	}
	if err := os.Setenv("E2E_TEST_API_ENV", envAPIMap[gkeEnv]); err != nil {
		return fmt.Errorf("failed to set E2E_TEST_API_ENV to %q: %v", envAPIMap[gkeEnv], err.Error())
	}
	if err := os.Setenv("E2E_TEST_SKIP_GCP_SA_TEST", "true"); err != nil {
		return fmt.Errorf("failed to set E2E_TEST_SKIP_GCP_SA_TEST to true: %v", err.Error())
	}

	oldProject, err := exec.Command("gcloud", "config", "get-value", "project").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get gcloud project: %s, err: %w", oldProject, err)
	}

	newproject := *testProjectID
	// If running in Prow, then acquire and set up a project through Boskos.
	if *inProw {
		newproject, _ = SetupProwConfig(*boskosResourceType)
		if _, ok := os.LookupEnv("USER"); !ok {
			err = os.Setenv("USER", "prow")
			if err != nil {
				return fmt.Errorf("failed to set user in prow to prow: %v", err.Error())
			}
		}
	}

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
	project := newproject
	if *doDriverBuild {
		*stagingImage = fmt.Sprintf("gcr.io/%s/gcs-fuse-csi-driver", strings.TrimSpace(project))
	}

	// Build and push the driver, if required. Defer the driver image deletion.
	if *doDriverBuild {
		klog.Infof("Building GCS Fuse CSI Driver")
		err := pushImage(testParams.pkgDir, fmt.Sprintf("gcr.io/%s", testParams.projectID), testParams.stagingVersion)
		if err != nil {
			return fmt.Errorf("failed pushing GCSFuse image: %v", err.Error())
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
	if err := clusterUpGKE(testParams.projectID, *gceRegion, *numNodes, *imageType, testParams.useGKEManagedDriver, testParams.useGKEAutopilot, *nodeMachineType); err != nil {
		return fmt.Errorf("failed to cluster up: %w", err)
	}

	// Defer the tear down of the cluster through GKE.
	if *tearDownCluster {
		defer func() {
			if err := clusterDownGKE(*gceRegion); err != nil {
				klog.Errorf("failed to cluster down: %w", err)
			}
		}()
	}

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

	// Now that clusters are running, we need to actually run the tests on the cluster with the following.
	// TODO(amacaskill): make ginkgo procs and artifacts path variables configurable.
	e2eGinkgoProcs := "5"
	timeout := "20m"
	artifactsDir, ok := os.LookupEnv("ARTIFACTS")
	if !ok {
		artifactsDir = "../../_artifacts"
	}
	klog.Infof("artifacts dir is %q", artifactsDir)

	//nolint:gosec
	out, err := exec.Command("ginkgo", "run", "--procs", e2eGinkgoProcs, "-v", "--flake-attempts", "2", "--timeout", timeout, "--focus", *testFocus, "--skip", testParams.testSkip, "--junit-report", "junit-gcsfusecsi.xml", "--output-dir", artifactsDir, "./test/e2e/", "--", "--provider", "skeleton").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to run tests with ginkgo: %s, err: %w", out, err)
	}

	return nil
}

func generateTestSkip(testParams *testParameters) string {
	skipString := "Dynamic.PV"
	if testParams.useGKEAutopilot {
		skipString += "|OOM|high.resource.usage"
	}
	// TODO(amacaskill): Remove this once these tests are ready to be run.
	skipString += "|failedMount|should.succeed.in.performance.test|should.store.data.and.retain.the.data.when.Pod.RestartPolicy.is.Never"
	klog.Infof("Generated testskip %q", skipString)

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
