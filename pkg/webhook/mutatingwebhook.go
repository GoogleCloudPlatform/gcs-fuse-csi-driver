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
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/version"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/util/parsers"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	AnnotationGcsfuseVolumeEnableKey                   = "gke-gcsfuse/volumes"
	annotationGcsfuseSidecarCPULimitKey                = "gke-gcsfuse/cpu-limit"
	annotationGcsfuseSidecarMemoryLimitKey             = "gke-gcsfuse/memory-limit"
	annotationGcsfuseSidecarEphemeralStorageLimitKey   = "gke-gcsfuse/ephemeral-storage-limit"
	annotationGcsfuseSidecarCPURequestKey              = "gke-gcsfuse/cpu-request"
	annotationGcsfuseSidecarMemoryRequestKey           = "gke-gcsfuse/memory-request"
	annotationGcsfuseSidecarEphemeralStorageRequestKey = "gke-gcsfuse/ephemeral-storage-request"

	istioSidecarName = "istio-proxy"
)

type SidecarInjector struct {
	Client client.Client
	// default sidecar container config values, can be overwritten by the pod annotations
	Config     *Config
	Decoder    *admission.Decoder
	NodeLister listersv1.NodeLister
}

// Handle injects a gcsfuse sidecar container and a emptyDir to incoming qualified pods.
func (si *SidecarInjector) Handle(_ context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}

	if err := si.Decoder.Decode(req, pod); err != nil {
		klog.Errorf("Could not decode request: name %q, namespace %q, error: %v", req.Name, req.Namespace, err)

		return admission.Errored(http.StatusBadRequest, err)
	}

	if req.Operation != admissionv1.Create {
		return admission.Allowed(fmt.Sprintf("No injection required for operation %v.", req.Operation))
	}

	enableGcsfuseVolumes, ok := pod.Annotations[AnnotationGcsfuseVolumeEnableKey]
	if !ok {
		return admission.Allowed(fmt.Sprintf("The annotation key %q is not found, no injection required.", AnnotationGcsfuseVolumeEnableKey))
	}

	switch strings.ToLower(enableGcsfuseVolumes) {
	case "false":
		return admission.Allowed(fmt.Sprintf("found annotation '%v: false' for Pod: Name %q, GenerateName %q, Namespace %q, no injection required.", AnnotationGcsfuseVolumeEnableKey, pod.Name, pod.GenerateName, pod.Namespace))
	case "true":
		klog.Infof("found annotation '%v: true' for Pod: Name %q, GenerateName %q, Namespace %q, start to inject the sidecar container.", AnnotationGcsfuseVolumeEnableKey, pod.Name, pod.GenerateName, pod.Namespace)
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("the acceptable values for %q are 'True', 'true', 'false' or 'False'", AnnotationGcsfuseVolumeEnableKey))
	}

	sidecarInjected, _ := ValidatePodHasSidecarContainerInjected(pod, true)
	if sidecarInjected {
		return admission.Allowed("The sidecar container was injected, no injection required.")
	}

	config, err := si.prepareConfig(pod.Annotations)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if image, err := parseSidecarContainerImage(pod); err == nil {
		if image != "" {
			config.ContainerImage = image
		}
	} else {
		return admission.Errored(http.StatusBadRequest, err)
	}

	klog.Infof("mutating Pod: Name %q, GenerateName %q, Namespace %q, sidecar image %q, CPU request %q, CPU limit %q, memory request %q, memory limit %q, ephemeral storage request %q, ephemeral storage limit %q",
		pod.Name, pod.GenerateName, pod.Namespace, config.ContainerImage, &config.CPURequest, &config.CPULimit, &config.MemoryRequest, &config.MemoryLimit, &config.EphemeralStorageRequest, &config.EphemeralStorageLimit)

	// Check support for native sidecar.
	supportsNativeSidecar, err := si.supportsNativeSidecar()
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to verify native sidecar support: %w", err))
	}

	// Inject container.
	injectSidecarContainer(pod, config, supportsNativeSidecar)

	pod.Spec.Volumes = append(GetSidecarContainerVolumeSpec(pod.Spec.Volumes), pod.Spec.Volumes...)
	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to marshal pod: %w", err))
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

