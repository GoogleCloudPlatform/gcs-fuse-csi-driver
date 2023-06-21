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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	gcsfuseIntegrationTestsBasePath = "gcsfuse/tools/integration_tests"
	exportGoPath                    = "export PATH=$PATH:/usr/local/go/bin"
	commonTestCommand               = "GODEBUG=asyncpreemptoff=1 go test . -p 1 --integrationTest -v --mountedDirectory="
)

type gcsFuseCSIGCSFuseIntegrationTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIGCSFuseIntegrationTestSuite returns gcsFuseCSIGCSFuseIntegrationTestSuite that implements TestSuite interface.
func InitGcsFuseCSIGCSFuseIntegrationTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIGCSFuseIntegrationTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "gcsfuseIntegration",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIGCSFuseIntegrationTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIGCSFuseIntegrationTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIGCSFuseIntegrationTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("gcsfuse-integration", storageframework.GetDriverTimeouts(driver))
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

	gcsfuseIntegrationTest := func(testName string, readOnly bool, mountOptions ...string) {
		ginkgo.By("Configuring the test pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetImage(specs.GoogleCloudCliImage)
		tPod.SetResource("1", "1Gi")
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, readOnly, mountOptions...)
		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/volumes":                 "true",
			"gke-gcsfuse/cpu-limit":               "250m",
			"gke-gcsfuse/memory-limit":            "256Mi",
			"gke-gcsfuse/ephemeral-storage-limit": "1Gi",
		})

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]

		ginkgo.By("Deploying the test pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the test pod is running")
		tPod.WaitForRunning()

		ginkgo.By("Checking that the test pod command exits with no error")
		if readOnly {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath))
		} else {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		}

		ginkgo.By("Checking that the gcsfuse integration tests exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "apt-get update && apt-get install wget git -y")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "wget https://go.dev/dl/go1.20.5.linux-$(dpkg --print-architecture).tar.gz -q && tar -C /usr/local -xzf go1.20.5.linux-$(dpkg --print-architecture).tar.gz")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "git clone https://github.com/GoogleCloudPlatform/gcsfuse.git")

		switch testName {
		case "readonly":
			if readOnly {
				tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, fmt.Sprintf("%v && cd %v/readonly && %v'%v' --testbucket='%v'", exportGoPath, gcsfuseIntegrationTestsBasePath, commonTestCommand, mountPath, bucketName))
			} else {
				tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, fmt.Sprintf("chmod 777 %v/readonly && useradd -u 6666 -m test-user && su test-user -c '%v && cd %v/readonly && %v%v --testbucket=%v'", gcsfuseIntegrationTestsBasePath, exportGoPath, gcsfuseIntegrationTestsBasePath, commonTestCommand, mountPath, bucketName))
			}
		case "explicit_dir":
			tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, fmt.Sprintf("%v && cd %v/%v && %v'%v' --testbucket='%v'", exportGoPath, gcsfuseIntegrationTestsBasePath, testName, commonTestCommand, mountPath, bucketName))
		case "implicit_dir":
			tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, fmt.Sprintf("%v && cd %v/%v && %v'%v' --testbucket='%v'", exportGoPath, gcsfuseIntegrationTestsBasePath, testName, commonTestCommand, mountPath, bucketName))
		default:
			tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, fmt.Sprintf("%v && cd %v/%v && %v'%v'", exportGoPath, gcsfuseIntegrationTestsBasePath, testName, commonTestCommand, mountPath))
		}
	}

	ginkgo.It("should succeed in operations test 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("operations", false)
	})

	ginkgo.It("should succeed in operations test 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("operations", false, "enable-storage-client-library")
	})

	ginkgo.It("should succeed in operations test 3", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("operations", false, "implicit-dirs")
	})

	ginkgo.It("should succeed in operations test 4", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("operations", false, "enable-storage-client-library", "implicit-dirs")
	})

	ginkgo.It("should succeed in operations test 5", func() {
		// passing implicit-dirs and only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest("operations", false)
	})

	ginkgo.It("should succeed in operations test 6", func() {
		// passing implicit-dirs and only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest("operations", false, "enable-storage-client-library")
	})

	ginkgo.It("should succeed in rename_dir_limit test 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("rename_dir_limit", false, "rename-dir-limit=3")
	})

	ginkgo.It("should succeed in rename_dir_limit test 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("rename_dir_limit", false, "rename-dir-limit=3", "implicit-dirs")
	})

	ginkgo.It("should succeed in rename_dir_limit test 3", func() {
		// passing implicit-dirs and only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest("rename_dir_limit", false, "rename-dir-limit=3")
	})

	ginkgo.It("should succeed in readonly test 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("readonly", true, "implicit-dirs")
	})

	ginkgo.It("should succeed in readonly test 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("readonly", false, "file-mode=544", "dir-mode=544", "uid=6666", "gid=6666", "implicit-dirs")
	})

	// TODO(tulsishah): troubleshoot the failing tests.
	// ginkgo.It("should succeed in readonly test 3", func() {
	// 	// passing implicit-dirs and only-dir flags
	// 	init(specs.SubfolderInBucketPrefix)
	// 	defer cleanup()

	// 	gcsfuseIntegrationTest("readonly", true)
	// })

	// ginkgo.It("should succeed in readonly test 4", func() {
	// 	// passing implicit-dirs and only-dir flags
	// 	init(specs.SubfolderInBucketPrefix)
	// 	defer cleanup()

	// 	gcsfuseIntegrationTest("readonly", false, "file-mode=544", "dir-mode=544", "uid=6666", "gid=6666")
	// })

	ginkgo.It("should succeed in explicit_dir test 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("explicit_dir", false)
	})

	ginkgo.It("should succeed in explicit_dir test 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("explicit_dir", false, "enable-storage-client-library")
	})

	ginkgo.It("should succeed in explicit_dir test 3", func() {
		// passing implicit-dirs and only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest("explicit_dir", false)
	})

	ginkgo.It("should succeed in explicit_dir test 4", func() {
		// passing implicit-dirs and only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest("explicit_dir", false, "enable-storage-client-library")
	})

	ginkgo.It("should succeed in implicit_dir test 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("implicit_dir", false, "implicit-dirs")
	})

	ginkgo.It("should succeed in implicit_dir test 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest("implicit_dir", false, "implicit-dirs", "enable-storage-client-library")
	})

	// TODO(tulsishah): troubleshoot the failing tests.
	// ginkgo.It("should succeed in implicit_dir test 3", func() {
	// 	// passing implicit-dirs and only-dir flags
	// 	init(specs.SubfolderInBucketPrefix)
	// 	defer cleanup()

	// 	gcsfuseIntegrationTest("implicit_dir", false)
	// })

	// ginkgo.It("should succeed in implicit_dir test 4", func() {
	// 	// passing implicit-dirs and only-dir flags
	// 	init(specs.SubfolderInBucketPrefix)
	// 	defer cleanup()

	// 	gcsfuseIntegrationTest("implicit_dir", false, "enable-storage-client-library")
	// })
}
