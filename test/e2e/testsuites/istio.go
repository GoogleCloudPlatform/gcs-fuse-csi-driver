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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"google.golang.org/grpc/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

// Outbound IP ranges that a Pod excludes from Istio redirection so that DirectPath gRPC traffic bypasses the
// Istio sidecar proxy: the GCE metadata server and the DirectPath IPv4 and IPv6 ranges.
const directPathExcludeOutboundIPRanges = "169.254.169.254/32,34.126.0.0/18,2001:4860:8040::/42"

type gcsFuseCSIIstioTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIIstioTestSuite returns gcsFuseCSIIstioTestSuite that implements TestSuite interface.
func InitGcsFuseCSIIstioTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIIstioTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "istio",
			TestPatterns: []storageframework.TestPattern{
				// Only run Istio tests on a single volume type to keep E2E test count low,
				// since Istio/protocol behavior does not depend on how the volume is provisioned.
				storageframework.DefaultFsCSIEphemeralVolume,
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

	// extraChecks run on the pod after it has stored data.
	runIstioTest := func(configPrefix string, holdApplicationUntilProxyStarts, registryOnly, nonNativeIstio, forceDirectPathExclusions bool, extraChecks ...func(tPod *specs.TestPod)) {
		init(configPrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		tPod.SetLabels(map[string]string{"sidecar.istio.io/inject": "true"})

		if nonNativeIstio {
			// Inject istio-proxy as a regular container, so that istio-init runs after gke-gcsfuse-sidecar.
			tPod.SetAnnotations(map[string]string{"sidecar.istio.io/nativeSidecar": "false"})
		}

		if holdApplicationUntilProxyStarts {
			tPod.SetAnnotations(map[string]string{"proxy.istio.io/config": "{ \"holdApplicationUntilProxyStarts\": true }"})
		}

		if registryOnly {
			tPod.SetAnnotations(map[string]string{"traffic.sidecar.istio.io/excludeOutboundIPRanges": "169.254.169.254/32"})
			specs.DeployIstioSidecar(f.Namespace.Name)
			specs.DeployIstioServiceEntry(f.Namespace.Name)
		}

		// When using gRPC or Zonal Buckets, Istio blocks outbound connections to GCS and causes gcsfuse to hang on mount.
		// Excluding port 443 and the metadata server and DirectPath IP ranges lets gRPC traffic bypass the Istio sidecar proxy.
		// These IP ranges include the metadata server range that registryOnly sets above.
		if forceDirectPathExclusions || zbEnabled(driver) || (getClientProtocol(driver) == "grpc" && configPrefix != specs.EnableGrpcByDefaultPrefix) {
			tPod.SetAnnotations(map[string]string{
				"traffic.sidecar.istio.io/excludeOutboundPorts":    "443",
				"traffic.sidecar.istio.io/excludeOutboundIPRanges": directPathExcludeOutboundIPRanges,
			})
		}

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		if nonNativeIstio {
			// gke-gcsfuse-sidecar is either an init container that comes before istio-init, or a regular container.
			ginkgo.By("Checking that istio-init runs after gke-gcsfuse-sidecar")
			pod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(ctx, tPod.GetPodName(), metav1.GetOptions{})
			framework.ExpectNoError(err)
			var initContainers []string
			for _, c := range pod.Spec.InitContainers {
				initContainers = append(initContainers, c.Name)
			}
			istioInitIdx := slices.Index(initContainers, "istio-init")
			gomega.Expect(istioInitIdx).To(gomega.BeNumerically(">=", 0), "istio-init should be an init container, init containers are %v", initContainers)
			sidecarIdx := slices.Index(initContainers, webhook.GcsFuseSidecarName)
			if supportsNativeSidecar {
				gomega.Expect(sidecarIdx).To(gomega.BeNumerically(">=", 0), "%s should be an init container, init containers are %v", webhook.GcsFuseSidecarName, initContainers)
			}
			gomega.Expect(istioInitIdx).To(gomega.BeNumerically(">", sidecarIdx),
				"istio-init should come after %s, init containers are %v", webhook.GcsFuseSidecarName, initContainers)
		}

		if !holdApplicationUntilProxyStarts {
			ginkgo.By("Checking that the pod has failed container error")
			tPod.WaitForFailedContainerError(ctx, "Error: failed to reserve container name")
		}

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))

		for _, check := range extraChecks {
			check(tPod)
		}
	}
	testGCSFuseWithIstio := func(configPrefix string, holdApplicationUntilProxyStarts, registryOnly, nonNativeIstio bool, extraChecks ...func(tPod *specs.TestPod)) {
		runIstioTest(configPrefix, holdApplicationUntilProxyStarts, registryOnly, nonNativeIstio, false, extraChecks...)
	}

	// For gRPC, this test covers the case where the user explicitly sets client-protocol=grpc and excludes
	// port 443 and the metadata server and DirectPath IP ranges from Istio redirection.
	ginkgo.It("should store data with istio injected at index 0", func() {
		testGCSFuseWithIstio("", true, false, false)
	})
	ginkgo.It("[metadata prefetch] should store data with istio injected at index 0", func() {
		if !supportsNativeSidecar {
			e2eskipper.Skipf("skip when native sidecar is not supported")
		}
		testGCSFuseWithIstio(specs.EnableMetadataPrefetchPrefix, true, false, false)
	})

	ginkgo.It("should store data with istio when grpc is enabled by default and DirectPath exclusions are not set", func() {
		if getClientProtocol(driver) != "grpc" {
			e2eskipper.Skipf("skip when client-protocol is not grpc")
		}
		if zbEnabled(driver) {
			e2eskipper.Skipf("skip with Zonal Buckets, where the Pod excludes DirectPath traffic from Istio redirection")
		}
		skipIfEnableGrpcByDefaultNotSupported()
		testGCSFuseWithIstio(specs.EnableGrpcByDefaultPrefix, true, false, false, func(tPod *specs.TestPod) {
			ginkgo.By("Checking that the driver passed --enable-grpc-by-default=true to gcsfuse")
			stdout, err := tPod.GetSidecarLogs()
			framework.ExpectNoError(err)
			gomega.Expect(stdout).To(gomega.ContainSubstring("--enable-grpc-by-default=true"))
			gomega.Expect(stdout).NotTo(gomega.ContainSubstring("--enable-grpc-by-default=false"))
		})
	})

	ginkgo.It("should store data with istio registry only outbound traffic policy mode and fallback to http1 when grpc is enabled by default and DirectPath exclusions are not set", func() {
		if getClientProtocol(driver) != "grpc" {
			e2eskipper.Skipf("skip when client-protocol is not grpc")
		}
		if zbEnabled(driver) {
			e2eskipper.Skipf("skip with Zonal Buckets, where the Pod excludes DirectPath traffic from Istio redirection")
		}
		skipIfEnableGrpcByDefaultNotSupported()
		testGCSFuseWithIstio(specs.EnableGrpcByDefaultPrefix, true, true, false, func(tPod *specs.TestPod) {
			ginkgo.By("Checking that gcsfuse received --enable-grpc-by-default=true and fell back to HTTP/1")
			stdout, err := tPod.GetSidecarLogs()
			framework.ExpectNoError(err)
			gomega.Expect(stdout).To(gomega.ContainSubstring("--enable-grpc-by-default=true"))
			gomega.Expect(stdout).To(gomega.ContainSubstring("Grpc dp is not available and falling back to Http"))
		})
	})

	ginkgo.It("[flaky] should store data with istio injected at the last index", func() {
		testGCSFuseWithIstio("", false, false, false)
	})

	ginkgo.It("should store data with istio registry only outbound traffic policy mode", func() {
		testGCSFuseWithIstio("", true, true, false)
	})
	ginkgo.It("[metadata prefetch] should store data with istio registry only outbound traffic policy mode", func() {
		if !supportsNativeSidecar {
			e2eskipper.Skipf("skip when native sidecar is not supported")
		}
		testGCSFuseWithIstio(specs.EnableMetadataPrefetchPrefix, true, true, false)
	})

	// These tests inject Istio without native sidecars, so that istio-init runs after gke-gcsfuse-sidecar.
	ginkgo.It("should store data with non-native istio sidecar", func() {
		testGCSFuseWithIstio("", true, false, true)
	})
	ginkgo.It("should store data with non-native istio sidecar in registry only outbound traffic policy mode", func() {
		testGCSFuseWithIstio("", true, true, true)
	})

	// This test covers the case where gRPC is enabled by default, istio-init runs after gke-gcsfuse-sidecar, and the
	// Pod does not exclude DirectPath traffic from Istio redirection. The driver then passes
	// enable-grpc-by-default=false to gcsfuse.
	ginkgo.It("should pass --enable-grpc-by-default=false from driver to gcsfuse and store data with non-native istio sidecar", func() {
		if getClientProtocol(driver) != "grpc" {
			e2eskipper.Skipf("skip when client-protocol is not grpc")
		}
		if zbEnabled(driver) {
			e2eskipper.Skipf("skip with Zonal Buckets, where the Pod excludes DirectPath traffic from Istio redirection")
		}
		if !supportsNativeSidecar {
			e2eskipper.Skipf("skip when native sidecar is not supported")
		}
		skipIfEnableGrpcByDefaultNotSupported()
		testGCSFuseWithIstio(specs.EnableGrpcByDefaultPrefix, true, false, true, func(tPod *specs.TestPod) {
			ginkgo.By("Checking that the driver passed --enable-grpc-by-default=false to gcsfuse")
			stdout, err := tPod.GetSidecarLogs()
			framework.ExpectNoError(err)
			gomega.Expect(stdout).To(gomega.ContainSubstring("--enable-grpc-by-default=false"))
			gomega.Expect(stdout).NotTo(gomega.ContainSubstring("--enable-grpc-by-default=true"))
		})
	})

	ginkgo.It("should pass --enable-grpc-by-default=true from driver to gcsfuse and store data with non-native istio sidecar when DirectPath exclusions are set", func() {
		if getClientProtocol(driver) != "grpc" {
			e2eskipper.Skipf("skip when client-protocol is not grpc")
		}
		if !supportsNativeSidecar {
			e2eskipper.Skipf("skip when native sidecar is not supported")
		}
		skipIfEnableGrpcByDefaultNotSupported()
		runIstioTest(specs.EnableGrpcByDefaultPrefix, true, true, true, true, func(tPod *specs.TestPod) {
			ginkgo.By("Checking that the driver passed --enable-grpc-by-default=true to gcsfuse")
			stdout, err := tPod.GetSidecarLogs()
			framework.ExpectNoError(err)
			gomega.Expect(stdout).To(gomega.ContainSubstring("--enable-grpc-by-default=true"))
			gomega.Expect(stdout).NotTo(gomega.ContainSubstring("--enable-grpc-by-default=false"))
		})
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
