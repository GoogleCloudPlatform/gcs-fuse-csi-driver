/*
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
	"strings"

	v1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	annotationGcsfuseVolumeEnableKey                  = "gke-gcsfuse/volumes"
	annotationGcsfuseSidecarCPULimitKey               = "gke-gcsfuse/cpu-limit"
	annotationGcsfuseSidecarMemoryLimitKey            = "gke-gcsfuse/memory-limit"
	annotationGcsfuseSidecarEphermeralStorageLimitKey = "gke-gcsfuse/ephemeral-storage-limit"
)

type SidecarInjector struct {
	Client  client.Client
	Config  *Config
	decoder *admission.Decoder
}

// Handle injects a gcsfuse sidecar container and a emptyDir to incoming qualified pods.
func (si *SidecarInjector) Handle(_ context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}

	if err := si.decoder.Decode(req, pod); err != nil {
		klog.Error("Could not decode request: name %q, namespace %q, error: ", req.Name, req.Namespace, err)

		return admission.Errored(http.StatusBadRequest, err)
	}

	if req.Operation != v1.Create {
		return admission.Allowed(fmt.Sprintf("No injection required for operation %v.", req.Operation))
	}

	if v, ok := pod.Annotations[annotationGcsfuseVolumeEnableKey]; !ok || strings.ToLower(v) != "true" {
		return admission.Allowed(fmt.Sprintf("The annotation key %q is not found, no injection required.", annotationGcsfuseVolumeEnableKey))
	}

	enableGcsfuseVolumes, ok := pod.Annotations[annotationGcsfuseVolumeEnableKey]
	if !ok || strings.ToLower(enableGcsfuseVolumes) == "false" {
		return admission.Allowed("No injection required.")
	}

	if strings.ToLower(enableGcsfuseVolumes) != "true" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("the acceptable values for %q are 'True', 'true', 'false' or 'False'", annotationGcsfuseVolumeEnableKey))
	}

	if ValidatePodHasSidecarContainerInjected(si.Config.ContainerImage, pod) {
		return admission.Allowed("The sidecar container was injected, no injection required.")
	}

	configCopy := &Config{
		ContainerImage:        si.Config.ContainerImage,
		ImageVersion:          si.Config.ImageVersion,
		ImagePullPolicy:       si.Config.ImagePullPolicy,
		CPULimit:              si.Config.CPULimit.DeepCopy(),
		MemoryLimit:           si.Config.MemoryLimit.DeepCopy(),
		EphemeralStorageLimit: si.Config.EphemeralStorageLimit.DeepCopy(),
	}
	if v, ok := pod.Annotations[annotationGcsfuseSidecarCPULimitKey]; ok {
		if q, err := resource.ParseQuantity(v); err == nil {
			configCopy.CPULimit = q
		} else {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("bad value %q for %q: %w", v, annotationGcsfuseSidecarCPULimitKey, err))
		}
	}

	if v, ok := pod.Annotations[annotationGcsfuseSidecarMemoryLimitKey]; ok {
		if q, err := resource.ParseQuantity(v); err == nil {
			configCopy.MemoryLimit = q
		} else {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("bad value %q for %q: %w", v, annotationGcsfuseSidecarMemoryLimitKey, err))
		}
	}

	if v, ok := pod.Annotations[annotationGcsfuseSidecarEphermeralStorageLimitKey]; ok {
		if q, err := resource.ParseQuantity(v); err == nil {
			configCopy.EphemeralStorageLimit = q
		} else {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("bad value %q for %q: %w", v, annotationGcsfuseSidecarEphermeralStorageLimitKey, err))
		}
	}

	klog.Infof("mutating Pod: Name %q, GenerateName %q, Namespace %q, CPU limit %q, memory limit %q, ephemeral storage limit %q", pod.Name, pod.GenerateName, pod.Namespace, configCopy.CPULimit.String(), configCopy.MemoryLimit.String(), configCopy.EphemeralStorageLimit.String())
	// the gcsfuse sidecar container has to before the containers that consume the gcsfuse volume
	pod.Spec.Containers = append([]corev1.Container{GetSidecarContainerSpec(configCopy)}, pod.Spec.Containers...)
	pod.Spec.Volumes = append([]corev1.Volume{GetSidecarContainerVolumeSpec()}, pod.Spec.Volumes...)
	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to marshal pod: %w", err))
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

// InjectDecoder injects the decoder.
// SidecarInjector implements admission.DecoderInjector.
// A decoder will be automatically injected.
func (si *SidecarInjector) InjectDecoder(d *admission.Decoder) error {
	si.decoder = d

	return nil
}
