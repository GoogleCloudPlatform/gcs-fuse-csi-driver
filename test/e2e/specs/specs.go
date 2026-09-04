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

package specs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"local/test/e2e/utils"

	driver "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_driver"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubernetes/pkg/kubelet/events"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eevents "k8s.io/kubernetes/test/e2e/framework/events"
	e2ejob "k8s.io/kubernetes/test/e2e/framework/job"
	e2ekubectl "k8s.io/kubernetes/test/e2e/framework/kubectl"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	e2epodooutput "k8s.io/kubernetes/test/e2e/framework/pod/output"
	e2epv "k8s.io/kubernetes/test/e2e/framework/pv"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"k8s.io/utils/ptr"
)

const (
	SharedMountTag                = "shared-mount"
	TesterContainerName           = "volume-tester"
	K8sServiceAccountName         = "gcsfuse-csi-sa"
	DefaultMounterPodTemplateName = "default-mounter-pod-template"
	//nolint:gosec
	K8sSecretName                                              = "gcsfuse-csi-test-secret"
	FakeVolumePrefix                                           = "gcsfuse-csi-fake-volume"
	InvalidVolumePrefix                                        = "gcsfuse-csi-invalid-volume"
	NonRootVolumePrefix                                        = "gcsfuse-csi-non-root-volume"
	InvalidMountOptionsVolumePrefix                            = "gcsfuse-csi-invalid-mount-options-volume"
	InvalidBoolMountOptionsVolumePrefix                        = "gcsfuse-csi-invalid-bool-mount-options-volume"
	ImplicitDirsVolumePrefix                                   = "gcsfuse-csi-implicit-dirs-volume"
	ForceNewBucketPrefix                                       = "gcsfuse-csi-force-new-bucket"
	SubfolderInBucketPrefix                                    = "gcsfuse-csi-subfolder-in-bucket"
	MultipleBucketsPrefix                                      = "gcsfuse-csi-multiple-buckets"
	BucketWithTwoUniqueVolSuffixPrefix                         = "gcsfuse-csi-bucket-with-two-unique-vol-suffix"
	EnableFileCacheForceNewBucketPrefix                        = "gcsfuse-csi-enable-file-cache-force-new-bucket"
	EnableFileCacheForceNewBucketAndMetricsPrefix              = "gcsfuse-csi-enable-file-cache-force-new-bucket-and-metrics"
	EnableFileCachePrefix                                      = "gcsfuse-csi-enable-file-cache"
	ProfilesOverrideAllOverridablePrefix                       = "gcsfuse-csi-profiles-override-all-overridable"
	ProfilesControllerCrashTestPrefix                          = "gcsfuse-csi-profiles-controller-crash-test"
	EnableFileCacheAndMetricsPrefix                            = "gcsfuse-csi-enable-file-cache-and-metrics"
	EnableFileCacheWithLargeCapacityPrefix                     = "gcsfuse-csi-enable-file-cache-large-capacity"
	EnableMetadataPrefetchPrefix                               = "gcsfuse-csi-enable-metadata-prefetch"
	EnableHostNetworkPrefix                                    = "gcsfuse-csi-enable-hostnetwork"
	EnableCustomReadAhead                                      = "gcsfuse-csi-enable-custom-read-ahead"
	EnableMetadataPrefetchAndFakeVolumePrefix                  = "gcsfuse-csi-enable-metadata-prefetch-and-fake-volume"
	EnableMetadataPrefetchPrefixForceNewBucketPrefix           = "gcsfuse-csi-enable-metadata-prefetch-and-force-new-bucket"
	EnableMetadataPrefetchAndInvalidMountOptionsVolumePrefix   = "gcsfuse-csi-enable-metadata-prefetch-and-invalid-mount-options-volume"
	ImplicitDirsPath                                           = "implicit-dir"
	OptInHnwKSAPrefix                                          = "opt-in-hnw-ksa"
	SkipCSIBucketAccessCheckPrefix                             = "gcsfuse-csi-skip-bucket-access-check"
	SkipCSIBucketAccessCheckAndFakeVolumePrefix                = "gcsfuse-csi-skip-bucket-access-check-fake-volume"
	SkipCSIBucketAccessCheckAndInvalidVolumePrefix             = "gcsfuse-csi-skip-bucket-access-check-invalid-volume"
	SkipCSIBucketAccessCheckAndInvalidMountOptionsVolumePrefix = "gcsfuse-csi-skip-bucket-access-check-invalid-mount-options-volume"
	SkipCSIBucketAccessCheckAndNonRootVolumePrefix             = "gcsfuse-csi-skip-bucket-access-check-non-root-volume"
	SkipCSIBucketAccessCheckAndImplicitDirsVolumePrefix        = "gcsfuse-csi-skip-bucket-access-check-implicit-dirs-volume"
	EnableKernelParamsPrefix                                   = "gcsfuse-csi-enable-kernel-params"
	SidecarAndSharedMountCoexistencePrefix                     = "gcsfuse-csi-sidecar-and-shared-mount-coexistence"
	SharedDynamicMountPrefix                                   = "gcsfuse-csi-shared-dynamic-mount"
	SharedMountCloudProfilerPrefix                             = "gcsfuse-csi-shared-mount-cloud-profiler"
	SharedMountCloudProfilerDisabledGCSFusePrefix              = "gcsfuse-csi-shared-mount-cloud-profiler-disabled-gcsfuse"

	expectedTrainingProfileFlag   = "--profile=aiml-training"
	expectedTrainingProfileConfig = `map\[.*profile:aiml-training.*\]`

	// Read ahead config custom settings to verify testing.
	ReadAheadCustomReadAheadKb = "15360"
	ReadAheadCustomMaxRatio    = "100"

	DisableAutoconfig           = "disable-autoconfig"
	PassProfilesToSidecarPrefix = "pass-profiles-to-sidecar"

	GoogleCloudCliImage = "gcr.io/google.com/cloudsdktool/google-cloud-cli:slim"
	GolangImage         = "golang:1.22.7"
	UbuntuImage         = "ubuntu:20.04"

	LastPublishedSidecarContainerImage           = "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.7.1-gke.3@sha256:380bd2a716b936d9469d09e3a83baf22dddca1586a04a0060d7006ea78930cac"
	LastPublishedMounterPodSidecarContainerImage = "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.24.4-gke.2@sha256:3ac0c4db17c456192810771351a3cd39f5e861211ebcf1ee8a74fd8ae923bc42"

	pollInterval     = 1 * time.Second
	pollTimeout      = 1 * time.Minute
	pollIntervalSlow = 10 * time.Second
	pollTimeoutSlow  = 20 * time.Minute

	backoffDuration = 5 * time.Second
	backoffFactor   = 2.0
	backoffCap      = 2 * time.Minute
	backoffSteps    = 8
	backoffJitter   = 0.1

	driverNamespaceOSS     = "gcs-fuse-csi-driver"
	driverNamespaceManaged = "kube-system"
	driverContainer        = "gcs-fuse-csi-driver"
	driverDaemonsetLabel   = "k8s-app=gcs-fuse-csi-driver"

	IsOSSEnvVar = "IS_OSS"
)

var InvalidVolume = fmt.Sprintf("non-existent-test-bucket-%s", rand.String(8))

// Note to developers adding new testing methods - Please check the code path of newly added methods and ensure that those requiring
// konnectivity agents are wrapped with retry logic, see `runKubectlWithFullOutputWithRetry` as an example.
// See here for the list of commands that require the agents - go/konnectivity-network-proxy#egress_traffic.

type MounterPodTemplateOptions struct {
	Name               string
	ServiceAccountName string
	FSGroup            *int64
	Resources          *corev1.ResourceRequirements
	Image              string
	Volumes            []corev1.Volume
	DNSPolicy          corev1.DNSPolicy
}

func CreateMounterPodTemplate(ctx context.Context, c clientset.Interface, namespace string, opts MounterPodTemplateOptions) (*corev1.PodTemplate, error) {
	image := opts.Image
	if image == "" {
		image = driver.MounterPodManagedImageKeyword
	}
	saName := opts.ServiceAccountName
	if saName == "" {
		saName = K8sServiceAccountName
	}
	podSpec := corev1.PodSpec{
		ServiceAccountName: saName,
		Containers: []corev1.Container{
			{
				Name:  util.MounterPodNamePrefix,
				Image: image,
			},
		},
	}
	if len(opts.Volumes) > 0 {
		podSpec.Volumes = opts.Volumes
	}
	if opts.Resources != nil {
		podSpec.Containers[0].Resources = *opts.Resources
	}
	if opts.FSGroup != nil {
		podSpec.SecurityContext = &corev1.PodSecurityContext{
			FSGroup: opts.FSGroup,
		}
	}
	if opts.DNSPolicy != "" {
		podSpec.DNSPolicy = opts.DNSPolicy
	}
	template := &corev1.PodTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: namespace,
		},
		Template: corev1.PodTemplateSpec{
			Spec: podSpec,
		},
	}
	return c.CoreV1().PodTemplates(namespace).Create(ctx, template, metav1.CreateOptions{})
}

