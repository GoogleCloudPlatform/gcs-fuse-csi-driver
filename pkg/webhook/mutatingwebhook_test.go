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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var istioContainer = corev1.Container{
	Name: IstioSidecarName,
}
var testNamespace = "default"

func TestPrepareConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		prefix      string
		annotations map[string]string
		wantConfig  *Config
		expectErr   bool
	}{
		{
			name:   "use default values if no annotation is found",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation: "true",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                FakeConfig().CPULimit,
				CPURequest:              FakeConfig().CPURequest,
				MemoryLimit:             FakeConfig().MemoryLimit,
				MemoryRequest:           FakeConfig().MemoryRequest,
				EphemeralStorageLimit:   FakeConfig().EphemeralStorageLimit,
				EphemeralStorageRequest: FakeConfig().EphemeralStorageRequest,
			},
			expectErr: false,
		},
		{
			name:   "only limits are specified",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:   "true",
				cpuLimitAnnotation:              "500m",
				memoryLimitAnnotation:           "1Gi",
				ephemeralStorageLimitAnnotation: "50Gi",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                resource.MustParse("500m"),
				CPURequest:              resource.MustParse("500m"),
				MemoryLimit:             resource.MustParse("1Gi"),
				MemoryRequest:           resource.MustParse("1Gi"),
				EphemeralStorageLimit:   resource.MustParse("50Gi"),
				EphemeralStorageRequest: resource.MustParse("50Gi"),
			},
			expectErr: false,
		},
		{
			name:   "only requests are specified",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:     "true",
				cpuRequestAnnotation:              "500m",
				memoryRequestAnnotation:           "1Gi",
				ephemeralStorageRequestAnnotation: "50Gi",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                resource.MustParse("500m"),
				CPURequest:              resource.MustParse("500m"),
				MemoryLimit:             resource.MustParse("1Gi"),
				MemoryRequest:           resource.MustParse("1Gi"),
				EphemeralStorageLimit:   resource.MustParse("50Gi"),
				EphemeralStorageRequest: resource.MustParse("50Gi"),
			},
			expectErr: false,
		},
		{
			name:   "limits are set to '0'",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:   "true",
				cpuLimitAnnotation:              "0",
				memoryLimitAnnotation:           "0",
				ephemeralStorageLimitAnnotation: "0",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                resource.Quantity{},
				CPURequest:              FakeConfig().CPURequest,
				MemoryLimit:             resource.Quantity{},
				MemoryRequest:           FakeConfig().MemoryRequest,
				EphemeralStorageLimit:   resource.Quantity{},
				EphemeralStorageRequest: FakeConfig().EphemeralStorageRequest,
			},
			expectErr: false,
		},
		{
			name:   "requests are set to '0'",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:     "true",
				cpuRequestAnnotation:              "0",
				memoryRequestAnnotation:           "0",
				ephemeralStorageRequestAnnotation: "0",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                resource.Quantity{},
				CPURequest:              resource.Quantity{},
				MemoryLimit:             resource.Quantity{},
				MemoryRequest:           resource.Quantity{},
				EphemeralStorageLimit:   resource.Quantity{},
				EphemeralStorageRequest: resource.Quantity{},
			},
			expectErr: false,
		},
		{
			name:   "requests and limits are explicitly set",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:     "true",
				cpuLimitAnnotation:                "500m",
				memoryLimitAnnotation:             "1Gi",
				ephemeralStorageLimitAnnotation:   "50Gi",
				cpuRequestAnnotation:              "100m",
				memoryRequestAnnotation:           "500Mi",
				ephemeralStorageRequestAnnotation: "10Gi",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                resource.MustParse("500m"),
				CPURequest:              resource.MustParse("100m"),
				MemoryLimit:             resource.MustParse("1Gi"),
				MemoryRequest:           resource.MustParse("500Mi"),
				EphemeralStorageLimit:   resource.MustParse("50Gi"),
				EphemeralStorageRequest: resource.MustParse("10Gi"),
			},
			expectErr: false,
		},
		{
			name:   "requests and limits are explicitly set with '0' limits",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:     "true",
				cpuLimitAnnotation:                "0",
				memoryLimitAnnotation:             "0",
				ephemeralStorageLimitAnnotation:   "0",
				cpuRequestAnnotation:              "100m",
				memoryRequestAnnotation:           "500Mi",
				ephemeralStorageRequestAnnotation: "10Gi",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                resource.Quantity{},
				CPURequest:              resource.MustParse("100m"),
				MemoryLimit:             resource.Quantity{},
				MemoryRequest:           resource.MustParse("500Mi"),
				EphemeralStorageLimit:   resource.Quantity{},
				EphemeralStorageRequest: resource.MustParse("10Gi"),
			},
			expectErr: false,
		},
		{
			name:   "requests and limits are explicitly set with '0' requests",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:     "true",
				cpuLimitAnnotation:                "500m",
				memoryLimitAnnotation:             "1Gi",
				ephemeralStorageLimitAnnotation:   "50Gi",
				cpuRequestAnnotation:              "0",
				memoryRequestAnnotation:           "0",
				ephemeralStorageRequestAnnotation: "0",
			},
			wantConfig: &Config{
				ContainerImage:          FakeConfig().ContainerImage,
				ImagePullPolicy:         FakeConfig().ImagePullPolicy,
				CPULimit:                resource.MustParse("500m"),
				CPURequest:              resource.Quantity{},
				MemoryLimit:             resource.MustParse("1Gi"),
				MemoryRequest:           resource.Quantity{},
				EphemeralStorageLimit:   resource.MustParse("50Gi"),
				EphemeralStorageRequest: resource.Quantity{},
			},
			expectErr: false,
		},
		{
			name:   "metadata prefetch requests and limits are provided",
			prefix: sidecarPrefixMap[MetadataPrefetchSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:           "true",
				cpuLimitAnnotation:                      "500m",
				memoryLimitAnnotation:                   "1Gi",
				ephemeralStorageLimitAnnotation:         "50Gi",
				cpuRequestAnnotation:                    "0",
				memoryRequestAnnotation:                 "0",
				ephemeralStorageRequestAnnotation:       "0",
				metadataPrefetchMemoryLimitAnnotation:   "20Mi",
				metadataPrefetchMemoryRequestAnnotation: "20Mi",
			},
			wantConfig: &Config{
				ContainerImage:          FakePrefetchConfig().ContainerImage,
				ImagePullPolicy:         FakePrefetchConfig().ImagePullPolicy,
				CPULimit:                resource.MustParse("50m"),
				CPURequest:              resource.MustParse("10m"),
				MemoryLimit:             resource.MustParse("20Mi"),
				MemoryRequest:           resource.MustParse("20Mi"),
				EphemeralStorageLimit:   resource.MustParse("10Mi"),
				EphemeralStorageRequest: resource.MustParse("10Mi"),
			},
			expectErr: false,
		},
		{
			name:   "metadata prefetch memory limits are provided",
			prefix: sidecarPrefixMap[MetadataPrefetchSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:         "true",
				cpuLimitAnnotation:                    "500m",
				memoryLimitAnnotation:                 "1Gi",
				ephemeralStorageLimitAnnotation:       "50Gi",
				cpuRequestAnnotation:                  "0",
				memoryRequestAnnotation:               "0",
				ephemeralStorageRequestAnnotation:     "0",
				metadataPrefetchMemoryLimitAnnotation: "20Mi",
			},
			wantConfig: &Config{
				ContainerImage:          FakePrefetchConfig().ContainerImage,
				ImagePullPolicy:         FakePrefetchConfig().ImagePullPolicy,
				CPULimit:                resource.MustParse("50m"),
				CPURequest:              resource.MustParse("10m"),
				MemoryLimit:             resource.MustParse("20Mi"),
				MemoryRequest:           resource.MustParse("20Mi"),
				EphemeralStorageLimit:   resource.MustParse("10Mi"),
				EphemeralStorageRequest: resource.MustParse("10Mi"),
			},
			expectErr: false,
		},
		{
			name:   "metadata prefetch memory requests are provided",
			prefix: sidecarPrefixMap[MetadataPrefetchSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation:           "true",
				cpuLimitAnnotation:                      "500m",
				memoryLimitAnnotation:                   "1Gi",
				ephemeralStorageLimitAnnotation:         "50Gi",
				cpuRequestAnnotation:                    "0",
				memoryRequestAnnotation:                 "0",
				ephemeralStorageRequestAnnotation:       "0",
				metadataPrefetchMemoryRequestAnnotation: "20Mi",
			},
			wantConfig: &Config{
				ContainerImage:          FakePrefetchConfig().ContainerImage,
				ImagePullPolicy:         FakePrefetchConfig().ImagePullPolicy,
				CPULimit:                resource.MustParse("50m"),
				CPURequest:              resource.MustParse("10m"),
				MemoryLimit:             resource.MustParse("20Mi"),
				MemoryRequest:           resource.MustParse("20Mi"),
				EphemeralStorageLimit:   resource.MustParse("10Mi"),
				EphemeralStorageRequest: resource.MustParse("10Mi"),
			},
			expectErr: false,
		},
		{
			name:   "invalid resource Quantity should throw error",
			prefix: sidecarPrefixMap[GcsFuseSidecarName],
			annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation: "true",
				cpuLimitAnnotation:            "invalid",
			},
			wantConfig: nil,
			expectErr:  true,
		},
	}

	for _, tc := range testCases {
		si := SidecarInjector{
			Client:                 nil,
			Config:                 FakeConfig(),
			MetadataPrefetchConfig: FakePrefetchConfig(),
			Decoder:                admission.NewDecoder(runtime.NewScheme()),
		}
		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: tc.annotations,
			},
		}
		gotConfig, gotErr := si.prepareConfig(tc.prefix, pod)
		if tc.expectErr != (gotErr != nil) {
			t.Errorf(`for "%s", expect error: %v, but got error: %v`, tc.name, tc.expectErr, gotErr)
		}

		if diff := cmp.Diff(gotConfig, tc.wantConfig); diff != "" {
			t.Errorf(`for "%s", config differ (-got, +want)\n%s`, tc.name, diff)
		}
	}
}

