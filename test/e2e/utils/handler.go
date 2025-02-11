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

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

type TestParameters struct {
	PkgDir string

	GkeClusterRegion    string
	GkeClusterVersion   string
	GkeNodeVersion      string
	GkeClusterName      string
	NodeImageType       string
	NodeMachineType     string
	NumNodes            int
	ProjectID           string
	UseGKEAutopilot     bool
	APIEndpointOverride string

	InProw             bool
	BoskosResourceType string

	ImageRegistry          string
	BuildGcsFuseCsiDriver  bool
	BuildGcsFuseFromSource bool
	BuildArm               bool
	DeployOverlayName      string
	UseGKEManagedDriver    bool

	GinkgoSkip          string
	GinkgoFocus         string
	GinkgoProcs         string
	GinkgoTimeout       string
	GinkgoFlakeAttempts string
	GinkgoSkipGcpSaTest bool

	SupportsNativeSidecar bool
	SupportSAVolInjection bool
	IstioVersion          string
	GcsfuseClientProtocol string
}

const (
	TestWithNativeSidecarEnvVar     = "TEST_WITH_NATIVE_SIDECAR"
	TestWithSAVolumeInjectionEnvVar = "TEST_WITH_SA_VOL_INJECTION"
)

func Handle(testParams *TestParameters) error {
	oldMask := syscall.Umask(0o000)
	defer syscall.Umask(oldMask)

	// If the test is running in Prow, do the following steps:
	// 1. Get the old project ID.
	// 2. Acquire and set up a new project through Boskos.
	// 3. Create a GKE cluster.
	// 4. After the test, tear down the cluster, and switch back to the old project.
	if testParams.InProw {
		// 1. Get the old project ID.
		output, err := exec.Command("gcloud", "config", "get-value", "project").CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to get gcloud project, output: %v, err: %w", string(output), err)
		}
		oldProject := string(output)

		// 2. Acquire and set up a new project through Boskos.
		newProject := setupProwConfig(testParams.BoskosResourceType)
		if _, ok := os.LookupEnv("USER"); !ok {
			if err := os.Setenv("USER", "prow"); err != nil {
				return fmt.Errorf("failed to set user in prow to prow: %w", err)
			}
		}

		if err := setEnvProject(newProject); err != nil {
			return fmt.Errorf("failed to set project environment to %s: %w", newProject, err)
		}
		testParams.ProjectID = newProject
		testParams.ImageRegistry = fmt.Sprintf("gcr.io/%s/gcs-fuse-csi-driver", strings.TrimSpace(newProject))

		// 4. After the test, tear down the cluster, and switch back to the old project.
		defer func() {
			if err := setEnvProject(oldProject); err != nil {
				klog.Errorf("failed to set project environment to %s: %v", oldProject, err)
			}
		}()

		// 3. Create a GKE cluster.
		testParams.GkeClusterName = "gcsfuse" + string(uuid.NewUUID())[0:4]
		if err := clusterUpGKE(testParams); err != nil {
			return fmt.Errorf("failed to cluster up: %w", err)
		}

		// 4. After the test, tear down the cluster, and switch back to the old project.
		defer func() {
			if err := clusterDownGKE(testParams); err != nil {
				klog.Errorf("failed to cluster down: %v", err)
			}
		}()
	}
	// TODO(jaimebz): Extract server version using kubeapi if not present.

	// Build and push the driver if the test does not use the pre-installed managed CSI driver. Defer the driver image deletion.
	if !testParams.UseGKEManagedDriver {
		if testParams.BuildGcsFuseCsiDriver {
			klog.Infof("Building GCS FUSE CSI Driver")
			if err := buildAndPushImage(testParams.PkgDir, testParams.ImageRegistry, testParams.BuildGcsFuseFromSource, testParams.BuildArm); err != nil {
				return fmt.Errorf("failed pushing GCS FUSE CSI Driver images: %w", err)
			}

			// Defer the image deletion.
			if testParams.InProw {
				defer func() {
					if err := deleteImage(); err != nil {
						klog.Errorf("failed to delete GCS FUSE CSI Driver images: %v", err)
					}
				}()
			}
		}

		// Uninstall and install the CSI driver.
		if err := deleteDriver(testParams.PkgDir, testParams.DeployOverlayName); err != nil {
			klog.Errorf("failed to delete CSI driver: %v", err)
		}
		if err := installDriver(testParams.PkgDir, testParams.ImageRegistry, testParams.DeployOverlayName); err != nil {
			return fmt.Errorf("failed to install CSI Driver: %w", err)
		}
	}

	// Now that cluster is running and the CSI driver is installed, run the ginkgo tests on the cluster.
	artifactsDir, ok := os.LookupEnv("ARTIFACTS")
	if !ok {
		artifactsDir = testParams.PkgDir + "/_artifacts"
	}

	testFocusStr := testParams.GinkgoFocus
	if len(testFocusStr) != 0 {
		testFocusStr = fmt.Sprintf(".*%s.*", testFocusStr)
	}

	supportsNativeSidecar, err := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, nativeSidecarMinimumVersion)
	if err != nil {
		klog.Fatalf(`native sidecar support could not be determined: %v`, err)
	}
	testParams.SupportsNativeSidecar = supportsNativeSidecar

	if err = os.Setenv(TestWithNativeSidecarEnvVar, strconv.FormatBool(supportsNativeSidecar)); err != nil {
		klog.Fatalf(`env variable "%s" could not be set: %v`, TestWithNativeSidecarEnvVar, err)
	}

	supportSAVolInjection, err := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, saTokenVolInjectionMinimumVersion)
	if err != nil {
		klog.Fatalf(`SA Vol Injection support could not be determined: %v`, err)
	}
	testParams.SupportSAVolInjection = supportSAVolInjection

	if err = os.Setenv(TestWithSAVolumeInjectionEnvVar, strconv.FormatBool(supportSAVolInjection)); err != nil {
		klog.Fatalf(`env variable "%s" could not be set: %v`, TestWithSAVolumeInjectionEnvVar, err)
	}

	testSkipStr := generateTestSkip(testParams)
	if !strings.Contains(testSkipStr, "istio") && (len(testFocusStr) == 0 || strings.Contains(testFocusStr, "istio")) {
		installIstio(testParams.IstioVersion)
	}

	//nolint:gosec
	cmd := exec.Command("ginkgo", "run", "-v",
		"--procs", testParams.GinkgoProcs,
		"--flake-attempts", testParams.GinkgoFlakeAttempts,
		"--timeout", testParams.GinkgoTimeout,
		"--focus", testFocusStr,
		"--skip", testSkipStr,
		"--junit-report", "junit-gcsfusecsi.xml",
		"--output-dir", artifactsDir,
		testParams.PkgDir+"/test/e2e/",
		"--",
		"--client-protocol", testParams.GcsfuseClientProtocol,
		"--provider", "skeleton",
		"--test-bucket-location", testParams.GkeClusterRegion,
		"--skip-gcp-sa-test", strconv.FormatBool(testParams.GinkgoSkipGcpSaTest),
		"--api-env", envAPIMap[testParams.APIEndpointOverride],
	)

	if err := runCommand("Running Ginkgo e2e test...", cmd); err != nil {
		return fmt.Errorf("failed to run e2e tests with ginkgo: %w", err)
	}

	return nil
}