// VerifyMounterPods verifies the expected count of Mounter Pods scheduled on expectedNodeName in the namespace.
func VerifyMounterPods(ctx context.Context, c clientset.Interface, namespace string, expectedCount int, expectedNodeName string) *corev1.PodList {
	if expectedNodeName == "" {
		framework.Failf("expectedNodeName must be provided to verify Mounter Pods")
	}
	listOpts := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", webhook.SharedMountLabel, util.TrueStr),
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", expectedNodeName),
	}

	mounterPods, err := c.CoreV1().Pods(namespace).List(ctx, listOpts)
	framework.ExpectNoError(err, "failed to list mounter pods")
	gomega.Expect(mounterPods.Items).To(gomega.HaveLen(expectedCount), fmt.Sprintf("expected %d Mounter Pod(s) on node %s", expectedCount, expectedNodeName))
	return mounterPods
}

// GetMounterPod returns the Mounter Pod scheduled on expectedNodeName in the namespace.
func GetMounterPod(ctx context.Context, c clientset.Interface, namespace string, expectedNodeName string) *corev1.Pod {
	if expectedNodeName == "" {
		framework.Failf("expectedNodeName must be provided to get Mounter Pod")
	}
	listOpts := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", webhook.SharedMountLabel, util.TrueStr),
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", expectedNodeName),
	}

	mounterPods, err := c.CoreV1().Pods(namespace).List(ctx, listOpts)
	framework.ExpectNoError(err, "failed to list mounter pods")
	gomega.Expect(mounterPods.Items).To(gomega.HaveLen(1), fmt.Sprintf("expected 1 Mounter Pod on node %s", expectedNodeName))
	return &mounterPods.Items[0]
}

type TestPod struct {
	client                   clientset.Interface
	pod                      *corev1.Pod
	namespace                *corev1.Namespace
	inplacePodRestartEnabled bool
}

func NewTestPodModifiedSpec(c clientset.Interface, ns *corev1.Namespace, setAutomountServiceAccountToken bool) *TestPod {
	testpod := NewTestPod(c, ns)
	testpod.pod.Spec.AutomountServiceAccountToken = ptr.To(setAutomountServiceAccountToken)
	return testpod
}

func NewTestPod(c clientset.Interface, ns *corev1.Namespace) *TestPod {
	cpu, _ := resource.ParseQuantity("100m")
	mem, _ := resource.ParseQuantity("20Mi")

	return &TestPod{
		client:    c,
		namespace: ns,
		pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "gcsfuse-volume-tester-",
				Annotations: map[string]string{
					"gke-gcsfuse/volumes":                   "true",
					"gke-gcsfuse/cpu-limit":                 "0",
					"gke-gcsfuse/memory-limit":              "0",
					"gke-gcsfuse/ephemeral-storage-limit":   "0",
					"gke-gcsfuse/cpu-request":               "100m",
					"gke-gcsfuse/memory-request":            "100Mi",
					"gke-gcsfuse/ephemeral-storage-request": "100Mi",
				},
				Labels: map[string]string{},
			},
			Spec: corev1.PodSpec{
				TerminationGracePeriodSeconds: ptr.To(int64(5)),
				NodeSelector:                  map[string]string{"kubernetes.io/os": "linux"},
				ServiceAccountName:            K8sServiceAccountName,
				Containers: []corev1.Container{
					{
						Name:         TesterContainerName,
						Image:        imageutils.GetE2EImage(imageutils.BusyBox),
						Command:      []string{"/bin/sh"},
						Args:         []string{"-c", "tail -f /dev/null"},
						VolumeMounts: make([]corev1.VolumeMount, 0),
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    cpu,
								corev1.ResourceMemory: mem,
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    cpu,
								corev1.ResourceMemory: mem,
							},
						},
						Env: []corev1.EnvVar{
							{
								Name: "POD_NAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
								},
							},
						},
					},
				},
				RestartPolicy:                corev1.RestartPolicyAlways,
				Volumes:                      make([]corev1.Volume, 0),
				AutomountServiceAccountToken: ptr.To(false),
				Tolerations: []corev1.Toleration{
					{Operator: corev1.TolerationOpExists},
				},
			},
		},
	}
}

// InplacePodRestart enables the Kubernetes 1.35+ in-place pod restart feature (RestartAllContainers on exit code 88).
func (t *TestPod) InplacePodRestart() {
	framework.Logf("Enabling in-place pod restart (RestartAllContainers)")
	t.inplacePodRestartEnabled = true
}

func (t *TestPod) Create(ctx context.Context) {
	framework.Logf("Creating Pod %s", t.pod.Name)
	if !t.inplacePodRestartEnabled {
		var err error
		t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Create(ctx, t.pod, metav1.CreateOptions{})
		framework.ExpectNoError(err)
		return
	}

	// TODO(k8s-1.35): When k8s.io/api and client-go in test/go.mod are upgraded to >= v0.35.0,
	// replace this raw JSON injection with typed corev1.Container.RestartPolicyRules fields.
	// In-place pod restart was introduced in Kubernetes 1.35+.
	// Because this repository's vendored k8s.io/api and client-go dependencies are pinned to v0.33.3 (K8s 1.33),
	// the typed Go structs lack Container.RestartPolicyRules and container-level RestartPolicy fields.
	// To enable the feature without requiring a repo-wide Kubernetes dependency upgrade, we serialize the
	// pod to a JSON map, inject restartPolicy: "Always" and restartPolicyRules directly into the containers,
	// and submit the raw JSON payload to the API server via the RESTClient.
	podBytes, err := json.Marshal(t.pod)
	framework.ExpectNoError(err, "failed to marshal pod to json")

	var podMap map[string]any
	err = json.Unmarshal(podBytes, &podMap)
	framework.ExpectNoError(err, "failed to unmarshal pod json to map")

	// When creating a Pod via raw JSON using RESTClient, the API server and admission
	// webhooks strictly require apiVersion and kind. Since NewTestPod does not populate
	// TypeMeta, explicitly inject them here.
	podMap["apiVersion"] = "v1"
	podMap["kind"] = "Pod"

	if spec, ok := podMap["spec"].(map[string]any); ok {
		if containers, ok := spec["containers"].([]any); ok {
			for _, c := range containers {
				if containerMap, ok := c.(map[string]any); ok {
					containerMap["restartPolicy"] = "Always"
					containerMap["restartPolicyRules"] = []any{
						map[string]any{
							"action": "RestartAllContainers",
							"exitCodes": map[string]any{
								"operator": "In",
								"values":   []int{88},
							},
						},
					}
				}
			}
		}
	}

	updatedBytes, err := json.Marshal(podMap)
	framework.ExpectNoError(err, "failed to marshal updated pod map")

	result := &corev1.Pod{}
	err = t.client.CoreV1().RESTClient().Post().
		Namespace(t.namespace.Name).
		Resource("pods").
		VersionedParams(&metav1.CreateOptions{}, scheme.ParameterCodec).
		SetHeader("Content-Type", "application/json").
		Body(updatedBytes).
		Do(ctx).
		Into(result)
	framework.ExpectNoError(err, "failed to create pod with in-place restart rules")
	t.pod = result
}

func (t *TestPod) CreateExpectError(ctx context.Context) {
	framework.Logf("Creating Pod %s", t.pod.Name)
	var err error
	t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Create(ctx, t.pod, metav1.CreateOptions{})
	gomega.Expect(err).Should(gomega.HaveOccurred())
}

// CreateExpectErrorContaining attempts to create the pod and asserts that creation fails with an error message containing expectedSubstring.
func (t *TestPod) CreateExpectErrorContaining(ctx context.Context, expectedSubstring string) {
	framework.Logf("Creating Pod %s and expecting error containing %q", t.pod.Name, expectedSubstring)
	_, err := t.client.CoreV1().Pods(t.namespace.Name).Create(ctx, t.pod, metav1.CreateOptions{})
	gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring(expectedSubstring)), "expected pod creation to fail with error containing %q", expectedSubstring)
}

func (t *TestPod) GetPodName() string {
	return t.pod.Name
}

func (t *TestPod) GetPodNamespace() string {
	return t.pod.Namespace
}

func (t *TestPod) GetPodVols() []corev1.Volume {
	return t.pod.Spec.Volumes
}

func (t *TestPod) GetAutoMountServiceAccountToken() bool {
	return *t.pod.Spec.AutomountServiceAccountToken
}

func (t *TestPod) GetUID() types.UID {
	return t.pod.UID
}

// RefreshPod fetches the latest Pod from the API server and updates t.pod.
func (t *TestPod) RefreshPod(ctx context.Context) (*corev1.Pod, error) {
	pod, err := t.client.CoreV1().Pods(t.namespace.Name).Get(ctx, t.pod.Name, metav1.GetOptions{})
	if err == nil {
		t.pod = pod
	}
	return pod, err
}

