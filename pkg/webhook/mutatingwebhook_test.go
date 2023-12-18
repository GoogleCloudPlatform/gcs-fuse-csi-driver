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
	"fmt"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestPrepareConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                          string
		annotations                   map[string]string
		terminationGracePeriodSeconds int
		wantConfig                    *Config
		expectErr                     bool
	}{
		{
			name: "use default values if no annotation is found",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey: "true",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      FakeConfig().CPULimit,
				CPURequest:                    FakeConfig().CPURequest,
				MemoryLimit:                   FakeConfig().MemoryLimit,
				MemoryRequest:                 FakeConfig().MemoryRequest,
				EphemeralStorageLimit:         FakeConfig().EphemeralStorageLimit,
				EphemeralStorageRequest:       FakeConfig().EphemeralStorageRequest,
			},
			expectErr: false,
		},
		{
			name: "only limits are specified",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:                  "true",
				annotationGcsfuseSidecarCPULimitKey:               "500m",
				annotationGcsfuseSidecarMemoryLimitKey:            "1Gi",
				annotationGcsfuseSidecarEphermeralStorageLimitKey: "50Gi",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      resource.MustParse("500m"),
				CPURequest:                    resource.MustParse("500m"),
				MemoryLimit:                   resource.MustParse("1Gi"),
				MemoryRequest:                 resource.MustParse("1Gi"),
				EphemeralStorageLimit:         resource.MustParse("50Gi"),
				EphemeralStorageRequest:       resource.MustParse("50Gi"),
			},
			expectErr: false,
		},
		{
			name: "only requests are specified",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:                    "true",
				annotationGcsfuseSidecarCPURequestKey:               "500m",
				annotationGcsfuseSidecarMemoryRequestKey:            "1Gi",
				annotationGcsfuseSidecarEphermeralStorageRequestKey: "50Gi",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      resource.MustParse("500m"),
				CPURequest:                    resource.MustParse("500m"),
				MemoryLimit:                   resource.MustParse("1Gi"),
				MemoryRequest:                 resource.MustParse("1Gi"),
				EphemeralStorageLimit:         resource.MustParse("50Gi"),
				EphemeralStorageRequest:       resource.MustParse("50Gi"),
			},
			expectErr: false,
		},
		{
			name: "limits are set to '0'",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:                  "true",
				annotationGcsfuseSidecarCPULimitKey:               "0",
				annotationGcsfuseSidecarMemoryLimitKey:            "0",
				annotationGcsfuseSidecarEphermeralStorageLimitKey: "0",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      resource.Quantity{},
				CPURequest:                    resource.Quantity{},
				MemoryLimit:                   resource.Quantity{},
				MemoryRequest:                 resource.Quantity{},
				EphemeralStorageLimit:         resource.Quantity{},
				EphemeralStorageRequest:       resource.Quantity{},
			},
			expectErr: false,
		},
		{
			name: "requests are set to '0'",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:                    "true",
				annotationGcsfuseSidecarCPURequestKey:               "0",
				annotationGcsfuseSidecarMemoryRequestKey:            "0",
				annotationGcsfuseSidecarEphermeralStorageRequestKey: "0",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      resource.Quantity{},
				CPURequest:                    resource.Quantity{},
				MemoryLimit:                   resource.Quantity{},
				MemoryRequest:                 resource.Quantity{},
				EphemeralStorageLimit:         resource.Quantity{},
				EphemeralStorageRequest:       resource.Quantity{},
			},
			expectErr: false,
		},
		{
			name: "requests and limits are explicitly set",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:                    "true",
				annotationGcsfuseSidecarCPULimitKey:                 "500m",
				annotationGcsfuseSidecarMemoryLimitKey:              "1Gi",
				annotationGcsfuseSidecarEphermeralStorageLimitKey:   "50Gi",
				annotationGcsfuseSidecarCPURequestKey:               "100m",
				annotationGcsfuseSidecarMemoryRequestKey:            "500Mi",
				annotationGcsfuseSidecarEphermeralStorageRequestKey: "10Gi",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      resource.MustParse("500m"),
				CPURequest:                    resource.MustParse("100m"),
				MemoryLimit:                   resource.MustParse("1Gi"),
				MemoryRequest:                 resource.MustParse("500Mi"),
				EphemeralStorageLimit:         resource.MustParse("50Gi"),
				EphemeralStorageRequest:       resource.MustParse("10Gi"),
			},
			expectErr: false,
		},
		{
			name: "requests and limits are explicitly set with '0' limits",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:                    "true",
				annotationGcsfuseSidecarCPULimitKey:                 "0",
				annotationGcsfuseSidecarMemoryLimitKey:              "0",
				annotationGcsfuseSidecarEphermeralStorageLimitKey:   "0",
				annotationGcsfuseSidecarCPURequestKey:               "100m",
				annotationGcsfuseSidecarMemoryRequestKey:            "500Mi",
				annotationGcsfuseSidecarEphermeralStorageRequestKey: "10Gi",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      resource.Quantity{},
				CPURequest:                    resource.MustParse("100m"),
				MemoryLimit:                   resource.Quantity{},
				MemoryRequest:                 resource.MustParse("500Mi"),
				EphemeralStorageLimit:         resource.Quantity{},
				EphemeralStorageRequest:       resource.MustParse("10Gi"),
			},
			expectErr: false,
		},
		{
			name: "requests and limits are explicitly set with '0' requests",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:                    "true",
				annotationGcsfuseSidecarCPULimitKey:                 "500m",
				annotationGcsfuseSidecarMemoryLimitKey:              "1Gi",
				annotationGcsfuseSidecarEphermeralStorageLimitKey:   "50Gi",
				annotationGcsfuseSidecarCPURequestKey:               "0",
				annotationGcsfuseSidecarMemoryRequestKey:            "0",
				annotationGcsfuseSidecarEphermeralStorageRequestKey: "0",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig: &Config{
				ContainerImage:                FakeConfig().ContainerImage,
				TerminationGracePeriodSeconds: 10,
				ImagePullPolicy:               FakeConfig().ImagePullPolicy,
				CPULimit:                      resource.MustParse("500m"),
				CPURequest:                    resource.Quantity{},
				MemoryLimit:                   resource.MustParse("1Gi"),
				MemoryRequest:                 resource.Quantity{},
				EphemeralStorageLimit:         resource.MustParse("50Gi"),
				EphemeralStorageRequest:       resource.Quantity{},
			},
			expectErr: false,
		},
		{
			name: "invalid resource Quantity should throw error",
			annotations: map[string]string{
				AnnotationGcsfuseVolumeEnableKey:    "true",
				annotationGcsfuseSidecarCPULimitKey: "invalid",
			},
			terminationGracePeriodSeconds: 10,
			wantConfig:                    nil,
			expectErr:                     true,
		},
	}

	for n, tc := range testCases {
		t.Logf("test case %v: %s", n+1, tc.name)
		si := SidecarInjector{
			Client:  nil,
			Config:  FakeConfig(),
			Decoder: admission.NewDecoder(runtime.NewScheme()),
		}
		gotConfig, gotErr := si.prepareConfig(tc.annotations, tc.terminationGracePeriodSeconds)
		if tc.expectErr != (gotErr != nil) {
			t.Errorf("Expect error: %v, but got error: %v", tc.expectErr, gotErr)
		}

		if diff := cmp.Diff(gotConfig, tc.wantConfig); diff != "" {
			t.Errorf("config differ (-got, +want)\n%s", diff)
		}
	}
}