func generateTestSkip(testParams *TestParameters) string {
	skipTests := []string{}

	if testParams.GinkgoSkip != "" {
		skipTests = append(skipTests, testParams.GinkgoSkip)
	}

	if testParams.DeployOverlayName == "stable" {
		skipTests = append(skipTests, "Dynamic.PV")
	}

	if testParams.UseGKEAutopilot {
		skipTests = append(skipTests, "OOM", "high.resource.usage", "gcsfuseIntegration", "istio")
	}

	if !testParams.SupportsNativeSidecar {
		skipTests = append(skipTests, "init.container", "fast.termination")
		skipTests = append(skipTests, "metadata.prefetch")
	}

	if testParams.UseGKEManagedDriver {
		skipTests = append(skipTests, "metrics")
		skipTests = append(skipTests, "read.ahead")
		// TODO(jaimebz): Skip this test until Managed Driver has changes released.
		skipTests = append(skipTests, "metadata.prefetch")

		// TODO(saikatroyc) remove this skip when GCSFuse CSI v1.4.3 is back-ported to the below GKE versions.
		if strings.HasPrefix(testParams.GkeClusterVersion, "1.27") || strings.HasPrefix(testParams.GkeClusterVersion, "1.28") {
			skipTests = append(skipTests, "csi-skip-bucket-access-check")
		}
	}

	skipTests = append(skipTests, "flaky")

	skipString := strings.Join(skipTests, "|")

	klog.Infof("Generated ginkgo skip string: %q", skipString)

	return skipString
}

func installIstio(istioVersion string) {
	if err := os.Setenv("ISTIO_VERSION", istioVersion); err != nil {
		klog.Fatalf(`env variable "ISTIO_VERSION" could not be set: %v`, err)
	}

	cmd := exec.Command("bash", "./test/e2e/utils/install-istio.sh")
	if err := runCommand("Installing Istio...", cmd); err != nil {
		klog.Fatalf(`failed to install Istio: %v`, err)
	}
}
