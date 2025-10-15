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
	"strings"

	"local/test/e2e/specs"

	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIWorkloadsTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIWorkloadsTestSuite returns gcsFuseCSIWorkloadsTestSuite that implements TestSuite interface.
func InitGcsFuseCSIWorkloadsTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIWorkloadsTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "workloads",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIWorkloadsTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIWorkloadsTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIWorkloadsTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("workloads", storageframework.GetDriverTimeouts(driver))
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

	ginkgo.It("should store data in Deployment", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand(fmt.Sprintf("mount | grep %v | grep rw, && echo 'hello world' > %v/${POD_NAME} && grep 'hello world' %v/${POD_NAME} && tail -f /dev/null", mountPath, mountPath, mountPath))

		ginkgo.By("Configuring the deployment")
		tDeployment := specs.NewTestDeployment(f.ClientSet, f.Namespace, tPod)

		ginkgo.By("Deploying the deployment")
		tDeployment.Create(ctx)
		defer tDeployment.Cleanup(ctx)

		ginkgo.By("Checking that the deployment is in ready status")
		tDeployment.WaitForRunningAndReady(ctx)
	})

	ginkgo.It("[fast termination] should store data in Deployment while scaling down", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetGracePeriod(600)
		tPod1.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		cmd := []string{
			fmt.Sprintf("handle_term() { echo 'Caught SIGTERM signal!'; echo 'hello world' > %v/data && grep 'hello world' %v/data; sleep 5; exit 0; };", mountPath, mountPath),
			"trap handle_term SIGTERM;",
			"echo 'I am sleeping!';",
			"sleep infinity & wait $!;",
		}
		tPod1.SetCommand(strings.Join(cmd, " "))

		ginkgo.By("Configuring the deployment")
		tDeployment := specs.NewTestDeployment(f.ClientSet, f.Namespace, tPod1)
		tDeployment.SetReplicas(1)

		ginkgo.By("Deploying the deployment")
		tDeployment.Create(ctx)
		defer tDeployment.Cleanup(ctx)

		ginkgo.By("Checking that the deployment is in ready status")
		tDeployment.WaitForRunningAndReady(ctx)

		ginkgo.By("Scaling down the deployment to 0")
		tDeployment.Scale(ctx, 0)

		ginkgo.By("Checking that the deployment is in ready status")
		tDeployment.WaitForRunningAndReadyWithTimeout(ctx)

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

	ginkgo.It("should store data in StatefulSet", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand(fmt.Sprintf("mount | grep %v | grep rw, && echo 'hello world' > %v/${POD_NAME} && grep 'hello world' %v/${POD_NAME} && tail -f /dev/null", mountPath, mountPath, mountPath))

		ginkgo.By("Configuring the StatefulSet")
		tStatefulSet := specs.NewTestStatefulSet(f.ClientSet, f.Namespace, tPod)

		ginkgo.By("Deploying the StatefulSet")
		tStatefulSet.Create(ctx)
		defer tStatefulSet.Cleanup(ctx)

		ginkgo.By("Checking that the StatefulSet is in ready status")
		tStatefulSet.WaitForRunningAndReady(ctx)
	})

	ginkgo.It("[auto termination] should store data in Job with RestartPolicy Never", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(corev1.RestartPolicyNever)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetCommand("touch /mnt/test/test_file && echo $(date) >> /mnt/test/test_file && cat /mnt/test/test_file")

		ginkgo.By("Configuring the job")
		tJob := specs.NewTestJob(f.ClientSet, f.Namespace, tPod)

		ginkgo.By("Deploying the job")
		tJob.Create(ctx)
		defer tJob.Cleanup(ctx)

		ginkgo.By("Checking that the job is in succeeded status")
		tJob.WaitForJobPodsSucceeded(ctx)
	})

	ginkgo.It("[auto termination] should store data in Job with RestartPolicy OnFailure eventually succeed", func() {
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

		ginkgo.By("Configuring the job")
		tJob := specs.NewTestJob(f.ClientSet, f.Namespace, tPod)

		ginkgo.By("Deploying the job")
		tJob.Create(ctx)
		defer tJob.Cleanup(ctx)

		ginkgo.By("Checking that the job is in succeeded status")
		tJob.WaitForJobPodsSucceeded(ctx)
	})

	ginkgo.It("[fast termination] should fast terminate sidecar container in Job with RestartPolicy OnFailure eventually fail", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetGracePeriod(600)
		tPod.SetRestartPolicy(corev1.RestartPolicyOnFailure)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		cmd := []string{
			"sleep 10;",
			"exit 1;",
		}
		tPod.SetCommand(strings.Join(cmd, " "))

		ginkgo.By("Configuring the job")
		tJob := specs.NewTestJob(f.ClientSet, f.Namespace, tPod)

		ginkgo.By("Deploying the job")
		tJob.Create(ctx)
		defer tJob.Cleanup(ctx)

		ginkgo.By("Checking that the job is in failed status")
		tJob.WaitForJobFailed(ctx)

		ginkgo.By("The pod should terminate fast")
		tJob.WaitForAllJobPodsGone(ctx)
	})

	ginkgo.It("should successfully create pod with automountServiceAccountToken false", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPodModifiedSpec(f.ClientSet, f.Namespace, false)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)
	})

	ginkgo.It("should successfully create pod with automountServiceAccountToken true", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPodModifiedSpec(f.ClientSet, f.Namespace, true)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)
	})

}
