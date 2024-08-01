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
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/gomega"
	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
	iam "google.golang.org/api/iam/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/kubelet/events"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eevents "k8s.io/kubernetes/test/e2e/framework/events"
	e2ejob "k8s.io/kubernetes/test/e2e/framework/job"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	e2epodooutput "k8s.io/kubernetes/test/e2e/framework/pod/output"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"k8s.io/utils/ptr"
)

const (
	TesterContainerName   = "volume-tester"
	K8sServiceAccountName = "gcsfuse-csi-sa"
	//nolint:gosec
	K8sSecretName                                              = "gcsfuse-csi-test-secret"
	FakeVolumePrefix                                           = "gcsfuse-csi-fake-volume"
	InvalidVolumePrefix                                        = "gcsfuse-csi-invalid-volume"
	NonRootVolumePrefix                                        = "gcsfuse-csi-non-root-volume"
	InvalidMountOptionsVolumePrefix                            = "gcsfuse-csi-invalid-mount-options-volume"
	ImplicitDirsVolumePrefix                                   = "gcsfuse-csi-implicit-dirs-volume"
	ForceNewBucketPrefix                                       = "gcsfuse-csi-force-new-bucket"
	SubfolderInBucketPrefix                                    = "gcsfuse-csi-subfolder-in-bucket"
	MultipleBucketsPrefix                                      = "gcsfuse-csi-multiple-buckets"
	EnableFileCachePrefix                                      = "gcsfuse-csi-enable-file-cache"
	EnableFileCacheWithLargeCapacityPrefix                     = "gcsfuse-csi-enable-file-cache-large-capacity"
	ImplicitDirsPath                                           = "implicit-dir"
	InvalidVolume                                              = "<invalid-name>"
	SkipCSIBucketAccessCheckPrefix                             = "gcsfuse-csi-skip-bucket-access-check"
	SkipCSIBucketAccessCheckAndFakeVolumePrefix                = "gcsfuse-csi-skip-bucket-access-check-fake-volume"
	SkipCSIBucketAccessCheckAndInvalidVolumePrefix             = "gcsfuse-csi-skip-bucket-access-check-invalid-volume"
	SkipCSIBucketAccessCheckAndInvalidMountOptionsVolumePrefix = "gcsfuse-csi-skip-bucket-access-check-invalid-mount-options-volume"
	SkipCSIBucketAccessCheckAndNonRootVolumePrefix             = "gcsfuse-csi-skip-bucket-access-check-non-root-volume"
	SkipCSIBucketAccessCheckAndImplicitDirsVolumePrefix        = "gcsfuse-csi-skip-bucket-access-check-implicit-dirs-volume"
	EnableFileCacheWithMetricsEnabledPrefix                    = "gcsfuse-csi-enable-file-cache-metrics"

	GoogleCloudCliImage = "gcr.io/google.com/cloudsdktool/google-cloud-cli:slim"
	GolangImage         = "golang:1.22.1"
	UbuntuImage         = "ubuntu:20.04"

	LastPublishedSidecarContainerImage = "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter@sha256:c83609ecf50d05a141167b8c6cf4dfe14ff07f01cd96a9790921db6748d40902"

	pollInterval     = 1 * time.Second
	pollTimeout      = 1 * time.Minute
	pollIntervalSlow = 10 * time.Second
	pollTimeoutSlow  = 10 * time.Minute
)

