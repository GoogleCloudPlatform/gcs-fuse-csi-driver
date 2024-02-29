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

type testCase struct {
	name             string
	pod              *v1.Pod
	expectedInjected bool
	isInitContainer  bool
}

var commonTestCases = []testCase{
	{
		name: "should pass the validation with the standard sidecar container",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
				Volumes:    GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: true,
	},
	{
		name: "should pass the validation with the init sidecar container",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				InitContainers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
				Volumes:        GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: true,
		isInitContainer:  true,
	},
	{
		name: "should pass the validation with the both the init and regular sidecar containers",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				Containers:     []v1.Container{GetSidecarContainerSpec(FakeConfig())},
				InitContainers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
				Volumes:        GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: true,
		isInitContainer:  true,
	},
	{
		name: "should pass the validation with a simplified sidecar container",
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
								Name:      SidecarContainerBufferVolumeName,
								MountPath: SidecarContainerBufferVolumeMountPath,
							},
						},
					},
				},
				Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: true,
	},
	{
		name: "should pass the validation with a private sidecar container image",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name:  SidecarContainerName,
						Image: "private-repo/sidecar-image",
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
								Name:      SidecarContainerBufferVolumeName,
								MountPath: SidecarContainerBufferVolumeMountPath,
							},
						},
					},
				},
				Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: true,
	},
	{
		name: "should pass the validation with a private sidecar container image in init container",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				InitContainers: []v1.Container{
					{
						Name:  SidecarContainerName,
						Image: "private-repo/sidecar-image",
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
								Name:      SidecarContainerBufferVolumeName,
								MountPath: SidecarContainerBufferVolumeMountPath,
							},
						},
					},
				},
				Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: true,
		isInitContainer:  true,
	},
	{
		name: "should fail the validation with random UID and GID",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name:  SidecarContainerName,
						Image: FakeConfig().ContainerImage,
						SecurityContext: &v1.SecurityContext{
							RunAsUser:  ptr.To(int64(1234)),
							RunAsGroup: ptr.To(int64(1234)),
						},
						VolumeMounts: []v1.VolumeMount{
							{
								Name:      SidecarContainerTmpVolumeName,
								MountPath: SidecarContainerTmpVolumeMountPath,
							},
							{
								Name:      SidecarContainerBufferVolumeName,
								MountPath: SidecarContainerBufferVolumeMountPath,
							},
						},
					},
				},
				Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: false,
	},
	{
		name: "should fail the validation when the sidecar container is missing",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "first-container",
					},
				},
				Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: false,
	},
	{
		name: "should fail the validation when the temp volume name is wrong",
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
								Name:      SidecarContainerBufferVolumeName,
								MountPath: SidecarContainerBufferVolumeMountPath,
							},
						},
					},
				},
				Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: false,
	},
	{
		name: "should fail the validation when the temp volume mount path is wrong",
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
								Name:      SidecarContainerBufferVolumeName,
								MountPath: SidecarContainerBufferVolumeMountPath,
							},
						},
					},
				},
				Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
			},
		},
		expectedInjected: false,
	},
	{
		name: "should fail the validation when the temp volume is missing",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
				Volumes: []v1.Volume{
					{
						Name: SidecarContainerBufferVolumeName,
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
		name: "should fail the validation with a non-emptyDir temp volume",
		pod: &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{GetSidecarContainerSpec(FakeConfig())},
				Volumes: []v1.Volume{
					{
						Name:         SidecarContainerTmpVolumeName,
						VolumeSource: v1.VolumeSource{},
					},
					{
						Name: SidecarContainerBufferVolumeName,
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

func TestValidatePodHasSidecarContainerInjectedForAutoInjection(t *testing.T) {
	t.Parallel()

	testCases := []testCase{}
	testCases = append(testCases, commonTestCases...)
	testCases = append(testCases,
		testCase{
			name: "should fail the validation when the sidecar container is not at position 0",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "first-container",
						},
						GetSidecarContainerSpec(FakeConfig()),
					},
					Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
				},
			},
			expectedInjected: false,
		},
	)

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)

		injected, isInitContainer := ValidatePodHasSidecarContainerInjected(tc.pod, true)

		if injected != tc.expectedInjected {
			t.Errorf("got injection result %v, but expected %v", injected, tc.expectedInjected)
		}
		if isInitContainer != tc.isInitContainer {
			t.Errorf("got injection result for is init container %v, but expected %v", isInitContainer, tc.isInitContainer)
		}
	}
}

func TestValidatePodHasSidecarContainerInjectedForManualInjection(t *testing.T) {
	t.Parallel()

	testCases := []testCase{}
	testCases = append(testCases, commonTestCases...)
	testCases = append(testCases,
		testCase{
			name: "should pass the validation when the sidecar container is not at position 0",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "first-container",
						},
						GetSidecarContainerSpec(FakeConfig()),
					},
					Volumes: GetSidecarContainerVolumeSpec([]v1.Volume{}),
				},
			},
			expectedInjected: true,
		},
	)

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)

		injected, isInitContainer := ValidatePodHasSidecarContainerInjected(tc.pod, false)

		if injected != tc.expectedInjected {
			t.Errorf("got injection result %v, but expected %v", injected, tc.expectedInjected)
		}
		if isInitContainer != tc.isInitContainer {
			t.Errorf("got injection result for is init container %v, but expected %v", isInitContainer, tc.isInitContainer)
		}
	}
}
