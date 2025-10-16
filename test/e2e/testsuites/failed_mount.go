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
	"slices"
	"strconv"

	"local/test/e2e/specs"
	"local/test/e2e/utils"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"google.golang.org/grpc/codes"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIFailedMountTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIFailedMountTestSuite returns gcsFuseCSIFailedMountTestSuite that implements TestSuite interface.
func InitGcsFuseCSIFailedMountTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIFailedMountTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "failedMount",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIFailedMountTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIFailedMountTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIFailedMountTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	nativeSidecarEnvVar := os.Getenv(utils.TestWithNativeSidecarEnvVar)
	supportsNativeSidecar, err := strconv.ParseBool(nativeSidecarEnvVar)
	if err != nil {
		klog.Fatalf(`env variable "%s" could not be converted to boolean`, nativeSidecarEnvVar)
	}
	enableSidecarBucketAccessCheck, err := strconv.ParseBool(os.Getenv(utils.TestWithSidecarBucketAccessCheckEnvVar))
	if err != nil {
		klog.Fatalf(`env variable "%s" could not be converted to boolean`, enableSidecarBucketAccessCheck)
	}

	saVolInjectEnvVar := os.Getenv(utils.TestWithSAVolumeInjectionEnvVar)
	supportSAVolInjection, err := strconv.ParseBool(saVolInjectEnvVar)
	if err != nil {
		klog.Fatalf(`env variable "%s" could not be converted to boolean`, saVolInjectEnvVar)
	}

	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("failed-mount", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	setupIAMPolicy := func(bucketName, namespace, saName string) {
		gcsfuseCSITestDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("Failed to cast driver to GCSFuseCSITestDriver")
		}

		gcsfuseCSITestDriver.SetIAMPolicy(ctx, &storage.ServiceBucket{Name: bucketName}, namespace, saName)
	}

	removeIAMPolicy := func(bucketName, namespace, saName string) {
		gcsfuseCSITestDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("Failed to cast driver to GCSFuseCSITestDriver")
		}

		gcsfuseCSITestDriver.RemoveIAMPolicy(ctx, &storage.ServiceBucket{Name: bucketName}, namespace, saName)
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	testCaseNonExistentBucket := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error")

		if enableSidecarBucketAccessCheck && configPrefix == specs.SkipCSIBucketAccessCheckAndFakeVolumePrefix {
			tPod.WaitForFailedContainerError(ctx, "Error: failed to reserve container name")
		} else {
			tPod.WaitForFailedMountError(ctx, codes.NotFound.String())
		}
		if gcsfuseVersionStr == "" {
			gcsfuseVersionStr = specs.GetGCSFuseVersion(ctx, f)
		}
		v, err := version.ParseSemantic(gcsfuseVersionStr)
		if configPrefix == specs.SkipCSIBucketAccessCheckAndFakeVolumePrefix && (err != nil || v.AtLeast(version.MustParseSemantic("v2.5.0-gke.0"))) {
			tPod.WaitForLog(ctx, webhook.GcsFuseSidecarName, "bucket does not exist")
		} else {
			tPod.WaitForFailedMountError(ctx, "storage: bucket doesn't exist")
		}
	}

	ginkgo.It("should fail when the specified GCS bucket does not exist", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseNonExistentBucket(specs.FakeVolumePrefix)
	})
	ginkgo.It("[metadata prefetch]should fail when the specified GCS bucket does not exist", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseNonExistentBucket(specs.EnableMetadataPrefetchAndFakeVolumePrefix)
	})

	ginkgo.It("[csi-skip-bucket-access-check] should fail when the specified GCS bucket does not exist", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseNonExistentBucket(specs.SkipCSIBucketAccessCheckAndFakeVolumePrefix)
	})

	testCaseInvalidBucketName := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error")

		if gcsfuseVersionStr == "" {
			gcsfuseVersionStr = specs.GetGCSFuseVersion(ctx, f)
		}
		v, err := version.ParseSemantic(gcsfuseVersionStr)
		if configPrefix == specs.SkipCSIBucketAccessCheckAndInvalidVolumePrefix && (err != nil || v.AtLeast(version.MustParseSemantic("v2.9.0-gke.0"))) {
			if enableSidecarBucketAccessCheck {
				tPod.WaitForFailedContainerError(ctx, "Error: failed to reserve container name")
				tPod.WaitForLog(ctx, webhook.GcsFuseSidecarName, "storage: bucket doesn't exist")
			} else {
				tPod.WaitForFailedMountError(ctx, codes.InvalidArgument.String())
				tPod.WaitForFailedMountError(ctx, "name should be a valid bucket resource name")
			}
		} else {
			tPod.WaitForFailedMountError(ctx, codes.NotFound.String())
			tPod.WaitForFailedMountError(ctx, "storage: bucket doesn't exist")
		}
	}

	ginkgo.It("should fail when the specified GCS bucket name is invalid", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}

		testCaseInvalidBucketName(specs.InvalidVolumePrefix)
	})

	ginkgo.It("[csi-skip-bucket-access-check] should fail when the specified GCS bucket name is invalid", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}

		testCaseInvalidBucketName(specs.SkipCSIBucketAccessCheckAndInvalidVolumePrefix)
	})

	testCaseSAInsufficientAccess := func(configPrefix ...string) {
		init(configPrefix...)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		if slices.Contains(configPrefix, specs.EnableHostNetworkPrefix) {
			tPod.EnableHostNetwork()
		}
		ksaOptIn := slices.Contains(configPrefix, specs.OptInHnwKSAPrefix)
		if ksaOptIn {
			ginkgo.By("Opting in to hostnetwork KSA feature")
			tPod.SetupVolumeWithHostNetworkKSAOptIn(l.volumeResource, volumeName, mountPath, false)
		} else {
			tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		}

		ginkgo.By("Deploying a Kubernetes service account without access to the GCS bucket")
		saName := "sa-without-access"
		testK8sSA := specs.NewTestKubernetesServiceAccount(f.ClientSet, f.Namespace, saName, "")
		testK8sSA.Create(ctx)
		tPod.SetServiceAccount(saName)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error PermissionDenied")

		// For invalid SA testcase, The Unauthenticated error spawns from prepareStorageClient() in CSI NodePublish. When CSI skips bucket access check, this step is skipped and access is checked in sidecar checkBucketAccessWithRetry() .
		if enableSidecarBucketAccessCheck {
			if slices.Contains(configPrefix, specs.SkipCSIBucketAccessCheckPrefix) {
				tPod.WaitForFailedContainerError(ctx, "Error: failed to reserve container name")
				tPod.WaitForLog(ctx, webhook.GcsFuseSidecarName, "does not have storage.objects.list access to the Google Cloud Storage bucket")
				return
			}
		}
		tPod.WaitForFailedMountError(ctx, codes.PermissionDenied.String())
		tPod.WaitForFailedMountError(ctx, "does not have storage.objects.list access to the Google Cloud Storage bucket.")

		ginkgo.By("Deleting the Kubernetes service account")
		testK8sSA.Cleanup(ctx)

		// For invalid SA testcase, The Unauthenticated error spawns from prepareStorageClient() in CSI NodePublish. When CSI skips bucket access check, this step is skipped.
		if slices.Contains(configPrefix, specs.SkipCSIBucketAccessCheckPrefix) {
			return
		}

		ginkgo.By("Checking that the pod has failed mount error Unauthenticated")
		tPod.WaitForFailedMountError(ctx, codes.Unauthenticated.String())
		tPod.WaitForFailedMountError(ctx, "storage service manager failed to setup service: context deadline exceeded")
	}

	ginkgo.It("should fail when the specified service account does not have access to the GCS bucket", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseSAInsufficientAccess("")
	})
	ginkgo.It("[metadata prefetch] should fail when the specified service account does not have access to the GCS bucket", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseSAInsufficientAccess(specs.EnableMetadataPrefetchPrefix)
	})

	ginkgo.It("[csi-skip-bucket-access-check] should fail when the specified service account does not have access to the GCS bucket", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseSAInsufficientAccess(specs.SkipCSIBucketAccessCheckPrefix)
	})

	ginkgo.It("[hostnetwork] should fail when the specified service account does not have access to the GCS bucket", func() {
		if pattern.VolType == storageframework.DynamicPV || pattern.VolType == storageframework.PreprovisionedPV {
			e2eskipper.Skipf("skip for volume type %v", pattern.VolType)
		}
		if supportSAVolInjection {
			testCaseSAInsufficientAccess(specs.EnableHostNetworkPrefix, specs.OptInHnwKSAPrefix)
		} else {
			ginkgo.By("Skipping the hostnetwork test for managed cluster version <  " + utils.SaTokenVolInjectionMinimumVersion.String() + " or cluster without init container support")
		}
	})

	ginkgo.It("should respond to service account permission changes", func() {
		init()
		defer cleanup()

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		bucketName := l.config.Prefix

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying a Kubernetes service account without access to the GCS bucket")
		saName := "sa-without-access"
		testK8sSA := specs.NewTestKubernetesServiceAccount(f.ClientSet, f.Namespace, saName, "")
		testK8sSA.Create(ctx)
		tPod.SetServiceAccount(saName)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error PermissionDenied")
		tPod.WaitForFailedMountError(ctx, codes.PermissionDenied.String())
		tPod.WaitForFailedMountError(ctx, "does not have storage.objects.list access to the Google Cloud Storage bucket.")

		ginkgo.By("Setting up SA IAM policy")
		setupIAMPolicy(bucketName, f.Namespace.Name, saName)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))

		ginkgo.By("Removing SA IAM policy")
		removeIAMPolicy(bucketName, f.Namespace.Name, saName)

		ginkgo.By("Expecting error when write to the volume with permission removed")
		tPod.VerifyExecInPodFail(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data", mountPath), 1)
	})

	testCaseSidecarNotInjected := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/volumes": "false",
		})

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(ctx, codes.FailedPrecondition.String())
		tPod.WaitForFailedMountError(ctx, "failed to find the sidecar container in Pod spec")
	}

	ginkgo.It("should fail when the sidecar container is not injected", func() {
		testCaseSidecarNotInjected("")
	})
	ginkgo.It("[metadata prefetch] should fail when the sidecar container is not injected", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseSidecarNotInjected(specs.EnableMetadataPrefetchPrefix)
	})

	ginkgo.It("[csi-skip-bucket-access-check] should fail when the sidecar container is not injected", func() {
		testCaseSidecarNotInjected(specs.SkipCSIBucketAccessCheckPrefix)
	})

	testCaseGCSFuseOOM := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand("touch /mnt/test/test_file && while true; do echo $(date) >> /mnt/test/test_file; done")

		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/memory-limit":   "15Mi",
			"gke-gcsfuse/memory-request": "15Mi",
		})

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(ctx, codes.ResourceExhausted.String())
	}

	ginkgo.It("should fail when the gcsfuse processes got killed due to OOM", func() {
		testCaseGCSFuseOOM("")
	})
	ginkgo.It("[metadata prefetch] should fail when the gcsfuse processes got killed due to OOM", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseGCSFuseOOM(specs.EnableMetadataPrefetchPrefix)
	})

	ginkgo.It("[csi-skip-bucket-access-check] should fail when the gcsfuse processes got killed due to OOM", func() {
		testCaseGCSFuseOOM(specs.SkipCSIBucketAccessCheckPrefix)
	})

	testcaseInvalidMountOptions := func(configPrefix string) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(ctx, codes.InvalidArgument.String())
		tPod.WaitForFailedMountError(ctx, "-invalid-option")
	}

	ginkgo.It("should fail when invalid mount options are passed", func() {
		testcaseInvalidMountOptions(specs.InvalidMountOptionsVolumePrefix)
	})

	ginkgo.It("[metadata prefetch] should fail when invalid mount options are passed", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testcaseInvalidMountOptions(specs.EnableMetadataPrefetchAndInvalidMountOptionsVolumePrefix)
	})

	ginkgo.It("[csi-skip-bucket-access-check] should fail when invalid mount options are passed", func() {
		testcaseInvalidMountOptions(specs.SkipCSIBucketAccessCheckAndInvalidMountOptionsVolumePrefix)
	})

	ginkgo.It("should fail when the sidecar container is specified with high resource usage", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/memory-limit":   "1000000000Gi",
			"gke-gcsfuse/cpu-limit":      "1000000000",
			"gke-gcsfuse/memory-request": "1000000000Gi",
			"gke-gcsfuse/cpu-request":    "1000000000",
		})

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is in Unschedulable status")
		tPod.WaitForUnschedulable(ctx)
	})
}
