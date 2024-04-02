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
	if supportsNativeSidecar {
		pod.Spec.InitContainers = insert(pod.Spec.InitContainers, GetNativeSidecarContainerSpec(config), getInjectIndex(pod.Spec.InitContainers))
	} else {
		pod.Spec.Containers = insert(pod.Spec.Containers, GetSidecarContainerSpec(config), getInjectIndex(pod.Spec.Containers))
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
	idx, present := containerPresent(containers, IstioSidecarName)
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