func TestValidateMutatingWebhookResponse(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		inputPod     *corev1.Pod
		operation    admissionv1.Operation
		wantResponse admission.Response
		nodes        []corev1.Node
	}{
		{
			name:         "Empty request test.",
			inputPod:     nil,
			wantResponse: admission.Errored(http.StatusBadRequest, errors.New("there is no content to decode")),
		},
		{
			name:      "Invalid resource request test.",
			operation: admissionv1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers:                    []corev1.Container{},
					Volumes:                       []corev1.Volume{},
					TerminationGracePeriodSeconds: ptr.To[int64](60),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation: "true",
						cpuLimitAnnotation:            "invalid",
					},
				},
			},
			wantResponse: admission.Errored(http.StatusBadRequest, errors.New("failed to parse sidecar container resource allocation from pod annotations: quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$'")),
		},
		{
			name:      "Different operation test.",
			operation: admissionv1.Update,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			wantResponse: admission.Allowed(fmt.Sprintf("No injection required for operation %v.", admissionv1.Update)),
		},
		{
			name:      "Annotation key not found test.",
			operation: admissionv1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			wantResponse: admission.Allowed(fmt.Sprintf("The annotation key %q is not found, no injection required.", GcsFuseVolumeEnableAnnotation)),
		},
		{
			name:      "Sidecar already injected test first index.",
			operation: admissionv1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation: "true",
					},
				},
			},
			wantResponse: admission.Allowed("The sidecar container was injected, no injection required."),
		},
		{
			name:      "Sidecar already injected test other index.",
			operation: admissionv1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{getWorkloadSpec("workload"), GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation: "true",
					},
				},
			},
			wantResponse: admission.Allowed("The sidecar container was injected, no injection required."),
		},
		{
			name:      "Sidecar already injected in initContainer list test.",
			operation: admissionv1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{getWorkloadSpec("init-workload"), GetSidecarContainerSpec(FakeConfig())},
					Containers:     []corev1.Container{getWorkloadSpec("workload")},
					Volumes:        GetSidecarContainerVolumeSpec(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation: "true",
					},
				},
			},
			wantResponse: admission.Allowed("The sidecar container was injected, no injection required."),
		},
		{
			name:         "native container injection successful test with multiple sidecar entries present",
			operation:    admissionv1.Create,
			inputPod:     getDuplicateDeclarationPodSpec(),
			wantResponse: generatePatch(t, getDuplicateDeclarationPodSpec(), getDuplicateDeclarationPodSpecResponse(false)),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "regular container injection successful test with custom image",
			operation:    admissionv1.Create,
			inputPod:     getDuplicateDeclarationPodSpec(),
			wantResponse: generatePatch(t, getDuplicateDeclarationPodSpec(), getDuplicateDeclarationPodSpecResponse(false)),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "container injection successful test with multiple sidecar entries present",
			operation:    admissionv1.Create,
			inputPod:     getDuplicateDeclarationPodSpec(),
			wantResponse: generatePatch(t, getDuplicateDeclarationPodSpec(), getDuplicateDeclarationPodSpecResponse(false)),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "regular container injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPod(),
			wantResponse: wantResponse(t, false, false, false),
			nodes:        skewVersionNodes(),
		},
		{
			name:         "native container set via annotation injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, false, "true"),
			wantResponse: wantResponse(t, false, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container set via annotation injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(true, true, "true"),
			wantResponse: wantResponse(t, true, true, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container set via annotation injection successful with custom image test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(true, false, "true"),
			wantResponse: wantResponse(t, true, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "regular container set via annotation injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, false, "false"),
			wantResponse: wantResponse(t, false, false, false),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container set via invalid annotation injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, false, "maybe"),
			wantResponse: wantResponse(t, false, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container set via annotation injection unsupported test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, false, "true"),
			wantResponse: wantResponse(t, false, false, false),
			nodes:        skewVersionNodes(),
		},
		{
			name:         "Injection with custom sidecar container image successful test with regular nodes.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithCustomImage(false),
			wantResponse: wantResponse(t, true, false, false),
			nodes:        regularSidecarSupportNodes(),
		},
		{
			name:         "Injection with custom native sidecar container image successful as regular container.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithSettings(true, true),
			wantResponse: wantResponse(t, true, true, false),
			nodes:        regularSidecarSupportNodes(),
		},
		{
			name:         "native container injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPod(),
			wantResponse: wantResponse(t, false, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container injection successful test, with prefetch.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithPrefetchIncluded(),
			wantResponse: wantResponseWithPrefetch(t, false, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "Injection with custom sidecar container image successful test with native nodes.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithCustomImage(false),
			wantResponse: wantResponse(t, true, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "Injection with custom native sidecar container image successful test as native sidecar.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithCustomImage(true),
			wantResponse: wantResponse(t, true, true, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "regular container injection with istio present success test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithIstio(false, false, false),
			wantResponse: wantResponseWithIstio(t, false, false, false),
			nodes:        skewVersionNodes(),
		},
		{
			name:         "Injection with custom sidecar container image successful test with istio.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithIstio(true, false, true),
			wantResponse: wantResponseWithIstio(t, true, false, true),
			nodes:        nativeSupportNodes(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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

			si := SidecarInjector{
				Client:                 nil,
				Config:                 FakeConfig(),
				MetadataPrefetchConfig: FakePrefetchConfig(),
				Decoder:                admission.NewDecoder(runtime.NewScheme()),
				NodeLister:             lister,
			}

			stopCh := make(<-chan struct{})
			informerFactory.Start(stopCh)
			informerFactory.WaitForCacheSync(stopCh)

			request := &admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: tc.operation,
				},
			}
			if tc.inputPod != nil {
				request.Object = runtime.RawExtension{
					Raw: serialize(t, tc.inputPod),
				}
			}

			gotResponse := si.Handle(context.Background(), *request)

			if err := compareResponses(tc.wantResponse, gotResponse); err != nil {
				t.Errorf("for test: %s\nGot injection result: %v, but want: %v. details: %v", tc.name, gotResponse, tc.wantResponse, err)
			}
		})
	}
}

func TestValidateMutatingWebhookResponseForPV(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		inputPv      *corev1.PersistentVolume
		operation    admissionv1.Operation
		wantResponse admission.Response
		nodes        []corev1.Node
	}{
		{
			name:         "Empty request test.",
			inputPv:      nil,
			wantResponse: admission.Errored(http.StatusBadRequest, errors.New("there is no content to decode")),
		},
		{
			name:      "Noop",
			operation: admissionv1.Create,
			inputPv: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							Driver: util.GCSFuseCsiDriverName,
						},
					},
				},
			},
			wantResponse: admission.Allowed("No mutation Required on PersistentVolume: "),
		},
		{
			name:      "Noop with profiles enabled",
			operation: admissionv1.Create,
			inputPv: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							Driver: util.GCSFuseCsiDriverName,
						},
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:        "example-pv",
					Annotations: map[string]string{},
				},
			},
			wantResponse: admission.Allowed("No mutation Required on PersistentVolume: example-pv"),
		},
		{
			name:      "Block bc of override",
			operation: admissionv1.Create,
			inputPv: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							Driver: util.GCSFuseCsiDriverName,
						},
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "example-pv",
					Annotations: map[string]string{
						putil.AnnotationStatus: putil.ScanOverride,
					},
				},
			},
			wantResponse: admission.Errored(http.StatusBadRequest, status.Errorf(codes.InvalidArgument, "status %q requires annotation %q", putil.ScanOverride, putil.AnnotationNumObjects)),
		},
		{
			name:      "Block bc of annotation no override",
			operation: admissionv1.Create,
			inputPv: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							Driver: util.GCSFuseCsiDriverName,
						},
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "example-pv",
					Annotations: map[string]string{
						putil.AnnotationNumObjects: "some val",
					},
				},
			},
			wantResponse: admission.Errored(http.StatusBadRequest, status.Errorf(codes.InvalidArgument, "scanner annotations for PV %q found in non-override mode: [%+v]", "example-pv", putil.AnnotationNumObjects)),
		},
		{
			name:      "Block bc of annotation with invalid value",
			operation: admissionv1.Create,
			inputPv: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							Driver: util.GCSFuseCsiDriverName,
						},
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "example-pv",
					Annotations: map[string]string{
						putil.AnnotationStatus:     putil.ScanOverride,
						putil.AnnotationNumObjects: "-10",
						putil.AnnotationTotalSize:  "10",
						putil.AnnotationHNSEnabled: "true",
					},
				},
			},
			wantResponse: admission.Errored(http.StatusBadRequest, status.Errorf(codes.InvalidArgument, "invalid value for annotation %q: value must be non-negative, got: \"-10\"", putil.AnnotationNumObjects)),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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

			si := SidecarInjector{
				Client: nil,
				Config: &Config{
					EnableGcsfuseProfiles: true,
				},
				Decoder:    admission.NewDecoder(runtime.NewScheme()),
				NodeLister: lister,
			}

			stopCh := make(<-chan struct{})
			informerFactory.Start(stopCh)
			informerFactory.WaitForCacheSync(stopCh)

			request := &admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Kind: metav1.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "PersistentVolume",
					},
					Operation: tc.operation,
				},
			}
			if tc.inputPv != nil {
				request.Object = runtime.RawExtension{
					Raw: serialize(t, tc.inputPv),
				}
			}

			gotResponse := si.Handle(context.Background(), *request)

			if err := compareResponses(tc.wantResponse, gotResponse); err != nil {
				t.Errorf("for test: %s\nGot injection result: %v, but want: %v. details: %v", tc.name, gotResponse, tc.wantResponse, err)
			}
		})
	}
}

