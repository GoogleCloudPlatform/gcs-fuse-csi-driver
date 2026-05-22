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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"gomodules.xyz/jsonpatch/v2"
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
		name                          string
		inputPod                      *corev1.Pod
		operation                     admissionv1.Operation
		wantResponse                  admission.Response
		nodes                         []corev1.Node
		pvcs                          []*corev1.PersistentVolumeClaim
		pvs                           []*corev1.PersistentVolume
		enableGCSFuseMetadataPrefetch bool
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
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig(), nil /*credentialConfig*/)},
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
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig(), nil /*credentialConfig*/)},
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
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig(), nil /*credentialConfig*/)},
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
					Containers: []corev1.Container{getWorkloadSpec("workload"), GetSidecarContainerSpec(FakeConfig(), nil /*credentialConfig*/)},
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
					InitContainers: []corev1.Container{getWorkloadSpec("init-workload"), GetSidecarContainerSpec(FakeConfig(), nil /*credentialConfig*/)},
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
		{
			name:         "workload identity injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithWorkloadIdentity("test-credentials"),
			wantResponse: wantWorkloadIdentityResponse(t, "test-credentials"),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "workload identity injection no annotation test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPod(),
			wantResponse: wantResponse(t, false, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "workload identity injection empty annotation test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithWorkloadIdentity(""),
			wantResponse: wantResponse(t, false, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:                          "native prefetch: enabled via PVC/PV volume attributes",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithImageAndPVC("my-pvc-default", "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v999.999.999-gke.0"),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithImageAndPVC("my-pvc-default", "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v999.999.999-gke.0"), true, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-default", "default", "my-pv-default"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-default", nil, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: disabled automatically when no volume requests prefetch",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPod(),
			wantResponse:                  wantResponse(t, false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
		},
		{
			name:                          "native prefetch: enabled via inline CSI ephemeral volume",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithImageAndEphemeral("gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v999.999.999-gke.0", ""),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithImageAndEphemeral("gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v999.999.999-gke.0", ""), true, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
		},
		{
			name:                          "native prefetch: legacy fallback when explicitly disabled via ephemeral volume attributes",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithEphemeralVolumeMountOptions("metadata-cache:enable-metadata-prefetch:false"),
			wantResponse:                  wantResponseWithLegacyPrefetch(t, validInputPodWithEphemeralVolumeMountOptions("metadata-cache:enable-metadata-prefetch:false"), []string{"gcsfuse-volume"}, false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
		},
		{
			name:                          "native prefetch: enabled automatically and parses mountOptions from PV VolumeAttributes correctly",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithPVC("my-pvc-vol-opts"),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithPVC("my-pvc-vol-opts"), false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-vol-opts", "default", "my-pv-vol-opts"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-vol-opts", nil, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
					"mountOptions": "metadata-cache:enable-metadata-prefetch:true",
				}),
			},
		},
		{
			name:                          "native prefetch: legacy fallback when explicitly disabled via PV mount options",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithPVC("my-pvc-fallback"),
			wantResponse:                  wantResponseWithLegacyPrefetch(t, validInputPodWithPVC("my-pvc-fallback"), []string{"gcsfuse-volume"}, false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-fallback", "default", "my-pv-fallback"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-fallback", []string{"metadata-cache:enable-metadata-prefetch:false"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: falls back to injecting legacy prefetch container when enableGCSFuseMetadataPrefetch is false",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithPVC("my-pvc-unsupported"),
			wantResponse:                  wantResponseWithLegacyPrefetch(t, validInputPodWithPVC("my-pvc-unsupported"), []string{"gcsfuse-volume"}, false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: false,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-unsupported", "default", "my-pv-unsupported"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-unsupported", nil, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: user override forces native prefetch even if feature gate is disabled",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithPVC("my-pvc-unsupported-override"),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithPVC("my-pvc-unsupported-override"), false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: false,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-unsupported-override", "default", "my-pv-unsupported-override"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-unsupported-override", []string{"metadata-cache:enable-metadata-prefetch:true"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: legacy fallback for custom sidecar image",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithImageAndPVC("my-pvc-custom-image", "private-repo/fake-sidecar-image:v999.999.999"),
			wantResponse:                  wantResponseWithLegacyPrefetch(t, validInputPodWithImageAndPVC("my-pvc-custom-image", "private-repo/fake-sidecar-image:v999.999.999"), []string{"gcsfuse-volume"}, true, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-custom-image", "default", "my-pv-custom-image"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-custom-image", nil, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: user override forces native prefetch for custom sidecar image",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithImageAndPVC("my-pvc-custom-image-override", "private-repo/fake-sidecar-image:v999.999.999"),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithImageAndPVC("my-pvc-custom-image-override", "private-repo/fake-sidecar-image:v999.999.999"), true, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-custom-image-override", "default", "my-pv-custom-image-override"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-custom-image-override", []string{"metadata-cache:enable-metadata-prefetch:true"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: legacy fallback when managed sidecar image version is unsupported",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithImageAndPVC("my-pvc-old-version", "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.0.0-gke.0"),
			wantResponse:                  wantResponseWithLegacyPrefetch(t, validInputPodWithImageAndPVC("my-pvc-old-version", "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.0.0-gke.0"), []string{"gcsfuse-volume"}, true, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-old-version", "default", "my-pv-old-version"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-old-version", nil, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: user override forces native prefetch for unsupported sidecar version",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithImageAndPVC("my-pvc-old-version-override", "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.0.0-gke.0"),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithImageAndPVC("my-pvc-old-version-override", "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.0.0-gke.0"), true, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-old-version-override", "default", "my-pv-old-version-override"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-old-version-override", []string{"metadata-cache:enable-metadata-prefetch:true"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: enabled when all ephemeral volume overrides are true",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithMultipleEphemeralVolumes("metadata-cache:enable-metadata-prefetch:true", "metadata-cache:enable-metadata-prefetch:true"),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithMultipleEphemeralVolumes("metadata-cache:enable-metadata-prefetch:true", "metadata-cache:enable-metadata-prefetch:true"), false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
		},
		{
			name:                          "native prefetch: legacy fallback when all ephemeral volume overrides are false",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithMultipleEphemeralVolumes("metadata-cache:enable-metadata-prefetch:false", "metadata-cache:enable-metadata-prefetch:false"),
			wantResponse:                  wantResponseWithLegacyPrefetch(t, validInputPodWithMultipleEphemeralVolumes("metadata-cache:enable-metadata-prefetch:false", "metadata-cache:enable-metadata-prefetch:false"), []string{"gcsfuse-volume-1", "gcsfuse-volume-2"}, false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
		},
		{
			name:                          "native prefetch: enabled when all PVC/PV overrides are true",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithMultiplePVCs("my-pvc-1", "my-pvc-2"),
			wantResponse:                  wantResponseWithNativePrefetch(t, validInputPodWithMultiplePVCs("my-pvc-1", "my-pvc-2"), false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-1", "default", "my-pv-1"),
				makePrefetchPVC("my-pvc-2", "default", "my-pv-2"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-1", []string{"metadata-cache:enable-metadata-prefetch:true"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
				makePrefetchPV("my-pv-2", []string{"metadata-cache:enable-metadata-prefetch:true"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
		},
		{
			name:                          "native prefetch: legacy fallback when all PVC/PV overrides are false",
			operation:                     admissionv1.Create,
			inputPod:                      validInputPodWithMultiplePVCs("my-pvc-1", "my-pvc-2"),
			wantResponse:                  wantResponseWithLegacyPrefetch(t, validInputPodWithMultiplePVCs("my-pvc-1", "my-pvc-2"), []string{"gcsfuse-volume-0", "gcsfuse-volume-1"}, false, false, true),
			nodes:                         nativeSupportNodes(),
			enableGCSFuseMetadataPrefetch: true,
			pvcs: []*corev1.PersistentVolumeClaim{
				makePrefetchPVC("my-pvc-1", "default", "my-pv-1"),
				makePrefetchPVC("my-pvc-2", "default", "my-pv-2"),
			},
			pvs: []*corev1.PersistentVolume{
				makePrefetchPV("my-pv-1", []string{"metadata-cache:enable-metadata-prefetch:false"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
				makePrefetchPV("my-pv-2", []string{"metadata-cache:enable-metadata-prefetch:false"}, map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
				}),
			},
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

			for _, pv := range tc.pvs {
				_, err := fakeClient.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to setup test PV: %v", err)
				}
			}
			for _, pvc := range tc.pvcs {
				_, err := fakeClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).Create(context.Background(), pvc, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to setup test PVC: %v", err)
				}
			}

			// Create workload identity ConfigMap if the test uses it
			if tc.inputPod != nil && tc.inputPod.Annotations != nil {
				if configMapName, ok := tc.inputPod.Annotations[GCPWorkloadIdentityCredentialConfigMapAnnotation]; ok && configMapName != "" {
					configMap := &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      configMapName,
							Namespace: testNamespace,
						},
						Data: map[string]string{
							"credential-configuration.json": `{
								"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
								"credential_source": {
									"file": "/var/run/service-account/token"
								}
							}`,
						},
					}
					_, err := fakeClient.CoreV1().ConfigMaps(testNamespace).Create(context.Background(), configMap, metav1.CreateOptions{})
					if err != nil {
						t.Error("failed to create workload identity configmap")
					}
				}
			}

			informerFactory := informers.NewSharedInformerFactoryWithOptions(fakeClient, time.Second*1, informers.WithNamespace(metav1.NamespaceAll))
			lister := informerFactory.Core().V1().Nodes().Lister()
			pvLister := informerFactory.Core().V1().PersistentVolumes().Lister()
			pvcLister := informerFactory.Core().V1().PersistentVolumeClaims().Lister()

			si := SidecarInjector{
				Client:                        nil,
				Config:                        FakeConfig(),
				MetadataPrefetchConfig:        FakePrefetchConfig(),
				Decoder:                       admission.NewDecoder(runtime.NewScheme()),
				NodeLister:                    lister,
				PvLister:                      pvLister,
				PvcLister:                     pvcLister,
				K8SClient:                     fakeClient,
				EnableGCSFuseMetadataPrefetch: tc.enableGCSFuseMetadataPrefetch,
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
	if len(wantResponse.Patches) > 0 {
		less := func(a, b jsonpatch.JsonPatchOperation) bool {
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			return a.Operation < b.Operation
		}
		if diff := cmp.Diff(gotResponse.Patches, wantResponse.Patches, cmpopts.SortSlices(less)); diff != "" {
			return fmt.Errorf("unexpected patches (-got, +want)\n%s", diff)
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
			Namespace: testNamespace,
			Annotations: map[string]string{
				GcsFuseVolumeEnableAnnotation: "true",
			},
		},
	}
}

func getDuplicateDeclarationPodSpecResponse(nativeCustomImage bool) *corev1.Pod {
	result := modifySpec(*validInputPodWithCustomImage(nativeCustomImage), true, nativeCustomImage, true, false, false)

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
			Namespace: testNamespace,
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
		SecurityContext: GetSecurityContext(FakePrefetchConfig()),
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
	newPod := *modifySpec(*validInputPodWithSettings(customImage, nativeCustomImage), customImage, nativeCustomImage, native, false, false)

	return generatePatch(t, pod, &newPod)
}

func wantResponseWithPrefetch(t *testing.T, customImage bool, nativeCustomImage bool, native bool) admission.Response {
	t.Helper()
	pod := validInputPodWithPrefetchIncluded()
	newPod := *modifySpec(*validInputPodWithPrefetchIncluded(), customImage, nativeCustomImage, native, false, false)

	return generatePatch(t, pod, &newPod)
}

func modifySpec(newPod corev1.Pod, customImage bool, nativeCustomImage, native, webhookHandledPrefetch, prefetchRequested bool) *corev1.Pod {
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

	var sidecar corev1.Container
	if native {
		sidecar = GetNativeSidecarContainerSpec(config, nil)
	} else {
		sidecar = GetSidecarContainerSpec(config, nil /*credentialConfig*/)
	}

	if webhookHandledPrefetch {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{Name: util.WebhookHandledPrefetchEnvVar, Value: "true"})
	}
	if prefetchRequested {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{Name: util.PrefetchRequestedEnvVar, Value: "true"})
	}

	if native {
		newPod.Spec.InitContainers = append([]corev1.Container{sidecar}, newPod.Spec.InitContainers...)
	} else {
		newPod.Spec.Containers = append([]corev1.Container{sidecar}, newPod.Spec.Containers...)
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
		newPod.Spec.InitContainers = append([]corev1.Container{istioContainer, GetNativeSidecarContainerSpec(config, nil /*credentialConfig*/)}, newPod.Spec.InitContainers...)
	} else {
		newPod.Spec.Containers = append([]corev1.Container{istioContainer, GetSidecarContainerSpec(config, nil /*credentialConfig*/)}, newPod.Spec.Containers...)
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

// Only testing modification logic on a pod with a sidecar already injected because when this method is called if the sidecar is not present it would have already failed
func TestModifyPodSpecForGCSFuseProfiles(t *testing.T) {
	// Define common structures to be added
	expectedGate := corev1.PodSchedulingGate{Name: BucketScanPendingSchedulingGate}
	expectedDefaultCacheVolume := corev1.Volume{
		Name:         SidecarContainerCacheVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
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
	expectedDefaultCacheMount := corev1.VolumeMount{
		Name:      SidecarContainerCacheVolumeName,
		MountPath: SidecarContainerCacheVolumeMountPath,
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
		name               string
		inputPod           *corev1.Pod
		wantPod            *corev1.Pod
		cacheCreatedByUser bool
		expectErr          bool
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
		{
			name: "pod with user provided cache volume, should add user created cache volume label set to true",
			// Passed by the webhook handler.
			cacheCreatedByUser: true,
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Containers: []corev1.Container{
						{Name: "app-container"},
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedDefaultCacheMount,
							},
						},
					},
					// User provided cache volume.
					Volumes: []corev1.Volume{expectedDefaultCacheVolume},
				},
			},
			wantPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Labels: map[string]string{
						GcsfuseProfilesManagedLabel: "true",
						// Should trigger this annotation.
						GcsfuseCacheCreatedByUserLabel: "true",
					},
				},
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{expectedGate},
					Volumes:         []corev1.Volume{expectedDefaultCacheVolume, expectedEphemeralVolume, expectedRAMVolume},
					Containers: []corev1.Container{
						{Name: "app-container"},
						{
							Name: GcsFuseSidecarName,
							VolumeMounts: []corev1.VolumeMount{
								expectedDefaultCacheMount,
								expectedEphemeralMount,
								expectedRAMMount,
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a deep copy to avoid modifying the original test case data
			podToModify := tc.inputPod.DeepCopy()

			// The function doesn't actually use the client or context,
			// so we can pass in nil or empty values.
			err := ModifyPodSpecForGCSFuseProfiles(podToModify, tc.cacheCreatedByUser)
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

func validInputPodWithWorkloadIdentity(configMapName string) *corev1.Pod {
	pod := validInputPod()
	pod.ObjectMeta.Namespace = testNamespace
	if configMapName != "" {
		pod.ObjectMeta.Annotations[GCPWorkloadIdentityCredentialConfigMapAnnotation] = configMapName
	}
	return pod
}

func wantWorkloadIdentityResponse(t *testing.T, configMapName string) admission.Response {
	t.Helper()
	originalPod := validInputPodWithWorkloadIdentity(configMapName)
	newPod := *modifySpecWithWorkloadIdentity(*originalPod, configMapName)
	return generatePatch(t, originalPod, &newPod)
}

func modifySpecWithWorkloadIdentity(newPod corev1.Pod, configMapName string) *corev1.Pod {
	config := FakeConfig()

	// Create sidecar credential configuration
	var sidecarCredentialConfig *SidecarContainerCredentialConfiguration
	if configMapName != "" {
		sidecarCredentialConfig = &SidecarContainerCredentialConfiguration{
			GacEnv: &corev1.EnvVar{
				Name:  "GOOGLE_APPLICATION_CREDENTIALS",
				Value: fmt.Sprintf("%s/%s", SidecarContainerWICredentialConfigMapVolumeMountPath, "credential-configuration.json"),
			},
			CredentialVolumeMounts: []corev1.VolumeMount{
				{Name: SidecarContainerWITokenVolumeName, MountPath: "/var/run/service-account"},
				{Name: SidecarContainerWICredentialConfigMapVolumeName, MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath},
			},
		}

		// Add the workload identity volumes
		newPod.Spec.Volumes = append(newPod.Spec.Volumes,
			corev1.Volume{
				Name: SidecarContainerWITokenVolumeName,
				VolumeSource: corev1.VolumeSource{
					Projected: &corev1.ProjectedVolumeSource{
						Sources: []corev1.VolumeProjection{
							{
								ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
									Audience:          "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
									ExpirationSeconds: &tokenExpirationSeconds,
									Path:              "token",
								},
							},
						},
						DefaultMode: &defaultMode,
					},
				},
			},
			corev1.Volume{
				Name: SidecarContainerWICredentialConfigMapVolumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: configMapName,
						},
						DefaultMode: &defaultMode,
					},
				},
			},
		)
	}

	// Add native sidecar container
	newPod.Spec.InitContainers = append([]corev1.Container{GetNativeSidecarContainerSpec(config, sidecarCredentialConfig)}, newPod.Spec.InitContainers...)
	newPod.Spec.Volumes = append(GetSidecarContainerVolumeSpec(newPod.Spec.Volumes...), newPod.Spec.Volumes...)

	return &newPod
}

func TestOIDCAuthenticationWithHostNetwork(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		inputPod            *corev1.Pod
		expectError         bool
		expectedErrorSubstr string
	}{
		{
			name: "reject pod with hostNetwork=true and OIDC annotation",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation:                    "true",
						GCPWorkloadIdentityCredentialConfigMapAnnotation: "workload-identity-credentials",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork:                   true,
					Containers:                    []corev1.Container{{Name: "app-container"}},
					Volumes:                       []corev1.Volume{},
					TerminationGracePeriodSeconds: ptr.To[int64](60),
				},
			},
			expectError:         true,
			expectedErrorSubstr: "OIDC authentication",
		},
		{
			name: "allow pod with hostNetwork=true without OIDC annotation",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation: "true",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork:                   true,
					Containers:                    []corev1.Container{{Name: "app-container"}},
					Volumes:                       []corev1.Volume{},
					TerminationGracePeriodSeconds: ptr.To[int64](60),
				},
			},
			expectError: false,
		},
		{
			name: "allow pod with OIDC annotation without hostNetwork",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation:                    "true",
						GCPWorkloadIdentityCredentialConfigMapAnnotation: "workload-identity-credentials",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork:                   false,
					Containers:                    []corev1.Container{{Name: "app-container"}},
					Volumes:                       []corev1.Volume{},
					TerminationGracePeriodSeconds: ptr.To[int64](60),
				},
			},
			expectError: false,
		},
		{
			name: "allow pod without hostNetwork and without OIDC annotation",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation: "true",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork:                   false,
					Containers:                    []corev1.Container{{Name: "app-container"}},
					Volumes:                       []corev1.Volume{},
					TerminationGracePeriodSeconds: ptr.To[int64](60),
				},
			},
			expectError: false,
		},
		{
			name: "allow pod with hostNetwork=true and empty OIDC annotation",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					Annotations: map[string]string{
						GcsFuseVolumeEnableAnnotation:                    "true",
						GCPWorkloadIdentityCredentialConfigMapAnnotation: "",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork:                   true,
					Containers:                    []corev1.Container{{Name: "app-container"}},
					Volumes:                       []corev1.Volume{},
					TerminationGracePeriodSeconds: ptr.To[int64](60),
				},
			},
			expectError: false, // Empty annotation value should be ignored
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create fake clientset with necessary ConfigMap for OIDC tests
			fakeClient := fake.NewSimpleClientset()
			if tc.inputPod.Annotations[GCPWorkloadIdentityCredentialConfigMapAnnotation] != "" {
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tc.inputPod.Annotations[GCPWorkloadIdentityCredentialConfigMapAnnotation],
						Namespace: testNamespace,
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
							"credential_source": {
								"file": "/var/run/service-account/token"
							}
						}`,
					},
				}
				_, err := fakeClient.CoreV1().ConfigMaps(testNamespace).Create(context.Background(), configMap, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("Failed to create test ConfigMap: %v", err)
				}
			}

			// Create informer factory
			informerFactory := informers.NewSharedInformerFactory(fakeClient, time.Hour)
			nodeInformer := informerFactory.Core().V1().Nodes()
			nodeInformer.Informer() // Initialize the informer

			// Create test nodes
			testNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{
						KubeletVersion: "v1.29.0-gke.1234",
					},
				},
			}
			_, err := fakeClient.CoreV1().Nodes().Create(context.Background(), testNode, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("Failed to create test node: %v", err)
			}

			// Start informer and wait for cache sync
			stopCh := make(chan struct{})
			defer close(stopCh)
			informerFactory.Start(stopCh)
			informerFactory.WaitForCacheSync(stopCh)

			// Create SidecarInjector
			si := &SidecarInjector{
				Client:                 nil,
				K8SClient:              fakeClient,
				Config:                 FakeConfig(),
				MetadataPrefetchConfig: FakePrefetchConfig(),
				Decoder:                admission.NewDecoder(runtime.NewScheme()),
				NodeLister:             nodeInformer.Lister(),
			}

			// Marshal the pod to create admission request
			podJSON, err := json.Marshal(tc.inputPod)
			if err != nil {
				t.Fatalf("Failed to marshal pod: %v", err)
			}

			// Create admission request
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Create,
					Object: runtime.RawExtension{
						Raw: podJSON,
					},
				},
			}

			// Call Handle
			resp := si.Handle(context.Background(), req)

			// Verify response
			if tc.expectError {
				if resp.Allowed {
					t.Errorf("Expected request to be denied, but it was allowed")
				}
				if tc.expectedErrorSubstr != "" && !strings.Contains(resp.Result.Message, tc.expectedErrorSubstr) {
					t.Errorf("Expected error message to contain %q, but got: %q", tc.expectedErrorSubstr, resp.Result.Message)
				}
				if resp.Result.Code != http.StatusBadRequest {
					t.Errorf("Expected status code %d, but got: %d", http.StatusBadRequest, resp.Result.Code)
				}
			} else {
				if !resp.Allowed {
					t.Errorf("Expected request to be allowed, but it was denied with: %v", resp.Result)
				}
			}
		})
	}
}

// wantResponseWithNativePrefetch constructs the expected mutated Pod spec patch where
// native prefetching is enabled (WEBHOOK_HANDLED_PREFETCH=true and PREFETCH_REQUESTED=true).
func wantResponseWithNativePrefetch(t *testing.T, pod *corev1.Pod, customImage, nativeCustomImage, native bool) admission.Response {
	t.Helper()
	newPod := *modifySpec(*pod, customImage, nativeCustomImage, native, true, true)
	return generatePatch(t, pod, &newPod)
}

func validInputPodWithEphemeralVolumeMountOptions(mountOptions string) *corev1.Pod {
	pod := validInputPodWithSettings(false, false)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "gcsfuse-volume",
		VolumeSource: corev1.VolumeSource{
			CSI: &corev1.CSIVolumeSource{
				Driver: gcsFuseCsiDriverName,
				VolumeAttributes: map[string]string{
					gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
					"mountOptions": mountOptions,
				},
			},
		},
	})
	return pod
}

func validInputPodWithImageAndEphemeral(imageName, mountOptions string) *corev1.Pod {
	pod := validInputPodWithEphemeralVolumeMountOptions(mountOptions)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:  GcsFuseSidecarName,
		Image: imageName,
	})
	return pod
}

func validInputPodWithPVC(pvcName string) *corev1.Pod {
	pod := validInputPodWithSettings(false, false)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "gcsfuse-volume",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	})
	return pod
}

func makePrefetchPVC(name, namespace, pvName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: pvName,
		},
	}
}

func makePrefetchPV(name string, mountOptions []string, volumeAttributes map[string]string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeSpec{
			MountOptions: mountOptions,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:           gcsFuseCsiDriverName,
					VolumeAttributes: volumeAttributes,
				},
			},
		},
	}
}

// wantResponseWithLegacyPrefetch constructs the expected mutated Pod spec patch where
// legacy prefetching sidecar container injection is expected (WEBHOOK_HANDLED_PREFETCH=false and PREFETCH_REQUESTED=true).
func wantResponseWithLegacyPrefetch(t *testing.T, pod *corev1.Pod, volumeNames []string, customImage bool, nativeCustomImage bool, native bool) admission.Response {
	t.Helper()
	newPod := *modifySpec(*pod, customImage, nativeCustomImage, native, false, true)

	limits, requests := prepareResourceList(FakePrefetchConfig())
	legacyPrefetchContainer := corev1.Container{
		Name:            MetadataPrefetchSidecarName,
		SecurityContext: GetSecurityContext(FakePrefetchConfig()),
		Image:           FakePrefetchConfig().ContainerImage,
		ImagePullPolicy: corev1.PullPolicy(FakePrefetchConfig().ImagePullPolicy),
		Resources: corev1.ResourceRequirements{
			Requests: requests,
			Limits:   limits,
		},
		VolumeMounts: []corev1.VolumeMount{},
	}

	for _, vn := range volumeNames {
		legacyPrefetchContainer.VolumeMounts = append(legacyPrefetchContainer.VolumeMounts, corev1.VolumeMount{
			Name:      vn,
			ReadOnly:  true,
			MountPath: filepath.Join("/volumes/", vn),
		})
	}

	if native {
		legacyPrefetchContainer.Env = []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}}
		legacyPrefetchContainer.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)
		// Insert legacy prefetch container at index 1 (right after main sidecar which is at index 0)
		newPod.Spec.InitContainers = append(newPod.Spec.InitContainers[:1], append([]corev1.Container{legacyPrefetchContainer}, newPod.Spec.InitContainers[1:]...)...)
	} else {
		// Insert legacy prefetch container at index 1 (right after main sidecar which is at index 0)
		newPod.Spec.Containers = append(newPod.Spec.Containers[:1], append([]corev1.Container{legacyPrefetchContainer}, newPod.Spec.Containers[1:]...)...)
	}

	return generatePatch(t, pod, &newPod)
}

func validInputPodWithImageAndPVC(pvcName string, imageName string) *corev1.Pod {
	pod := validInputPodWithPVC(pvcName)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:  GcsFuseSidecarName,
		Image: imageName,
	})
	return pod
}

func validInputPodWithMultipleEphemeralVolumes(prefetchOpts ...string) *corev1.Pod {
	pod := validInputPodWithSettings(false, false)
	for i, opt := range prefetchOpts {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: fmt.Sprintf("gcsfuse-volume-%d", i+1),
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver: gcsFuseCsiDriverName,
					VolumeAttributes: map[string]string{
						gcsFuseMetadataPrefetchOnMountVolumeAttribute: "true",
						"mountOptions": opt,
					},
				},
			},
		})
	}
	return pod
}

func validInputPodWithMultiplePVCs(pvcNames ...string) *corev1.Pod {
	pod := validInputPodWithSettings(false, false)
	for i, name := range pvcNames {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: fmt.Sprintf("gcsfuse-volume-%d", i),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: name,
				},
			},
		})
	}
	return pod
}
