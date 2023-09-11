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

	"github.com/google/uuid"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
	"k8s.io/utils/ptr"
)

type gcsFuseCSISubPathTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSISubPathTestSuite returns gcsFuseCSISubPathTestSuite that implements TestSuite interface.
func InitGcsFuseCSISubPathTestSuite() storageframework.TestSuite {
	return &gcsFuseCSISubPathTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "subPath",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSISubPathTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSISubPathTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSISubPathTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("subpath", storageframework.GetDriverTimeouts(driver))
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

	ginkgo.It("should support non-existent paths", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the first pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolumeWithSubPath(l.volumeResource, "test-gcsfuse-volume", mountPath+"1", false, "subpath1", false /* add the first volume */)
		tPod1.SetupVolumeWithSubPath(nil, "test-gcsfuse-volume", mountPath+"2", false, "subpath2", true /* reuse the first volume */)

		ginkgo.By("Deploying the first pod")
		tPod1.Create(ctx)

		ginkgo.By("Checking that the first pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Checking that the first pod command exits with no error")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath+"1"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath+"1", mountPath+"1"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath+"2"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath+"2", mountPath+"2"))

		ginkgo.By("Deleting the first pod")
		tPod1.Cleanup(ctx)

		ginkgo.By("Configuring the second pod")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Deploying the second pod")
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		ginkgo.By("Checking that the second pod is running")
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Checking that the second pod command exits with no error")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/subpath1/data", mountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/subpath2/data", mountPath))
	})

	ginkgo.It("should support existing paths", func() {
		init()
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create sub-paths using gsutil
		specs.CreateImplicitDirInBucket("subpath1", bucketName)
		specs.CreateImplicitDirInBucket("subpath2", bucketName)

		ginkgo.By("Configuring the pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolumeWithSubPath(l.volumeResource, "test-gcsfuse-volume", mountPath+"1", false, "subpath1", false /* add the first volume */)
		tPod1.SetupVolumeWithSubPath(nil, "test-gcsfuse-volume", mountPath+"2", false, "subpath2", true /* reuse the first volume */)

		ginkgo.By("Deploying the pod")
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath+"1"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath+"1", mountPath+"1"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath+"2"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath+"2", mountPath+"2"))
	})

	ginkgo.It("should support files as paths", func(ctx context.Context) {
		init()
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create files using gsutil
		file1 := uuid.NewString()
		file2 := uuid.NewString()
		specs.CreateEmptyFileInBucket(file1, bucketName)
		specs.CreateEmptyFileInBucket(file2, bucketName)

		ginkgo.By("Configuring the pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolumeWithSubPath(l.volumeResource, "test-gcsfuse-volume", mountPath+"1", false, file1, false /* add the first volume */)
		tPod1.SetupVolumeWithSubPath(nil, "test-gcsfuse-volume", mountPath+"2", false, file2, true /* reuse the first volume */)

		ginkgo.By("Deploying the pod")
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath+"1"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v && grep 'hello world' %v", mountPath+"1", mountPath+"1"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath+"2"))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v && grep 'hello world' %v", mountPath+"2", mountPath+"2"))
	})

	ginkgo.It("[read-only] should fail when write", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the writer pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetName("gcsfuse-volume-tester-writer")
		tPod.SetupVolumeWithSubPath(l.volumeResource, "test-gcsfuse-volume", mountPath, false, "subpath", false /* add the first volume */)

		ginkgo.By("Deploying the writer pod")
		tPod.Create(ctx)

		ginkgo.By("Checking that the writer pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Writing a file to the volume")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))

		ginkgo.By("Deleting the writer pod")
		tPod.Cleanup(ctx)

		ginkgo.By("Configuring the reader pod")
		tPod = specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetName("gcsfuse-volume-tester-reader")
		// Make the CSI ephemeral inline volume read-only.
		if pattern.VolType == storageframework.CSIInlineVolume && l.volumeResource.VolSource != nil {
			l.volumeResource.VolSource.CSI.ReadOnly = ptr.To(true)
		}
		tPod.SetupVolumeWithSubPath(l.volumeResource, "test-gcsfuse-volume", mountPath+"1", true, "subpath", false /* add the first volume */)
		tPod.SetupVolumeWithSubPath(nil, "test-gcsfuse-volume", mountPath+"2", true, "subpath/data", true /* reuse the first volume */)

		ginkgo.By("Deploying the reader pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the reader pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the reader pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath+"1"))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/data", mountPath+"1"))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath+"2"))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v", mountPath+"2"))

		ginkgo.By("Expecting error when write to read-only volumes")
		tPod.VerifyExecInPodFail(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data", mountPath+"1"), 1)
		tPod.VerifyExecInPodFail(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v", mountPath+"2"), 1)
	})
}