func serialize(t *testing.T, obj any) []byte {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Errorf("Error serializing object %o.", obj)

		return nil
	}

	return b
}

func compareResponses(wantResponse, gotResponse admission.Response) error {
	if diff := cmp.Diff(gotResponse.String(), wantResponse.String()); diff != "" {
		return fmt.Errorf("request args differ (-got, +want)\n%s", diff)
	}
	if len(wantResponse.Patches) != len(gotResponse.Patches) {
		return fmt.Errorf("expecting %d patches, got %d patches", len(wantResponse.Patches), len(gotResponse.Patches))
	}
	wantPaths := []string{}
	gotPaths := []string{}
	for i := range len(wantResponse.Patches) {
		wantPaths = append(wantPaths, wantResponse.Patches[i].Path)
		gotPaths = append(gotPaths, gotResponse.Patches[i].Path)
	}

	if len(wantPaths) > 0 && len(gotPaths) > 0 {
		less := func(a, b string) bool { return a > b }
		if diff := cmp.Diff(gotPaths, wantPaths, cmpopts.SortSlices(less)); diff != "" {
			return fmt.Errorf("unexpected pod args (-got, +want)\n%s", diff)
		}
	}

	return nil
}

func getDuplicateDeclarationPodSpec() *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{
					Name:  GcsFuseSidecarName,
					Image: "private-repo/fake-sidecar-image:v999.999.999",
				},
			},
			Containers: []corev1.Container{
				{
					Name: "FakeContainer1",
				},
				{
					Name: "FakeContainer2",
				},
				{
					Name:  GcsFuseSidecarName,
					Image: "private-repo/fake-sidecar-image:v999.999.999",
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "FakeVolume1",
				},
				{
					Name: "FakeVolume2",
				},
			},
			TerminationGracePeriodSeconds: ptr.To[int64](60),
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation: "true",
			},
		},
	}
}

