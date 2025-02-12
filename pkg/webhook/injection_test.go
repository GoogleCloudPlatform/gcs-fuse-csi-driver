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
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

var UnsupportedVersion = version.MustParseGeneric("1.28.0")

func TestInjectAsNativeSidecar(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		testName      string
		cpVersion     *version.Version
		nodes         []corev1.Node
		pod           *corev1.Pod
		expect        bool
		expectedError error
	}{
		{
			testName: "test should allow native sidecar",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "true",
					},
				},
			},
			cpVersion: minimumSupportedVersion,
			nodes:     nativeSupportNodes(),
			expect:    true,
		},
		{
			testName: "test should not native sidecar by user request",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "false",
					},
				},
			},
			cpVersion: minimumSupportedVersion,
			nodes:     nativeSupportNodes(),
			expect:    false,
		},
		{
			testName: "test should be native sidecar, user sent malformed request",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "maybe",
					},
				},
			},
			cpVersion: minimumSupportedVersion,
			nodes:     nativeSupportNodes(),
			expect:    true,
		},
		{
			testName: "test should not allow native sidecar, skew",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "true",
					},
				},
			},
			cpVersion: minimumSupportedVersion,
			nodes:     skewVersionNodes(),
			expect:    false,
		},
		{
			testName:  "test should not allow native sidecar, all under 1.29",
			cpVersion: minimumSupportedVersion,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "true",
					},
				},
			},
			nodes:  regularSidecarSupportNodes(),
			expect: false,
		},
		{
			testName:  "test should not allow native sidecar, all nodes are 1.29, cp is 1.28",
			cpVersion: UnsupportedVersion,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "true",
					},
				},
			},
			nodes:  nativeSupportNodes(),
			expect: false,
		},
		{
			testName:  "test no nodes present, native sidecar support false",
			cpVersion: UnsupportedVersion,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "true",
					},
				},
			},
			nodes:  []corev1.Node{},
			expect: false,
		},
		{
			testName:  "test no nodes present, allow native sidecar support true",
			cpVersion: minimumSupportedVersion,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "true",
					},
				},
			},
			nodes:  []corev1.Node{},
			expect: true,
		},
		{
			testName:  "test no nodes present, allow native sidecar support false",
			cpVersion: minimumSupportedVersion,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseNativeSidecarEnableAnnotation: "false",
					},
				},
			},
			nodes:  []corev1.Node{},
			expect: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewSimpleClientset()
			// Create the nodes.
			for _, node := range tc.nodes {
				n := node
				_, err := fakeClient.CoreV1().Nodes().Create(context.Background(), &n, metav1.CreateOptions{})
				if err != nil {
					t.Error("failed to setup/create nodes")
				}
			}

			informerFactory := informers.NewSharedInformerFactoryWithOptions(fakeClient, time.Second*1, informers.WithNamespace(metav1.NamespaceAll))
			lister := informerFactory.Core().V1().Nodes().Lister()
			si := &SidecarInjector{
				NodeLister:    lister,
				ServerVersion: tc.cpVersion,
			}

			stopCh := make(<-chan struct{})
			informerFactory.Start(stopCh)
			informerFactory.WaitForCacheSync(stopCh)

			result, err := si.injectAsNativeSidecar(tc.pod)
			if result != tc.expect {
				t.Errorf("\nfor %s, got native sidecar support to be: %t, but want: %t", tc.testName, result, tc.expect)
				if err != nil {
					t.Errorf("error returned from method: %v", err)
				}
			}
		})
	}
}

