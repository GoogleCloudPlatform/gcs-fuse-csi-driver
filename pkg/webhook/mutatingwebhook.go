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

	"cloud.google.com/go/compute/metadata"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	GcsFuseVolumeEnableAnnotation        = "gke-gcsfuse/volumes"
	GcsFuseNativeSidecarEnableAnnotation = "gke-gcsfuse/enable-native-sidecar"
	cpuLimitAnnotation                   = "gke-gcsfuse/cpu-limit"
	cpuRequestAnnotation                 = "gke-gcsfuse/cpu-request"
	memoryLimitAnnotation                = "gke-gcsfuse/memory-limit"
	memoryRequestAnnotation              = "gke-gcsfuse/memory-request"
	ephemeralStorageLimitAnnotation      = "gke-gcsfuse/ephemeral-storage-limit"
	ephemeralStorageRequestAnnotation    = "gke-gcsfuse/ephemeral-storage-request"
)

type SidecarInjector struct {
	Client client.Client
	// default sidecar container config values, can be overwritten by the pod annotations
	Config        *Config
	Decoder       admission.Decoder
	NodeLister    listersv1.NodeLister
	PvcLister     listersv1.PersistentVolumeClaimLister
	PvLister      listersv1.PersistentVolumeLister
	ServerVersion *version.Version
}

// Handle injects a gcsfuse sidecar container and a emptyDir to incoming qualified pods.
func (si *SidecarInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}

	if err := si.Decoder.Decode(req, pod); err != nil {
		klog.Errorf("Could not decode request: name %q, namespace %q, error: %v", req.Name, req.Namespace, err)

		return admission.Errored(http.StatusBadRequest, err)
	}

	if req.Operation != admissionv1.Create {
		return admission.Allowed(fmt.Sprintf("No injection required for operation %v.", req.Operation))
	}

	enableGcsfuseVolumes, ok := pod.Annotations[GcsFuseVolumeEnableAnnotation]
	if !ok {
		return admission.Allowed(fmt.Sprintf("The annotation key %q is not found, no injection required.", GcsFuseVolumeEnableAnnotation))
	}

	shouldInjectSidecar, err := ParseBool(enableGcsfuseVolumes)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("the acceptable values for %q are 'True', 'true', 'false' or 'False'", GcsFuseVolumeEnableAnnotation))
	}

	if shouldInjectSidecar {
		klog.Infof("found annotation '%v: true' for Pod: Name %q, GenerateName %q, Namespace %q, start to inject the sidecar container.", GcsFuseVolumeEnableAnnotation, pod.Name, pod.GenerateName, pod.Namespace)
	} else {
		return admission.Allowed(fmt.Sprintf("found annotation '%v: false' for Pod: Name %q, GenerateName %q, Namespace %q, no injection required.", GcsFuseVolumeEnableAnnotation, pod.Name, pod.GenerateName, pod.Namespace))
	}

	sidecarInjected, _ := ValidatePodHasSidecarContainerInjected(pod)
	if sidecarInjected {
		return admission.Allowed("The sidecar container was injected, no injection required.")
	}

	config, err := si.prepareConfig(pod.Annotations)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	config.PodHostNetworkSetting = pod.Spec.HostNetwork

	if userProvidedGcsFuseSidecarImage, err := ExtractImageAndDeleteContainer(&pod.Spec, GcsFuseSidecarName); err == nil {
		if userProvidedGcsFuseSidecarImage != "" {
			config.ContainerImage = userProvidedGcsFuseSidecarImage
		}
	} else {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Check support for native sidecar.
	injectAsNativeSidecar, err := si.injectAsNativeSidecar(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to verify native sidecar support: %w", err))
	}

	// Inject container.
	injectSidecarContainer(pod, config, injectAsNativeSidecar)

	// Inject service account volume
	if config.ShouldInjectSAVolume && pod.Spec.HostNetwork {
		projectID, err := metadata.ProjectIDWithContext(ctx)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to get project id: %w", err))
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, GetSATokenVolume(projectID))
	}

	pod.Spec.Volumes = append(GetSidecarContainerVolumeSpec(pod.Spec.Volumes...), pod.Spec.Volumes...)

	// Log pod mutation.
	LogPodMutation(pod, config)

	// Inject metadata prefetch sidecar.
	si.injectMetadataPrefetchSidecarContainer(pod, config, injectAsNativeSidecar)

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to marshal pod: %w", err))
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}
