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
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

var UnsupportedVersion = version.MustParseGeneric("1.28.0")

func getDefaultMetadataPrefetchConfig(image string) *Config {
	return &Config{
		CPURequest:              resource.MustParse("10m"),
		CPULimit:                resource.MustParse("50m"),
		MemoryRequest:           resource.MustParse("10Mi"),
		MemoryLimit:             resource.MustParse("10Mi"),
		EphemeralStorageRequest: resource.MustParse("10Mi"),
		EphemeralStorageLimit:   resource.MustParse("10Mi"),
		ImagePullPolicy:         "Always",
		ContainerImage:          image,
	}
}

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

	limits, requests := prepareResourceList(getDefaultMetadataPrefetchConfig("fake-image"))
	customLimits, customRequests := prepareResourceList(LoadConfig("fake-image", "Always", "250m", "250m", "20Mi", "20Mi", "5Gi", "5Gi"))

	testCases := []struct {
		testName      string
		pod           *corev1.Pod
		config        Config
		nativeSidecar *bool
		expectedPod   *corev1.Pod
		sc            *storagev1.StorageClass
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
							Image:           FakePrefetchConfig().ContainerImage,
							ImagePullPolicy: corev1.PullPolicy(FakePrefetchConfig().ImagePullPolicy),
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
			testName: "fuse sidecar present, injection successful, with custom memory limits and requests",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: generateAnnotationsFromConfig(LoadConfig("fake-image", "Always", "250m", "250m", "20Mi", "20Mi", "5Gi", "5Gi"), sidecarPrefixMap[MetadataPrefetchSidecarName]),
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"gke-gcsfuse/metadata-prefetch-container-image":           "fake-image",
						"gke-gcsfuse/metadata-prefetch-cpu-limit":                 "250m",
						"gke-gcsfuse/metadata-prefetch-cpu-request":               "250m",
						"gke-gcsfuse/metadata-prefetch-ephemeral-storage-limit":   "5Gi",
						"gke-gcsfuse/metadata-prefetch-ephemeral-storage-request": "5Gi",
						"gke-gcsfuse/metadata-prefetch-image-pull-policy":         "Always",
						"gke-gcsfuse/metadata-prefetch-memory-limit":              "20Mi",
						"gke-gcsfuse/metadata-prefetch-memory-request":            "20Mi",
					},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name:            MetadataPrefetchSidecarName,
							Image:           "fake-image",
							ImagePullPolicy: corev1.PullPolicy(FakeConfig().ImagePullPolicy),
							Env:             []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}},
							RestartPolicy:   ptr.To(corev1.ContainerRestartPolicyAlways),
							SecurityContext: GetSecurityContext(),
							Resources: corev1.ResourceRequirements{
								Requests: customRequests,
								Limits:   customLimits,
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
			testName: "fuse sidecar present, injection successful, with custom memory requests no limit provided",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: generateAnnotationsFromConfig(&Config{
						ContainerImage:          "fake-image",
						ImagePullPolicy:         "Always",
						CPURequest:              resource.MustParse("250m"),
						CPULimit:                resource.MustParse("250m"),
						MemoryRequest:           resource.MustParse("20Mi"),
						EphemeralStorageRequest: resource.MustParse("5Gi"),
						EphemeralStorageLimit:   resource.MustParse("5Gi"),
					}, sidecarPrefixMap[MetadataPrefetchSidecarName]),
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"gke-gcsfuse/metadata-prefetch-container-image":           "fake-image",
						"gke-gcsfuse/metadata-prefetch-cpu-limit":                 "250m",
						"gke-gcsfuse/metadata-prefetch-cpu-request":               "250m",
						"gke-gcsfuse/metadata-prefetch-ephemeral-storage-limit":   "5Gi",
						"gke-gcsfuse/metadata-prefetch-ephemeral-storage-request": "5Gi",
						"gke-gcsfuse/metadata-prefetch-image-pull-policy":         "Always",
						"gke-gcsfuse/metadata-prefetch-memory-request":            "20Mi",
					},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name:            MetadataPrefetchSidecarName,
							Image:           FakePrefetchConfig().ContainerImage,
							ImagePullPolicy: corev1.PullPolicy(FakeConfig().ImagePullPolicy),
							Env:             []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}},
							RestartPolicy:   ptr.To(corev1.ContainerRestartPolicyAlways),
							SecurityContext: GetSecurityContext(),
							Resources: corev1.ResourceRequirements{
								Requests: customRequests,
								Limits:   customLimits,
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
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: generateAnnotationsFromConfig(FakePrefetchConfig(), sidecarPrefixMap[MetadataPrefetchSidecarName]),
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"gke-gcsfuse/metadata-prefetch-container-image":           "fake-image",
						"gke-gcsfuse/metadata-prefetch-cpu-limit":                 "50m",
						"gke-gcsfuse/metadata-prefetch-cpu-request":               "10m",
						"gke-gcsfuse/metadata-prefetch-ephemeral-storage-limit":   "10Mi",
						"gke-gcsfuse/metadata-prefetch-ephemeral-storage-request": "10Mi",
						"gke-gcsfuse/metadata-prefetch-image-pull-policy":         "Always",
						"gke-gcsfuse/metadata-prefetch-memory-limit":              "10Mi",
						"gke-gcsfuse/metadata-prefetch-memory-request":            "10Mi",
					},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
						},
						{
							Name:            MetadataPrefetchSidecarName,
							Image:           FakePrefetchConfig().ContainerImage,
							ImagePullPolicy: corev1.PullPolicy(FakePrefetchConfig().ImagePullPolicy),
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
							Image:           "my-private-image",
							ImagePullPolicy: corev1.PullPolicy(FakePrefetchConfig().ImagePullPolicy),
							VolumeMounts:    []corev1.VolumeMount{{Name: "my-volume", ReadOnly: true, MountPath: "/volumes/my-volume"}},
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
			si := SidecarInjector{MetadataPrefetchConfig: FakePrefetchConfig()}
			err := si.injectSidecarContainer(MetadataPrefetchSidecarName, tc.pod, *tc.nativeSidecar, nil /*credentialConfig*/)
			t.Logf("%s resulted in %v and error: %v", tc.testName, err == nil, err)
			if !reflect.DeepEqual(tc.pod, tc.expectedPod) {
				t.Errorf(`failed to run %s, expected: "%v", but got "%v". Diff: %s`, tc.testName, tc.expectedPod, tc.pod, cmp.Diff(tc.expectedPod, tc.pod))
			}
		})
	}
}