type TestPod struct {
	client    clientset.Interface
	pod       *corev1.Pod
	namespace *corev1.Namespace
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

func (t *TestPod) Create(ctx context.Context) {
	framework.Logf("Creating Pod %s", t.pod.Name)
	var err error
	t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Create(ctx, t.pod, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestPod) GetPodName() string {
	return t.pod.Name
}

// VerifyExecInPodSucceed verifies shell cmd in target pod succeed.
func (t *TestPod) VerifyExecInPodSucceed(f *framework.Framework, containerName, shExec string) {
	stdout, stderr, err := e2epod.ExecCommandInContainerWithFullOutput(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	framework.ExpectNoError(err,
		"%q should succeed, but failed with error message %q\nstdout: %s\nstderr: %s",
		shExec, err, stdout, stderr)
}

// VerifyExecInPodSucceedWithFullOutput verifies shell cmd in target pod succeed with full output.
func (t *TestPod) VerifyExecInPodSucceedWithFullOutput(f *framework.Framework, containerName, shExec string) {
	stdout, stderr, err := e2epod.ExecCommandInContainerWithFullOutput(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	framework.ExpectNoError(err,
		"%q should succeed, but failed with error message %q\nstdout: %s\nstderr: %s",
		shExec, err, stdout, stderr)

	framework.Logf("Output of %q: \n%s", shExec, stdout)
}

// VerifyExecInPodFail verifies shell cmd in target pod fail with certain exit code.
func (t *TestPod) VerifyExecInPodFail(f *framework.Framework, containerName, shExec string, exitCode int) {
	stdout, stderr, err := e2epod.ExecCommandInContainerWithFullOutput(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	gomega.Expect(err).Should(gomega.HaveOccurred(),
		fmt.Sprintf("%q should fail with exit code %d, but exit without error\nstdout: %s\nstderr: %s", shExec, exitCode, stdout, stderr))
}

func (t *TestPod) WaitForRunning(ctx context.Context) {
	err := e2epod.WaitTimeoutForPodRunningInNamespace(ctx, t.client, t.pod.Name, t.pod.Namespace, pollTimeoutSlow)
	framework.ExpectNoError(err)

	t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Get(ctx, t.pod.Name, metav1.GetOptions{})
	framework.ExpectNoError(err)
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
	err := e2eevents.WaitTimeoutForEvent(
		ctx,
		t.client,
		t.namespace.Name,
		fields.Set{"reason": events.FailedMountVolume}.AsSelector().String(),
		msg,
		pollTimeoutSlow)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForPodNotFoundInNamespace(ctx context.Context) {
	err := e2epod.WaitForPodNotFoundInNamespace(ctx, t.client, t.pod.Name, t.namespace.Name, pollTimeout)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForLog(ctx context.Context, container string, expectedString string) {
	_, err := e2epodooutput.LookForStringInLogWithoutKubectl(ctx, t.client, t.namespace.Name, t.pod.Name, container, expectedString, pollTimeout)
	framework.ExpectNoError(err)
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
		if cs.Name == webhook.SidecarContainerName {
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

func (t *TestPod) SetupVolume(volumeResource *storageframework.VolumeResource, name, mountPath string, readOnly bool, mountOptions ...string) {
	t.setupVolume(volumeResource, name, readOnly, mountOptions...)
	t.setupVolumeMount(name, mountPath, readOnly, "", false)
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

func (t *TestPod) setupVolume(volumeResource *storageframework.VolumeResource, name string, readOnly bool, mountOptions ...string) {
	volume := corev1.Volume{
		Name: name,
	}
	if volumeResource.Pvc != nil {
		volume.VolumeSource = corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: volumeResource.Pvc.Name,
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
		Name:      webhook.SidecarContainerTmpVolumeName,
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

func (t *TestPod) GetCISDriverNodePodIP(ctx context.Context) string {
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

func (t *TestPod) SetIstioSidecar(isNativeSidecar bool) {
	istioSidecar := []corev1.Container{{
		Name:  webhook.IstioSidecarName,
		Image: imageutils.GetE2EImage(imageutils.BusyBox),
	}}
	if isNativeSidecar {
		t.pod.Spec.InitContainers = append(istioSidecar, t.pod.Spec.InitContainers...)
	} else {
		t.pod.Spec.Containers = append(istioSidecar, t.pod.Spec.Containers...)
	}
}

func (t *TestPod) VerifyInjectionOrder(gcsFuseNativeSidecar, istioNativeSidecar bool) {
	if gcsFuseNativeSidecar {
		// Expect the gcsfuse sidecar to be native.
		if istioNativeSidecar {
			// Expect both gcsfuse and istio to be native sidecar.
			gomega.Expect(t.pod.Spec.InitContainers).To(gomega.HaveLen(2))

			// Verify ordering of sidecars.
			gomega.Expect(t.pod.Spec.InitContainers[0].Name).To(gomega.BeEquivalentTo(webhook.IstioSidecarName))
			gomega.Expect(t.pod.Spec.InitContainers[1].Name).To(gomega.BeEquivalentTo(webhook.SidecarContainerName))

			// Verify workload is present
			gomega.Expect(t.pod.Spec.Containers).To(gomega.HaveLen(1))
		} else {
			gomega.Panic().FailureMessage("container istio-proxy is not supported with native GCSFuse sidecar")
		}
	} else {
		// Expect gcsfuse sidecar to be regular container.
		if istioNativeSidecar {
			// Verify istio-proxy is first in native containers.
			gomega.Expect(t.pod.Spec.InitContainers).To(gomega.HaveLen(1))
			gomega.Expect(t.pod.Spec.InitContainers[0].Name).To(gomega.BeEquivalentTo(webhook.IstioSidecarName))

			// Verify gcsfuse is first in regular containers.
			gomega.Expect(t.pod.Spec.Containers).To(gomega.HaveLen(2))
			gomega.Expect(t.pod.Spec.Containers[0].Name).To(gomega.BeEquivalentTo(webhook.SidecarContainerName))
		} else {
			// Expect both gcsfuse and istio to be regular containers.
			gomega.Expect(t.pod.Spec.Containers).To(gomega.HaveLen(3))

			// Verify ordering of sidecars.
			gomega.Expect(t.pod.Spec.Containers[0].Name).To(gomega.BeEquivalentTo(webhook.IstioSidecarName))
			gomega.Expect(t.pod.Spec.Containers[1].Name).To(gomega.BeEquivalentTo(webhook.SidecarContainerName))
		}
	}
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
		Name:  webhook.SidecarContainerName,
		Image: LastPublishedSidecarContainerImage,
	})
}

func (t *TestPod) VerifyCustomSidecarContainerImage(isNativeSidecar bool) {
	if isNativeSidecar {
		gomega.Expect(t.pod.Spec.InitContainers).To(gomega.HaveLen(1))
		gomega.Expect(t.pod.Spec.InitContainers[0].Name).To(gomega.Equal(webhook.SidecarContainerName))
		gomega.Expect(t.pod.Spec.InitContainers[0].Image).To(gomega.Equal(LastPublishedSidecarContainerImage))
	} else {
		gomega.Expect(t.pod.Spec.Containers).To(gomega.HaveLen(2))
		gomega.Expect(t.pod.Spec.Containers[0].Name).To(gomega.Equal(webhook.SidecarContainerName))
		gomega.Expect(t.pod.Spec.Containers[0].Image).To(gomega.Equal(LastPublishedSidecarContainerImage))
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

type TestKubernetesServiceAccount struct {
	client         clientset.Interface
	serviceAccount *corev1.ServiceAccount
	namespace      *corev1.Namespace
}

func NewTestKubernetesServiceAccount(c clientset.Interface, ns *corev1.Namespace, name, gcpSAEmail string) *TestKubernetesServiceAccount {
	sa := &TestKubernetesServiceAccount{
		client:    c,
		namespace: ns,
		serviceAccount: &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
	if gcpSAEmail != "" {
		sa.serviceAccount.Annotations = map[string]string{
			"iam.gke.io/gcp-service-account": gcpSAEmail,
		}
	}

	return sa
}

func (t *TestKubernetesServiceAccount) Create(ctx context.Context) {
	framework.Logf("Creating Kubernetes Service Account %s", t.serviceAccount.Name)
	var err error
	t.serviceAccount, err = t.client.CoreV1().ServiceAccounts(t.namespace.Name).Create(ctx, t.serviceAccount, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestKubernetesServiceAccount) Cleanup(ctx context.Context) {
	framework.Logf("Deleting Kubernetes Service Account %s", t.serviceAccount.Name)
	err := t.client.CoreV1().ServiceAccounts(t.namespace.Name).Delete(ctx, t.serviceAccount.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestGCPServiceAccount struct {
	serviceAccount *iam.ServiceAccount
}

func NewTestGCPServiceAccount(name, projectID string) *TestGCPServiceAccount {
	return &TestGCPServiceAccount{
		serviceAccount: &iam.ServiceAccount{
			Name:      name,
			ProjectId: projectID,
		},
	}
}

func (t *TestGCPServiceAccount) Create(ctx context.Context) {
	framework.Logf("Creating GCP IAM Service Account %s", t.serviceAccount.Name)
	iamService, err := iam.NewService(ctx)
	framework.ExpectNoError(err)

	request := &iam.CreateServiceAccountRequest{
		AccountId: t.serviceAccount.Name,
		ServiceAccount: &iam.ServiceAccount{
			DisplayName: "Cloud Storage FUSE CSI Driver E2E Test SA",
		},
	}
	t.serviceAccount, err = iamService.Projects.ServiceAccounts.Create("projects/"+t.serviceAccount.ProjectId, request).Do()
	framework.ExpectNoError(err)

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(context.Context) (bool, error) {
		if _, e := iamService.Projects.ServiceAccounts.Get(t.serviceAccount.Name).Do(); e != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestGCPServiceAccount) AddIAMPolicyBinding(ctx context.Context, ns *corev1.Namespace) {
	framework.Logf("Binding the GCP IAM Service Account %s with Role roles/iam.workloadIdentityUser", t.serviceAccount.Name)
	iamService, err := iam.NewService(ctx)
	framework.ExpectNoError(err)

	policy, err := iamService.Projects.ServiceAccounts.GetIamPolicy(t.serviceAccount.Name).Do()
	framework.ExpectNoError(err)

	policy.Bindings = append(policy.Bindings,
		&iam.Binding{
			Role: "roles/iam.workloadIdentityUser",
			Members: []string{
				fmt.Sprintf("serviceAccount:%v.svc.id.goog[%v/%v]", t.serviceAccount.ProjectId, ns.Name, K8sServiceAccountName),
			},
		})

	iamPolicyRequest := &iam.SetIamPolicyRequest{Policy: policy}
	_, err = iamService.Projects.ServiceAccounts.SetIamPolicy(t.serviceAccount.Name, iamPolicyRequest).Do()
	framework.ExpectNoError(err)
}

func (t *TestGCPServiceAccount) GetEmail() string {
	if t.serviceAccount != nil {
		return t.serviceAccount.Email
	}

	return ""
}

func (t *TestGCPServiceAccount) Cleanup(ctx context.Context) {
	framework.Logf("Deleting GCP IAM Service Account %s", t.serviceAccount.Name)
	service, err := iam.NewService(ctx)
	framework.ExpectNoError(err)
	_, err = service.Projects.ServiceAccounts.Delete(t.serviceAccount.Name).Do()
	framework.ExpectNoError(err)
}

type TestGCPProjectIAMPolicyBinding struct {
	projectID string
	member    string
	role      string
	condition string
}

func NewTestGCPProjectIAMPolicyBinding(projectID, member, role, condition string) *TestGCPProjectIAMPolicyBinding {
	return &TestGCPProjectIAMPolicyBinding{
		projectID: projectID,
		member:    member,
		role:      role,
		condition: condition,
	}
}

func (t *TestGCPProjectIAMPolicyBinding) Create(ctx context.Context) {
	framework.Logf("Binding member %s with role %s, condition %q to project %s", t.member, t.role, t.condition, t.projectID)
	crmService, err := cloudresourcemanager.NewService(ctx)
	framework.ExpectNoError(err)

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeoutSlow, true, func(context.Context) (bool, error) {
		if addBinding(crmService, t.projectID, t.member, t.role) != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestGCPProjectIAMPolicyBinding) Cleanup(ctx context.Context) {
	framework.Logf("Removing member %q from project %v", t.member, t.projectID)
	crmService, err := cloudresourcemanager.NewService(ctx)
	framework.ExpectNoError(err)

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeoutSlow, true, func(context.Context) (bool, error) {
		if removeMember(crmService, t.projectID, t.member, t.role) != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	framework.ExpectNoError(err)
}

// addBinding adds the member to the project's IAM policy.
func addBinding(crmService *cloudresourcemanager.Service, projectID, member, role string) error {
	policy, err := getPolicy(crmService, projectID)
	if err != nil {
		return err
	}

	// Find the policy binding for role. Only one binding can have the role.
	var binding *cloudresourcemanager.Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			binding = b

			break
		}
	}

	if binding != nil {
		// If the binding exists, adds the member to the binding
		binding.Members = append(binding.Members, member)
	} else {
		// If the binding does not exist, adds a new binding to the policy
		binding = &cloudresourcemanager.Binding{
			Role:    role,
			Members: []string{member},
		}
		policy.Bindings = append(policy.Bindings, binding)
	}

	return setPolicy(crmService, projectID, policy)
}

// removeMember removes the member from the project's IAM policy.
func removeMember(crmService *cloudresourcemanager.Service, projectID, member, role string) error {
	policy, err := getPolicy(crmService, projectID)
	if err != nil {
		return err
	}

	// Find the policy binding for role. Only one binding can have the role.
	var binding *cloudresourcemanager.Binding
	var bindingIndex int
	for i, b := range policy.Bindings {
		if b.Role == role {
			binding = b
			bindingIndex = i

			break
		}
	}

	// Order doesn't matter for bindings or members, so to remove, move the last item
	// into the removed spot and shrink the slice.
	if len(binding.Members) == 1 {
		// If the member is the only member in the binding, removes the binding
		last := len(policy.Bindings) - 1
		policy.Bindings[bindingIndex] = policy.Bindings[last]
		policy.Bindings = policy.Bindings[:last]
	} else {
		// If there is more than one member in the binding, removes the member
		var memberIndex int
		for i, mm := range binding.Members {
			if mm == member {
				memberIndex = i
			}
		}
		last := len(policy.Bindings[bindingIndex].Members) - 1
		binding.Members[memberIndex] = binding.Members[last]
		binding.Members = binding.Members[:last]
	}

	return setPolicy(crmService, projectID, policy)
}

// getPolicy gets the project's IAM policy.
func getPolicy(crmService *cloudresourcemanager.Service, projectID string) (*cloudresourcemanager.Policy, error) {
	request := new(cloudresourcemanager.GetIamPolicyRequest)

	return crmService.Projects.GetIamPolicy(projectID, request).Do()
}

// setPolicy sets the project's IAM policy.
func setPolicy(crmService *cloudresourcemanager.Service, projectID string, policy *cloudresourcemanager.Policy) error {
	request := new(cloudresourcemanager.SetIamPolicyRequest)
	request.Policy = policy
	_, err := crmService.Projects.SetIamPolicy(projectID, request).Do()

	return err
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

func (t *TestJob) WaitForJobFailed() {
	framework.Logf("Waiting Job %s to fail", t.job.Name)
	err := e2ejob.WaitForJobFailed(t.client, t.namespace.Name, t.job.Name)
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

func CreateImplicitDirInBucket(dirPath, bucketName string) {
	// Use bucketName as the name of a temp file since bucketName is unique.
	f, err := os.Create(bucketName)
	if err != nil {
		framework.Failf("Failed to create an empty data file: %v", err)
	}
	f.Close()
	defer func() {
		err = os.Remove(bucketName)
		if err != nil {
			framework.Failf("Failed to delete the empty data file: %v", err)
		}
	}()

	//nolint:gosec
	if output, err := exec.Command("gsutil", "cp", bucketName, fmt.Sprintf("gs://%v/%v/", bucketName, dirPath)).CombinedOutput(); err != nil {
		framework.Failf("Failed to create a implicit dir in GCS bucket: %v, output: %s", err, output)
	}
}

func CreateTestFileInBucket(fileName, bucketName string) {
	createTestFileInBucket(fileName, bucketName, []byte(fileName))
}

func CreateTestFileWithSizeInBucket(fileName, bucketName string, fileSize int) {
	createTestFileInBucket(fileName, bucketName, make([]byte, fileSize))
}

func createTestFileInBucket(fileName, bucketName string, fileContent []byte) {
	err := os.WriteFile(fileName, fileContent, 0o600)
	if err != nil {
		framework.Failf("Failed to create a test file: %v", err)
	}
	defer func() {
		err = os.Remove(fileName)
		if err != nil {
			framework.Failf("Failed to delete the empty data file: %v", err)
		}
	}()

	//nolint:gosec
	if output, err := exec.Command("gsutil", "cp", fileName, fmt.Sprintf("gs://%v", bucketName)).CombinedOutput(); err != nil {
		framework.Failf("Failed to create a test file in GCS bucket: %v, output: %s", err, output)
	}
}
