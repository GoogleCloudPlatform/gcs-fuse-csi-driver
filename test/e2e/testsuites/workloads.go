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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
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
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Configuring the deployment")
		tDeployment := specs.NewTestDeployment(f.ClientSet, f.Namespace, tPod)

		ginkgo.By("Deploying the deployment")
		tDeployment.Create(ctx)
		defer tDeployment.Cleanup(ctx)

		ginkgo.By("Checking that the deployment is in ready status")
		tDeployment.WaitForComplete()
		tPod.SetPod(tDeployment.GetPod(ctx))

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))
	})

	ginkgo.It("should store data in Job", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetRestartPolicy(v1.RestartPolicyNever)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)
		tPod.SetCommand("touch /mnt/test/test_file && echo $(date) >> /mnt/test/test_file && cat /mnt/test/test_file")

		ginkgo.By("Configuring the job")
		tJob := specs.NewTestJob(f.ClientSet, f.Namespace, tPod)

		ginkgo.By("Deploying the job")
		tJob.Create(ctx)
		defer tJob.Cleanup(ctx)

		ginkgo.By("Checking that the job is in succeeded status")
		tJob.WaitForJobPodsSucceeded(ctx)
	})
}