func getDuplicateDeclarationPodSpecResponse(nativeCustomImage bool) *corev1.Pod {
	result := modifySpec(*validInputPodWithCustomImage(nativeCustomImage), true, nativeCustomImage, true)

	return result
}

func validInputPodWithNativeAnnotation(customImage, customNativeImage bool, enableNativeSidecarAnnotation string) *corev1.Pod {
	pod := validInputPodWithSettings(customImage, customNativeImage)
	pod.ObjectMeta.Annotations[GcsFuseNativeSidecarEnableAnnotation] = enableNativeSidecarAnnotation

	return pod
}

func validInputPodWithSettings(customImage, native bool) *corev1.Pod {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "FakeContainer1",
				},
				{
					Name: "FakeContainer2",
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "FakeVolume1",
				},
				{
					Name: "FakeVolume2",
				},
			},
			TerminationGracePeriodSeconds: ptr.To[int64](60),
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation: "true",
			},
		},
	}

	userProvidedSidecar := corev1.Container{
		Name:  GcsFuseSidecarName,
		Image: "private-repo/fake-sidecar-image:v999.999.999",
	}

	if customImage {
		if native {
			pod.Spec.InitContainers = append(pod.Spec.InitContainers, userProvidedSidecar)
		} else {
			pod.Spec.Containers = append(pod.Spec.Containers, userProvidedSidecar)
		}
	}

	return pod
}