// GetContainerStatus returns the ContainerStatus for the specified container name from the latest pod status.
func (t *TestPod) GetContainerStatus(ctx context.Context, containerName string) (*corev1.ContainerStatus, error) {
	pod, err := t.RefreshPod(ctx)
	if err != nil {
		return nil, err
	}
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == containerName {
			return &pod.Status.ContainerStatuses[i], nil
		}
	}
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == containerName {
			return &pod.Status.InitContainerStatuses[i], nil
		}
	}
	return nil, fmt.Errorf("container %q not found in pod %s", containerName, t.pod.Name)
}

// WaitForContainerRestart waits for the container in the pod to reach expectedRestartCount and be running.
func (t *TestPod) WaitForContainerRestart(ctx context.Context, containerName string, expectedRestartCount int32) {
	gomega.Eventually(ctx, func(g gomega.Gomega) {
		cs, err := t.GetContainerStatus(ctx, containerName)
		g.Expect(err).NotTo(gomega.HaveOccurred(), fmt.Sprintf("failed to get container %q status in pod %s", containerName, t.pod.Name))
		if err != nil || cs == nil {
			return
		}
		g.Expect(cs.RestartCount).To(gomega.Equal(expectedRestartCount), fmt.Sprintf("expected container %q to have restartCount %d, got %d", containerName, expectedRestartCount, cs.RestartCount))
		g.Expect(cs.State.Running).ToNot(gomega.BeNil(), fmt.Sprintf("expected container %q to be running", containerName))
	}, "2m", "2s").Should(gomega.Succeed())
}

// VerifyExecInPodSucceed verifies shell cmd in target pod succeed.
func (t *TestPod) VerifyExecInPodSucceed(f *framework.Framework, containerName, shExec string) {
	_ = t.VerifyExecInPodSucceedWithOutput(f, containerName, shExec)
}

