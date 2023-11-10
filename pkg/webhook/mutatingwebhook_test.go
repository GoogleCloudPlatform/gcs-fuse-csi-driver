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
			name:      "Annotation key not found test",
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
						"gke-gcsfuse/volumes": "true",
					},
				},
			},
			wantResponse: admission.Allowed("The sidecar container was injected, no injection required."),
		},
		{
			name:         "Injection successful on empty containers and volumes test.",
			operation:    v1.Create,
			inputPod:     validInputPod(),
			wantResponse: wantResponse(t),
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

		if err := compareResponses(t, tc.wantResponse, gotResponse); err != nil {
			t.Errorf("\nGot injection result: %v, but want: %v.", gotResponse, tc.wantResponse)
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

func compareResponses(t *testing.T, wantResponse, gotResponse admission.Response) error {
	if diff := cmp.Diff(gotResponse.String(), wantResponse.String()); diff != "" {
		return fmt.Errorf("request args differ (-got, +want)\n%s", diff)
	}
	if len(wantResponse.Patches) != len(gotResponse.Patches) {
		return fmt.Errorf("expecting %d patches, got %d patches", len(gotResponse.Patches), len(gotResponse.Patches))
	}
	var wantPaths, gotPaths []string
	for i := 0; i < len(wantResponse.Patches); i++ {
		wantPaths = append(wantPaths, wantResponse.Patches[i].Path)
		gotPaths = append(gotPaths, gotResponse.Patches[i].Path)
	}
	sort.Strings(wantPaths)
	sort.Strings(gotPaths)
	if diff := cmp.Diff(wantPaths, gotPaths); diff != "" {
		return fmt.Errorf("unexpected pod args (-got, +want)\n%s", diff)
	}

	return nil
}

func validInputPod() *corev1.Pod {
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

func wantResponse(t *testing.T) admission.Response {
	t.Helper()
	newPod := validInputPod()
	newPod.Spec.Containers = []corev1.Container{GetSidecarContainerSpec(FakeConfig())}
	newPod.Spec.Volumes = GetSidecarContainerVolumeSpec()

	return admission.PatchResponseFromRaw(serialize(t, validInputPod()), serialize(t, newPod))
}
