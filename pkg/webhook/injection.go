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
	"k8s.io/utils/ptr"
)

var sidecarPrefixMap = map[string]string{
	GcsFuseSidecarName:          "gke-gcsfuse/",
	MetadataPrefetchSidecarName: "gke-gcsfuse/metadata-prefetch-",
}

// used to guarantee containers start in the correct sequence based on inter-container dependencies.
var containerIndexOrderMap = map[string]string{
	GcsFuseSidecarName:          IstioSidecarName,
	MetadataPrefetchSidecarName: GcsFuseSidecarName,
}

const IstioSidecarName = "istio-proxy"

func (si *SidecarInjector) injectAsNativeSidecar(pod *corev1.Pod) (bool, error) {
	supportsNativeSidecar, err := si.supportsNativeSidecar()
	if err != nil {
		return false, fmt.Errorf("failed to determine native sidecar injection: %w", err)
	}

	nativeSidecarEnabled := true
	if enable, ok := pod.Annotations[GcsFuseNativeSidecarEnableAnnotation]; ok {
		parsedAnnotation, err := ParseBool(enable)
		if err != nil {
			klog.Errorf("failed to parse enableNativeSidecar annotation: %v", err)
		} else {
			nativeSidecarEnabled = parsedAnnotation
			// Warn the user if they used annotation incorrectly.
			if nativeSidecarEnabled && !supportsNativeSidecar {
				klog.Errorf("attempting to enable native sidecar on a cluster that does not support it, this is not allowed")
			}
		}
	}

	return nativeSidecarEnabled && supportsNativeSidecar, nil
}

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

// InjectSidecarContainer injects a sidecar container into the pod and returns error if unexpected event occurs.
func (si *SidecarInjector) injectSidecarContainer(containerName string, pod *corev1.Pod, injectAsNativeSidecar bool) error {
	var containerSpec corev1.Container
	var index int
	config, err := si.prepareConfig(sidecarPrefixMap[containerName], *pod)
	if err != nil {
		return err
	}
	config.PodHostNetworkSetting = pod.Spec.HostNetwork

	// Extract user provided metadata prefetch sidecar image.
	if userProvidedSidecarImage, err := ExtractImageAndDeleteContainer(&pod.Spec, containerName); err == nil {
		if userProvidedSidecarImage != "" {
			config.ContainerImage = userProvidedSidecarImage
		}
	} else {
		return err
	}

	// Retrieve container spec and index to inject sidecar for either init containers or regular based on native sidecar support.
	if injectAsNativeSidecar {
		containerSpec = si.getNativeContainerSpec(containerName, pod, config)
		index = getInjectIndexAfterContainer(pod.Spec.InitContainers, containerIndexOrderMap[containerName])
	} else {
		containerSpec = si.getContainerSpec(containerName, pod, config)
		index = getInjectIndexAfterContainer(pod.Spec.Containers, containerIndexOrderMap[containerName])
	}

	// Skip metadata prefetch sidecar injection if no volumes are requesting metadata prefetch.
	if containerName == MetadataPrefetchSidecarName && len(containerSpec.VolumeMounts) == 0 {
		klog.Info("no volumes are requesting metadata prefetch, skipping metadata prefetch sidecar injection")

		return nil
	}

	// This should not happen as we always inject the sidecar after injecting our primary gcsfuse sidecar.
	if containerName == MetadataPrefetchSidecarName && index == 0 {
		klog.Warningf("%s not found when attempting to inject container. skipping injection", containerIndexOrderMap[containerName])

		return fmt.Errorf("%s", (containerIndexOrderMap[containerName] + "not found when attempting to inject metadata prefetch. skipping injection"))
	}

	if injectAsNativeSidecar {
		pod.Spec.InitContainers = insert(pod.Spec.InitContainers, containerSpec, index)
	} else {
		pod.Spec.Containers = insert(pod.Spec.Containers, containerSpec, index)
	}
	// Log pod mutation after fuse sidecar injection.
	LogPodMutation(pod, config)

	return nil
}

func (si *SidecarInjector) getNativeContainerSpec(containerName string, pod *corev1.Pod, config *Config) corev1.Container {
	containerSpec := si.getContainerSpec(containerName, pod, config)
	containerSpec.Env = append(containerSpec.Env, corev1.EnvVar{Name: "NATIVE_SIDECAR", Value: "TRUE"})
	containerSpec.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)

	return containerSpec
}

func (si *SidecarInjector) getContainerSpec(containerName string, pod *corev1.Pod, config *Config) corev1.Container {
	if containerName == MetadataPrefetchSidecarName {
		return si.GetMetadataPrefetchSidecarContainerSpec(pod, config)
	}

	return GetSidecarContainerSpec(config)
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
