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
	"slices"
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
	GkeReleaseChannel   string
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
	SkipCSIDriverInstall   bool

	GinkgoSkip          string
	GinkgoFocus         string
	GinkgoProcs         string
	GinkgoTimeout       string
	GinkgoFlakeAttempts string
	GinkgoSkipGcpSaTest bool

	SupportsNativeSidecar          bool
	SupportSAVolInjection          bool
	SupportMachineTypeAutoconfig   bool
	IstioVersion                   string
	GcsfuseClientProtocol          string
	EnableZB                       bool
	EnableSidecarBucketAccessCheck bool
	EnableGcsFuseProfiles          bool
	EnableGCSFuseKernelParams      bool

	GkeGcloudCommand string
	GkeGcloudArgs    string
}

const (
	TestWithNativeSidecarEnvVar            = "TEST_WITH_NATIVE_SIDECAR"
	TestWithSAVolumeInjectionEnvVar        = "TEST_WITH_SA_VOL_INJECTION"
	TestWithSidecarBucketAccessCheckEnvVar = "TEST_WITH_SIDECAR_BUCKET_ACCESS_CHECK"
	ClusterNameEnvVar                      = "CLUSTER_NAME"
	ClusterLocationEnvVar                  = "CLUSTER_LOCATION"
	ProjectEnvVar                          = "PROJECT"
	ProjectNumberEnvVar                    = "PROJECT_NUMBER"
	TestEnvEnvVar                          = "TEST_ENV"
	IsOSSEnvVar                            = "IS_OSS"
	IsZBEnabledEnvVar                      = "IS_ZB_ENABLED"
)

var skipDynamicPVTests = []string{"stable", "sidecar_bucket_access_check", "profiles"}

func Handle(testParams *TestParameters) error {
	setTestEnvVars(testParams)
	// Validating the test parameters.

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

		// Set env variables for OIDC auth prow tests. For local tests, these
		// env variables are set in the Makefile from kubectl config current-context.
		if err = os.Setenv(ClusterNameEnvVar, testParams.GkeClusterName); err != nil {
			klog.Fatalf(`env variable %q could not be set: %v`, ClusterNameEnvVar, err)
		}
		if err = os.Setenv(ClusterLocationEnvVar, testParams.GkeClusterRegion); err != nil {
			klog.Fatalf(`env variable %q could not be set: %v`, ClusterLocationEnvVar, err)
		}

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

		// Fetch the cluster version.
		cmd := exec.Command("bash", "-c",
			fmt.Sprintf("gcloud container clusters describe %s --location %s --format=\"value(currentMasterVersion)\"",
				testParams.GkeClusterName,
				testParams.GkeClusterRegion))
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to get cluster version, output: %s, err: %w", string(output), err)
		}
		testParams.GkeClusterVersion = strings.TrimSpace(string(output))
		klog.Infof("GKE cluster version: %s", testParams.GkeClusterVersion)
	}
	// TODO(jaimebz): Extract server version using kubeapi if not present.

	if testParams.SkipCSIDriverInstall && testParams.UseGKEManagedDriver {
		return fmt.Errorf("--skip-csi-driver-install used with --use-gke-managed-driver, this is invalid")
	}

	// Build and push the driver if the test does not use the pre-installed managed CSI driver. Defer the driver image deletion.
	if !testParams.UseGKEManagedDriver && !testParams.SkipCSIDriverInstall {
		os.Setenv(IsOSSEnvVar, "true")
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

	managedDriverVersionForHostNetworkTokenServerSatisfied, err := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, SaTokenVolInjectionMinimumVersion)
	if err != nil {
		klog.Fatalf(`managed driver version for host network token server support could not be determined: %v`, err)
	}
	supportSAVolInjection := (supportsNativeSidecar && !testParams.UseGKEManagedDriver) || managedDriverVersionForHostNetworkTokenServerSatisfied
	testParams.SupportSAVolInjection = supportSAVolInjection

	if err = os.Setenv(TestWithSAVolumeInjectionEnvVar, strconv.FormatBool(supportSAVolInjection)); err != nil {
		klog.Fatalf(`env variable "%s" could not be set: %v`, TestWithSAVolumeInjectionEnvVar, err)
	}
	supportSidecarBucketAccessCheckMinVersionSatisfied, err := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, "" /*CSI version depends on the master version only*/, sidecarBucketAccessCheckMinimumVersion)
	if err != nil {
		klog.Fatalf(`managed driver version for sidecar bucket access check support could not be determined: %v`, err)
	}
	supportSidecarBucketAccessCheck := !testParams.UseGKEManagedDriver || (testParams.UseGKEManagedDriver && supportSidecarBucketAccessCheckMinVersionSatisfied)

	if err = os.Setenv(TestWithSidecarBucketAccessCheckEnvVar, strconv.FormatBool(supportSidecarBucketAccessCheck && testParams.EnableSidecarBucketAccessCheck)); err != nil {
		klog.Fatalf(`env variable "%s" could not be set: %v`, TestWithSidecarBucketAccessCheckEnvVar, err)
	}

	supportsMachineTypeAutoConfig, err := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, supportsMachineTypeAutoConfigMinimumVersion)
	if err != nil {
		klog.Fatalf(`support for intelligent defaults on high-performance machine types could not be determined: %v`, err)
	}

	testParams.SupportMachineTypeAutoconfig = supportsMachineTypeAutoConfig

	testSkipStr := generateTestSkip(testParams)
	if !strings.Contains(testSkipStr, "istio") && (len(testFocusStr) == 0 || strings.Contains(testFocusStr, "istio")) {
		installIstio(testParams.IstioVersion)
	}

	// Setting project number and adding compute binding for the driver service account for Storage Profiles.
	if testParams.EnableGcsFuseProfiles {
		os.Setenv(TestEnvEnvVar, envAPIMap[testParams.APIEndpointOverride])
		// Project id is not already set when using managed driver flow.
		if testParams.ProjectID == "" {
			output, err := exec.Command("gcloud", "config", "get-value", "project").CombinedOutput()
			if err != nil {
				klog.Errorf("Failed while prepping env for profiles tests: %v", fmt.Errorf("failed to get gcloud project, output: %v, err: %w", string(output), err))
			}
			testParams.ProjectID = strings.TrimSpace(string(output))
		}

		// TODO(fuechr): Reenable custom role once we have a way to create it in boskos projects.
		_, err := setEnvProjectNumberUsingID(testParams.ProjectID)
		if err != nil {
			klog.Errorf("Failed while prepping env for profiles tests: %v", err)
		}
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
		fmt.Sprintf("--enable-zb=%s", strconv.FormatBool(testParams.EnableZB)),
		fmt.Sprintf("--skip-gcp-sa-test=%s", strconv.FormatBool(testParams.GinkgoSkipGcpSaTest)),
		fmt.Sprintf("--enable-gcsfuse-profiles-test=%s", strconv.FormatBool(testParams.EnableGcsFuseProfiles)),
		fmt.Sprintf("--enable-gcsfuse-kernel-params-test=%s", strconv.FormatBool(testParams.EnableGCSFuseKernelParams)),
		"--api-env", envAPIMap[testParams.APIEndpointOverride],
	)

	if err := runCommand("Running Ginkgo e2e test...", cmd); err != nil {
		return fmt.Errorf("failed to run e2e tests with ginkgo: %w", err)
	}

	return nil
}

