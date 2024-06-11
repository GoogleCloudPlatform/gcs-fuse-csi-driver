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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

const (
	SidecarContainerName                  = "gke-gcsfuse-sidecar"
	SidecarContainerTmpVolumeName         = "gke-gcsfuse-tmp"
	SidecarContainerTmpVolumeMountPath    = "/gcsfuse-tmp"
	SidecarContainerBufferVolumeName      = "gke-gcsfuse-buffer"
	SidecarContainerBufferVolumeMountPath = "/gcsfuse-buffer"
	SidecarContainerCacheVolumeName       = "gke-gcsfuse-cache"
	SidecarContainerCacheVolumeMountPath  = "/gcsfuse-cache"
	SidecarContainerSAVolumeName          = "gke-gcsfuse-sa"
	SidecarContainerSAVolumeMountPath     = "/gcsfuse-sa"

	// See the nonroot user discussion: https://github.com/GoogleContainerTools/distroless/issues/443
	NobodyUID = 65534
	NobodyGID = 65534
)

var (
	tmpVolume = corev1.Volume{
		Name: SidecarContainerTmpVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}

	buffVolume = corev1.Volume{
		Name: SidecarContainerBufferVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}

	cacheVolume = corev1.Volume{
		Name: SidecarContainerCacheVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}

	TmpVolumeMount = corev1.VolumeMount{
		Name:      SidecarContainerTmpVolumeName,
		MountPath: SidecarContainerTmpVolumeMountPath,
	}

	buffVolumeMount = corev1.VolumeMount{
		Name:      SidecarContainerBufferVolumeName,
		MountPath: SidecarContainerBufferVolumeMountPath,
	}

	cacheVolumeMount = corev1.VolumeMount{
		Name:      SidecarContainerCacheVolumeName,
		MountPath: SidecarContainerCacheVolumeMountPath,
	}

	saVolumeMount = corev1.VolumeMount{
		Name:      SidecarContainerSAVolumeName,
		MountPath: SidecarContainerSAVolumeMountPath,
		ReadOnly:  true,
	}
)

func GetNativeSidecarContainerSpec(c *Config) corev1.Container {
	container := GetSidecarContainerSpec(c)
	container.Env = append(container.Env, corev1.EnvVar{Name: "NATIVE_SIDECAR", Value: "TRUE"})
	container.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)

	return container
}

func GetSidecarContainerSpec(c *Config) corev1.Container {
	limits, requests := prepareResourceList(c)

	// The sidecar container follows Restricted Pod Security Standard,
	// see https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted
	container := corev1.Container{
		Name:            SidecarContainerName,
		Image:           c.ContainerImage,
		ImagePullPolicy: corev1.PullPolicy(c.ImagePullPolicy),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{
					corev1.Capability("ALL"),
				},
			},
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			RunAsNonRoot:   ptr.To(true),
			RunAsUser:      ptr.To(int64(NobodyUID)),
			RunAsGroup:     ptr.To(int64(NobodyGID)),
		},
		Args: []string{
			"--v=5",
		},
		Resources: corev1.ResourceRequirements{
			Limits:   limits,
			Requests: requests,
		},
		VolumeMounts: []corev1.VolumeMount{TmpVolumeMount, buffVolumeMount, cacheVolumeMount},
	}

	if c.GCPServiceAccountSecretName != "" {
		container.VolumeMounts = append(container.VolumeMounts, saVolumeMount)
	}

	return container
}

// GetSidecarContainerVolumeSpec returns volumes required by the sidecar container,
// skipping the existing custom volumes.
func GetSidecarContainerVolumeSpec(existingVolumes ...corev1.Volume) []corev1.Volume {
	volumes := []corev1.Volume{tmpVolume}
	var bufferVolumeExists, cacheVolumeExists bool

	for _, v := range existingVolumes {
		switch v.Name {
		case SidecarContainerBufferVolumeName:
			bufferVolumeExists = true
		case SidecarContainerCacheVolumeName:
			cacheVolumeExists = true
		}
	}

	if !bufferVolumeExists {
		volumes = append(volumes, buffVolume)
	}

	if !cacheVolumeExists {
		volumes = append(volumes, cacheVolume)
	}

	return volumes
}

