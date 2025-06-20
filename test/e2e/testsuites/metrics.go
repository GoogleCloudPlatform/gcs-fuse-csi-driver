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

package testsuites

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"local/test/e2e/specs"

	"github.com/google/uuid"
	csidriver "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_driver"
	metricspkg "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

var expectedMetricNames = []string{
	"fs_ops_count",
	"fs_ops_error_count",
	"fs_ops_latency",
	"gcs_download_bytes_count",
	"gcs_read_count",
	"gcs_read_bytes_count",
	"gcs_reader_count",
	"gcs_request_latencies",
	"file_cache_read_count",
	"file_cache_read_bytes_count",
	"file_cache_read_latencies",
}

type gcsFuseCSIMetricsTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIMetricsTestSuite returns gcsFuseCSIMetricsTestSuite that implements TestSuite interface.
func InitGcsFuseCSIMetricsTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIMetricsTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "metrics",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIMetricsTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIMetricsTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

//nolint:maintidx
func (t *gcsFuseCSIMetricsTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config             *storageframework.PerTestConfig
		volumeResourceList []*storageframework.VolumeResource
		artifactsDir       string
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("metrics", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(volumeNumber int, configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}

		l.volumeResourceList = []*storageframework.VolumeResource{}
		for range volumeNumber {
			l.volumeResourceList = append(l.volumeResourceList, storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{}))
		}

		l.artifactsDir = "../../_artifacts"
		if dir, ok := os.LookupEnv("ARTIFACTS"); ok {
			l.artifactsDir = dir
		}
	}

	cleanup := func() {
		var cleanUpErrs []error
		for _, vr := range l.volumeResourceList {
			cleanUpErrs = append(cleanUpErrs, vr.CleanupResource(ctx))
		}
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	verifyMetrics := func(tPod *specs.TestPod, volumeResource *storageframework.VolumeResource, mountPath, volumeName string) {
		ginkgo.By("Running file operations on the volume")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))

		// Create a new file A outside of the gcsfuse, using gsutil.
		var bucketName string
		if volumeResource.Pv != nil {
			bucketName = volumeResource.Pv.Spec.CSI.VolumeHandle
		} else {
			bucketName = volumeResource.VolSource.CSI.VolumeAttributes[csidriver.VolumeContextKeyBucketName]
		}

		fileName := uuid.NewString()
		specs.CreateTestFileInBucket(fileName, bucketName)

		// Read file A.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v", mountPath, fileName))

		// Read file A again.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v", mountPath, fileName))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v", mountPath, fileName))

		// Create a new file B using gcsfuse.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("touch %v/testfile", mountPath))

		// List the volume.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("ls %v", mountPath))

		// Write content to file B.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world!' > %v/testfile", mountPath))

		// Read file B.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/testfile", mountPath))

		// Remove file B.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("rm %v/testfile", mountPath))

		// Delete directory A with files in it.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mkdir %v/dir-to-delete/", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("touch %v/dir-to-delete/my-file", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("rm -r %v/dir-to-delete/", mountPath))

		// Copy a file A from dir1 to dir2.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mkdir %v/my-dir/", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("touch %v/my-dir/my-file", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mkdir %v/my-other-dir/", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mv %v/my-dir/my-file %v/my-other-dir/", mountPath, mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("rmdir %v/my-dir/", mountPath))

		// Mkdir dir A.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mkdir %v/my-new-dir/", mountPath))

		// Rename a file A to file B.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("touch %v/my-file", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mv %v/my-file %v/my-renamed-file", mountPath, mountPath))

		// Rename a directory A to directory B (set –rename-dir-limit flag to avoid i/o errors)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mkdir %v/my-dir-to-rename/", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mv %v/my-dir-to-rename/ %v/my-renamed-dir/", mountPath, mountPath))

		// Create symlink.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mkdir %v/my-dir/", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("touch %v/my-dir/my-file", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("ln -s %v/my-dir/my-file %v/my-symlink", mountPath, mountPath))

		// Read symlink.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("ls -l %v/my-symlink", mountPath))

		ginkgo.By("Collecting Prometheus metrics from the CSI driver node server")
		csiPodIP := tPod.GetCSIDriverNodePodIP(ctx)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("wget -O %v/metrics.prom http://%v:9920/metrics", mountPath, csiPodIP))
		promFile := fmt.Sprintf("%v/%v/metrics.prom", l.artifactsDir, f.Namespace.Name)

		//nolint:gosec
		if output, err := exec.Command("gsutil", "cp", fmt.Sprintf("gs://%v/metrics.prom", bucketName), promFile).CombinedOutput(); err != nil {
			framework.Failf("Failed to download the Prometheus metrics data from GCS bucket %q: %v, output: %s", bucketName, err, output)
		}

		ginkgo.By("Parsing Prometheus metrics")
		metricsFile, err := os.Open(promFile)
		if err != nil {
			framework.Failf("Failed to open the metrics file %q: %v", promFile, err)
		}
		defer metricsFile.Close()

		families, err := metricspkg.ProcessMetricsData(metricsFile)
		framework.ExpectNoError(err)

		volume := volumeName
		if volumeResource.Pv != nil {
			volume = volumeResource.Pv.Name
		}
		podName := tPod.GetPodName()

		for _, metricName := range expectedMetricNames {
			metricsList := []*dto.Metric{}
			metricFamily, ok := families[metricName]
			if ok {
			metricLoop:
				for _, m := range metricFamily.GetMetric() {
					for _, pair := range m.GetLabel() {
						name, value := pair.GetName(), pair.GetValue()
						switch name {
						case "bucket_name":
							if value != bucketName {
								continue metricLoop
							}
						case "pod_name":
							if value != podName {
								continue metricLoop
							}
						case "volume_name":
							if value != volume {
								continue metricLoop
							}
						case "namespace_name":
							if value != f.Namespace.Name {
								continue metricLoop
							}
						}
					}
					metricsList = append(metricsList, m)
				}
			}
			ginkgo.By(fmt.Sprintf("Printing full metricList %+v", metricsList))

			gomega.Expect(len(metricsList)).To(gomega.BeNumerically(">", 0), fmt.Sprintf("Found metric %q count: %v, expected count > 0", metricName, len(metricsList)))
			ginkgo.By(fmt.Sprintf("Found metric %q count: %v", metricName, len(metricsList)))
		}
	}

	// This tests below configuration:
	//    [pod1]
	//       |
	//   [volume1]
	//       |
	//   [bucket1]
	ginkgo.It("should emit metrics", func() {
		init(1, specs.EnableFileCacheAndMetricsPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResourceList[0], volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		verifyMetrics(tPod, l.volumeResourceList[0], mountPath, volumeName)
	})

	// This tests below configuration:
	//          [pod1]
	//          /    \
	//   [volume1]  [volume2]
	//       |          |
	//   [bucket1]  [bucket2]
	ginkgo.It("should emit metrics from multiple volumes", func() {
		init(2, specs.EnableFileCacheForceNewBucketAndMetricsPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		for i, vr := range l.volumeResourceList {
			tPod.SetupVolume(vr, fmt.Sprintf("%v-%v", volumeName, i), fmt.Sprintf("%v/%v", mountPath, i), false)
		}

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		for i, vr := range l.volumeResourceList {
			ginkgo.By(fmt.Sprintf("Checking metrics from volume %v", i))
			verifyMetrics(tPod, vr, fmt.Sprintf("%v/%v", mountPath, i), fmt.Sprintf("%v-%v", volumeName, i))
		}
	})
}