func generateAnnotationsFromConfig(config *Config, prefix string) map[string]string {
	annotations := make(map[string]string)
	if config.ImagePullPolicy != "" {
		annotations[prefix+"image-pull-policy"] = config.ImagePullPolicy
	}
	if config.CPULimit.Format != "" {
		annotations[prefix+"cpu-limit"] = config.CPULimit.String()
	}
	if config.CPURequest.Format != "" {
		annotations[prefix+"cpu-request"] = config.CPURequest.String()
	}
	if config.ContainerImage != "" {
		annotations[prefix+"container-image"] = config.ContainerImage
	}
	if config.MemoryRequest.Format != "" {
		annotations[prefix+"memory-request"] = config.MemoryRequest.String()
	}
	if config.MemoryLimit.Format != "" {
		annotations[prefix+"memory-limit"] = config.MemoryLimit.String()
	}
	if config.EphemeralStorageRequest.Format != "" {
		annotations[prefix+"ephemeral-storage-request"] = config.EphemeralStorageRequest.String()
	}
	if config.EphemeralStorageLimit.Format != "" {
		annotations[prefix+"ephemeral-storage-limit"] = config.EphemeralStorageLimit.String()
	}

	return annotations
}
func TestInjectMetadataPrefetchSidecarWithSC(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		testName      string
		pod           *corev1.Pod
		config        Config
		nativeSidecar *bool
		expectedPod   *corev1.Pod
		sc            *storagev1.StorageClass
		pv            *corev1.PersistentVolume
		pvc           *corev1.PersistentVolumeClaim
	}{
		{
			testName:    "fuse sidecar present, injection successful with sc",
			pod:         getInputPodForMetadataPrefetchWitSCTest("1", "true", false),
			expectedPod: getExpectedPodForMetadataPrefetchWithSCTest("1", true, "true", false),
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-pv1",
				},
				Spec: corev1.PersistentVolumeSpec{
					StorageClassName: "my-sc1",
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: gcsFuseCsiDriverName,
						},
					},
				},
			},
			pvc: getPVCForMetadataPrefetchWitSCTest("1"),
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-sc1",
				},
				Parameters: map[string]string{
					"gcsfuseMetadataPrefetchOnMount": "true",
				},
			},
		},
		{
			testName:    "fuse sidecar present, no injection with no sc",
			pod:         getInputPodForMetadataPrefetchWitSCTest("4", "true", false),
			expectedPod: getExpectedPodForMetadataPrefetchWithSCTest("4", false, "true", false),
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-pv4",
				},
				Spec: corev1.PersistentVolumeSpec{
					StorageClassName: "",
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: gcsFuseCsiDriverName,
						},
					},
				},
			},
			pvc: getPVCForMetadataPrefetchWitSCTest("4"),
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-sc4",
				},
			},
		},
		{
			testName:    "fuse sidecar present, no injection, one volume references prefetch and disables it",
			pod:         getInputPodForMetadataPrefetchWitSCTest("2", "true", true),
			expectedPod: getExpectedPodForMetadataPrefetchWithSCTest("2", false, "true", true),
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-pv2",
				},
				Spec: corev1.PersistentVolumeSpec{
					StorageClassName: "",
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: gcsFuseCsiDriverName,
						},
					},
				},
			},
			pvc: getPVCForMetadataPrefetchWitSCTest("2"),
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-sc2",
				},
				Parameters: map[string]string{
					"gcsfuseMetadataPrefetchOnMount": "true",
				},
			},
		},
		{
			testName:    "fuse sidecar present, no injection, sc disables",
			pod:         getInputPodForMetadataPrefetchWitSCTest("5", "true", false),
			expectedPod: getExpectedPodForMetadataPrefetchWithSCTest("5", false, "true", false),
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-pv5",
				},
				Spec: corev1.PersistentVolumeSpec{
					StorageClassName: "my-sc5",
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: gcsFuseCsiDriverName,
						},
					},
				},
			},
			pvc: getPVCForMetadataPrefetchWitSCTest("5"),
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-sc5",
				},
				Parameters: map[string]string{
					"gcsfuseMetadataPrefetchOnMount": "false",
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
			si := SidecarInjector{MetadataPrefetchConfig: FakePrefetchConfig()}
			si.Config = &Config{EnableGcsfuseProfiles: true}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fakeClient := fake.NewSimpleClientset()
			informer := informers.NewSharedInformerFactory(fakeClient, resyncDuration)
			si.ScLister = informer.Storage().V1().StorageClasses().Lister()
			si.PvLister = informer.Core().V1().PersistentVolumes().Lister()
			si.PvcLister = informer.Core().V1().PersistentVolumeClaims().Lister()
			informer.Start(ctx.Done())
			_, err := fakeClient.StorageV1().StorageClasses().Create(context.TODO(), tc.sc, metav1.CreateOptions{})

			if err != nil {
				t.Fatalf("failed to setup test SC: %v", err)
			}
			_, err = fakeClient.CoreV1().PersistentVolumes().Create(context.TODO(), tc.pv, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to setup test PV: %v", err)
			}
			_, err = fakeClient.CoreV1().PersistentVolumes().Create(context.TODO(), &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pv-mention-prefetch",
				}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to setup test PV: %v", err)
			}

			_, err = fakeClient.CoreV1().PersistentVolumeClaims("").Create(context.TODO(), tc.pvc, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to setup test PVC: %v", err)
			}
			_, err = fakeClient.CoreV1().PersistentVolumeClaims("").Create(context.TODO(), &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pvc-mention-prefetch",
				}, Spec: corev1.PersistentVolumeClaimSpec{
					VolumeName: "pv-mention-prefetch",
				}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to setup test PVC: %v", err)
			}

			informer.WaitForCacheSync(ctx.Done())

			err = si.injectSidecarContainer(MetadataPrefetchSidecarName, tc.pod, *tc.nativeSidecar, nil)
			t.Logf("%s resulted in %v and error: %v", tc.testName, err == nil, err)
			if !reflect.DeepEqual(tc.pod, tc.expectedPod) {
				t.Errorf(`failed to run %s, expected: "%v", but got "%v". Diff: %s`, tc.testName, tc.expectedPod, tc.pod, cmp.Diff(tc.expectedPod, tc.pod))
			}
		})
	}
}

