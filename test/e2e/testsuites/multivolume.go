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
	"strings"
	"time"

	"local/test/e2e/specs"
	"local/test/e2e/utils"
	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIMultiVolumeTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIMultiVolumeTestSuite returns gcsFuseCSIMultiVolumeTestSuite that implements TestSuite interface.
func InitGcsFuseCSIMultiVolumeTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIMultiVolumeTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "multivolume",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseCSIMultiVolumeTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIMultiVolumeTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIMultiVolumeTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	envVar := os.Getenv(utils.TestWithNativeSidecarEnvVar)
	supportsNativeSidecar, err := strconv.ParseBool(envVar)
	if err != nil {
		klog.Fatalf(`env variable "%s" could not be converted to boolean`, envVar)
	}
	type local struct {
		config             *storageframework.PerTestConfig
		volumeResourceList []*storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("multivolume", storageframework.GetDriverTimeouts(driver))
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
	}

	cleanup := func() {
		var cleanUpErrs []error
		for _, vr := range l.volumeResourceList {
			cleanUpErrs = append(cleanUpErrs, vr.CleanupResource(ctx))
		}
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	testTwoPodsSameBucket := func(diffVol, sameNode bool) {
		ginkgo.By("Configuring the first pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.volumeResourceList[0], volumeName, mountPath, false)

		ginkgo.By("Deploying the first pod")
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)

		ginkgo.By("Checking that the first pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Configuring the second pod")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		volIndex := 0
		if diffVol {
			volIndex = 1
		}
		tPod2.SetupVolume(l.volumeResourceList[volIndex], volumeName, mountPath, false)
		tPod2.SetNodeAffinity(tPod1.GetNode(), sameNode)

		ginkgo.By("Deploying the second pod")
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		ginkgo.By("Checking that the second pod is running")
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Checking that the first pod command exits with no error")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data-0 && grep 'hello world' %v/data-0", mountPath, mountPath))

		ginkgo.By("Checking that the second pod command exits with no error")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/data-0", mountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data-1 && grep 'hello world' %v/data-1", mountPath, mountPath))

		ginkgo.By("Checking that the first pod command exits with no error")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello world' %v/data-1", mountPath))
	}

	testOnePodTwoVols := func() {
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

		ginkgo.By("Checking that the pod command exits with no error")
		for i := range l.volumeResourceList {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", fmt.Sprintf("%v/%v", mountPath, i)))
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/%v/data-%v && grep 'hello world' %v/%v/data-%v", mountPath, i, i, mountPath, i, i))
		}
	}

	// This tests below configuration:
	//          [node1]
	//          /    \
	//    [pod1]     [pod2]
	//       |          |
	//   [volume1]  [volume2]
	//          \    /
	//         [bucket1]
	ginkgo.It("should access multiple volumes backed by the same bucket from different Pods on the same node", func() {
		init(2)
		defer cleanup()

		testTwoPodsSameBucket(true /* different volumes */, true /* same node */)
	})

	// This tests below configuration:
	//    [node1]    [node2]
	//       |          |
	//    [pod1]     [pod2]
	//       |          |
	//   [volume1]  [volume2]
	//          \    /
	//         [bucket1]
	ginkgo.It("should access multiple volumes backed by the same bucket from different Pods on different nodes", func() {
		init(2)
		defer cleanup()

		testTwoPodsSameBucket(true /* different volumes */, false /* different nodes */)
	})

	// This tests below configuration:
	//          [node1]
	//          /    \
	//     [pod1]    [pod2]
	//          \    /
	//         [volume1]
	//             |
	//         [bucket1]
	ginkgo.It("should access one volume backed by the same bucket from different Pods on the same node", func() {
		init(1)
		defer cleanup()

		testTwoPodsSameBucket(false /* same volume */, false /* same node */)
	})

	// This tests below configuration:
	//    [node1]    [node2]
	//       |          |
	//    [pod1]     [pod2]
	//          \    /
	//         [volume1]
	//             |
	//         [bucket1]
	ginkgo.It("should access one volume backed by the same bucket from different Pods on different nodes", func() {
		init(1)
		defer cleanup()

		testTwoPodsSameBucket(false /* same volume */, false /* different nodes */)
	})

	// This tests below configuration:
	//          [pod1]
	//          /    \
	//   [volume1]  [volume2]
	//          \    /
	//        [bucket1]
	ginkgo.It("should access multiple volumes backed by the same bucket from the same Pod", func() {
		// According to the code: https://github.com/kubernetes/kubernetes/blob/8f15859afc9cfaeb05d4915ffa204d84da512094/pkg/kubelet/volumemanager/cache/desired_state_of_world.go#L296-L298
		// For non-attachable and non-device-mountable volumes, generate a unique name based on the pod
		// namespace and name and the name of the volume within the pod.
		// In the case of using a CSI driver and a pre-provisioned PV, the volume name is specified via the volumeHandle.
		// different PVs will be treated as the same volume, therefore this test is not applicable.
		if pattern.VolType == storageframework.PreprovisionedPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.PreprovisionedPV)
		}

		init(2)
		defer cleanup()

		testOnePodTwoVols()
	})

	// This tests below configuration:
	//          [pod1]
	//          /    \
	//   [volume1]  [volume2]
	//       |          |
	//   [bucket1]  [bucket2]
	ginkgo.It("should access multiple volumes backed by different buckets from the same Pod", func() {
		init(2, specs.ForceNewBucketPrefix)
		defer cleanup()

		testOnePodTwoVols()
	})
	ginkgo.It("[metadata prefetch] should access multiple volumes backed by different buckets from the same Pod", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		init(2, specs.EnableMetadataPrefetchPrefixForceNewBucketPrefix)
		defer cleanup()

		testOnePodTwoVols()
	})

	// This tests below configuration:
	//          [pod1]
	//          /    \
	//   [volume1]  [volume2]
	//       |          |
	//   [      bucket1     ]
	//       |          |
	//     [dir1]     [dir2]
	ginkgo.It("should access different directories in the same GCS bucket via different volumes", func() {
		// According to the code: https://github.com/kubernetes/kubernetes/blob/8f15859afc9cfaeb05d4915ffa204d84da512094/pkg/kubelet/volumemanager/cache/desired_state_of_world.go#L296-L298
		// For non-attachable and non-device-mountable volumes, generate a unique name based on the pod
		// namespace and name and the name of the volume within the pod.
		// In the case of using a CSI driver and a pre-provisioned PV, the volume name is specified via the volumeHandle.
		// different PVs will be treated as the same volume, therefore this test is not applicable.
		if pattern.VolType == storageframework.PreprovisionedPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.PreprovisionedPV)
		}

		init(2, specs.SubfolderInBucketPrefix)
		defer cleanup()

		testOnePodTwoVols()
	})

	// This tests below configuration:
	//          [pod1]
	//            |
	//        [volume1]
	//          /    \
	//   [bucket1]  [bucket2]
	ginkgo.It("should access multiple GCS buckets via the same volume", func() {
		init(1, specs.MultipleBucketsPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResourceList[0], volumeName, mountPath, false)

		ginkgo.By("Sleeping 2 minutes for the service account being propagated")
		time.Sleep(time.Minute * 2)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))

		// The test driver uses config.Prefix to pass the bucket names back to the test suite.
		for i, bucketName := range strings.Split(l.config.Prefix, ",") {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/%v/data-%v && grep 'hello world' %v/%v/data-%v", mountPath, bucketName, i, mountPath, bucketName, i))
		}
	})
}
