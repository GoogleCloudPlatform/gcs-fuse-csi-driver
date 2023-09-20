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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type metrics struct {
	IOPS float32 `json:"iops"`
	//nolint:tagliatelle
	BwBytes float32 `json:"bw_bytes"`
}

type fioJobOptions struct {
	FileSize  string `json:"filesize"`
	ReadWrite string `json:"rw"`
}

type fioJob struct {
	//nolint:tagliatelle
	JobOptions  fioJobOptions `json:"job options"`
	ReadMetric  metrics       `json:"read"`
	WriteMetric metrics       `json:"write"`
}

type fioResult struct {
	Jobs []fioJob `json:"jobs"`
}

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

//nolint:maintidx
func (t *gcsFuseCSIPerformanceTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
		artifactsDir   string
		fioOutput      map[string]metrics
		thresholds     map[string]metrics
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("performance", storageframework.GetDriverTimeouts(driver))
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

	parseFioOutput := func(outputPath string) {
		jsonFile, err := os.Open(outputPath)
		if err != nil {
			framework.Failf("Failed to open the fio output file %q: %v", outputPath, err)
		}

		byteValue, err := io.ReadAll(jsonFile)
		if err != nil {
			framework.Failf("Failed to read the fio output file %q: %v", outputPath, err)
		}

		var fr fioResult
		if err := json.Unmarshal(byteValue, &fr); err != nil {
			framework.Failf("Failed to parse the fio output file %q: %v", outputPath, err)
		}

		l.fioOutput = map[string]metrics{}

		for _, job := range fr.Jobs {
			if job.JobOptions.ReadWrite == "" {
				job.JobOptions.ReadWrite = "read"
			}

			metricKey := job.JobOptions.ReadWrite + "_" + job.JobOptions.FileSize
			if job.JobOptions.ReadWrite == "read" || job.JobOptions.ReadWrite == "randread" {
				l.fioOutput[metricKey] = job.ReadMetric
			} else {
				l.fioOutput[metricKey] = job.WriteMetric
			}

			ginkgo.By(fmt.Sprintf("[%v %v] IOPS: %v, bandwidth bytes: %v", job.JobOptions.ReadWrite, job.JobOptions.FileSize, l.fioOutput[metricKey].IOPS, l.fioOutput[metricKey].BwBytes))
		}
	}

	parseThresholds := func(thresholdFile string) {
		jsonFile, err := os.Open(thresholdFile)
		if err != nil {
			framework.Failf("Failed to open the threshold file %q: %v", thresholdFile, err)
		}

		byteValue, err := io.ReadAll(jsonFile)
		if err != nil {
			framework.Failf("Failed to read the threshold file %q: %v", thresholdFile, err)
		}

		if err := json.Unmarshal(byteValue, &l.thresholds); err != nil {
			framework.Failf("Failed to parse the threshold file %q: %v", thresholdFile, err)
		}
	}

	checkFioResult := func(rw string, fileSize string) {
		if l.fioOutput == nil || l.thresholds == nil {
			ginkgo.Skip("Skip the check because the fio test output and threshold parsing failed.")
		}

		metricKey := rw + "_" + fileSize

		if l.fioOutput[metricKey].IOPS < l.thresholds[metricKey].IOPS {
			framework.Failf("[%v %v] The IOPS %v is lower than the threshold %v", rw, fileSize, l.fioOutput[metricKey].IOPS, l.thresholds[metricKey].IOPS)
		}

		ginkgo.By(fmt.Sprintf("[%v %v] The IOPS %v is higher than the threshold %v", rw, fileSize, l.fioOutput[metricKey].IOPS, l.thresholds[metricKey].IOPS))

		if l.fioOutput[metricKey].BwBytes < l.thresholds[metricKey].BwBytes {
			framework.Failf("[%v %v] The bandwidth bytes %v is lower than the threshold %v", rw, fileSize, l.fioOutput[metricKey].BwBytes, l.thresholds[metricKey].BwBytes)
		}

		ginkgo.By(fmt.Sprintf("[%v %v] The bandwidth bytes %v is higher than the threshold %v", rw, fileSize, l.fioOutput[metricKey].BwBytes, l.thresholds[metricKey].BwBytes))
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	ginkgo.Context("fio benchmarking", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.It("should succeed in performance test - fio job", func() {
			init()
			defer cleanup()

			ginkgo.By("Configuring the test pod")
			tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
			tPod.SetImage(specs.UbuntuImage)
			tPod.SetResource("2", "5Gi", "5Gi")
			mountPath := "/gcs"
			tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false, "implicit-dirs", "max-conns-per-host=100", "client-protocol=http1")
			tPod.SetAnnotations(map[string]string{
				"gke-gcsfuse/cpu-limit":               "10",
				"gke-gcsfuse/memory-limit":            "2Gi",
				"gke-gcsfuse/ephemeral-storage-limit": "5Gi",
			})
			tPod.SetNodeSelector(map[string]string{
				"kubernetes.io/os":                 "linux",
				"node.kubernetes.io/instance-type": "n2-standard-32",
			})

			ginkgo.By("Deploying the test pod")
			tPod.Create(ctx)
			defer tPod.Cleanup(ctx)

			ginkgo.By("Checking that the test pod is running")
			tPod.WaitForRunning(ctx)

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
			if output, err := exec.Command("gsutil", "cp", fmt.Sprintf("gs://%v/fio-logs/output.json", bucketName), l.artifactsDir+"/output.json").CombinedOutput(); err != nil {
				framework.Failf("Failed to download the FIO metrics from GCS bucket %q: %v, output: %s", bucketName, err, output)
			}
		})

		ginkgo.It("should succeed in performance test - parse the fio test output and threshold", func() {
			parseFioOutput(l.artifactsDir + "/output.json")
			parseThresholds("./testsuites/perf_threshold.json")
		})

		ginkgo.It("should succeed in performance test - sequential read 256k", func() {
			checkFioResult("read", "256k")
		})

		ginkgo.It("should succeed in performance test - sequential write 256k", func() {
			checkFioResult("write", "256k")
		})

		ginkgo.It("should succeed in performance test - sequential read 3M", func() {
			checkFioResult("read", "3M")
		})

		ginkgo.It("should succeed in performance test - sequential write 3M", func() {
			checkFioResult("write", "3M")
		})

		ginkgo.It("should succeed in performance test - sequential read 5M", func() {
			checkFioResult("read", "5M")
		})

		ginkgo.It("should succeed in performance test - sequential write 5M", func() {
			checkFioResult("write", "5M")
		})

		ginkgo.It("should succeed in performance test - sequential read 50M", func() {
			checkFioResult("read", "50M")
		})

		ginkgo.It("should succeed in performance test - sequential write 50M", func() {
			checkFioResult("write", "50M")
		})

		ginkgo.It("should succeed in performance test - random read 256k", func() {
			checkFioResult("randread", "256k")
		})

		ginkgo.It("should succeed in performance test - random write 256k", func() {
			checkFioResult("randwrite", "256k")
		})

		ginkgo.It("should succeed in performance test - random read 3M", func() {
			checkFioResult("randread", "3M")
		})

		ginkgo.It("should succeed in performance test - random write 3M", func() {
			checkFioResult("randwrite", "3M")
		})

		ginkgo.It("should succeed in performance test - random read 5M", func() {
			checkFioResult("randread", "5M")
		})

		ginkgo.It("should succeed in performance test - random write 5M", func() {
			checkFioResult("randwrite", "5M")
		})

		ginkgo.It("should succeed in performance test - random read 50M", func() {
			checkFioResult("randread", "50M")
		})

		ginkgo.It("should succeed in performance test - random write 50M", func() {
			checkFioResult("randwrite", "50M")
		})
	})
}