func validInputPodWithCustomImage(native bool) *corev1.Pod {
	return validInputPodWithSettings(true, native)
}

func validInputPod() *corev1.Pod {
	return validInputPodWithSettings(false, false)
}

func validInputPodWithPrefetchIncluded() *corev1.Pod {
	pod := validInputPodWithSettings(false, false)
	pod.ObjectMeta.Annotations[GcsFuseNativeSidecarEnableAnnotation] = "true"
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:            MetadataPrefetchSidecarName,
		Image:           "my-private-image",
		SecurityContext: GetSecurityContext(),
	})

	return pod
}

func getWorkloadSpec(name string) corev1.Container {
	return corev1.Container{
		Name:  name,
		Image: "busybox",
	}
}

func validInputPodWithIstio(customImage, nativeCustomImage, nativeIstio bool) *corev1.Pod {
	pod := validInputPodWithSettings(customImage, nativeCustomImage)

	if nativeIstio {
		pod.Spec.InitContainers = append([]corev1.Container{istioContainer}, pod.Spec.InitContainers...)
	} else {
		pod.Spec.Containers = append([]corev1.Container{istioContainer}, pod.Spec.Containers...)
	}

	return pod
}

func wantResponse(t *testing.T, customImage bool, nativeCustomImage bool, native bool) admission.Response {
	t.Helper()
	pod := validInputPodWithSettings(customImage, nativeCustomImage)
	newPod := *modifySpec(*validInputPodWithSettings(customImage, nativeCustomImage), customImage, nativeCustomImage, native)

	return generatePatch(t, pod, &newPod)
}

