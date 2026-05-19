/*
Copyright 2018 The Kubernetes Authors.
Copyright 2026 Google LLC

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

package testsuites

import (
	"context"
	"fmt"
	"os"

	"local/test/e2e/specs"
	"local/test/e2e/utils"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	cloudprofiler "google.golang.org/api/cloudprofiler/v2"
	"google.golang.org/api/option"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	sidecarServiceName = webhook.GcsFuseSidecarName
	gcsfuseServiceName = "gcsfuse"
)

type gcsFuseCSICloudProfilerTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSICloudProfilerTestSuite returns gcsFuseCSICloudProfilerTestSuite that implements TestSuite interface.
func InitGcsFuseCSICloudProfilerTestSuite() storageframework.TestSuite {
	return &gcsFuseCSICloudProfilerTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "cloudProfiler",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSICloudProfilerTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSICloudProfilerTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSICloudProfilerTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("cloud-profiler", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	testCaseCloudProfilerProfileCreation := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetName("gcsfuse-cloud-profiler-tester")
		l.volumeResource.VolSource.CSI.VolumeAttributes["enableCloudProfilerForSidecar"] = "true"
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Fetching pod UID to build expected profiler version string")
		livePod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(ctx, tPod.GetPodName(), metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		expectedVersion := util.GetCloudProfilerServiceVersion(livePod.Name, string(livePod.UID))

		ginkgo.By("Checking that the sidecar logs the correct profiler version string")
		expectedLogLine := fmt.Sprintf("Running cloud profiler on %v with version %s", sidecarServiceName, expectedVersion)

		// Wait for the log to appear
		tPod.WaitForLog(ctx, sidecarServiceName, expectedLogLine)

		ginkgo.By("Generating load")
		// Write a 10MB file to the mount path to generate activity for the profiler to capture.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("head -c 10485760 </dev/urandom > %s/test.bin", mountPath))

		ginkgo.By("Checking that sidecar profile is generated")
		framework.Logf("Checking if sidecar profile exists for version %s", expectedVersion)
		gomega.Eventually(ctx, func(g gomega.Gomega) {
			// Check Sidecar Profile
			sidecarOk, err := checkIfProfileExistForServiceAndVersion(ctx, sidecarServiceName, expectedVersion)
			g.Expect(err).NotTo(gomega.HaveOccurred(), fmt.Sprintf("failed to check sidecar profile for version %s", expectedVersion))
			g.Expect(sidecarOk).To(gomega.BeTrue(), fmt.Sprintf("sidecar profile does not exist yet for version %s", expectedVersion))
		}, "10m", "10s").Should(gomega.Succeed())

		ginkgo.By("Checking that gcsfuse profile is generated")
		framework.Logf("Checking if gcsfuse profile exists for version %s", expectedVersion)
		gomega.Eventually(ctx, func(g gomega.Gomega) {
			// Check GCSFuse Profile
			gcsfuseOk, err := checkIfProfileExistForServiceAndVersion(ctx, gcsfuseServiceName, expectedVersion)
			g.Expect(err).NotTo(gomega.HaveOccurred(), fmt.Sprintf("failed to check gcsfuse profile for version %s", expectedVersion))
			g.Expect(gcsfuseOk).To(gomega.BeTrue(), fmt.Sprintf("gcsfuse profile does not exist yet for version %s", expectedVersion))
		}, "10m", "10s").Should(gomega.Succeed())
	}

	ginkgo.It("cloud_profiler should create profiles for sidecar and gcsfuse", ginkgo.SpecPriority(10), func() {
		testCaseCloudProfilerProfileCreation("")
	})
}

func checkIfProfileExistForServiceAndVersion(ctx context.Context, serviceName, version string) (bool, error) {
	framework.Logf("Checking if profile exists for service %q and version %q", serviceName, version)
	service, err := cloudprofiler.NewService(ctx, option.WithScopes(cloudprofiler.CloudPlatformScope))
	if err != nil {
		return false, err
	}
	projectID := os.Getenv(utils.ProjectEnvVar)
	parent := "projects/" + projectID

	call := service.Projects.Profiles.List(parent)

	var found bool
	stopErr := fmt.Errorf("stop iteration")
	// Use Pages to iterate through all profiles, handling pagination automatically.
	err = call.Pages(ctx, func(page *cloudprofiler.ListProfilesResponse) error {
		framework.Logf("Scanning profiles for service %q", serviceName)
		for _, profile := range page.Profiles {
			if profile.Deployment != nil && profile.Deployment.Target == serviceName && profile.Deployment.Labels != nil && profile.Deployment.Labels["version"] == version {
				found = true
				framework.Logf("Found profile for service %q and version %q", serviceName, version)
				// Return a specific error to break out of the pagination early.
				return stopErr
			}
		}
		return nil
	})

	if err != nil && err != stopErr {
		return false, err
	}

	if !found {
		return false, fmt.Errorf("profile not found for service %q and version %q", serviceName, version)
	}

	return true, nil
}
