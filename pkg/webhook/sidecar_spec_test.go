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

	v1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func TestValidatePodHasSidecarContainerInjected(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		imageName        string
		pod              *v1.Pod
		expectedInjected bool
	}{
		{
			name:      "should pass the validation with an emptyDir cache volume",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: true,
		},
		{
			name:      "should pass the validation with a PVC cache volume",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: []v1.Volume{
						{
							Name: SidecarContainerTmpVolumeName,
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: SidecarContainerCacheVolumeName,
							VolumeSource: v1.VolumeSource{
								PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{},
							},
						},
					},
				},
			},
			expectedInjected: true,
		},
		{
			name:      "should pass the validation when the sidecar container is not at position 0",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
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
			name:      "should pass the validation with a simplified sidecar container",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  SidecarContainerName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &v1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerCacheVolumeName,
									MountPath: SidecarContainerCacheVolumeMountPath,
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
			name:      "should fail the validation when the sidecar container image is wrong",
			imageName: "wrong-image",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			expectedInjected: false,
		},
		{
			name:      "should fail the validation when the sidecar container is missing",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
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
			name:      "should fail the validation when the temp volume name is wrong",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  SidecarContainerName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &v1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "wrong-tmp-volume-name",
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerCacheVolumeName,
									MountPath: SidecarContainerCacheVolumeMountPath,
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
			name:      "should fail the validation when the temp volume mount path is wrong",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  SidecarContainerName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &v1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      SidecarContainerTmpVolumeName,
									MountPath: "wrong-tmp-volume-mount-path",
								},
								{
									Name:      SidecarContainerCacheVolumeName,
									MountPath: SidecarContainerCacheVolumeMountPath,
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
			name:      "should fail the validation when the cache volume name is wrong",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  SidecarContainerName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &v1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      "wrong-cache-volume-name",
									MountPath: SidecarContainerCacheVolumeMountPath,
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
			name:      "should fail the validation when the cache volume name is wrong",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  SidecarContainerName,
							Image: FakeConfig().ContainerImage,
							SecurityContext: &v1.SecurityContext{
								RunAsUser:  ptr.To(int64(NobodyUID)),
								RunAsGroup: ptr.To(int64(NobodyGID)),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      SidecarContainerTmpVolumeName,
									MountPath: SidecarContainerTmpVolumeMountPath,
								},
								{
									Name:      SidecarContainerCacheVolumeName,
									MountPath: "wrong-cache-volume-mount-path",
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
			name:      "should fail the validation when the temp volume is missing",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: []v1.Volume{
						{
							Name: SidecarContainerCacheVolumeName,
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			expectedInjected: false,
		},
		{
			name:      "should fail the validation with a non-emptyDir temp volume",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: []v1.Volume{
						{
							Name:         SidecarContainerTmpVolumeName,
							VolumeSource: v1.VolumeSource{},
						},
						{
							Name: SidecarContainerCacheVolumeName,
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			expectedInjected: false,
		},
		{
			name:      "should fail the validation when the cache volume is missing",
			imageName: FakeConfig().ContainerImage,
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes: []v1.Volume{
						{
							Name: SidecarContainerTmpVolumeName,
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			expectedInjected: false,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)

		injected := ValidatePodHasSidecarContainerInjected(tc.imageName, tc.pod)

		if injected != tc.expectedInjected {
			t.Errorf("got injection result %v, but expected %v", injected, tc.expectedInjected)
		}
	}
}