// VerifyExecInPodSucceedWithOutput verifies shell cmd in target pod succeed.
func (t *TestPod) VerifyExecInPodSucceedWithOutput(f *framework.Framework, containerName, shExec string) string {
	stdout, stderr, err := execCommandInContainerWithFullOutputWithRetry(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	framework.ExpectNoError(err,
		"%q should succeed, but failed with error message %q\nstdout: %s\nstderr: %s",
		shExec, err, stdout, stderr)

	return stdout
}

// VerifyExecInPodSucceedWithFullOutput verifies shell cmd in target pod succeed with full output.
func (t *TestPod) VerifyExecInPodSucceedWithFullOutput(f *framework.Framework, containerName, shExec string) {
	stdout, stderr, err := execCommandInContainerWithFullOutputWithRetry(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	framework.ExpectNoError(err,
		"%q should succeed, but failed with error message %q\nstdout: %s\nstderr: %s",
		shExec, err, stdout, stderr)

	framework.Logf("Output of %q: \n%s", shExec, stdout)
}

// VerifyExecInPodFail verifies shell cmd in target pod fail with certain exit code.
func (t *TestPod) VerifyExecInPodFail(f *framework.Framework, containerName, shExec string, exitCode int) {
	stdout, stderr, err := execCommandInContainerWithFullOutputWithRetry(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	gomega.Expect(err).Should(gomega.HaveOccurred(),
		fmt.Sprintf("%q should fail with exit code %d, but exit without error\nstdout: %s\nstderr: %s", shExec, exitCode, stdout, stderr))
}

// VerifyRWMount verifies that the mount point is mounted read-write in the target pod.
func (t *TestPod) VerifyRWMount(f *framework.Framework, mountPath string) {
	t.VerifyExecInPodSucceed(f, TesterContainerName, fmt.Sprintf("mount | grep %s | grep rw,", mountPath))
}

// VerifyROMount verifies that the mount point is mounted read-only in the target pod.
func (t *TestPod) VerifyROMount(f *framework.Framework, mountPath string) {
	t.VerifyExecInPodSucceed(f, TesterContainerName, fmt.Sprintf("mount | grep %s | grep ro,", mountPath))
}

// VerifyWriteAndReadFile writes content to filePath and verifies that reading it back returns the same content.
func (t *TestPod) VerifyWriteAndReadFile(f *framework.Framework, filePath, content string) {
	escapedContent := strings.ReplaceAll(content, "'", "'\\''")
	// Use `grep -F` for fixed string matching so that a pattern like "foo.bar" does not match "foo-bar" or "foo1bar", because `.` matches any single character in regex.
	t.VerifyExecInPodSucceed(f, TesterContainerName, fmt.Sprintf("echo '%s' > %s && grep -F '%s' %s", escapedContent, filePath, escapedContent, filePath))
}

// VerifyReadFile verifies that reading filePath returns the expected content.
func (t *TestPod) VerifyReadFile(f *framework.Framework, filePath, expectedContent string) {
	escapedContent := strings.ReplaceAll(expectedContent, "'", "'\\''")
	// Use `grep -F` for fixed string matching so that a pattern like "foo.bar" does not match "foo-bar" or "foo1bar", because `.` matches any single character in regex.
	t.VerifyExecInPodSucceed(f, TesterContainerName, fmt.Sprintf("grep -F '%s' %s", escapedContent, filePath))
}

// execCommandInContainerWithFullOutputWithRetry executes a command in a target pod and retries with gradual back until timeout(10 min) or success.
func execCommandInContainerWithFullOutputWithRetry(f *framework.Framework, podName, containerName string, cmd ...string) (string, string, error) {
	return RetryWithBackoffTwoReturnValues(func() (string, string, error) {
		return e2epod.ExecCommandInContainerWithFullOutput(f, podName, containerName, cmd...)
	})
}

// Retry executes a generic operation (op) with exponential backoff.
// T can be any type (string, a struct, a slice, etc).
func Retry[T any](op func() (T, error)) (T, error) {
	backoff := wait.Backoff{
		Duration: backoffDuration,
		Factor:   backoffFactor,
		Cap:      backoffCap,
		Steps:    backoffSteps,
		Jitter:   backoffJitter,
	}

	var result T
	var lastErr error

	wait.ExponentialBackoff(backoff, func() (bool, error) {
		result, lastErr = op()

		if lastErr != nil {
			framework.Logf("Operation failed with error: %v. Retrying...", lastErr)
			return false, nil
		}
		return true, nil
	})

	if lastErr != nil {
		framework.Logf("Operation failed after %d steps (total time/cap: %v). Last error: %v", backoff.Steps, backoff.Cap, lastErr)
	} else {
		framework.Logf("Operation succeeded.")
	}

	return result, lastErr
}

func RetryWithBackoffOneReturnValue(op func() (string, error)) (string, error) {
	return Retry(op)
}

func RetryWithBackoffTwoReturnValues(op func() (string, string, error)) (string, string, error) {
	res, err := Retry(func() ([2]string, error) {
		stdout, stderr, err := op()
		return [2]string{stdout, stderr}, err
	})
	return res[0], res[1], err
}

func (t *TestPod) WaitForRunning(ctx context.Context) {
	err := e2epod.WaitTimeoutForPodRunningInNamespace(ctx, t.client, t.pod.Name, t.pod.Namespace, pollTimeoutSlow)
	framework.ExpectNoError(err)

	t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Get(ctx, t.pod.Name, metav1.GetOptions{})
	framework.ExpectNoError(err)
}

// FindLogsByNewLine scans the log string and returns the line
// containing the given logToFind.
func (t *TestPod) FindLogsByNewLine(logToFind string) (string, error) {
	stdout, stderr, err := t.getDriverLogs()
	framework.ExpectNoError(err,
		"Error accessing logs from pod %v, but failed with error message %q\nstdout: %s\nstderr: %s",
		t.pod.Name, err, stdout, stderr)
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	for scanner.Scan() {
		line := scanner.Text()
		// Identify the line based on the unique message part
		if strings.Contains(line, logToFind) && strings.Contains(line, t.pod.Name) {
			return strings.TrimSpace(line), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scanning logs: %w", err)
	}
	return "", nil
}

func (t *TestPod) WaitForSuccess(ctx context.Context) {
	err := e2epod.WaitForPodSuccessInNamespaceTimeout(ctx, t.client, t.pod.Name, t.pod.Namespace, pollTimeoutSlow)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForFail(ctx context.Context) {
	err := e2epod.WaitForPodFailedReason(ctx, t.client, t.pod, "", pollTimeoutSlow)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForUnschedulable(ctx context.Context) {
	err := e2epod.WaitForPodNameUnschedulableInNamespace(ctx, t.client, t.pod.Name, t.namespace.Name)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForFailedMountError(ctx context.Context, msg string) {
	// e2eevents.WaitTimeoutForEvent will error if the Events.List api call fails, eg a slow api server. So we wrap the wait.
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, pollTimeoutSlow, true, func(context.Context) (bool, error) {
		if eventErr := e2eevents.WaitTimeoutForEvent(
			ctx,
			t.client,
			t.namespace.Name,
			fields.Set{"reason": events.FailedMountVolume}.AsSelector().String(),
			msg,
			pollTimeoutSlow); eventErr != nil {
			framework.Logf("Error fetching event, retrying: %v", eventErr)
			return false, nil
		}
		return true, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForFailedContainerError(ctx context.Context, msg string) {
	// e2eevents.WaitTimeoutForEvent will error if the Events.List api call fails, eg a slow api server. So we wrap the wait.
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, pollTimeoutSlow, true, func(context.Context) (bool, error) {
		if eventErr := e2eevents.WaitTimeoutForEvent(
			ctx,
			t.client,
			t.namespace.Name,
			fields.Set{"reason": events.FailedToStartContainer}.AsSelector().String(),
			msg,
			pollTimeoutSlow); eventErr != nil {
			framework.Logf("Error fetching event, retrying: %v", eventErr)
			return false, nil
		}
		return true, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForPodNotFoundInNamespace(ctx context.Context) {
	err := e2epod.WaitForPodNotFoundInNamespace(ctx, t.client, t.pod.Name, t.namespace.Name, pollTimeout)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForLog(ctx context.Context, container string, expectedString string) {
	_, err := lookForStringInLogWithoutKubectlWithRetry(ctx, t.client, t.namespace.Name, t.pod.Name, container, expectedString, pollTimeout)
	framework.ExpectNoError(err)
}

// WaitForContainerRunning waits until the named container within the pod is in Running state.
// Checks both regular and init container statuses to support native sidecar deployments.
// Use this before WaitForLog when only a specific container (e.g. the gcsfuse sidecar) is
// expected to start, without waiting for all pod containers to be ready.
func (t *TestPod) WaitForContainerRunning(ctx context.Context, containerName string) {
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, pollTimeoutSlow, true, func(ctx context.Context) (bool, error) {
		pod, err := t.client.CoreV1().Pods(t.namespace.Name).Get(ctx, t.pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		allStatuses := append(pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses...)
		for _, cs := range allStatuses {
			if cs.Name == containerName && cs.State.Running != nil {
				return true, nil
			}
		}
		return false, nil
	})
	framework.ExpectNoError(err)
}

func lookForStringInLogWithoutKubectlWithRetry(ctx context.Context, client clientset.Interface, namespace, podName, container, expectedString string, timeout time.Duration) (string, error) {
	return RetryWithBackoffOneReturnValue(func() (string, error) {
		return e2epodooutput.LookForStringInLogWithoutKubectl(ctx, client, namespace, podName, container, expectedString, timeout)
	})
}

// WaitForMounterPodLog waits for the expected string to appear in the Mounter Pod's container logs.
func WaitForMounterPodLog(ctx context.Context, client clientset.Interface, namespace, podName, expectedString string) {
	_, err := lookForStringInLogWithoutKubectlWithRetry(ctx, client, namespace, podName, util.MounterPodNamePrefix, expectedString, pollTimeout)
	framework.ExpectNoError(err)
}

// GetMounterPodLogs retrieves the full logs from the Mounter Pod's container.
func GetMounterPodLogs(namespace, podName string) (string, error) {
	stdout, stderr, err := runKubectlWithFullOutputWithRetry(namespace, "logs", podName, "-c", util.MounterPodNamePrefix)
	if err != nil {
		return stdout, fmt.Errorf("failed to get mounter pod logs: %w; stderr: %s", err, stderr)
	}
	return stdout, nil
}

func (t *TestPod) CheckSidecarNeverTerminatedAfterAWhile(ctx context.Context, isNativeSidecar bool) {
	time.Sleep(pollTimeout)

	var err error
	t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Get(ctx, t.pod.Name, metav1.GetOptions{})
	framework.ExpectNoError(err)

	var containerStatusList []corev1.ContainerStatus
	if isNativeSidecar {
		containerStatusList = t.pod.Status.InitContainerStatuses
	} else {
		containerStatusList = t.pod.Status.ContainerStatuses
	}

	var sidecarContainerStatus corev1.ContainerStatus
	for _, cs := range containerStatusList {
		if cs.Name == webhook.GcsFuseSidecarName {
			sidecarContainerStatus = cs

			break
		}
	}

	gomega.Expect(sidecarContainerStatus).ToNot(gomega.BeNil())
	gomega.Expect(sidecarContainerStatus.RestartCount).To(gomega.Equal(int32(0)))
	gomega.Expect(sidecarContainerStatus.State.Running).ToNot(gomega.BeNil())
}

func (t *TestPod) SetupVolumeForInitContainer(name, mountPath string, readOnly bool, subPath string) {
	t.setupVolumeMount(name, mountPath, readOnly, subPath, true)
}

func (t *TestPod) SetupVolumeForHNS(name string) {
	// Remove the --rename-dir-limit and --implicit-dirs flags from the mount options.
	for _, volume := range t.pod.Spec.Volumes {
		if volume.Name == name && volume.VolumeSource.CSI != nil {
			mountOptions := volume.VolumeSource.CSI.VolumeAttributes["mountOptions"]
			mountOptionsList := strings.Split(mountOptions, ",")

			newMountOptions := []string{}
			for _, mo := range mountOptionsList {
				if !strings.Contains(mo, "rename-dir-limit") && !strings.Contains(mo, "implicit-dirs") {
					newMountOptions = append(newMountOptions, mo)
				}
			}

			volume.VolumeSource.CSI.VolumeAttributes["mountOptions"] = strings.Join(newMountOptions, ",")

			break
		}
	}
}

func (t *TestPod) SetupVolume(volumeResource *storageframework.VolumeResource, name, mountPath string, readOnly bool, mountOptions ...string) {
	t.setupVolume(volumeResource, name, readOnly, mountOptions...)
	t.setupVolumeMount(name, mountPath, readOnly, "", false)
}

// OverrideGCSFuseCache overrides the `gke-gcsfuse-cache` with dummy values, needed for certain workflows in profiles.
func (t *TestPod) OverrideGCSFuseCache() {
	sizeLimit := resource.MustParse("115Mi")
	t.pod.Spec.Volumes = append(t.pod.Spec.Volumes, corev1.Volume{
		Name: webhook.SidecarContainerCacheVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium:    "",
				SizeLimit: &sizeLimit,
			},
		},
	})
	if t.pod.Spec.SecurityContext == nil {
		t.pod.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: ptr.To(int64(1000))}
	} else {
		*t.pod.Spec.SecurityContext.FSGroup = 1000
	}

}

func (t *TestPod) SetupVolumeWithSubPath(volumeResource *storageframework.VolumeResource, name, mountPath string, readOnly bool, subPath string, reuseMount bool, mountOptions ...string) {
	if !reuseMount {
		t.setupVolume(volumeResource, name, readOnly, mountOptions...)
	}

	t.setupVolumeMount(name, mountPath, readOnly, subPath, false)
}

func (t *TestPod) setupVolumeMount(name, mountPath string, readOnly bool, subPath string, isNativeSidecar bool) {
	if name == webhook.SidecarContainerBufferVolumeName || name == webhook.SidecarContainerCacheVolumeName {
		return
	}

	volumeMount := corev1.VolumeMount{
		Name:      name,
		MountPath: mountPath,
		ReadOnly:  readOnly,
		SubPath:   subPath,
	}

	if isNativeSidecar {
		t.pod.Spec.InitContainers[0].VolumeMounts = append(t.pod.Spec.InitContainers[0].VolumeMounts, volumeMount)
	} else {
		t.pod.Spec.Containers[0].VolumeMounts = append(t.pod.Spec.Containers[0].VolumeMounts, volumeMount)
	}
}

// CreateVolumeResource wraps storageframework.CreateVolumeResource for E2E tests.
// For shared-mount PreprovisionedPV, it creates the PV with an explicit name instead of
// GenerateName, because upstream's GenerateName leaves pv.Name empty during webhook CREATE
// admission, preventing the webhook from populating csi.storage.k8s.io/pv/name.
func CreateVolumeResource(ctx context.Context, driver storageframework.TestDriver, config *storageframework.PerTestConfig, pattern storageframework.TestPattern, testVolumeSizeRange e2evolume.SizeRange, customVolumeHandleSuffix ...string) *storageframework.VolumeResource {
	gcsDriver, ok := driver.(*GCSFuseCSITestDriver)
	if !ok || !gcsDriver.EnableSharedMount || pattern.VolType != storageframework.PreprovisionedPV {
		return storageframework.CreateVolumeResource(ctx, driver, config, pattern, testVolumeSizeRange)
	}

	r := &storageframework.VolumeResource{
		Config:  config,
		Pattern: pattern,
	}
	f := config.Framework
	cs := f.ClientSet

	r.Volume = storageframework.CreateVolume(ctx, driver, config, pattern.VolType)
	pDriver, ok := driver.(storageframework.PreprovisionedPVTestDriver)
	if !ok {
		framework.Failf("Driver %s does not implement PreprovisionedPVTestDriver", driver.GetDriverInfo().Name)
	}

	pvSource, volumeNodeAffinity := pDriver.GetPersistentVolumeSource(false /* readOnly */, pattern.FsType, r.Volume)
	if pvSource == nil {
		framework.Failf("Failed to get PersistentVolumeSource for volume")
	}

	if len(customVolumeHandleSuffix) > 0 && customVolumeHandleSuffix[0] != "" {
		if pvSource.CSI == nil || pvSource.CSI.VolumeHandle == "" {
			framework.Failf("Failed to apply custom volume handle suffix")
		}
		pvSource.CSI.VolumeHandle += customVolumeHandleSuffix[0]
	}

	pvName := fmt.Sprintf("gcsfuse-shared-pv-%s", rand.String(8))
	pvcName := fmt.Sprintf("pvc-%s", rand.String(8))

	accessModes := driver.GetDriverInfo().RequiredAccessModes
	if len(accessModes) == 0 {
		accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	}

	var success bool
	defer func() {
		if !success {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = r.CleanupResource(cleanupCtx)
		}
	}()

	// 1. Create PVC
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: f.Namespace.Name,
			Annotations: map[string]string{
				webhook.MounterPodTemplateAnnotation: DefaultMounterPodTemplateName,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("2Gi"),
				},
			},
			StorageClassName: ptr.To(""),
		},
	}
	if pattern.VolMode != "" {
		pvc.Spec.VolumeMode = &pattern.VolMode
	}

	var err error
	r.Pvc, err = cs.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Create(ctx, pvc, metav1.CreateOptions{})
	framework.ExpectNoError(err, "failed to create PVC %s", pvcName)

	// 2. Create PV with explicit Name (not GenerateName)
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("2Gi"),
			},
			PersistentVolumeSource: *pvSource,
			AccessModes:            accessModes,
			ClaimRef: &corev1.ObjectReference{
				Kind:       "PersistentVolumeClaim",
				APIVersion: "v1",
				Name:       r.Pvc.Name,
				Namespace:  r.Pvc.Namespace,
				UID:        r.Pvc.UID,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "",
			NodeAffinity:                  volumeNodeAffinity,
		},
	}
	if pattern.VolMode != "" {
		pv.Spec.VolumeMode = &pattern.VolMode
	}

	r.Pv, err = cs.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
	framework.ExpectNoError(err, "failed to create PV %s", pvName)

	// 3. Wait for PV and PVC to be Bound
	err = e2epv.WaitOnPVandPVC(ctx, cs, f.Timeouts, f.Namespace.Name, r.Pv, r.Pvc)
	framework.ExpectNoError(err, "PVC, PV failed to bind")

	r.VolSource = &corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: r.Pvc.Name,
			ReadOnly:  false,
		},
	}

	success = true
	return r
}

// ensureMounterPodTemplateAnnotation ensures that when shared node mount is enabled,
// the PVC references a mounter PodTemplate via the gke-gcsfuse/mounter-pod-template annotation.
// If absent, it defaults to the default mounter PodTemplate provisioned during PrepareTest.
func (t *TestPod) ensureMounterPodTemplateAnnotation(volumeResource *storageframework.VolumeResource) {
	if volumeResource == nil || volumeResource.Pvc == nil || volumeResource.Pv == nil || volumeResource.Pv.Spec.CSI == nil {
		return
	}
	if volumeResource.Pv.Spec.CSI.VolumeAttributes[driver.VolumeContextSharedNodeMount] != util.TrueStr {
		return
	}

	pvc := volumeResource.Pvc
	if pvc.Annotations != nil && pvc.Annotations[webhook.MounterPodTemplateAnnotation] != "" {
		return
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := t.client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Get(context.Background(), pvc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if latest.Annotations == nil {
			latest.Annotations = make(map[string]string)
		}
		if latest.Annotations[webhook.MounterPodTemplateAnnotation] == "" {
			latest.Annotations[webhook.MounterPodTemplateAnnotation] = DefaultMounterPodTemplateName
			updated, err := t.client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Update(context.Background(), latest, metav1.UpdateOptions{})
			if err == nil {
				volumeResource.Pvc = updated
			}
			return err
		}
		return nil
	})
	framework.ExpectNoError(err, "failed to update PVC with mounter pod template annotation")
}

func (t *TestPod) setupVolume(volumeResource *storageframework.VolumeResource, name string, readOnly bool, mountOptions ...string) {
	volume := corev1.Volume{
		Name: name,
	}
	if volumeResource.Pvc != nil {
		t.ensureMounterPodTemplateAnnotation(volumeResource)
		volume.VolumeSource = corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: volumeResource.Pvc.Name,
				ReadOnly:  readOnly,
			},
		}
	} else if volumeResource.VolSource != nil {
		volume.VolumeSource = *volumeResource.VolSource
		if volumeResource.VolSource.CSI != nil {
			volume.VolumeSource.CSI.ReadOnly = &readOnly
			if len(mountOptions) > 0 {
				volume.VolumeSource.CSI.VolumeAttributes["mountOptions"] += "," + strings.Join(mountOptions, ",")
			}
		}
	}

	t.pod.Spec.Volumes = append(t.pod.Spec.Volumes, volume)
}

