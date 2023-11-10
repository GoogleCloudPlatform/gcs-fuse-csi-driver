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
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	v1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestValidateMutatingWebhookResponse(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		inputPod         *corev1.Pod
		operation        v1.Operation
		expectedResponse admission.Response
	}{
		{
			name:             "Empty request test.",
			inputPod:         nil,
			expectedResponse: admission.Errored(http.StatusBadRequest, fmt.Errorf("there is no content to decode")),
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
			expectedResponse: admission.Allowed(fmt.Sprintf("No injection required for operation %v.", v1.Update)),
		},
		{
			name:      "Annotation key not found test",
			operation: v1.Create,
			inputPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
					Volumes:    GetSidecarContainerVolumeSpec(),
				},
			},
			expectedResponse: admission.Allowed(fmt.Sprintf("The annotation key %q is not found, no injection required.", AnnotationGcsfuseVolumeEnableKey)),
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
						"gke-gcsfuse/volumes": "true",
					},
				},
			},
			expectedResponse: admission.Allowed("The sidecar container was injected, no injection required."),
		},
		{
			name:             "Injection successful test.",
			operation:        v1.Create,
			inputPod:         InputPodTest5(),
			expectedResponse: ResponseTest5(t, InputPodTest5()),
		},
	}

	for n, tc := range testCases {
		t.Logf("test case %v: %s", n, tc.name)
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
				Raw: RawPayload(t, tc.inputPod),
			}
		}

		response := si.Handle(context.Background(), *request)

		if err := compareResponses(t, tc.expectedResponse, response); err != nil {
			t.Errorf("\nGot injection result: %v, but expected: %v.", response, tc.expectedResponse)
		}
	}
}

func RawPayload(t *testing.T, pod *corev1.Pod) []byte {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Errorf("Error running json encoding for pod.")

		return nil
	}

	return raw
}

func compareResponses(t *testing.T, expectedResponse, response admission.Response) error {
	if diff := cmp.Diff(response.String(), expectedResponse.String()); diff != "" {
		return fmt.Errorf("request args differ (-got, +expected)\n%s", diff)
	}
	if len(expectedResponse.Patches) != len(response.Patches) {
		return fmt.Errorf("expecting %d patches, got %d patches", len(response.Patches), len(response.Patches))
	}
	var expectedPaths, gotPaths []string
	for i := 0; i < len(expectedResponse.Patches); i++ {
		expectedPaths = append(expectedPaths, expectedResponse.Patches[i].Path)
		gotPaths = append(gotPaths, response.Patches[i].Path)
	}
	sort.Strings(expectedPaths)
	sort.Strings(gotPaths)
	if diff := cmp.Diff(expectedPaths, gotPaths); diff != "" {
		return fmt.Errorf("unexpected pod args (-got, +expected)\n%s", diff)
	}
	return nil
}

func InputPodTest5() *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: ptr.To[int64](60),
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"gke-gcsfuse/volumes": "true",
			},
		},
	}
}

func ExpectedPodTest5() *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers:                    []corev1.Container{GetSidecarContainerSpec(FakeConfig())},
			Volumes:                       GetSidecarContainerVolumeSpec(),
			TerminationGracePeriodSeconds: ptr.To[int64](60),
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"gke-gcsfuse/volumes": "true",
			},
		},
	}
}

func ResponseTest5(t *testing.T, inputPod *corev1.Pod) admission.Response {
	t.Helper()
	newPod, err := json.Marshal(ExpectedPodTest5())
	if err != nil {
		t.Errorf("Error running json encoding for test 5.")

		return admission.Response{}
	}

	return admission.PatchResponseFromRaw(RawPayload(t, inputPod), newPod)
}
