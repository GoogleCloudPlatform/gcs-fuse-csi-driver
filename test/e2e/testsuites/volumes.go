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
	"strconv"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"local/test/e2e/specs"
	"local/test/e2e/utils"
	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
	"k8s.io/utils/ptr"
)

const (
	mountPath  = "/mnt/test"
	volumeName = "test-gcsfuse-volume"
)

type gcsFuseCSIVolumesTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIVolumesTestSuite returns gcsFuseCSIVolumesTestSuite that implements TestSuite interface.
func InitGcsFuseCSIVolumesTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIVolumesTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "volumes",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIVolumesTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIVolumesTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIVolumesTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	envVar := os.Getenv(utils.TestWithNativeSidecarEnvVar)
	supportsNativeSidecar, err := strconv.ParseBool(envVar)
	if err != nil {
		klog.Fatalf(`env variable "%s" could not be converted to boolean`, envVar)
	}
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("volumes", storageframework.GetDriverTimeouts(driver))
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

	testCaseStoreAndRetainData := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the first pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the first pod")
		tPod1.Create(ctx)

		ginkgo.By("Checking that the first pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Checking that the first pod command exits with no error")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))

		ginkgo.By("Deleting the first pod")
		tPod1.Cleanup(ctx)

		ginkgo.By("Configuring the second pod")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the second pod")
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		ginkgo.By("Checking that the second pod is running")
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Checking that the second pod command exits with no error")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/data", mountPath))
	}

	ginkgo.It("should store data and retain the data", func() {
		testCaseStoreAndRetainData("")
	})

	ginkgo.It("[csi-skip-bucket-access-check] should store data and retain the data", func() {
		testCaseStoreAndRetainData(specs.SkipCSIBucketAccessCheckPrefix)
	})

	ginkgo.It("[metadata prefetch] should store data and retain the data", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseStoreAndRetainData(specs.EnableMetadataPrefetchPrefix)
	})

	testCaseReadOnlyFailedWrite := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the writer pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetName("gcsfuse-volume-tester-writer")
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

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
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, true)

		ginkgo.By("Deploying the reader pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the reader pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the reader pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/data", mountPath))

		ginkgo.By("Expecting error when write to read-only volumes")
		tPod.VerifyExecInPodFail(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data", mountPath), 1)
	}

	ginkgo.It("[read-only] should fail when write", func() {
		testCaseReadOnlyFailedWrite("")
	})
	ginkgo.It("[read-only][csi-skip-bucket-access-check] should fail when write", func() {
		testCaseReadOnlyFailedWrite(specs.SkipCSIBucketAccessCheckPrefix)
	})
	ginkgo.It("[read-only][metadata prefetch] should fail when write", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseReadOnlyFailedWrite(specs.EnableMetadataPrefetchPrefix)
	})

	testCaseStoreRetainData := func(configPrefix string, uid, gid, fsgroup int) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the first pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetNonRootSecurityContext(uid, gid, fsgroup)
		tPod1.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the first pod")
		tPod1.Create(ctx)

		ginkgo.By("Checking that the first pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Checking that the first pod command exits with no error")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))

		ginkgo.By("Deleting the first pod")
		tPod1.Cleanup(ctx)

		ginkgo.By("Configuring the second pod")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the second pod")
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		ginkgo.By("Checking that the second pod is running")
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Checking that the second pod command exits with no error")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/data", mountPath))
	}

	ginkgo.It("[non-root] should store data and retain the data", func() {
		testCaseStoreRetainData(specs.NonRootVolumePrefix, 1001, 2002, 0)
	})
	ginkgo.It("[non-root][csi-skip-bucket-access-check] should store data and retain the data", func() {
		testCaseStoreRetainData(specs.SkipCSIBucketAccessCheckAndNonRootVolumePrefix, 1001, 2002, 0)
	})

	ginkgo.It("[fsgroup delegation] should store data and retain the data", func() {
		testCaseStoreRetainData("", 1001, 2002, 3003)
	})

	ginkgo.It("[fsgroup delegation][csi-skip-bucket-access-check] should store data and retain the data", func() {
		testCaseStoreRetainData(specs.SkipCSIBucketAccessCheckPrefix, 1001, 2002, 3003)
	})

	ginkgo.It("[metadata prefetch] should store data and retain the data", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseStoreRetainData(specs.EnableMetadataPrefetchPrefix, 1001, 2002, 3003)
	})

	testCaseImplicitDir := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

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
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/%v/data && grep 'hello world' %v/%v/data", mountPath, specs.ImplicitDirsPath, mountPath, specs.ImplicitDirsPath))
	}
	ginkgo.It("should store data in implicit directory", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}

		testCaseImplicitDir(specs.ImplicitDirsVolumePrefix)
	})
	ginkgo.It("[csi-skip-bucket-access-check] should store data in implicit directory", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}

		testCaseImplicitDir(specs.SkipCSIBucketAccessCheckAndImplicitDirsVolumePrefix)
	})

	testCaseStoreDataCustomContainerImage := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		// Check if test is using metadata prefetch.
		var hasMetadataPrefetch bool
		if configPrefix == specs.EnableMetadataPrefetchPrefix {
			hasMetadataPrefetch = true
		}

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetCustomSidecarContainerImage()
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the sidecar container is using the custom image")
		tPod.VerifyCustomSidecarContainerImage(supportsNativeSidecar, hasMetadataPrefetch)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))
	}

	ginkgo.It("should store data using custom sidecar container image", func() {
		testCaseStoreDataCustomContainerImage("")
	})
	ginkgo.It("[csi-skip-bucket-access-check] should store data using custom sidecar container image", func() {
		testCaseStoreDataCustomContainerImage(specs.SkipCSIBucketAccessCheckPrefix)
	})
	ginkgo.It("[metadata prefetch] should store data using custom sidecar container image", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseStoreDataCustomContainerImage(specs.EnableMetadataPrefetchPrefix)
	})

	testCaseCustomBufferVol := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPVC := specs.NewTestPVC(f.ClientSet, f.Namespace, "custom-buffer", "standard-rwo", "5Gi", corev1.ReadWriteOnce)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: tPVC.PVC}, webhook.SidecarContainerBufferVolumeName, "", false)
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
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))
	}

	ginkgo.It("should store data using custom buffer volume", func() {
		testCaseCustomBufferVol("")
	})
	ginkgo.It("[csi-skip-bucket-access-check] should store data using custom buffer volume", func() {
		testCaseCustomBufferVol(specs.SkipCSIBucketAccessCheckPrefix)
	})

	testCaseStoreDataInitContainer := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)
		tPod.SetInitContainerWithCommand(fmt.Sprintf("echo 'hello world from the init container' > %v/data1 && grep 'hello world from the init container' %v/data1", mountPath, mountPath))
		tPod.SetupVolumeForInitContainer("test-gcsfuse-volume", mountPath, false, "")

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world from the init container' %v/data1", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world from the regular container' > %v/data2 && grep 'hello world from the regular container' %v/data2", mountPath, mountPath))
	}

	ginkgo.It("should store data and retain the data in init container", func() {
		testCaseStoreDataInitContainer("")
	})
	ginkgo.It("[csi-skip-bucket-access-check] should store data and retain the data in init container", func() {
		testCaseStoreDataInitContainer(specs.SkipCSIBucketAccessCheckPrefix)
	})
	ginkgo.It("[metadata prefetch] should store data and retain the data in init container", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseStoreDataInitContainer(specs.EnableMetadataPrefetchPrefix)
	})
}