func wantResponseWithPrefetch(t *testing.T, customImage bool, nativeCustomImage bool, native bool) admission.Response {
	t.Helper()
	pod := validInputPodWithPrefetchIncluded()
	newPod := *modifySpec(*validInputPodWithPrefetchIncluded(), customImage, nativeCustomImage, native)

	return generatePatch(t, pod, &newPod)
}

func modifySpec(newPod corev1.Pod, customImage bool, nativeCustomImage, native bool) *corev1.Pod {
	config := FakeConfig()
	if customImage {
		if nativeCustomImage {
			config.ContainerImage = newPod.Spec.InitContainers[len(newPod.Spec.InitContainers)-1].Image
			newPod.Spec.InitContainers = newPod.Spec.InitContainers[:len(newPod.Spec.InitContainers)-1]
		} else {
			config.ContainerImage = newPod.Spec.Containers[len(newPod.Spec.Containers)-1].Image
			newPod.Spec.Containers = newPod.Spec.Containers[:len(newPod.Spec.Containers)-1]
		}
	}

	if native {
		newPod.Spec.InitContainers = append([]corev1.Container{GetNativeSidecarContainerSpec(config)}, newPod.Spec.InitContainers...)
	} else {
		newPod.Spec.Containers = append([]corev1.Container{GetSidecarContainerSpec(config)}, newPod.Spec.Containers...)
	}
	newPod.Spec.Volumes = append(GetSidecarContainerVolumeSpec(newPod.Spec.Volumes...), newPod.Spec.Volumes...)

	return &newPod
}