// use the default config values,
// overwritten by the user input from pod annotations.
func (si *SidecarInjector) prepareConfig(annotations map[string]string) (*Config, error) {
	config := &Config{
		ContainerImage:  si.Config.ContainerImage,
		ImagePullPolicy: si.Config.ImagePullPolicy,
	}

	jsonData, err := json.Marshal(annotations)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pod annotations: %w", err)
	}

	if err := json.Unmarshal(jsonData, config); err != nil {
		return nil, fmt.Errorf("failed to parse sidecar container resource allocation from pod annotations: %w", err)
	}

	// if both of the request and limit are unset, assign the default values.
	// if one of the request or limit is set and another is unset, enforce them to be set as the same.
	populateResource := func(rq, lq *resource.Quantity, drq, dlq resource.Quantity) {
		if rq.Format == "" && lq.Format == "" {
			*rq = drq
			*lq = dlq
		}

		if lq.Format == "" {
			*lq = *rq
		}

		if rq.Format == "" {
			*rq = *lq
		}
	}

	populateResource(&config.CPURequest, &config.CPULimit, si.Config.CPURequest, si.Config.CPULimit)
	populateResource(&config.MemoryRequest, &config.MemoryLimit, si.Config.MemoryRequest, si.Config.MemoryLimit)
	populateResource(&config.EphemeralStorageRequest, &config.EphemeralStorageLimit, si.Config.EphemeralStorageRequest, si.Config.EphemeralStorageLimit)

	return config, nil
}

// iterates the container list,
// if a container is named "gke-gcsfuse-sidecar",
// extract the container image and check if the image is valid,
// then removes this container from the container list.
func parseSidecarContainerImage(pod *corev1.Pod) (string, error) {
	var image string
	var index int
	for i, c := range pod.Spec.Containers {
		if c.Name == SidecarContainerName {
			image = c.Image
			index = i

			if _, _, _, err := parsers.ParseImageName(image); err != nil {
				return "", fmt.Errorf("could not parse input image: %q, error: %w", image, err)
			}
		}
	}

	if image != "" {
		copy(pod.Spec.Containers[index:], pod.Spec.Containers[index+1:])
		pod.Spec.Containers = pod.Spec.Containers[:len(pod.Spec.Containers)-1]
	}

	return image, nil
}

func MustParseVersion(v string) *version.Version {
	minimumSupportedVersion, err := version.ParseGeneric(v)
	if err != nil {
		panic(err)
	}

	return minimumSupportedVersion
}

var minimumSupportedVersion = MustParseVersion("1.29.0")

func (si *SidecarInjector) supportsNativeSidecar() (bool, error) {
	clusterNodes, err := si.NodeLister.List(labels.Everything())
	if err != nil {
		return false, fmt.Errorf("failed to get cluster nodes: %w", err)
	}

	supportsNativeSidecar := true
	for _, node := range clusterNodes {
		nodeVersion, err := version.ParseGeneric(node.Status.NodeInfo.KubeletVersion)
		if !nodeVersion.AtLeast(minimumSupportedVersion) || err != nil {
			if err != nil {
				klog.Errorf(`invalid node gke version: could not get node "%s" k8s release from version "%s": "%v"`, node.Name, nodeVersion, err)
			}
			supportsNativeSidecar = false

			break
		}
	}

	if len(clusterNodes) == 0 {
		// TODO(jaimebz): Rely on cluster version in the event there's no nodes to reference.
		supportsNativeSidecar = false
	}

	return supportsNativeSidecar, nil
}

func injectSidecarContainer(pod *corev1.Pod, config *Config, supportsNativeSidecar bool) {
	if supportsNativeSidecar {
		sidecarSpec := GetNativeSidecarContainerSpec(config)
		if sidecarPresentAtFirstPosition(pod.Spec.InitContainers, istioSidecarName) {
			pod.Spec.InitContainers = injectAtSecondPosition(pod.Spec.InitContainers, sidecarSpec)
		} else {
			pod.Spec.InitContainers = append([]corev1.Container{sidecarSpec}, pod.Spec.InitContainers...)
		}
	} else {
		sidecarSpec := GetSidecarContainerSpec(config)
		if sidecarPresentAtFirstPosition(pod.Spec.Containers, istioSidecarName) {
			pod.Spec.Containers = injectAtSecondPosition(pod.Spec.Containers, sidecarSpec)
		} else {
			pod.Spec.Containers = append([]corev1.Container{sidecarSpec}, pod.Spec.Containers...)
		}
	}
}

func injectAtSecondPosition(containers []corev1.Container, sidecar corev1.Container) []corev1.Container {
	const index = 1
	if len(containers) == 0 {
		return []corev1.Container{sidecar}
	}
	containers = append(containers, corev1.Container{})
	copy(containers[index+1:], containers[index:])
	containers[index] = sidecar

	return containers
}

// Checks the first index of the container array for the istio container sidecar.
func sidecarPresentAtFirstPosition(containers []corev1.Container, sidecarName string) bool {
	if len(containers) == 0 {
		return false
	}

	return (containers[0].Name == sidecarName)
}
