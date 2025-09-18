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

package webhook

import (
	"testing"

	util "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

type testCase struct {
	name             string
	pod              *corev1.Pod
	expectedInjected bool
	isInitContainer  bool
}

func TestValidatePodHasSidecarContainerInjectedForAutoInjection(t *testing.T) {
	t.Parallel()

	testCases := []testCase{
		{
			name: "should pass the validation with the standard sidecar container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
		},
		{
			name: "should pass the validation with the init sidecar container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:        GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
			isInitContainer:  true,
		},
		{
			name: "should pass the validation with the both init and regular sidecar containers",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers:     []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					InitContainers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:        GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
			isInitContainer:  true,
		},
		{
			name: "should pass the validation with a simplified sidecar container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      util.SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerBufferVolumeName,
									MountPath: SidecarContainerBufferVolumeMountPath,
								},
							},
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
		},
		{
			// This test ensures that we meet backwards compatibility.
			//
			// First version of GCSFuse only supported tmp volume. Any workloads using this sidecar version
			// should still be able to pass validation after an upgrade that affects node.

			name: "should pass the validation with only tmp volume/volumeMount sidecar container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      util.SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						tmpVolume,
					},
				},
			},
			expectedInjected: true,
		},
		{
			name: "should pass the validation with a private sidecar container image",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: "private-repo/sidecar-image",
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      util.SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerBufferVolumeName,
									MountPath: SidecarContainerBufferVolumeMountPath,
								},
							},
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
		},
		{
			name: "should pass the validation with a private sidecar container image in init container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: "private-repo/sidecar-image",
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      util.SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerBufferVolumeName,
									MountPath: SidecarContainerBufferVolumeMountPath,
								},
							},
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
			isInitContainer:  true,
		},
		{
			name: "should fail the validation with random UID and GID",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  ptr.To(int64(1234)),
								RunAsGroup: ptr.To(int64(1234)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      util.SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerBufferVolumeName,
									MountPath: SidecarContainerBufferVolumeMountPath,
								},
							},
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: false,
		},
		{
			name: "should fail the validation when the sidecar container is missing",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "first-container",
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: false,
		},
		{
			name: "should fail the validation when the temp volume name is wrong",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "wrong-tmp-volume-name",
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerBufferVolumeName,
									MountPath: SidecarContainerBufferVolumeMountPath,
								},
							},
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: false,
		},
		{
			name: "should fail the validation when the temp volume mount path is wrong",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      util.SidecarContainerTmpVolumeName,
									MountPath: "wrong-tmp-volume-mount-path",
								},
								{
									Name:      SidecarContainerBufferVolumeName,
									MountPath: SidecarContainerBufferVolumeMountPath,
								},
							},
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: false,
		},
		{
			name: "should fail the validation when the temp volume is missing",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: []corev1.Volume{
						{
							Name: SidecarContainerBufferVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			expectedInjected: false,
		},
		{
			name: "should fail the validation with a non-emptyDir temp volume",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: []corev1.Volume{
						{
							Name:         util.SidecarContainerTmpVolumeName,
							VolumeSource: corev1.VolumeSource{},
						},
						{
							Name: SidecarContainerBufferVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			expectedInjected: false,
		},
		{
			name: "should pass the validation with a custom buffer volume",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: append(GetSidecarContainerVolumeSpec(), corev1.Volume{
						Name: SidecarContainerBufferVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "custom-buffer-volume-pvc",
							},
						},
					}),
				},
			},
			expectedInjected: true,
		},
		{
			name: "should pass the validation with a custom cache volume",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: append(GetSidecarContainerVolumeSpec(), corev1.Volume{
						Name: SidecarContainerCacheVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "custom-cache-volume-pvc",
							},
						},
					}),
				},
			},
			expectedInjected: true,
		},
		{
			name: "should pass the validation when the sidecar container is not at position 0",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "first-container",
						},
						GetSidecarContainerSpec(FakeConfig()),
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
		},
		{
			name: "should pass the validation when the init sidecar container is not at position 0",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "first-container",
						},
						GetSidecarContainerSpec(FakeConfig()),
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
			isInitContainer:  true,
		},
		{
			name: "should pass the validation when the init container is at another position",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "first-container",
						},
						{
							Name: "second-container",
						},
						GetSidecarContainerSpec(FakeConfig()),
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
			isInitContainer:  true,
		},
		{
			name: "should pass the validation when the container is at position 1 and istio is present",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: IstioSidecarName,
						},
						GetSidecarContainerSpec(FakeConfig()),
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
		},
		{
			name: "should pass the validation when the init container is at position 1 and istio is present",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: IstioSidecarName,
						},
						GetSidecarContainerSpec(FakeConfig()),
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
			isInitContainer:  true,
		},
		{
			name: "should pass the validation when the init container is at another position and istio is present",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "istio-init",
						},
						{
							Name: IstioSidecarName,
						},
						GetSidecarContainerSpec(FakeConfig()),
						{
							Name: "workload",
						},
					},
					Volumes: GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
			isInitContainer:  true,
		},
	}

	for _, tc := range testCases {
		injected, isInitContainer := ValidatePodHasSidecarContainerInjected(tc.pod)

		if injected != tc.expectedInjected {
			t.Errorf("got injection result %v, but expected %v", injected, tc.expectedInjected)
		}
		if isInitContainer != tc.isInitContainer {
			t.Errorf("got injection result for is init container %v, but expected %v", isInitContainer, tc.isInitContainer)
		}
	}
}
