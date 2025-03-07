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

	"local/test/e2e/specs"
	"local/test/e2e/utils"
	"github.com/onsi/ginkgo/v2"
	"google.golang.org/grpc/codes"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIIstioTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIIstioTestSuite returns gcsFuseCSIIstioTestSuite that implements TestSuite interface.
func InitGcsFuseCSIIstioTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIIstioTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "istio",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIIstioTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIIstioTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIIstioTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
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

	f := framework.NewFrameworkWithCustomTimeouts("istio", storageframework.GetDriverTimeouts(driver))
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

	testGCSFuseWithIstio := func(configPrefix string, holdApplicationUntilProxyStarts, registryOnly bool) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetLabels(map[string]string{"sidecar.istio.io/inject": "true"})

		if holdApplicationUntilProxyStarts {
			tPod.SetAnnotations(map[string]string{"proxy.istio.io/config": "{ \"holdApplicationUntilProxyStarts\": true }"})
		}

		if registryOnly {
			tPod.SetAnnotations(map[string]string{"traffic.sidecar.istio.io/excludeOutboundIPRanges": "169.254.169.254/32"})
			specs.DeployIstioSidecar(f.Namespace.Name)
			specs.DeployIstioServiceEntry(f.Namespace.Name)
		}

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		if !holdApplicationUntilProxyStarts {
			ginkgo.By("Checking that the pod has failed container error")
			tPod.WaitForFailedContainerError(ctx, "Error: failed to reserve container name")
		}

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))
	}

	ginkgo.It("should store data with istio injected at index 0", func() {
		testGCSFuseWithIstio("", true, false)
	})
	ginkgo.It("[metadata prefetch] should store data with istio injected at index 0", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testGCSFuseWithIstio(specs.EnableMetadataPrefetchPrefix, true, false)
	})

	ginkgo.It("[flaky] should store data with istio injected at the last index", func() {
		testGCSFuseWithIstio("", false, false)
	})

	ginkgo.It("should store data with istio registry only outbound traffic policy mode", func() {
		testGCSFuseWithIstio("", true, true)
	})
	ginkgo.It("[metadata prefetch] should store data with istio registry only outbound traffic policy mode", func() {
		if pattern.VolType == storageframework.DynamicPV || !supportsNativeSidecar {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testGCSFuseWithIstio(specs.EnableMetadataPrefetchPrefix, true, true)
	})

	ginkgo.It("[flaky] should fail with istio registry only outbound traffic policy mode missing Pod annotation", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetLabels(map[string]string{"sidecar.istio.io/inject": "true"})
		tPod.SetAnnotations(map[string]string{"proxy.istio.io/config": "{ \"holdApplicationUntilProxyStarts\": true }"})

		specs.DeployIstioSidecar(f.Namespace.Name)
		specs.DeployIstioServiceEntry(f.Namespace.Name)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(ctx, codes.Internal.String())
		tPod.WaitForFailedMountError(ctx, "mountWithStorageHandle: fs.NewServer: create file system: SetUpBucket: Error in iterating through objects: Get")
	})

	ginkgo.It("[flaky] should fail with istio registry only outbound traffic policy mode missing ServiceEntry", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetLabels(map[string]string{"sidecar.istio.io/inject": "true"})
		tPod.SetAnnotations(map[string]string{"proxy.istio.io/config": "{ \"holdApplicationUntilProxyStarts\": true }"})
		tPod.SetAnnotations(map[string]string{"traffic.sidecar.istio.io/excludeOutboundIPRanges": "169.254.169.254/32"})

		specs.DeployIstioSidecar(f.Namespace.Name)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(ctx, codes.Internal.String())
		tPod.WaitForFailedMountError(ctx, "mountWithStorageHandle: fs.NewServer: create file system: SetUpBucket: Error in iterating through objects: Get")
	})
}