func TestSupportsNativeSidecar(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		testName      string
		cpVersion     *version.Version
		nodes         []corev1.Node
		expect        bool
		expectedError error
	}{
		{
			testName:  "test should support native sidecar",
			cpVersion: minimumSupportedVersion,
			nodes:     nativeSupportNodes(),
			expect:    true,
		},
		{
			testName:  "test should not support native sidecar, skew",
			cpVersion: minimumSupportedVersion,
			nodes:     skewVersionNodes(),
			expect:    false,
		},
		{
			testName:  "test should not support native sidecar, all under 1.29",
			cpVersion: minimumSupportedVersion,
			nodes:     regularSidecarSupportNodes(),
			expect:    false,
		},
		{
			testName:  "test should not support native sidecar, all nodes are 1.29, cp is 1.28",
			cpVersion: UnsupportedVersion,
			nodes:     nativeSupportNodes(),
			expect:    false,
		},
		{
			testName:  "test no nodes present, supports native sidecar support false",
			cpVersion: UnsupportedVersion,
			nodes:     []corev1.Node{},
			expect:    false,
		},
		{
			testName:  "test no nodes present, supports native sidecar support true",
			cpVersion: minimumSupportedVersion,
			nodes:     []corev1.Node{},
			expect:    true,
		},
	}
	for _, tc := range testCases {
		fakeClient := fake.NewSimpleClientset()
		// Create the nodes.
		for _, node := range tc.nodes {
			n := node
			_, err := fakeClient.CoreV1().Nodes().Create(context.Background(), &n, metav1.CreateOptions{})
			if err != nil {
				t.Error("failed to setup/create nodes")
			}
		}

		informerFactory := informers.NewSharedInformerFactoryWithOptions(fakeClient, time.Second*1, informers.WithNamespace(metav1.NamespaceAll))
		lister := informerFactory.Core().V1().Nodes().Lister()
		si := &SidecarInjector{
			NodeLister:    lister,
			ServerVersion: tc.cpVersion,
		}

		stopCh := make(<-chan struct{})
		informerFactory.Start(stopCh)
		informerFactory.WaitForCacheSync(stopCh)

		result, err := si.supportsNativeSidecar()
		if result != tc.expect {
			t.Errorf("\nfor %s, got native sidecar support to be: %t, but want: %t", tc.testName, result, tc.expect)
			if err != nil {
				t.Errorf("error returned from method: %v", err)
			}
		}
	}
}

func TestInsert(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		containers   []corev1.Container
		sidecar      corev1.Container
		expectResult []corev1.Container
		idx          int
	}{
		{
			name:       "successful injection at 1st position, 0 element initially",
			containers: []corev1.Container{},
			sidecar: corev1.Container{
				Name: "one",
			},
			expectResult: []corev1.Container{
				{
					Name: "one",
				},
			},
			idx: 0,
		},
		{
			name: "successful injection at second position, 1 element initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
			},
			sidecar: corev1.Container{
				Name: "two",
			},
			expectResult: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "two",
				},
			},
			idx: 1,
		},
		{
			name: "successful injection at second position, 3 elements initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "three",
				},
				{
					Name: "four",
				},
			},
			sidecar: corev1.Container{
				Name: "two",
			},
			expectResult: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "two",
				},
				{
					Name: "three",
				},
				{
					Name: "four",
				},
			},
			idx: 1,
		},

		{
			name: "successful injection at first position, 3 elements initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "two",
				},
				{
					Name: "three",
				},
			},
			sidecar: corev1.Container{
				Name: "sidecar",
			},
			expectResult: []corev1.Container{
				{
					Name: "sidecar",
				},
				{
					Name: "one",
				},
				{
					Name: "two",
				},
				{
					Name: "three",
				},
			},
			idx: 0,
		},
		{
			name: "successful injection at last position, 3 elements initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "two",
				},
				{
					Name: "three",
				},
			},
			sidecar: corev1.Container{
				Name: "four",
			},
			expectResult: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "two",
				},
				{
					Name: "three",
				},
				{
					Name: "four",
				},
			},
			idx: 3,
		},
	}
	for _, tc := range testCases {
		result := insert(tc.containers, tc.sidecar, tc.idx)
		if diff := cmp.Diff(tc.expectResult, result); diff != "" {
			t.Errorf(`for test "%s", got different results (-expect, +got):\n"%s"`, tc.name, diff)
		}
	}
}

func TestGetInjectIndex(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		containers []corev1.Container
		idx        int
	}{
		{
			name:       "injection at first position, 0 element initially",
			containers: []corev1.Container{},
			idx:        0,
		},
		{
			name: "injection at first position, 1 element initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
			},
			idx: 0,
		},
		{
			name: "injection at first position, 3 elements initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "two",
				},
				{
					Name: "three",
				},
			},
			idx: 0,
		},
		{
			name: "injection at second position, 3 elements initially",
			containers: []corev1.Container{
				istioContainer,
				{
					Name: "two",
				},
				{
					Name: "three",
				},
			},
			idx: 1,
		},
		{
			name: "injection at third position, 3 elements initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
				istioContainer,
				{
					Name: "three",
				},
			},
			idx: 2,
		},
		{
			name: "injection at last position, 3 elements initially",
			containers: []corev1.Container{
				{
					Name: "one",
				},
				{
					Name: "two",
				},
				istioContainer,
			},
			idx: 3,
		},
	}
	for _, tc := range testCases {
		idx := getInjectIndex(tc.containers)
		if idx != tc.idx {
			t.Errorf(`expected injection to be at index "%d" but got "%d"`, tc.idx, idx)
		}
	}
}