func (t *TestPod) SetupVolumeWithHostNetworkKSAOptIn(volumeResource *storageframework.VolumeResource, name string, mountPath string, readOnly bool, mountOptions ...string) {
	volume := corev1.Volume{
		Name: name,
	}
	if volumeResource.Pvc != nil {
		t.ensureMounterPodTemplateAnnotation(volumeResource)
		volume.VolumeSource = corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: volumeResource.Pvc.Name,
			},
		}
	} else if volumeResource.VolSource != nil {
		volume.VolumeSource = *volumeResource.VolSource
		if volumeResource.VolSource.CSI != nil {
			volume.VolumeSource.CSI.VolumeAttributes["hostNetworkPodKSA"] = "true"
			volume.VolumeSource.CSI.ReadOnly = &readOnly
			if len(mountOptions) > 0 {
				volume.VolumeSource.CSI.VolumeAttributes["mountOptions"] += "," + strings.Join(mountOptions, ",")
			}
		}
	}

	t.pod.Spec.Volumes = append(t.pod.Spec.Volumes, volume)
	t.setupVolumeMount(name, mountPath, readOnly, "", false)
}

func (t *TestPod) SetupCacheVolumeMount(mountPath string, subPath ...string) {
	volumeMount := corev1.VolumeMount{
		Name:      webhook.SidecarContainerCacheVolumeName,
		MountPath: mountPath,
	}

	if len(subPath) != 0 {
		volumeMount.SubPath = subPath[0]
	}

	t.pod.Spec.Containers[0].VolumeMounts = append(t.pod.Spec.Containers[0].VolumeMounts, volumeMount)
}

func (t *TestPod) SetupTmpVolumeMount(mountPath string) {
	volumeMount := corev1.VolumeMount{
		Name:      util.SidecarContainerTmpVolumeName,
		MountPath: mountPath,
	}
	t.pod.Spec.Containers[0].VolumeMounts = append(t.pod.Spec.Containers[0].VolumeMounts, volumeMount)
}

func (t *TestPod) SetName(name string) {
	t.pod.Name = name
}

func (t *TestPod) GetNode() string {
	return t.pod.Spec.NodeName
}

func (t *TestPod) GetMachineType(ctx context.Context) (string, error) {
	nodeName := t.GetNode()
	node, err := t.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("error getting test node %v's machine type ", nodeName)
	}
	return node.ObjectMeta.Labels["node.kubernetes.io/instance-type"], nil
}

func (t *TestPod) GetCSIDriverNodePodIP(ctx context.Context) string {
	node := t.GetNode()
	daemonSetLabelSelector := "k8s-app=gcs-fuse-csi-driver"

	pods, err := t.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node,
		LabelSelector: daemonSetLabelSelector,
	})
	framework.ExpectNoError(err)
	gomega.Expect(pods.Items).To(gomega.HaveLen(1))

	pod := pods.Items[0]
	gomega.Expect(pod.Status).ToNot(gomega.BeNil())

	return pod.Status.PodIP
}

func (t *TestPod) SetNodeAffinity(nodeName string, sameNode bool) {
	gomega.Expect(nodeName).ToNot(gomega.Equal(""))

	ns := &e2epod.NodeSelection{}
	if sameNode {
		e2epod.SetAffinity(ns, nodeName)
	} else {
		e2epod.SetAntiAffinity(ns, nodeName)
	}
	t.pod.Spec.Affinity = ns.Affinity
}

func (t *TestPod) SetNodeSelector(nodeSelector map[string]string) {
	t.pod.Spec.NodeSelector = nodeSelector
}

func (t *TestPod) SetAnnotations(annotations map[string]string) {
	for k, v := range annotations {
		t.pod.Annotations[k] = v
	}
}

func (t *TestPod) SetLabels(labels map[string]string) {
	for k, v := range labels {
		t.pod.Labels[k] = v
	}
}

func (t *TestPod) SetServiceAccount(sa string) {
	t.pod.Spec.ServiceAccountName = sa
}

func (t *TestPod) SetNonRootSecurityContext(uid, gid, fsgroup int) {
	psc := &corev1.PodSecurityContext{}
	if uid != 0 {
		psc.RunAsNonRoot = ptr.To(true)
		psc.RunAsUser = ptr.To(int64(uid))
	}
	if gid != 0 {
		psc.RunAsGroup = ptr.To(int64(gid))
	}
	if fsgroup != 0 {
		psc.FSGroup = ptr.To(int64(fsgroup))
	}

	t.pod.Spec.SecurityContext = psc
}

