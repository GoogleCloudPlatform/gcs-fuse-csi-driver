/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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
	"strconv"
	"strings"
	"time"

	"local/test/e2e/specs"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseKernelReaderTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

func InitGcsFuseKernelReaderTestSuite() storageframework.TestSuite {
	return &gcsFuseKernelReaderTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "kernelReader",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseKernelReaderTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseKernelReaderTestSuite) SkipUnsupportedTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
}

func (t *gcsFuseKernelReaderTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()
	f := framework.NewFrameworkWithCustomTimeouts("kernel-reader", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(mountOptions []string, configPrefix ...string) {
		if os.Getenv(specs.IsOSSEnvVar) != "true" {
			ginkgo.Skip("Skipping test: Test is not yet supported on managed")
		}

		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok {
			if !driver.EnableZB {
				ginkgo.Skip("Skipping test: Zonal Buckets are not enabled")
			}
		}

		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})

		if l.volumeResource.Pv != nil && len(mountOptions) > 0 {
			pv, err := f.ClientSet.CoreV1().PersistentVolumes().Get(ctx, l.volumeResource.Pv.Name, metav1.GetOptions{})
			framework.ExpectNoError(err)
			pv.Spec.MountOptions = append(pv.Spec.MountOptions, mountOptions...)
			_, err = f.ClientSet.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
			framework.ExpectNoError(err)
		}
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	verifyKernelParameter := func(tPod *specs.TestPod, paramPath, expectedValue string) {
		gomega.Eventually(func(g gomega.Gomega) {
			// Check if file exists before reading to debug "No such file" errors
			exists := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf("if [ -e %s ]; then echo true; else echo false; fi", paramPath))
			g.Expect(strings.TrimSpace(exists)).To(gomega.Equal("true"), fmt.Sprintf("Kernel parameter file %s not found. FUSE connection might be dead.", paramPath))
			output := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, "cat "+paramPath)
			g.Expect(strings.TrimSpace(output)).To(gomega.Equal(expectedValue))
		}, 1*time.Minute, 3*time.Second).Should(gomega.Succeed())
	}

	ginkgo.It("should enable kernel reader and tune parameters by default for Zonal Buckets", func() {
		init(nil, specs.KernelReaderPrefix)
		defer cleanup()

		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		fuseControlVol := &storageframework.VolumeResource{
			VolSource: &corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/sys/fs/fuse/connections",
				},
			},
		}
		tPod.SetupVolume(fuseControlVol, "fuse-control", "/sys/fs/fuse/connections", false)
		tPod.Create(ctx)
		tPod.WaitForRunning(ctx)
		defer tPod.Cleanup(ctx)

		// Get device minor number
		bdi := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf(`mountpoint -d "%s"`, mountPath))
		parts := strings.Split(strings.TrimSpace(bdi), ":")
		gomega.Expect(len(parts)).To(gomega.Equal(2), "mountpoint -d output should be major:minor")
		minor := parts[1]

		// Verify read_ahead_kb is optimized (16384 KB = 16MB)
		readAheadPath := fmt.Sprintf("/sys/class/bdi/%s/read_ahead_kb", strings.TrimSpace(bdi))
		verifyKernelParameter(tPod, readAheadPath, "16384")

		// Get CPU count
		nprocStr := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, "nproc")
		nproc, err := strconv.Atoi(strings.TrimSpace(nprocStr))
		framework.ExpectNoError(err, "failed to parse nproc output")

		// Calculate expected values
		maxBackgroundLimit := 1024
		expectedMaxBackground := 2 * nproc
		if expectedMaxBackground < 12 {
			expectedMaxBackground = 12
		}
		if expectedMaxBackground > maxBackgroundLimit {
			expectedMaxBackground = maxBackgroundLimit
		}
		expectedCongestionThreshold := (3 * expectedMaxBackground) / 4

		// Verify max_background
		maxBackgroundPath := fmt.Sprintf("/sys/fs/fuse/connections/%s/max_background", minor)
		verifyKernelParameter(tPod, maxBackgroundPath, strconv.Itoa(expectedMaxBackground))

		// Verify congestion_threshold
		congestionThresholdPath := fmt.Sprintf("/sys/fs/fuse/connections/%s/congestion_threshold", minor)
		verifyKernelParameter(tPod, congestionThresholdPath, strconv.Itoa(expectedCongestionThreshold))
	})

	ginkgo.It("should disable optimizations when explicitly requested", func() {
		mountOptions := []string{"file-system:enable-kernel-reader:false"}
		init(mountOptions, specs.KernelReaderPrefix)
		defer cleanup()

		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		options := mountOptions
		if l.volumeResource.Pv != nil {
			options = nil
		}
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false, options...)
		fuseControlVol := &storageframework.VolumeResource{
			VolSource: &corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/sys/fs/fuse/connections",
				},
			},
		}
		tPod.SetupVolume(fuseControlVol, "fuse-control", "/sys/fs/fuse/connections", false)
		tPod.Create(ctx)
		tPod.WaitForRunning(ctx)
		defer tPod.Cleanup(ctx)

		// Verify read_ahead_kb is default (usually 128 or similar, definitely not 16384)
		bdi := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf(`mountpoint -d "%s"`, mountPath))
		readAheadPath := fmt.Sprintf("/sys/class/bdi/%s/read_ahead_kb", strings.TrimSpace(bdi))
		gomega.Eventually(func(g gomega.Gomega) {
			output := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, "cat "+readAheadPath)
			g.Expect(strings.TrimSpace(output)).NotTo(gomega.Equal("16384"))
		}, 1*time.Minute, 1*time.Second).Should(gomega.Succeed())

		// Get device minor number
		parts := strings.Split(strings.TrimSpace(bdi), ":")
		gomega.Expect(len(parts)).To(gomega.Equal(2), "mountpoint -d output should be major:minor")
		minor := parts[1]

		// Verify max_background and congestion_threshold are not the optimized values,
		// but the FUSE defaults.
		verifyKernelParameter(tPod, fmt.Sprintf("/sys/fs/fuse/connections/%s/max_background", minor), "12")
		verifyKernelParameter(tPod, fmt.Sprintf("/sys/fs/fuse/connections/%s/congestion_threshold", minor), "9")
	})

	ginkgo.It("should respect user overwrites for kernel parameters", func() {
		mountOptions := []string{
			"max-read-ahead-kb=2048",
			"congestion-threshold=10",
			"max-background=15",
		}
		init(mountOptions, specs.KernelReaderPrefix)
		defer cleanup()

		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		// Explicitly set kernel parameters
		options := mountOptions
		if l.volumeResource.Pv != nil {
			options = nil
		}
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false, options...)
		fuseControlVol := &storageframework.VolumeResource{
			VolSource: &corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/sys/fs/fuse/connections",
				},
			},
		}
		tPod.SetupVolume(fuseControlVol, "fuse-control", "/sys/fs/fuse/connections", false)
		tPod.Create(ctx)
		tPod.WaitForRunning(ctx)
		defer tPod.Cleanup(ctx)

		// Get device minor number
		bdi := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf(`mountpoint -d "%s"`, mountPath))
		parts := strings.Split(strings.TrimSpace(bdi), ":")
		gomega.Expect(len(parts)).To(gomega.Equal(2), "mountpoint -d output should be major:minor")
		minor := parts[1]

		// Verify read_ahead_kb is 2048
		readAheadPath := fmt.Sprintf("/sys/class/bdi/%s/read_ahead_kb", strings.TrimSpace(bdi))
		verifyKernelParameter(tPod, readAheadPath, "2048")

		// Verify max_background
		verifyKernelParameter(tPod, fmt.Sprintf("/sys/fs/fuse/connections/%s/max_background", minor), "15")

		// Verify congestion_threshold
		verifyKernelParameter(tPod, fmt.Sprintf("/sys/fs/fuse/connections/%s/congestion_threshold", minor), "10")
	})
}
