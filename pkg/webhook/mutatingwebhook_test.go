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
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
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

func TestPrepareConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		annotations map[string]string
		wantConfig  *Config
		expectErr   bool
	}{
		{
			name: "use default values if no annotation is found",
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
			name: "only limits are specified",
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
			name: "only requests are specified",
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
			name: "limits are set to '0'",
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
			name: "requests are set to '0'",
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
			name: "requests and limits are explicitly set",
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
			name: "requests and limits are explicitly set with '0' limits",
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
			name: "requests and limits are explicitly set with '0' requests",
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
			name: "invalid resource Quantity should throw error",
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
			Client:  nil,
			Config:  FakeConfig(),
			Decoder: admission.NewDecoder(runtime.NewScheme()),
		}
		gotConfig, gotErr := si.prepareConfig(tc.annotations)
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
			name:         "container injection successful test with multiple sidecar entries present",
			operation:    admissionv1.Create,
			inputPod:     getDuplicateDeclarationPodSpec(),
			wantResponse: generatePatch(t, getDuplicateDeclarationPodSpec(), getDuplicateDeclarationPodSpecResponse()),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "regular container injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPod(false),
			wantResponse: wantResponse(t, false, false),
			nodes:        skewVersionNodes(),
		},
		{
			name:         "native container set via annotation injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, "true"),
			wantResponse: wantResponse(t, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container set via annotation injection successful with custom image test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(true, "true"),
			wantResponse: wantResponse(t, true, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "regular container set via annotation injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, "false"),
			wantResponse: wantResponse(t, false, false),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container set via invalid annotation injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, "maybe"),
			wantResponse: wantResponse(t, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "native container set via annotation injection unsupported test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithNativeAnnotation(false, "true"),
			wantResponse: wantResponse(t, false, false),
			nodes:        skewVersionNodes(),
		},
		{
			name:         "Injection with custom sidecar container image successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPod(true),
			wantResponse: wantResponse(t, true, false),
			nodes:        regularSidecarSupportNodes(),
		},
		{
			name:         "native container injection successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPod(false),
			wantResponse: wantResponse(t, false, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "Injection with custom sidecar container image successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPod(true),
			wantResponse: wantResponse(t, true, true),
			nodes:        nativeSupportNodes(),
		},
		{
			name:         "regular container injection with istio present success test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithIstio(false, false),
			wantResponse: wantResponseWithIstio(t, false, false),
			nodes:        skewVersionNodes(),
		},
		{
			name:         "Injection with custom sidecar container image successful test.",
			operation:    admissionv1.Create,
			inputPod:     validInputPodWithIstio(true, true),
			wantResponse: wantResponseWithIstio(t, true, true),
			nodes:        nativeSupportNodes(),
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

		si := SidecarInjector{
			Client:     nil,
			Config:     FakeConfig(),
			Decoder:    admission.NewDecoder(runtime.NewScheme()),
			NodeLister: lister,
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
					Name:  SidecarContainerName,
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
					Name:  SidecarContainerName,
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

func getDuplicateDeclarationPodSpecResponse() *corev1.Pod {
	result := modifySpec(*validInputPod(true), true, true)

	return result
}

func validInputPodWithNativeAnnotation(customImage bool, enableNativeSidecarAnnotation string) *corev1.Pod {
	pod := validInputPod(customImage)
	pod.ObjectMeta.Annotations[GcsFuseNativeSidecarEnableAnnotation] = enableNativeSidecarAnnotation

	return pod
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
				GcsFuseVolumeEnableAnnotation: "true",
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

func getWorkloadSpec(name string) corev1.Container {
	return corev1.Container{
		Name:  name,
		Image: "busybox",
	}
}

func validInputPodWithIstio(customImage, nativeIstio bool) *corev1.Pod {
	pod := validInputPod(customImage)

	if nativeIstio {
		pod.Spec.InitContainers = append([]corev1.Container{istioContainer}, pod.Spec.InitContainers...)
	} else {
		pod.Spec.Containers = append([]corev1.Container{istioContainer}, pod.Spec.Containers...)
	}

	return pod
}

func wantResponse(t *testing.T, customImage bool, native bool) admission.Response {
	t.Helper()
	pod := *validInputPod(customImage)
	newPod := *modifySpec(*validInputPod(customImage), customImage, native)

	return generatePatch(t, &pod, &newPod)
}

func modifySpec(newPod corev1.Pod, customImage bool, native bool) *corev1.Pod {
	config := FakeConfig()
	if customImage {
		config.ContainerImage = newPod.Spec.Containers[len(newPod.Spec.Containers)-1].Image
		newPod.Spec.Containers = newPod.Spec.Containers[:len(newPod.Spec.Containers)-1]
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

func wantResponseWithIstio(t *testing.T, customImage bool, native bool) admission.Response {
	t.Helper()

	originalPod := validInputPod(customImage)
	if native {
		originalPod.Spec.InitContainers = append([]corev1.Container{istioContainer}, originalPod.Spec.InitContainers...)
	} else {
		originalPod.Spec.Containers = append([]corev1.Container{istioContainer}, originalPod.Spec.Containers...)
	}

	newPod := validInputPod(customImage)
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
