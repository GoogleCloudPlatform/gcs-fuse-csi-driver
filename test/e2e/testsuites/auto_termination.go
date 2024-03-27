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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/utils"
	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIAutoTerminationTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIAutoTerminationTestSuite returns gcsFuseCSIAutoTerminationTestSuite that implements TestSuite interface.
func InitGcsFuseCSIAutoTerminationTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIAutoTerminationTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "autoTermination",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIAutoTerminationTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIAutoTerminationTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIAutoTerminationTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
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
	f := framework.NewFrameworkWithCustomTimeouts("auto-termination", storageframework.GetDriverTimeouts(driver))
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

	ginkgo.It("[fast termination] should store data and retain the data when Pod is terminating", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the first pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetRestartPolicy(corev1.RestartPolicyAlways)
		tPod1.SetGracePeriod(600)
		tPod1.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		cmd := []string{
			fmt.Sprintf("handle_term() { echo 'Caught SIGTERM signal!'; echo 'hello world' > %v/data && grep 'hello world' %v/data; sleep 5; exit 0; };", mountPath, mountPath),
			"trap handle_term SIGTERM;",
			"echo 'I am sleeping!';",
			"sleep infinity & wait $!;",
		}
		tPod1.SetCommand(strings.Join(cmd, " "))

		ginkgo.By("Deploying the first pod")
		tPod1.Create(ctx)

		ginkgo.By("Checking that the first pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Deleting the first pod")
		tPod1.Cleanup(ctx)

		ginkgo.By("The pod should terminate fast")
		tPod1.WaitForPodNotFoundInNamespace(ctx)

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
	})

	ginkgo.It("should succeed when Pod RestartPolicy is Never and eventually succeeding", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(corev1.RestartPolicyNever)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand(fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod succeeded")
		tPod.WaitForSuccess(ctx)
	})

	ginkgo.It("should fail when Pod RestartPolicy is Never and eventually failing", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(corev1.RestartPolicyNever)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand("sleep 10; exit 1;")

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod failed")
		tPod.WaitForFail(ctx)
	})

	ginkgo.It("[no auto termination] should not terminate the sidecar when Pod RestartPolicy is Always and succeeding", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the first pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(corev1.RestartPolicyAlways)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand(fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))

		ginkgo.By("Deploying the first pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the first pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the sidecar container is still running after a while")
		tPod.CheckSidecarNeverTerminatedAfterAWhile(ctx, supportsNativeSidecar)
	})

	ginkgo.It("[no auto termination] should not terminate the sidecar when Pod RestartPolicy is Always and failing", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(corev1.RestartPolicyAlways)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand("sleep 10; exit 1;")

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the sidecar container is still running after a while")
		tPod.CheckSidecarNeverTerminatedAfterAWhile(ctx, supportsNativeSidecar)
	})

	ginkgo.It("should succeed when Pod RestartPolicy is OnFailure", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(corev1.RestartPolicyOnFailure)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		cmd := []string{
			"sleep 10;",
			fmt.Sprintf("if [ -f %v/testfile ]; then echo 'Completed Successfully!'; exit 0; fi;", mountPath),
			fmt.Sprintf("touch %v/testfile;", mountPath),
			"echo 'Job Failed!';",
			"exit 1;",
		}
		tPod.SetCommand(strings.Join(cmd, " "))

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod succeeded")
		tPod.WaitForSuccess(ctx)
	})

	ginkgo.It("[no auto termination] should not terminate the sidecar when Pod RestartPolicy is OnFailure and failing", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(corev1.RestartPolicyAlways)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand("sleep 10; exit 1;")

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the sidecar container is still running after a while")
		tPod.CheckSidecarNeverTerminatedAfterAWhile(ctx, supportsNativeSidecar)
	})
}