func TestInjectMetadataPrefetchSidecar(t *testing.T) {
	t.Parallel()

	limits, requests := prepareResourceList(getMetadataPrefetchConfig("fake-image"))

	testCases := []struct {
		testName      string
		pod           *corev1.Pod
		config        Config
		nativeSidecar *bool
		expectedPod   *corev1.Pod
	}{
		{
			testName: "no injection",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "one",
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "one",
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar present, no injection",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar present, no injection due to different driver",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: "other-csi",
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "false",
									},
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: "other-csi",
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "false",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar present, no injection with volume annotation",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "false",
									},
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "false",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar not present, already injected",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:            MetadataPrefetchSidecarName,
							Image:           "my-private-image",
							SecurityContext: GetSecurityContext(),
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:            MetadataPrefetchSidecarName,
							Image:           "my-private-image",
							SecurityContext: GetSecurityContext(),
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar not present, privately hosted image",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  MetadataPrefetchSidecarName,
							Image: "my-private-image",
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar present, injection successful",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name:            MetadataPrefetchSidecarName,
							Env:             []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}},
							RestartPolicy:   ptr.To(corev1.ContainerRestartPolicyAlways),
							SecurityContext: GetSecurityContext(),
							Resources: corev1.ResourceRequirements{
								Requests: requests,
								Limits:   limits,
							},
							VolumeMounts: []corev1.VolumeMount{{Name: "my-volume", ReadOnly: true, MountPath: "/volumes/my-volume"}},
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar present with many volumes and config, injection successful",
			config:   *FakeConfig(),
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
						{
							Name: "my-other-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "false",
									},
								},
							},
						},
						{
							Name: "other-csi-vol",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: "other-csi",
								},
							},
						},
						{
							Name: "my-emptydir",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name:            MetadataPrefetchSidecarName,
							Image:           FakeConfig().MetadataContainerImage,
							Env:             []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}},
							RestartPolicy:   ptr.To(corev1.ContainerRestartPolicyAlways),
							SecurityContext: GetSecurityContext(),
							Resources: corev1.ResourceRequirements{
								Requests: requests,
								Limits:   limits,
							},
							VolumeMounts: []corev1.VolumeMount{{Name: "my-volume", ReadOnly: true, MountPath: "/volumes/my-volume"}},
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
						{
							Name: "my-other-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "false",
									},
								},
							},
						},
						{
							Name: "other-csi-vol",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: "other-csi",
								},
							},
						},
						{
							Name: "my-emptydir",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar present & using privately hosted image, injection successful",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  MetadataPrefetchSidecarName,
							Image: "my-private-image",
						},
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name:            MetadataPrefetchSidecarName,
							Env:             []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}},
							RestartPolicy:   ptr.To(corev1.ContainerRestartPolicyAlways),
							SecurityContext: GetSecurityContext(),
							Resources: corev1.ResourceRequirements{
								Requests: requests,
								Limits:   limits,
							},
							Image:        "my-private-image",
							VolumeMounts: []corev1.VolumeMount{{Name: "my-volume", ReadOnly: true, MountPath: "/volumes/my-volume"}},
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			testName: "fuse sidecar present & using privately hosted image, injection fail",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  MetadataPrefetchSidecarName,
							Image: "a:a:a:a",
						},
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name: "two",
						},
						{
							Name: "three",
						},
					},
					Containers: []corev1.Container{
						{
							Name: "workload-one",
						},
						{
							Name: "workload-two",
						},
						{
							Name: "workload-three",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "my-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: gcsFuseCsiDriverName,
									VolumeAttributes: map[string]string{
										gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
									},
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			t.Parallel()
			if tc.nativeSidecar == nil {
				tc.nativeSidecar = ptr.To(true)
			}
			si := SidecarInjector{}
			si.injectMetadataPrefetchSidecarContainer(tc.pod, &tc.config, *tc.nativeSidecar)
			if !reflect.DeepEqual(tc.pod, tc.expectedPod) {
				t.Errorf(`failed to run %s, expected: "%v", but got "%v". Diff: %s`, tc.testName, tc.expectedPod, tc.pod, cmp.Diff(tc.expectedPod, tc.pod))
			}
		})
	}
}
