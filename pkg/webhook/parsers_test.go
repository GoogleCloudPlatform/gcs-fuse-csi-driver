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

func TestExtractImageAndDeleteContainer(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		pod           corev1.Pod
		expectedPod   corev1.Pod
		expectedImage string
		expectedError error
	}{
		{
			name: "no declarations present",
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
			name: "one declaration present",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
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
			name: "dual declaration present", // This is invalid but we should cover
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
							Image: "busybox2",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  GcsFuseSidecarName,
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
			expectedImage: "busybox2",
			expectedError: nil,
		},
		{
			name: "one declaration present, many containers",
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
							Name:  GcsFuseSidecarName,
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
			name: "one init declaration present, many containers",
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
							Name:  GcsFuseSidecarName,
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
			expectedImage: "our-image",
			expectedError: nil,
		},
		{
			name: "dual declaration present, many containers", // This is invalid but we should cover anyway.
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
							Name:  GcsFuseSidecarName,
							Image: "another-one",
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
							Name:  GcsFuseSidecarName,
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
			expectedImage: "another-one",
			expectedError: nil,
		},
		{
			name: "no image present",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
					},
				},
			},
			expectedPod: corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{},
				},
			},
			expectedImage: "",
			expectedError: errors.New(`could not parse input image: "", error: couldn't parse image name "": invalid reference format`),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pod := tc.pod

			image, err := ExtractImageAndDeleteContainer(&pod.Spec, GcsFuseSidecarName)
			if image != tc.expectedImage {
				t.Errorf(`unexpected image: want: "%s" but got: "%s"`, tc.expectedImage, image)
			}
			if err != nil && tc.expectedError != nil {
				if err.Error() != tc.expectedError.Error() {
					t.Error("for test: ", tc.name, ", want: ", tc.expectedError.Error(), " but got: ", err.Error())
				}
			} else if err != nil || tc.expectedError != nil {
				// if one of them is nil, both must be nil to pass
				t.Error("for test: ", tc.name, ", want: ", tc.expectedError, " but got: ", err)
			}

			// verifyPod
			if diff := cmp.Diff(pod, tc.expectedPod); diff != "" {
				t.Errorf(`unexpected pod: diff "%s" want: "%v" but got: "%v"`, diff, tc.expectedPod, pod)
			}
		})
	}
}
