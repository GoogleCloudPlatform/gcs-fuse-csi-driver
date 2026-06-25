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
	"sync"
	"time"

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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
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

	testCaseCloudProfilerProfileCreation := func(configPrefix string, disableGcsfuseProfiler bool) {
		if zbEnabled(driver) {
			e2eskipper.Skipf("skip cloud_profiler tests when Zonal Buckets is enabled")
		}

		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetName("gcsfuse-cloud-profiler-tester")
		l.volumeResource.VolSource.CSI.VolumeAttributes["enableCloudProfilerForSidecar"] = "true"
		if disableGcsfuseProfiler {
			l.volumeResource.VolSource.CSI.VolumeAttributes["mountOptions"] = "enable-cloud-profiler=false"
		}
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

		if disableGcsfuseProfiler {
			ginkgo.By("Checking that GCSFuse profiler is disabled via logs")
			disabledLogLine := "Cloud Profiler for GCSFuse is disabled via mount options: enable-cloud-profiler=false"
			tPod.WaitForLog(ctx, sidecarServiceName, disabledLogLine)
		}

		ginkgo.By("Generating load")
		// Write a 10MB file to the mount path to generate activity for the profiler to capture.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("head -c 10485760 </dev/urandom > %s/test.bin", mountPath))

		ginkgo.By("Initializing Cloud Profiler client once")
		profilerClient, err := cloudprofiler.NewService(ctx, option.WithScopes(cloudprofiler.CloudPlatformScope))
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to initialize cloudprofiler service client")

		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()

			ginkgo.By("Checking that sidecar profile is generated")
			framework.Logf("Checking if sidecar profile exists for version %s", expectedVersion)
			gomega.Eventually(ctx, func(g gomega.Gomega) {
				// Check Sidecar Profile
				sidecarOk, err := checkIfProfileExistForServiceAndVersion(ctx, profilerClient, sidecarServiceName, expectedVersion)
				g.Expect(err).NotTo(gomega.HaveOccurred(), fmt.Sprintf("failed to check sidecar profile for version %s", expectedVersion))
				g.Expect(sidecarOk).To(gomega.BeTrue(), fmt.Sprintf("sidecar profile does not exist yet for version %s", expectedVersion))
			}, "10m", "10s").Should(gomega.Succeed())
		}()

		if !disableGcsfuseProfiler {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer ginkgo.GinkgoRecover()

				ginkgo.By("Checking that gcsfuse profile is generated")
				framework.Logf("Checking if gcsfuse profile exists for version %s", expectedVersion)
				gomega.Eventually(ctx, func(g gomega.Gomega) {
					// Check GCSFuse Profile
					gcsfuseOk, err := checkIfProfileExistForServiceAndVersion(ctx, profilerClient, gcsfuseServiceName, expectedVersion)
					g.Expect(err).NotTo(gomega.HaveOccurred(), fmt.Sprintf("failed to check gcsfuse profile for version %s", expectedVersion))
					g.Expect(gcsfuseOk).To(gomega.BeTrue(), fmt.Sprintf("gcsfuse profile does not exist yet for version %s", expectedVersion))
				}, "10m", "10s").Should(gomega.Succeed())
			}()
		}
		wg.Wait()
	}

	ginkgo.It("cloud_profiler should create profiles for sidecar and gcsfuse", ginkgo.SpecPriority(10), func() {
		testCaseCloudProfilerProfileCreation("", false)
	})

	ginkgo.It("cloud_profiler should only create profiles for sidecar when gcsfuse profiler is disabled", ginkgo.SpecPriority(10), func() {
		testCaseCloudProfilerProfileCreation("", true)
	})
}

func checkIfProfileExistForServiceAndVersion(ctx context.Context, service *cloudprofiler.Service, serviceName, version string) (bool, error) {
	framework.Logf("Checking if profile exists for service %q and version %q", serviceName, version)
	projectID := os.Getenv(utils.ProjectEnvVar)
	parent := "projects/" + projectID

	var pageToken string
	var pageIndex int

	for {
		pageIndex++
		framework.Logf("Scanning page %d of profiles for service %q and version %q", pageIndex, serviceName, version)

		backoff := wait.Backoff{
			Duration: 5 * time.Second,
			Factor:   2.0,
			Cap:      30 * time.Second,
			Steps:    5,
			Jitter:   0.1,
		}
		var page *cloudprofiler.ListProfilesResponse
		err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
			call := service.Projects.Profiles.List(parent).Context(ctx)
			if pageToken != "" {
				call = call.PageToken(pageToken)
			}
			var apiErr error
			page, apiErr = call.Do()
			if apiErr != nil {
				framework.Logf("Failed to list profiles: %v", apiErr)
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			return false, fmt.Errorf("failed to list profiles on page %d: %w", pageIndex, err)
		}

		if page == nil {
			return false, fmt.Errorf("received nil page response on page %d", pageIndex)
		}

		for _, profile := range page.Profiles {
			if profile.Deployment != nil {
				target := profile.Deployment.Target
				var ver string
				if profile.Deployment.Labels != nil {
					ver = profile.Deployment.Labels["version"]
				}

				// Early stopping logic based on lexicographical sorting.
				// Cloud Profiler List API returns profiles sorted by service name first, then by version.
				if target > serviceName {
					framework.Logf("Early stopping at page %d: current service %q > expected %q", pageIndex, target, serviceName)
					return false, fmt.Errorf("profile not found (early stop on service)")
				}

				if target == serviceName {
					if ver > version {
						framework.Logf("Early stopping at page %d: current version %q > expected %q", pageIndex, ver, version)
						return false, fmt.Errorf("profile not found (early stop on version)")
					}
					if ver == version {
						framework.Logf("Found profile for service %q and version %q on page %d", serviceName, version, pageIndex)
						return true, nil
					}
				}
			}
		}

		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}

	return false, fmt.Errorf("profile not found for service %q and version %q after scanning %d pages", serviceName, version, pageIndex)
}