func getInputPodForMetadataPrefetchWitSCTest(modifier string, enableProfiles string, includeVolumeThatMentionsPrefetch bool) *corev1.Pod {
	volumes := []corev1.Volume{
		{
			Name: "my-volume",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "my-pvc" + modifier,
				},
			},
		},
	}
	if includeVolumeThatMentionsPrefetch {
		volumes = append(volumes, corev1.Volume{
			Name: "pvc-mention-prefetch",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "pvc-mentions-prefetch",
				},
			},
		})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"gke-gcsfuse/profiles": enableProfiles,
			},
		},
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
			Volumes: volumes,
		},
	}
}

func getExpectedPodForMetadataPrefetchWithSCTest(modifier string, enableMP bool, enableProfiles string, includeVolumeThatMentionsPrefetch bool) *corev1.Pod {
	volumes := []corev1.Volume{
		{
			Name: "my-volume",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "my-pvc" + modifier,
				},
			},
		},
	}
	if includeVolumeThatMentionsPrefetch {
		volumes = append(volumes, corev1.Volume{
			Name: "pvc-mention-prefetch",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "pvc-mentions-prefetch",
				},
			},
		})
	}
	initContainerArray := []corev1.Container{
		{
			Name: GcsFuseSidecarName,
		},
		{
			Name: "two",
		},
		{
			Name: "three",
		},
	}
	if enableMP {
		limits, requests := prepareResourceList(getDefaultMetadataPrefetchConfig("fake-image"))
		initContainerArray = append(initContainerArray[:1], append([]corev1.Container{
			{
				Name:            MetadataPrefetchSidecarName,
				Env:             []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}},
				RestartPolicy:   ptr.To(corev1.ContainerRestartPolicyAlways),
				SecurityContext: GetSecurityContext(),
				Image:           FakePrefetchConfig().ContainerImage,
				ImagePullPolicy: corev1.PullPolicy(FakePrefetchConfig().ImagePullPolicy),
				Resources: corev1.ResourceRequirements{
					Requests: requests,
					Limits:   limits,
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "my-volume",
						ReadOnly:  true,
						MountPath: "/volumes/my-volume",
					},
				},
			},
		}, initContainerArray[1:]...)...)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"gke-gcsfuse/profiles": enableProfiles,
			},
		},
		Spec: corev1.PodSpec{
			InitContainers: initContainerArray,
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
			Volumes: volumes,
		},
	}
}
func getPVCForMetadataPrefetchWitSCTest(modifier string) *corev1.PersistentVolumeClaim {
	sc := fmt.Sprintf("my-sc%s", modifier)

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("my-pvc%s", modifier),
		},
		Spec: corev1.PersistentVolumeClaimSpec{

			StorageClassName: &sc,
			VolumeName:       fmt.Sprintf("my-pv%s", modifier),
		},
	}
}
