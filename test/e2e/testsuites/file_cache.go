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
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
	"local/test/e2e/specs"
)

type gcsFuseCSIFileCacheTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIFileCacheTestSuite returns gcsFuseCSIFileCacheTestSuite that implements TestSuite interface.
func InitGcsFuseCSIFileCacheTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIFileCacheTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "fileCache",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIFileCacheTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIFileCacheTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIFileCacheTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("file-cache", storageframework.GetDriverTimeouts(driver))
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

	ginkgo.It("should cache the data", func() {
		init(specs.EnableFileCachePrefix)
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create files using gsutil
		fileName := uuid.NewString()
		specs.CreateTestFileInBucket(fileName, bucketName)

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		// Mount the gcsfuse cache volume to the test container
		tPod.SetupCacheVolumeMount("/cache")

		cacheSubfolder := volumeName
		if l.volumeResource.Pv != nil {
			cacheSubfolder = l.volumeResource.Pv.Name
		}

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v", mountPath, fileName))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep '%v' /cache/.volumes/%v/gcsfuse-file-cache/%v/%v", fileName, cacheSubfolder, bucketName, fileName))
	})

	ginkgo.It("should cache the data using custom cache volume", func() {
		init(specs.EnableFileCachePrefix)
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create files using gsutil
		fileName := uuid.NewString()
		specs.CreateTestFileInBucket(fileName, bucketName)

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPVC := specs.NewTestPVC(f.ClientSet, f.Namespace, "custom-cache", "standard-rwo", "5Gi", corev1.ReadWriteOnce)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: tPVC.PVC}, webhook.SidecarContainerCacheVolumeName, "", false)
		tPod.SetupCacheVolumeMount("/cache")
		tPod.SetNonRootSecurityContext(0, 0, 1000)

		cacheSubfolder := volumeName
		if l.volumeResource.Pv != nil {
			cacheSubfolder = l.volumeResource.Pv.Name
		}

		ginkgo.By("Creating the PVC")
		tPVC.Create(ctx)
		defer tPVC.Cleanup(ctx)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v", mountPath, fileName))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep '%v' /cache/.volumes/%v/gcsfuse-file-cache/%v/%v", fileName, cacheSubfolder, bucketName, fileName))
	})

	ginkgo.It("should cache the data using in-memory custom cache volume", func() {
		init(specs.EnableFileCachePrefix)
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create files using gsutil
		fileName := uuid.NewString()
		specs.CreateTestFileInBucket(fileName, bucketName)

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		inMemoryCache := &storageframework.VolumeResource{
			VolSource: &corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		}
		tPod.SetupVolume(inMemoryCache, webhook.SidecarContainerCacheVolumeName, "", false)
		tPod.SetupCacheVolumeMount("/cache")

		cacheSubfolder := volumeName
		if l.volumeResource.Pv != nil {
			cacheSubfolder = l.volumeResource.Pv.Name
		}

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v", mountPath, fileName))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep '%v' /cache/.volumes/%v/gcsfuse-file-cache/%v/%v", fileName, cacheSubfolder, bucketName, fileName))
	})

	ginkgo.It("should not cache the data when the file cache is disabled", func() {
		init()
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create files using gsutil
		fileName := uuid.NewString()
		specs.CreateTestFileInBucket(fileName, bucketName)

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		// Mount the gcsfuse cache volume to the test container
		tPod.SetupCacheVolumeMount("/cache")

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v", mountPath, fileName))
		// the cache volume should be empty
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "[ ! -d '/cache/.volumes' ] && exit 0 || exit 1")
	})

	ginkgo.It("should have cache miss when the data is larger than fileCacheCapacity", func() {
		init(specs.EnableFileCachePrefix)
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create files using gsutil
		fileName := uuid.NewString()
		// The file size 110 MB is larger than the 100 MB fileCacheCapacity
		specs.CreateTestFileWithSizeInBucket(fileName, bucketName, 110*1024*1024)

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
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v > /dev/null", mountPath, fileName))

		tPod.WaitForLog(ctx, webhook.GcsFuseSidecarName, "while inserting into the cache: size of the entry is more than the cache's maxSize")
	})

	ginkgo.It("should have cache miss when the fileCacheCapacity is larger than underlying storage", func() {
		init(specs.EnableFileCacheWithLargeCapacityPrefix)
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		// Create files using gsutil
		fileName := uuid.NewString()
		// The file size 2 GB is larger than the 1 GB PD
		specs.CreateTestFileWithSizeInBucket(fileName, bucketName, 2*1024*1024*1024)

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPVC := specs.NewTestPVC(f.ClientSet, f.Namespace, "custom-cache", "standard-rwo", "1Gi", corev1.ReadWriteOnce)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: tPVC.PVC}, webhook.SidecarContainerCacheVolumeName, "", false)
		tPod.SetNonRootSecurityContext(0, 0, 1000)

		ginkgo.By("Creating the PVC")
		tPVC.Create(ctx)
		defer tPVC.Cleanup(ctx)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat %v/%v > /dev/null", mountPath, fileName))

		tPod.WaitForLog(ctx, webhook.GcsFuseSidecarName, "no space left on device")
	})
}