func (t *TestPod) SetCommand(cmd string) {
	t.pod.Spec.Containers[0].Args = []string{"-c", cmd}
}

func (t *TestPod) SetGracePeriod(s int) {
	t.pod.Spec.TerminationGracePeriodSeconds = ptr.To(int64(s))
}

func (t *TestPod) SetPod(pod *corev1.Pod) {
	t.pod = pod
}

func (t *TestPod) SetRestartPolicy(rp corev1.RestartPolicy) {
	t.pod.Spec.RestartPolicy = rp
}

func (t *TestPod) SetImage(image string) {
	t.pod.Spec.Containers[0].Image = image
}

func (t *TestPod) EnableHostNetwork() {
	t.pod.Spec.HostNetwork = true
}

func (t *TestPod) SetResource(cpuLimit, memoryLimit, storageLimit string) {
	cpu, _ := resource.ParseQuantity(cpuLimit)
	mem, _ := resource.ParseQuantity(memoryLimit)
	eph, _ := resource.ParseQuantity(storageLimit)
	t.pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              cpu,
			corev1.ResourceMemory:           mem,
			corev1.ResourceEphemeralStorage: eph,
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:              cpu,
			corev1.ResourceMemory:           mem,
			corev1.ResourceEphemeralStorage: eph,
		},
	}
}

func (t *TestPod) SetCustomSidecarContainerImage() {
	t.pod.Spec.Containers = append(t.pod.Spec.Containers, corev1.Container{
		Name:  webhook.GcsFuseSidecarName,
		Image: LastPublishedSidecarContainerImage,
	})
}

func (t *TestPod) VerifyCustomSidecarContainerImage(isNativeSidecar, hasMetadataPrefetch bool) {
	if isNativeSidecar {
		if hasMetadataPrefetch {
			gomega.Expect(t.pod.Spec.InitContainers).To(gomega.HaveLen(2))
			gomega.Expect(t.pod.Spec.InitContainers[1].Name).To(gomega.Equal(webhook.MetadataPrefetchSidecarName))
		} else {
			gomega.Expect(t.pod.Spec.InitContainers).To(gomega.HaveLen(1))
		}
		gomega.Expect(t.pod.Spec.InitContainers[0].Name).To(gomega.Equal(webhook.GcsFuseSidecarName))
		gomega.Expect(t.pod.Spec.InitContainers[0].Image).To(gomega.Equal(LastPublishedSidecarContainerImage))
	} else {
		if hasMetadataPrefetch {
			gomega.Expect(t.pod.Spec.Containers).To(gomega.HaveLen(3))
			gomega.Expect(t.pod.Spec.Containers[1].Name).To(gomega.Equal(webhook.MetadataPrefetchSidecarName))
		} else {
			gomega.Expect(t.pod.Spec.Containers).To(gomega.HaveLen(2))
		}
		gomega.Expect(t.pod.Spec.Containers[0].Name).To(gomega.Equal(webhook.GcsFuseSidecarName))
		gomega.Expect(t.pod.Spec.Containers[0].Image).To(gomega.Equal(LastPublishedSidecarContainerImage))
	}
}

func (t *TestPod) VerifyMetadataPrefetchPresence() {
	gomega.Expect(t.pod.Spec.InitContainers).To(gomega.HaveLen(2))
	gomega.Expect(t.pod.Spec.InitContainers[1].Name).To(gomega.Equal(webhook.MetadataPrefetchSidecarName))
	gomega.Expect(t.pod.Spec.Containers).ToNot(gomega.ContainElement(gomega.HaveField("Name", webhook.MetadataPrefetchSidecarName)))
}

func (t *TestPod) VerifyMetadataPrefetchNotPresent() {
	gomega.Expect(t.pod.Spec.InitContainers).ToNot(gomega.ContainElement(gomega.HaveField("Name", webhook.MetadataPrefetchSidecarName)))
	gomega.Expect(t.pod.Spec.Containers).ToNot(gomega.ContainElement(gomega.HaveField("Name", webhook.MetadataPrefetchSidecarName)))
}

func (t *TestPod) VerifySidecarPresence(expectPresent bool) {
	hasSidecar := false
	for _, c := range t.pod.Spec.Containers {
		if c.Name == webhook.GcsFuseSidecarName {
			hasSidecar = true
			break
		}
	}
	for _, c := range t.pod.Spec.InitContainers {
		if c.Name == webhook.GcsFuseSidecarName {
			hasSidecar = true
			break
		}
	}
	if expectPresent {
		gomega.Expect(hasSidecar).To(gomega.BeTrue(), fmt.Sprintf("expected Pod %s to have sidecar container injected", t.pod.Name))
	} else {
		gomega.Expect(hasSidecar).To(gomega.BeFalse(), fmt.Sprintf("expected Pod %s to NOT have sidecar container injected", t.pod.Name))
	}
}

func (t *TestPod) SetInitContainerWithCommand(cmd string) {
	cpu, _ := resource.ParseQuantity("100m")
	mem, _ := resource.ParseQuantity("20Mi")

	t.pod.Spec.InitContainers = []corev1.Container{
		{
			Name:         TesterContainerName + "-init",
			Image:        imageutils.GetE2EImage(imageutils.BusyBox),
			Command:      []string{"/bin/sh"},
			Args:         []string{"-c", cmd},
			VolumeMounts: make([]corev1.VolumeMount, 0),
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    cpu,
					corev1.ResourceMemory: mem,
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    cpu,
					corev1.ResourceMemory: mem,
				},
			},
		},
	}
}

func (t *TestPod) Cleanup(ctx context.Context) {
	e2epod.DeletePodOrFail(ctx, t.client, t.namespace.Name, t.pod.Name)
}

type TestPVC struct {
	client    clientset.Interface
	PVC       *corev1.PersistentVolumeClaim
	namespace *corev1.Namespace
}

func NewTestPVC(c clientset.Interface, ns *corev1.Namespace, pvcName, storageClassName, capacity string, accessMode corev1.PersistentVolumeAccessMode) *TestPVC {
	return &TestPVC{
		client:    c,
		namespace: ns,
		PVC: &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvcName,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					accessMode,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						"storage": resource.MustParse(capacity),
					},
				},
				StorageClassName: &storageClassName,
			},
		},
	}
}