func setTestEnvVars(testParams *TestParameters) {
	if err := os.Setenv(IsZBEnabledEnvVar, strconv.FormatBool(testParams.EnableZB)); err != nil {
		klog.Errorf(`env variable "%s" could not be set: %v`, IsZBEnabledEnvVar, err)
	}
}

func generateTestSkip(testParams *TestParameters) string {
	skipTests := []string{}

	if testParams.GinkgoSkip != "" {
		skipTests = append(skipTests, testParams.GinkgoSkip)
	}

	if slices.Contains(skipDynamicPVTests, testParams.DeployOverlayName) {
		skipTests = append(skipTests, "Dynamic.PV")
	}

	if testParams.UseGKEAutopilot {
		skipTests = append(skipTests, "OOM", "high.resource.usage", "gcsfuseIntegration", "istio")
	}

	if testParams.EnableGcsFuseProfiles {
		skipTests = append(skipTests, "should.not.pass.profile")
	}

	if testParams.EnableGCSFuseKernelParams {
		skipTests = append(skipTests, "should.not.pass.kernel.params.file")
	} else {
		skipTests = append(skipTests, "should.pass.kernel.params.file")
	}

	if !testParams.SupportsNativeSidecar {
		skipTests = append(skipTests, "init.container", "fast.termination")
		skipTests = append(skipTests, "should.not.pass.profile")
		skipTests = append(skipTests, "metadata.prefetch")
	}

	supportsLongMountOptions, _ := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, longMountOptionsMinimumVersion)
	if !supportsLongMountOptions {
		skipTests = append(skipTests, "long.mount.options")
	}

	if testParams.UseGKEAutopilot {
		skipTests = append(skipTests, "hostnetwork.enabled.pods")
	}

	if testParams.UseGKEManagedDriver {
		skipTests = append(skipTests, "metrics") // Skipping as these tests are known to be unstable

		skipTests = append(skipTests, "should.not.pass.profile") // Skipping for managed as changes have not been picked up yet

		skipTests = append(skipTests, "oidc") // OIDC authentication requires non-managed driver features

		supportsKernelReadAhead, _ := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, kernelReadAheadMinimumVersion)
		if !supportsKernelReadAhead {
			skipTests = append(skipTests, "read.ahead")
		}

		supportsMetadataPrefetch, _ := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, metadataPrefetchMinimumVersion)
		if !supportsMetadataPrefetch {
			skipTests = append(skipTests, "metadata.prefetch")
		}

		supportsSkipBucketAccessCheck, _ := ClusterAtLeastMinVersion(testParams.GkeClusterVersion, testParams.GkeNodeVersion, skipBucketCheckMinimumVersion)
		// Note that for local testing you want to use the dev deployment that sets --assume-good-sidecar-version, for these tests to pass.
		if !supportsSkipBucketAccessCheck {
			skipTests = append(skipTests, "csi-skip-bucket-access-check")
		}
	}

	if !testParams.SupportMachineTypeAutoconfig {
		skipTests = append(skipTests, "disable-autoconfig")
	}

	if testParams.InProw {
		// TODO(amacaskill): Remove oidc from skipTests when the test can be run without additional configuration.
		// See https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/979#issuecomment-3445151653.
		skipTests = append(skipTests, "oidc")
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