// ValidatePodHasSidecarContainerInjected validates the following:
//  1. One of the container or init container name matches the sidecar container name.
//  2. The container uses NobodyUID and NobodyGID.
//  3. The container uses the temp volume.
//  4. The temp volume have correct volume mount paths.
//  5. The Pod has the temp volume and the volume is an emptyDir volumes.
//
// Returns two booleans:
//  1. True when either native or regular sidecar is present.
//  2. True iff the sidecar present is a native sidecar container.
func ValidatePodHasSidecarContainerInjected(pod *corev1.Pod, shouldInjectedByWebhook bool) (bool, bool) {
	return validatePodHasSidecarContainerInjected(SidecarContainerName, pod, []corev1.Volume{tmpVolume}, []corev1.VolumeMount{TmpVolumeMount}, shouldInjectedByWebhook)
}

func sidecarContainerPresent(containerName string, containers []corev1.Container, volumeMounts []corev1.VolumeMount, shouldInjectedByWebhook bool) bool {
	containerInjected := false
	volumeMountMap := map[string]string{}
	for _, vm := range volumeMounts {
		volumeMountMap[vm.Name] = vm.MountPath
	}

	for i, c := range containers {
		if c.Name == containerName {
			// If the sidecar container is injected by the webhook,
			// the sidecar container needs to be at 0 index,
			// unless the istio-proxy is present, then we check
			// the container is present immediately after istio-proxy.
			if shouldInjectedByWebhook {
				containerAtValidIndex := false

				idx, present := containerPresent(containers, IstioSidecarName)
				if i == 0 && !present {
					containerAtValidIndex = true
				}

				if i == idx+1 && present {
					containerAtValidIndex = true
				}

				if !containerAtValidIndex {
					break
				}
			}

			if c.SecurityContext != nil &&
				c.SecurityContext.RunAsUser != nil &&
				c.SecurityContext.RunAsGroup != nil &&
				*c.SecurityContext.RunAsUser == NobodyUID &&
				*c.SecurityContext.RunAsGroup == NobodyGID {
				containerInjected = true
			}
			// Delete volumeMounts present from map.
			for _, vm := range c.VolumeMounts {
				if mountPath, exists := volumeMountMap[vm.Name]; exists {
					if vm.MountPath == mountPath {
						delete(volumeMountMap, vm.Name)
					}
				}
			}

			break
		}
	}

	if containerInjected && len(volumeMountMap) == 0 {
		return true
	}

	return false
}

func validatePodHasSidecarContainerInjected(containerName string, pod *corev1.Pod, volumes []corev1.Volume, volumeMounts []corev1.VolumeMount, shouldInjectedByWebhook bool) (bool, bool) {
	// Checks that the default emptyDir volumes are present in pod, skipping the custom volumes.
	volumesInjected := func(pod *corev1.Pod) bool {
		volumeMap := map[string]corev1.EmptyDirVolumeSource{}
		for _, v := range volumes {
			volumeMap[v.Name] = *v.EmptyDir
		}

		// volumeMap/volumes represents all of the volumes that should be present in the pod.
		for _, v := range pod.Spec.Volumes {
			if _, exists := volumeMap[v.Name]; exists {
				if v.EmptyDir != nil {
					delete(volumeMap, v.Name)
				}
			}
		}

		return len(volumeMap) == 0
	}

	// Check the sidecar container is present in regular or init container list.
	containerAndVolumeMountPresentInContainers := sidecarContainerPresent(containerName, pod.Spec.Containers, volumeMounts, shouldInjectedByWebhook)
	containerAndVolumeMountPresentInInitContainers := sidecarContainerPresent(containerName, pod.Spec.InitContainers, volumeMounts, shouldInjectedByWebhook)

	if containerAndVolumeMountPresentInContainers && containerAndVolumeMountPresentInInitContainers {
		klog.Errorf("sidecar present in containers and init containers... make sure only one sidecar is present.")
	}

	if !containerAndVolumeMountPresentInContainers && !containerAndVolumeMountPresentInInitContainers {
		return false, false
	}

	// We continue validation if all sidecar volumes are present in the pod.
	if !volumesInjected(pod) {
		return false, false
	}

	return true, containerAndVolumeMountPresentInInitContainers
}