func generatePatch(t *testing.T, originalPod *corev1.Pod, newPod *corev1.Pod) admission.Response {
	t.Helper()

	return admission.PatchResponseFromRaw(serialize(t, originalPod), serialize(t, newPod))
}

func wantResponseWithIstio(t *testing.T, customImage bool, nativeCustomImage, native bool) admission.Response {
	t.Helper()

	originalPod := validInputPodWithSettings(customImage, nativeCustomImage)
	if native {
		originalPod.Spec.InitContainers = append([]corev1.Container{istioContainer}, originalPod.Spec.InitContainers...)
	} else {
		originalPod.Spec.Containers = append([]corev1.Container{istioContainer}, originalPod.Spec.Containers...)
	}

	newPod := validInputPodWithSettings(customImage, nativeCustomImage)
	config := FakeConfig()
	if customImage {
		config.ContainerImage = newPod.Spec.Containers[len(newPod.Spec.Containers)-1].Image
		newPod.Spec.Containers = newPod.Spec.Containers[:len(newPod.Spec.Containers)-1]
	}

	if native {
		newPod.Spec.InitContainers = append([]corev1.Container{istioContainer, GetNativeSidecarContainerSpec(config)}, newPod.Spec.InitContainers...)
	} else {
		newPod.Spec.Containers = append([]corev1.Container{istioContainer, GetSidecarContainerSpec(config)}, newPod.Spec.Containers...)
	}
	newPod.Spec.Volumes = append(GetSidecarContainerVolumeSpec(newPod.Spec.Volumes...), newPod.Spec.Volumes...)

	return admission.PatchResponseFromRaw(serialize(t, originalPod), serialize(t, newPod))
}

func nativeSupportNodes() []corev1.Node {
	return []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "1.29.1-gke.1670000",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "1.29.1-gke.1670000",
				},
			},
		},
	}
}

func regularSidecarSupportNodes() []corev1.Node {
	return []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "1.28.3-gke.1111000",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "1.28.3-gke.1111000",
				},
			},
		},
	}
}

func skewVersionNodes() []corev1.Node {
	return []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "1.29.1-gke.1670000",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "1.28.3-gke.1111000",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node3",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "1.29.1-gke.1670000",
				},
			},
		},
	}
}

func TestIsGCSFuseProfilesEnabled(t *testing.T) {

	tests := []struct {
		name      string
		pod       *corev1.Pod
		spEnabled bool
		expectErr bool
	}{
		{
			name: "pod with profiles enabled",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsfuseProfilesAnnotation: "true",
					},
				},

				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			spEnabled: true,
		},
		{
			name: "pod with profiles enabled but TRUE",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsfuseProfilesAnnotation: "TRUE",
					},
				},

				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			spEnabled: false,
			expectErr: true,
		},
		{
			name: "pod with profiles annotation set to false",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsfuseProfilesAnnotation: "False",
					},
				},

				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			spEnabled: false,
		},
		{
			name: "pod with wrong annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Annotations: map[string]string{
						"some other label": "true",
					},
				},

				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			spEnabled: false,
		},
		{
			name: "pod with no annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
				},

				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			spEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsGCSFuseProfilesEnabled(tt.pod)
			if err != nil && tt.expectErr == false {
				t.Errorf("IsGCSFuseProfilesEnabled() unexpected error: %v", err)
			}
			if tt.expectErr == true && err == nil {
				t.Errorf("IsGCSFuseProfilesEnabled() expected error but didn't get one")
			}
			if got != tt.spEnabled {
				t.Errorf("IsGCSFuseProfilesEnabled() = %v, want %v", got, tt.spEnabled)
			}
		})
	}
}

