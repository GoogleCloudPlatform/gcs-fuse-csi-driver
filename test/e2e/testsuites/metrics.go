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
	"time"

	"github.com/google/uuid"
	metricspkg "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

var expectedMetricNames = map[string]int{
	"fs_ops_count":                20,
	"fs_ops_error_count":          2,
	"fs_ops_latency":              20,
	"gcs_download_bytes_count":    1,
	"gcs_read_count":              1,
	"gcs_read_bytes_count":        1,
	"gcs_reader_count":            2,
	"gcs_request_count":           7,
	"gcs_request_latencies":       7,
	"file_cache_read_count":       1,
	"file_cache_read_bytes_count": 1,
	"file_cache_read_latencies":   1,
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
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
		artifactsDir   string
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("metrics", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})

		l.artifactsDir = "../../_artifacts"
		if dir, ok := os.LookupEnv("ARTIFACTS"); ok {
			l.artifactsDir = dir
		}
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	ginkgo.It("should emit metrics", func() {
		init(specs.EnableFileCachePrefix)
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create a new file A outside of the gcsfuse, using gsutil.
		fileName := uuid.NewString()
		specs.CreateTestFileInBucket(fileName, bucketName)

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))

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

		ginkgo.By("Sleeping 20 seconds for metrics to be collected")
		time.Sleep(20 * time.Second)

		ginkgo.By("Collecting Prometheus metrics from the CSI driver node server")
		csiPodIP := tPod.GetCSIDriverNodePodIP(ctx)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("wget -O %v/metrics.prom http://%v:9920/metrics", mountPath, csiPodIP))
		promFile := fmt.Sprintf("%v/%v/metrics.prom", l.artifactsDir, f.Namespace.Name)

		//nolint:gosec
		if output, err := exec.Command("gsutil", "cp", fmt.Sprintf("gs://%v/metrics.prom", bucketName), promFile).CombinedOutput(); err != nil {
			framework.Failf("Failed to download the Prometheus metrics data from GCS bucket %q: %v, output: %s", bucketName, err, output)
		}

		ginkgo.By("Parsing Prometheus metrics")
		families, err := metricspkg.ProcessMetricsFile(promFile)
		framework.ExpectNoError(err)

		volume := volumeName
		if l.volumeResource.Pv != nil {
			volume = l.volumeResource.Pv.Name
		}
		podName := tPod.GetPodName()

		for metricName, metricCount := range expectedMetricNames {
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

			gomega.Expect(metricsList).To(gomega.HaveLen(metricCount), fmt.Sprintf("Found metric %q count: %v, expected count: %v", metricName, len(metricsList), metricCount))
			ginkgo.By(fmt.Sprintf("Found metric %q count: %v", metricName, len(metricsList)))
		}
	})
}