func TestValidateMutatingWebhookResponse(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		inputPod     *corev1.Pod
		operation    v1.Operation
		wantResponse admission.Response
	}{
		{
			name:         "Empty request test.",
			inputPod:     nil,
			wantResponse: admission.Errored(http.StatusBadRequest, fmt.Errorf("there is no content to decode")),
		},
		{
			name:      "Invalid resource request test.",
			operation: v1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers:                    []corev1.Container{},
					Volumes:                       []corev1.Volume{},
					TerminationGracePeriodSeconds: ptr.To[int64](60),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationGcsfuseVolumeEnableKey:    "true",
						annotationGcsfuseSidecarCPULimitKey: "invalid",
					},
				},
			},
			wantResponse: admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to parse sidecar container resource allocation from pod annotations: quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$'")),
		},
		{
			name:      "Different operation test.",
			operation: v1.Update,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			wantResponse: admission.Allowed(fmt.Sprintf("No injection required for operation %v.", v1.Update)),
		},
		{
			name:      "Annotation key not found test.",
			operation: v1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			wantResponse: admission.Allowed(fmt.Sprintf("The annotation key %q is not found, no injection required.", AnnotationGcsfuseVolumeEnableKey)),
		},
		{
			name:      "Sidecar already injected test.",
			operation: v1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationGcsfuseVolumeEnableKey: "true",
					},
				},
			},
			wantResponse: admission.Allowed("The sidecar container was injected, no injection required."),
		},
		{
			name:         "Injection successful test.",
			operation:    v1.Create,
			inputPod:     validInputPod(false),
			wantResponse: wantResponse(false, t),
		},
		{
			name:         "Injection with custom sidecar container image successful test.",
			operation:    v1.Create,
			inputPod:     validInputPod(true),
			wantResponse: wantResponse(true, t),
		},
	}

	for n, tc := range testCases {
		t.Logf("test case %v: %s", n+1, tc.name)
		si := SidecarInjector{
			Client:  nil,
			Config:  FakeConfig(),
			Decoder: admission.NewDecoder(runtime.NewScheme()),
		}
		request := &admission.Request{
			AdmissionRequest: v1.AdmissionRequest{
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
			t.Errorf("\nGot injection result: %v, but want: %v.", gotResponse, tc.wantResponse)
			t.Error("Details: ", err)
		}
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
	var wantPaths, gotPaths []string
	for i := 0; i < len(wantResponse.Patches); i++ {
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

func validInputPod(customImage bool) *corev1.Pod {
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
				AnnotationGcsfuseVolumeEnableKey: "true",
			},
		},
	}

	if customImage {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
			Name:  SidecarContainerName,
			Image: "private-repo/fake-sidecar-image:v999.999.999",
		})
	}

	return pod
}

func wantResponse(customImage bool, t *testing.T) admission.Response {
	t.Helper()
	newPod := validInputPod(customImage)
	config := FakeConfig()
	if customImage {
		config.ContainerImage = newPod.Spec.Containers[len(newPod.Spec.Containers)-1].Image
		newPod.Spec.Containers = newPod.Spec.Containers[:len(newPod.Spec.Containers)-1]
	}
	newPod.Spec.Containers = append([]corev1.Container{GetSidecarContainerSpec(config)}, newPod.Spec.Containers...)
	newPod.Spec.Volumes = append(GetSidecarContainerVolumeSpec(), newPod.Spec.Volumes...)

	return admission.PatchResponseFromRaw(serialize(t, validInputPod(customImage)), serialize(t, newPod))
}
