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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
)

const IstioSidecarName = "istio-proxy"

func (si *SidecarInjector) supportsNativeSidecar() (bool, error) {
	if si.ServerVersion != nil && !si.ServerVersion.AtLeast(minimumSupportedVersion) {
		return false, nil
	}

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
		// Rely on cluster version in the event there's no nodes to reference.
		if si.ServerVersion != nil {
			supportsNativeSidecar = si.ServerVersion.AtLeast(minimumSupportedVersion)
		} else {
			supportsNativeSidecar = false
		}
	}

	return supportsNativeSidecar, nil
}

func injectSidecarContainer(pod *corev1.Pod, config *Config, supportsNativeSidecar bool) {
	nativeSidecarEnabled := true
	if enable, ok := pod.Annotations[GcsFuseNativeSidecarEnableAnnotation]; ok {
		parsedAnnotation, err := ParseBool(enable)
		if err != nil {
			klog.Errorf("failed to parse enableNativeSidecar annotation... ignoring annotation: %v", err)
		} else {
			nativeSidecarEnabled = parsedAnnotation
		}
	}
	if supportsNativeSidecar && nativeSidecarEnabled {
		pod.Spec.InitContainers = insert(pod.Spec.InitContainers, GetNativeSidecarContainerSpec(config), getInjectIndex(pod.Spec.InitContainers))
	} else {
		pod.Spec.Containers = insert(pod.Spec.Containers, GetSidecarContainerSpec(config), getInjectIndex(pod.Spec.Containers))
	}
}

func (si *SidecarInjector) injectMetadataPrefetchSidecarContainer(pod *corev1.Pod, config *Config, supportsNativeSidecar bool) {
	var containerSpec corev1.Container
	var index int

	// Let's check our sidecar is not present anywhere before injecting.
	// This means we wont support the privately hosted sidecar image feature for this sidecar.
	_, presentInContainerList := containerPresent(pod.Spec.Containers, SidecarMetadataPrefetchName)
	_, presentInInitContainerList := containerPresent(pod.Spec.InitContainers, SidecarMetadataPrefetchName)
	if presentInContainerList || presentInInitContainerList {
		klog.Infof(`%s sidecar is already injected in pod "%s", skipping injection...`, SidecarMetadataPrefetchName, pod.Name)

		return
	}

	if supportsNativeSidecar {
		containerSpec = si.GetNativeMetadataPrefetchSidecarContainerSpec(pod, config)
		index = getInjectIndexAfterContainer(pod.Spec.InitContainers, SidecarContainerName)
	} else {
		containerSpec = si.GetMetadataPrefetchSidecarContainerSpec(pod, config)
		index = getInjectIndexAfterContainer(pod.Spec.Containers, SidecarContainerName)
	}

	if len(containerSpec.VolumeMounts) == 0 {
		klog.Info("no volumes are requesting metadata prefetch, skipping metadata prefetch sidecar injection...")

		return
	}

	if index == 0 {
		klog.Warning("gke-gcsfuse-sidecar not found when attempting to inject metadata prefetch sidecar... skipping injection")

		return
	}

	if supportsNativeSidecar {
		pod.Spec.InitContainers = insert(pod.Spec.InitContainers, containerSpec, index)
	} else {
		pod.Spec.Containers = insert(pod.Spec.Containers, containerSpec, index)
	}
}

func insert(a []corev1.Container, value corev1.Container, index int) []corev1.Container {
	// For index == len(a)
	if len(a) == index {
		return append(a, value)
	}

	// For index < len(a)
	a = append(a[:index+1], a[index:]...)
	a[index] = value

	return a
}

func getInjectIndex(containers []corev1.Container) int {
	return getInjectIndexAfterContainer(containers, IstioSidecarName)
}

func getInjectIndexAfterContainer(containers []corev1.Container, containerName string) int {
	idx, present := containerPresent(containers, containerName)
	if present {
		return idx + 1
	}

	return 0
}

// Checks by name matching that the container is present in container list.
func containerPresent(containers []corev1.Container, container string) (int, bool) {
	for idx, c := range containers {
		if c.Name == container {
			return idx, true
		}
	}

	return -1, false
}