// Only testing modification logic on a pod with a sidecar already injected because when this method is called if the sidecar is not present it would have already failed
func TestModifyPodSpecForGCSFuseProfiles(t *testing.T) {
	// Define common structures to be added
	expectedGate := corev1.PodSchedulingGate{Name: BucketScanPendingSchedulingGate}
	expectedEphemeralVolume := corev1.Volume{
		Name:         SidecarContainerFileCacheEphemeralDiskVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
	expectedRAMVolume := corev1.Volume{
		Name: SidecarContainerFileCacheRamDiskVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			Medium: corev1.StorageMediumMemory,
		}},
	}
	expectedEphemeralMount := corev1.VolumeMount{
		Name:      SidecarContainerFileCacheEphemeralDiskVolumeName,
		MountPath: SidecarContainerFileCacheEphemeralDiskVolumeMountPath,
	}
	expectedRAMMount := corev1.VolumeMount{
		Name:      SidecarContainerFileCacheRamDiskVolumeName,
		MountPath: SidecarContainerFileCacheRamDiskVolumeMountPath,
	}

	testCases := []struct {
		name      string
		inputPod  *corev1.Pod
		wantPod   *corev1.Pod
		expectErr bool
	}{
		{
			name: "pod with sidecar in containers list, already has existing scheduling gate",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Containers: []corev1.Container{
						{Name: "app-container"},
						{Name: GcsFuseSidecarName},
					},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedEphemeralVolume, expectedRAMVolume},
					Containers: []corev1.Container{
						{Name: "app-container"},
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
				},
			},
		},
		{
			name: "pod with sidecar in containers list",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app-container"},
						{Name: GcsFuseSidecarName},
					},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedEphemeralVolume, expectedRAMVolume},
					Containers: []corev1.Container{
						{Name: "app-container"},
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
				},
			},
		},
		{
			name: "pod with sidecar in init containers list",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{Name: GcsFuseSidecarName},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedEphemeralVolume, expectedRAMVolume},
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
		},
		{
			name: "pod with sidecar in init containers list but has existing volume mounts",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{Name: GcsFuseSidecarName, VolumeMounts: []corev1.VolumeMount{
							expectedEphemeralMount, expectedRAMMount,
						}},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedEphemeralVolume, expectedRAMVolume},
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
		},
		{
			name: "pod with sidecar in init containers list but has existing volume mounts - missing one mount",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{Name: GcsFuseSidecarName, VolumeMounts: []corev1.VolumeMount{
							expectedEphemeralMount,
						}},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedEphemeralVolume, expectedRAMVolume},
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
		},
		{
			name: "pod with sidecar in init containers list but has existing volume - missing one",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{expectedEphemeralVolume},
					InitContainers: []corev1.Container{
						{Name: GcsFuseSidecarName},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedEphemeralVolume, expectedRAMVolume},
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
		},
		{
			name: "pod with no sidecar",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{expectedEphemeralVolume},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedEphemeralVolume, expectedRAMVolume},
					InitContainers: []corev1.Container{
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
			},
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a deep copy to avoid modifying the original test case data
			podToModify := tc.inputPod.DeepCopy()

			// The function doesn't actually use the client or context,
			// so we can pass in nil or empty values.
			err := ModifyPodSpecForGCSFuseProfiles(podToModify)
			if err != nil || tc.expectErr {
				if !tc.expectErr {
					t.Fatalf("ModifyPodSpecForGcsfuseProfiles() returned an unexpected error: %v", err)
				}
				// If we expected an error and got one, the test passes.
				if err == nil {
					t.Fatalf("ModifyPodSpecForGcsfuseProfiles() expected to return an error but got nil")
				}
				return
			}
			if diff := cmp.Diff(tc.wantPod, podToModify); diff != "" {
				t.Errorf("ModifyPodSpecForGcsfuseProfiles() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
