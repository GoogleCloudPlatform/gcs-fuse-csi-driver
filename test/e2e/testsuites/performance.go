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
	"fmt"
	"os/exec"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIPerformanceTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIPerformanceTestSuite returns gcsFuseCSIPerformanceTestSuite that implements TestSuite interface.
func InitGcsFuseCSIPerformanceTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIPerformanceTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "performance",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIPerformanceTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIPerformanceTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIPerformanceTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("performance", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource())
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	ginkgo.It("should succeed in performance test", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the test pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetImage(specs.UbuntuImage)
		tPod.SetResource("2", "5Gi")
		mountPath := "/gcs"
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false, "implicit-dirs", "max-conns-per-host=100", "client-protocol=http1")
		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/volumes":                 "true",
			"gke-gcsfuse/cpu-limit":               "10",
			"gke-gcsfuse/memory-limit":            "2Gi",
			"gke-gcsfuse/ephemeral-storage-limit": "5Gi",
		})
		tPod.SetNodeSelector(map[string]string{
			"kubernetes.io/os":                 "linux",
			"node.kubernetes.io/instance-type": "n2-standard-32",
		})

		ginkgo.By("Deploying the test pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the test pod is running")
		tPod.WaitForRunning()

		ginkgo.By("Checking that the test pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))

		ginkgo.By("Checking that the performance test exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "apt-get update && apt-get install curl fio -y")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "curl -o /seq_rand_read_write.fio https://raw.githubusercontent.com/GoogleCloudPlatform/gcsfuse/master/perfmetrics/scripts/job_files/seq_rand_read_write.fio")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "mkdir -p /gcs/256kb /gcs/3mb /gcs/5mb /gcs/50mb /gcs/fio-logs")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "fio /seq_rand_read_write.fio --lat_percentiles 1 --output-format=json --output='/output.json'")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "cp /output.json /gcs/fio-logs/output.json")

		ginkgo.By("Checking that the metrics are downloaded with no error")
		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		//nolint:gosec
		if output, err := exec.Command("gsutil", "cp", fmt.Sprintf("gs://%v/fio-logs/output.json", bucketName), "../../_artifacts/output.json").CombinedOutput(); err != nil {
			framework.Failf("Failed to download the FIO metrics from GCS bucket %q: %v, output: %s", bucketName, err, output)
		}
	})
}