func (t *TestPVC) Create(ctx context.Context) {
	framework.Logf("Creating PVC %s", t.PVC.Name)
	var err error
	t.PVC, err = t.client.CoreV1().PersistentVolumeClaims(t.namespace.Name).Create(ctx, t.PVC, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

// SetMounterPodTemplate sets the gke-gcsfuse/mounter-pod-template annotation on the PVC.
// By default, if this annotation is not set, setupVolume will automatically associate the PVC
// with the default mounter PodTemplate (DefaultMounterPodTemplateName).
// Calling this method allows tests to bind the PVC to a custom PodTemplate (e.g. for testing
// custom fsGroup, custom ServiceAccountName). If the PVC has already been created on the
// API server, it actively updates the PVC resource.
func (t *TestPVC) SetMounterPodTemplate(ctx context.Context, templateName string) {
	if t.PVC.Annotations == nil {
		t.PVC.Annotations = make(map[string]string)
	}
	t.PVC.Annotations[webhook.MounterPodTemplateAnnotation] = templateName
	if t.PVC.ResourceVersion != "" {
		updatedPVC, err := t.client.CoreV1().PersistentVolumeClaims(t.namespace.Name).Update(ctx, t.PVC, metav1.UpdateOptions{})
		framework.ExpectNoError(err)
		t.PVC = updatedPVC
	}
}

func (t *TestPVC) Cleanup(ctx context.Context) {
	framework.Logf("Deleting PVC %s", t.PVC.Name)
	err := t.client.CoreV1().PersistentVolumeClaims(t.namespace.Name).Delete(ctx, t.PVC.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestSecret struct {
	client    clientset.Interface
	secret    *corev1.Secret
	namespace *corev1.Namespace
}

func NewTestSecret(c clientset.Interface, ns *corev1.Namespace, name string, data map[string]string) *TestSecret {
	return &TestSecret{
		client:    c,
		namespace: ns,
		secret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			StringData: data,
			Type:       corev1.SecretTypeOpaque,
		},
	}
}

func (t *TestSecret) Create(ctx context.Context) {
	framework.Logf("Creating Secret %s", t.secret.Name)
	var err error
	t.secret, err = t.client.CoreV1().Secrets(t.namespace.Name).Create(ctx, t.secret, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestSecret) Cleanup(ctx context.Context) {
	framework.Logf("Deleting Secret %s", t.secret.Name)
	err := t.client.CoreV1().Secrets(t.namespace.Name).Delete(ctx, t.secret.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestDeployment struct {
	client     clientset.Interface
	deployment *appsv1.Deployment
	namespace  *corev1.Namespace
}

func NewTestDeployment(c clientset.Interface, ns *corev1.Namespace, tPod *TestPod) *TestDeployment {
	tPod.pod.Spec.RestartPolicy = corev1.RestartPolicyAlways
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "gcsfuse-volume-deployment-tester-",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(3)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "gcsfuse-volume-deployment-tester",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: tPod.pod.ObjectMeta,
				Spec:       tPod.pod.Spec,
			},
		},
	}
	deployment.Spec.Template.ObjectMeta.Labels = deployment.Spec.Selector.MatchLabels

	return &TestDeployment{
		client:     c,
		namespace:  ns,
		deployment: deployment,
	}
}

func (t *TestDeployment) Create(ctx context.Context) {
	framework.Logf("Creating Deployment %s", t.deployment.Name)
	var err error
	t.deployment, err = t.client.AppsV1().Deployments(t.namespace.Name).Create(ctx, t.deployment, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestDeployment) SetReplicas(replica int) {
	t.deployment.Spec.Replicas = ptr.To(int32(replica))
}

func (t *TestDeployment) WaitForRunningAndReady(ctx context.Context) {
	framework.Logf("Waiting Deployment %s to running and ready", t.deployment.Name)
	WaitForWorkloadReady(ctx, t.client, t.namespace.Name, t.deployment.Spec.Selector, *t.deployment.Spec.Replicas, corev1.PodRunning, pollTimeoutSlow)
}

func (t *TestDeployment) WaitForRunningAndReadyWithTimeout(ctx context.Context) {
	framework.Logf("Waiting Deployment %s to running and ready", t.deployment.Name)
	WaitForWorkloadReady(ctx, t.client, t.namespace.Name, t.deployment.Spec.Selector, *t.deployment.Spec.Replicas, corev1.PodRunning, pollTimeoutSlow)
}

func (t *TestDeployment) Scale(ctx context.Context, replica int) {
	framework.Logf("Scaling Deployment %s from %v to %v", t.deployment.Name, t.deployment.Spec.Replicas, replica)
	scale, err := t.client.AppsV1().Deployments(t.namespace.Name).GetScale(ctx, t.deployment.Name, metav1.GetOptions{})
	framework.ExpectNoError(err)
	scale.Spec.Replicas = int32(replica)
	newScale, err := t.client.AppsV1().Deployments(t.namespace.Name).UpdateScale(ctx, t.deployment.Name, scale, metav1.UpdateOptions{})
	framework.ExpectNoError(err)
	t.deployment.Spec.Replicas = &newScale.Spec.Replicas
}

func (t *TestDeployment) Cleanup(ctx context.Context) {
	framework.Logf("Deleting Deployment %s", t.deployment.Name)
	err := t.client.AppsV1().Deployments(t.namespace.Name).Delete(ctx, t.deployment.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestStatefulSet struct {
	client      clientset.Interface
	statefulSet *appsv1.StatefulSet
	namespace   *corev1.Namespace
}

func NewTestStatefulSet(c clientset.Interface, ns *corev1.Namespace, tPod *TestPod) *TestStatefulSet {
	tPod.pod.Spec.RestartPolicy = corev1.RestartPolicyAlways
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "gcsfuse-volume-statefulset-tester-",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "gcsfuse-volume-statefulset-tester",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: tPod.pod.ObjectMeta,
				Spec:       tPod.pod.Spec,
			},
		},
	}
	statefulSet.Spec.Template.ObjectMeta.Labels = statefulSet.Spec.Selector.MatchLabels

	return &TestStatefulSet{
		client:      c,
		namespace:   ns,
		statefulSet: statefulSet,
	}
}

func (t *TestStatefulSet) Create(ctx context.Context) {
	framework.Logf("Creating StatefulSet %s", t.statefulSet.Name)
	var err error
	t.statefulSet, err = t.client.AppsV1().StatefulSets(t.namespace.Name).Create(ctx, t.statefulSet, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestStatefulSet) WaitForRunningAndReady(ctx context.Context) {
	framework.Logf("Waiting StatefulSet %s to running and ready", t.statefulSet.Name)
	WaitForWorkloadReady(ctx, t.client, t.namespace.Name, t.statefulSet.Spec.Selector, *t.statefulSet.Spec.Replicas, corev1.PodRunning, pollTimeoutSlow)
}

func (t *TestStatefulSet) Cleanup(ctx context.Context) {
	framework.Logf("Deleting StatefulSet %s", t.statefulSet.Name)
	err := t.client.AppsV1().StatefulSets(t.namespace.Name).Delete(ctx, t.statefulSet.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

// WaitForWorkloadReady waits for the pods in the workload to reach the expected status.
func WaitForWorkloadReady(ctx context.Context, c clientset.Interface, namespace string, selector *metav1.LabelSelector, replica int32, expectedPodStatus corev1.PodPhase, timeout time.Duration) {
	err := wait.PollUntilContextTimeout(ctx, pollIntervalSlow, timeout, true,
		func(ctx context.Context) (bool, error) {
			replicaSetSelector, err := metav1.LabelSelectorAsSelector(selector)
			framework.ExpectNoError(err)

			podList, err := c.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: replicaSetSelector.String()})
			framework.ExpectNoError(err)

			if int32(len(podList.Items)) != replica {
				framework.Logf("Found %d workload pods, waiting for %d", len(podList.Items), replica)

				return false, nil
			}

			for _, p := range podList.Items {
				if p.Status.Phase != expectedPodStatus {
					framework.Logf("Waiting for pod %v to enter %v, currently %v", p.Name, expectedPodStatus, p.Status.Phase)

					return false, nil
				}
			}

			return true, nil
		})

	framework.ExpectNoError(err)
}

type TestJob struct {
	client    clientset.Interface
	job       *batchv1.Job
	namespace *corev1.Namespace
}

func NewTestJob(c clientset.Interface, ns *corev1.Namespace, tPod *TestPod) *TestJob {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gcsfuse-volume-job-tester",
		},
		Spec: batchv1.JobSpec{
			ManualSelector: ptr.To(true),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					e2ejob.JobSelectorKey: "gcsfuse-volume-job-tester",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: tPod.pod.ObjectMeta,
				Spec:       tPod.pod.Spec,
			},
			BackoffLimit: ptr.To(int32(2)),
		},
	}
	job.Spec.Template.ObjectMeta.Labels = job.Spec.Selector.MatchLabels

	return &TestJob{
		client:    c,
		namespace: ns,
		job:       job,
	}
}

func (t *TestJob) Create(ctx context.Context) {
	framework.Logf("Creating Job %s", t.job.Name)
	var err error
	t.job, err = t.client.BatchV1().Jobs(t.namespace.Name).Create(ctx, t.job, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestJob) WaitForJobPodsSucceeded(ctx context.Context) {
	framework.Logf("Waiting Job %s to have 1 succeeded pod", t.job.Name)
	err := e2ejob.WaitForJobPodsSucceeded(ctx, t.client, t.namespace.Name, t.job.Name, 1)
	framework.ExpectNoError(err)
}

func (t *TestJob) WaitForJobFailed(ctx context.Context) {
	framework.Logf("Waiting Job %s to fail", t.job.Name)
	err := e2ejob.WaitForJobFailed(ctx, t.client, t.namespace.Name, t.job.Name)
	framework.ExpectNoError(err)
}

func (t *TestJob) WaitForAllJobPodsGone(ctx context.Context) {
	err := wait.PollUntilContextTimeout(ctx, framework.Poll, pollTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := e2ejob.GetJobPods(ctx, t.client, t.namespace.Name, t.job.Name)
		if err != nil {
			return false, err
		}

		return len(pods.Items) == 0, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestJob) Cleanup(ctx context.Context) {
	framework.Logf("Deleting Job %s", t.job.Name)
	d := metav1.DeletePropagationBackground
	err := t.client.BatchV1().Jobs(t.namespace.Name).Delete(ctx, t.job.Name, metav1.DeleteOptions{PropagationPolicy: &d})
	framework.ExpectNoError(err)
}

// GetGCSFuseVersion returns the GCSFuse version to be tested.
// The env var is set by the handler.go before running the tests.
func GetGCSFuseVersion() string {
	return os.Getenv(utils.GcsfuseVersionVarName)
}

func GCSFuseVersionAndBranch() (*version.Version, string) {
	vStr := GetGCSFuseVersion()
	v, branch := utils.GCSFuseBranch(vStr)
	if v == nil {
		// This happens for master branch builds. We still need to parse the version.
		var err error
		v, err = version.ParseSemantic(vStr)
		framework.ExpectNoError(err, "Failed to parse GCS Fuse version string %s into version.Version", vStr)
	}
	return v, branch
}

// GetGKEClusterVersion returns the GKE cluster version being tested.
// The env var is set by handler.go before running the tests.
func GetGKEClusterVersion() string {
	return os.Getenv(utils.GkeClusterVersionVarName)
}

// GKEClusterVersion returns the parsed semantic version of the GKE cluster.
func GKEClusterVersion() *version.Version {
	vStr := GetGKEClusterVersion()
	if vStr == "" {
		return nil
	}
	v, err := version.ParseGeneric(vStr)
	framework.ExpectNoError(err, "Failed to parse GKE Cluster version string %s into version.Version", vStr)
	return v
}

// GKEClusterVersionAtLeast returns true if the GKE cluster version is at least minVersionStr.
func GKEClusterVersionAtLeast(minVersionStr string) bool {
	v := GKEClusterVersion()
	if v == nil {
		return true
	}

	minV, err := version.ParseGeneric(minVersionStr)
	framework.ExpectNoError(err, "Failed to parse minimum GKE Cluster version string %s into version.Version", minVersionStr)

	return v.AtLeast(minV)
}

func DeployIstioSidecar(namespace string) {
	runKubectlOrDie(namespace, "apply", "--filename", "./specs/istio-sidecar.yaml")
}

func DeployIstioServiceEntry(namespace string) {
	runKubectlOrDie(namespace, "apply", "--filename", "./specs/istio-service-entry.yaml")
}

func (t *TestPod) VerifyDefaultingFlagsArePassed(namespace string, expectedMachineTypeFlag string, expectedDisableAutoconfigFlag bool) {
	stdout, stderr, err := runKubectlWithFullOutputWithRetry(namespace, "logs", t.pod.Name, "-c", "gke-gcsfuse-sidecar")
	framework.ExpectNoError(err,
		"Error accessing logs from pod %v, but failed with error message %q\nstdout: %s\nstderr: %s",
		t.pod.Name, err, stdout, stderr)

	expectedDisableAutoconfigFlagString := fmt.Sprintf(`"DisableAutoconfig":%t`, expectedDisableAutoconfigFlag)
	expectedMachineTypeFlagString := fmt.Sprintf(`"MachineType":"%s"`, expectedMachineTypeFlag)

	gomega.Expect(stdout).To(gomega.ContainSubstring(expectedDisableAutoconfigFlagString),
		"Should find DisableAutoconfig flag string %q in stdout", expectedDisableAutoconfigFlagString)
	gomega.Expect(stdout).To(gomega.ContainSubstring(expectedMachineTypeFlagString),
		"Should find MachineType flag string %q in stdout", expectedMachineTypeFlagString)
}

func (t *TestPod) GetSidecarLogs() (string, error) {
	stdout, _, err := runKubectlWithFullOutputWithRetry(t.namespace.Name, "logs", t.pod.Name, "-c", "gke-gcsfuse-sidecar")
	return stdout, err
}

func (t *TestPod) VerifyProfileFlagsArePassed(namespace string, expectedProfileFlag string, expectedProfile bool) {
	stdout, stderr, err := runKubectlWithFullOutputWithRetry(namespace, "logs", t.pod.Name, "-c", "gke-gcsfuse-sidecar")
	framework.ExpectNoError(err,
		"Error accessing logs from pod %v, but failed with error message %q\nstdout: %s\nstderr: %s",
		t.pod.Name, err, stdout, stderr)

	if expectedProfile {
		// handles profile=<profile> case
		gomega.Expect(stdout).To(gomega.ContainSubstring(expectedTrainingProfileFlag),
			"Should find profile flag string in stdout")

		// handles profile:<profile> case
		gomega.Expect(stdout).To(gomega.MatchRegexp(expectedTrainingProfileConfig),
			"Should find 'profile:%s' within the gcsfuse config file content map, but it was not found.", expectedProfileFlag)
	} else {
		// handles profile=<profile> case
		gomega.Expect(stdout).NotTo(gomega.ContainSubstring(expectedTrainingProfileFlag),
			"Should not find profile flag string in stdout")

		// handles profile:<profile> case
		gomega.Expect(stdout).NotTo(gomega.MatchRegexp(expectedTrainingProfileConfig),
			"Should not find 'profile:%s' within the gcsfuse config file content map, but it was found.", expectedProfileFlag)
	}
}

func (t *TestPod) VerifyKernelParamsFlagsAreNotPassed(namespace, volumeName string) {
	stdout, stderr, err := runKubectlWithFullOutputWithRetry(namespace, "logs", t.pod.Name, "-c", "gke-gcsfuse-sidecar")
	framework.ExpectNoError(err,
		"Error accessing logs from pod %v, but failed with error message %q\nstdout: %s\nstderr: %s",
		t.pod.Name, err, stdout, stderr)

	// Should not find kernel-params-file=/path/to/params-file (User Provided)
	gomega.Expect(stdout).To(gomega.Not(gomega.ContainSubstring("--kernel-params-file params-file")),
		"Should not find kernel-params-file flag string in stdout")

	// Shuld not find kernel-params-file:/path/to/params-file (User Provided)
	gomega.Expect(stdout).To(gomega.Not(gomega.MatchRegexp(`map\[.*kernel-params-file:params-file.*\]`)),
		"Should not find 'kernel-params-file:params-file' within the gcsfuse config file content map, but it was found.")

	// Should not find file-system:kernel-params-file:/gcsfuse-tmp/.volumes/<volume-name>/kernel-params.json (CSI Driver provided)
	gomega.Expect(stdout).To(gomega.Not(gomega.MatchRegexp(`file-system:map\[kernel-params-file:/gcsfuse-tmp/.volumes/.*/kernel-params.json\]`)),
		"Should not find CSI Driver provided kernel-params-file flag string in stdout")
}

func (t *TestPod) VerifyKernelParamsFlagsArePassed(namespace string) {
	stdout, stderr, err := runKubectlWithFullOutputWithRetry(namespace, "logs", t.pod.Name, "-c", "gke-gcsfuse-sidecar")
	framework.ExpectNoError(err,
		"Error accessing logs from pod %v, but failed with error message %q\nstdout: %s\nstderr: %s",
		t.pod.Name, err, stdout, stderr)

	// Should find file-system:kernel-params-file:/gcsfuse-tmp/.volumes/<volume-name>/kernel-params.json (CSI Driver provided flag in config map)
	gomega.Expect(stdout).To(gomega.MatchRegexp(`file-system:map\[kernel-params-file:/gcsfuse-tmp/.volumes/.*/kernel-params.json\]`),
		"Should find CSI Driver provided kernel-params-file flag string in stdout")
}

func runKubectlOrDie(namespace string, args ...string) {
	_, _, err := runKubectlWithFullOutputWithRetry(namespace, args...)
	framework.ExpectNoError(err)
}

func (t *TestPod) VerifyMountOptionsArePassedWithConfigFormat(namespace string, mountOptions map[string]string) {
	stdout, stderr, err := runKubectlWithFullOutputWithRetry(namespace, "logs", t.pod.Name, "-c", "gke-gcsfuse-sidecar")
	framework.ExpectNoError(err,
		"Error accessing logs from pod %v, but failed with error message %q\nstdout: %s\nstderr: %s",
		t.pod.Name, err, stdout, stderr)

	for key, value := range mountOptions {
		optionWithColon := fmt.Sprintf("%s:%s", key, value)

		gomega.Expect(stdout).To(
			gomega.ContainSubstring(optionWithColon),
			"Should find mount option %q with ':' separator in stdout", key,
		)
	}
}

func (t *TestPod) VerifyDriverLogsDoNotContain(logNotExpected string) {
	stdout, stderr, err := t.getDriverLogs()
	framework.ExpectNoError(err,
		"Error accessing logs from pod %v, but failed with error message %q\nstdout: %s\nstderr: %s",
		t.pod.Name, err, stdout, stderr)
	gomega.Expect(stdout).To(gomega.Not(gomega.ContainSubstring(logNotExpected)),
		"Should not find flag string in stdout")
}

func (t *TestPod) getDriverLogs() (string, string, error) {
	podNode := t.pod.Spec.NodeName
	driverNamespace := driverNamespaceOSS
	isOss := os.Getenv(IsOSSEnvVar)
	if isOss != "true" {
		driverNamespace = driverNamespaceManaged
	}
	// Get all node driver pods.
	podsOut, _, err := runKubectlWithFullOutputWithRetry(driverNamespace,
		"get", "pods",
		"-l", driverDaemonsetLabel,
		"-o", "jsonpath={range .items[*]}{.metadata.name}:{.spec.nodeName}{\"\\n\"}{end}",
	)

	if err != nil {
		return "", "", err
	}

	// Isolate driver pod on test pod node.
	var targetPod string
	for _, line := range strings.Split(podsOut, "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			continue
		}
		if parts[1] == podNode {
			targetPod = parts[0]
			break
		}
	}

	if targetPod == "" {
		return "", "", fmt.Errorf("no driver pod found on node %s", podNode)
	}

	// Get logs from the driver container
	stdout, stderr, err := runKubectlWithFullOutputWithRetry(driverNamespace,
		"logs", targetPod, "-c", driverContainer,
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs for pod %s: %v", targetPod, err)
	}

	return stdout, stderr, nil
}

func runKubectlWithFullOutputWithRetry(namespace string, args ...string) (string, string, error) {
	return RetryWithBackoffTwoReturnValues(func() (string, string, error) {
		return e2ekubectl.RunKubectlWithFullOutput(namespace, args...)
	})
}
