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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
)

func TestParseSidecarContainerImage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		testName      string
		pod           corev1.Pod
		expectedPod   corev1.Pod
		expectedImage string
		expectedError error
	}{
		{
			testName: "no declarations present",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "workload",
							Image: "busybox",
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "workload",
							Image: "busybox",
						},
					},
				},
			},
			expectedImage: "",
			expectedError: nil,
		},
		{
			testName: "one declaration present",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  SidecarContainerName,
							Image: "busybox",
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{},
				},
			},
			expectedImage: "busybox",
			expectedError: nil,
		},
		{
			testName: "dual declaration present", // This is invalid but we should cover
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  SidecarContainerName,
							Image: "other",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  SidecarContainerName,
							Image: "busybox",
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{},
					Containers:     []corev1.Container{},
				},
			},
			expectedImage: "busybox",
			expectedError: nil,
		},
		{
			testName: "one declaration present, many containers",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init1",
							Image: "busybox",
						},
						{
							Name:  "init2",
							Image: "busybox",
						},
						{
							Name:  "init3",
							Image: "busybox",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  SidecarContainerName,
							Image: "our-image",
						},
						{
							Name:  "workload1",
							Image: "busybox",
						},
						{
							Name:  "workload2",
							Image: "busybox",
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init1",
							Image: "busybox",
						},
						{
							Name:  "init2",
							Image: "busybox",
						},
						{
							Name:  "init3",
							Image: "busybox",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "workload1",
							Image: "busybox",
						},
						{
							Name:  "workload2",
							Image: "busybox",
						},
					},
				},
			},
			expectedImage: "our-image",
			expectedError: nil,
		},
		{
			// We do not extract image as we dont support init container privately hosted sidecar image declaration.
			testName: "one init declaration present, many containers",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init1",
							Image: "busybox",
						},
						{
							Name:  "init2",
							Image: "busybox",
						},
						{
							Name:  SidecarContainerName,
							Image: "our-image",
						},
						{
							Name:  "init3",
							Image: "busybox",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "workload1",
							Image: "busybox",
						},
						{
							Name:  "workload2",
							Image: "busybox",
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init1",
							Image: "busybox",
						},
						{
							Name:  "init2",
							Image: "busybox",
						},
						{
							Name:  "init3",
							Image: "busybox",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "workload1",
							Image: "busybox",
						},
						{
							Name:  "workload2",
							Image: "busybox",
						},
					},
				},
			},
			expectedImage: "",
			expectedError: nil,
		},
		{
			testName: "dual declaration present, many containers", // This is invalid but we should cover anyway.
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init1",
							Image: "busybox",
						},
						{
							Name:  "init2",
							Image: "busybox",
						},
						{
							Name:  "init3",
							Image: "busybox",
						},
						{
							Name:  SidecarContainerName,
							Image: "other",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "workload1",
							Image: "busybox",
						},
						{
							Name:  "workload2",
							Image: "busybox",
						},
						{
							Name:  SidecarContainerName,
							Image: "custom-image",
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init1",
							Image: "busybox",
						},
						{
							Name:  "init2",
							Image: "busybox",
						},
						{
							Name:  "init3",
							Image: "busybox",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "workload1",
							Image: "busybox",
						},
						{
							Name:  "workload2",
							Image: "busybox",
						},
					},
				},
			},
			expectedImage: "custom-image",
			expectedError: nil,
		},
		{
			testName: "no image present",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: SidecarContainerName,
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: SidecarContainerName,
						},
					},
				},
			},
			expectedImage: "",
			expectedError: errors.New(`could not parse input image: "", error: couldn't parse image name "": invalid reference format`),
		},
	}
	for _, tc := range testCases {
		pod := tc.pod

		image, err := parseSidecarContainerImage(&pod)
		if image != tc.expectedImage {
			t.Errorf(`unexpected image: want: "%s" but got: "%s"`, tc.expectedImage, image)
		}
		if err != nil && tc.expectedError != nil {
			if err.Error() != tc.expectedError.Error() {
				t.Error("for test: ", tc.testName, ", want: ", tc.expectedError.Error(), " but got: ", err.Error())
			}
		} else if err != nil || tc.expectedError != nil {
			// if one of them is nil, both must be nil to pass
			t.Error("for test: ", tc.testName, ", want: ", tc.expectedError, " but got: ", err)
		}

		// verifyPod
		if diff := cmp.Diff(pod, tc.expectedPod); diff != "" {
			t.Errorf(`unexpected pod: diff "%s" want: "%v" but got: "%v"`, diff, tc.expectedPod, pod)
		}
	}
}
