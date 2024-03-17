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
	"k8s.io/apimachinery/pkg/api/resource"
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

	// See the nonroot user discussion: https://github.com/GoogleContainerTools/distroless/issues/443
	NobodyUID = 65534
	NobodyGID = 65534
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
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      SidecarContainerTmpVolumeName,
				MountPath: SidecarContainerTmpVolumeMountPath,
			},
			{
				Name:      SidecarContainerBufferVolumeName,
				MountPath: SidecarContainerBufferVolumeMountPath,
			},
			{
				Name:      SidecarContainerCacheVolumeName,
				MountPath: SidecarContainerCacheVolumeMountPath,
			},
		},
	}

	return container
}

func GetSidecarContainerVolumeSpec(existingVolumes []corev1.Volume) []corev1.Volume {
	var bufferVolumeExisted, cacheVolumeExisted bool

	for _, v := range existingVolumes {
		switch v.Name {
		case SidecarContainerBufferVolumeName:
			bufferVolumeExisted = true
		case SidecarContainerCacheVolumeName:
			cacheVolumeExisted = true
		}
	}

	volumes := []corev1.Volume{
		{
			Name: SidecarContainerTmpVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	if !bufferVolumeExisted {
		volumes = append(volumes, corev1.Volume{
			Name: SidecarContainerBufferVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	if !cacheVolumeExisted {
		volumes = append(volumes, corev1.Volume{
			Name: SidecarContainerCacheVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	return volumes
}

func sidecarContainerPresent(containers []corev1.Container, shouldInjectedByWebhook bool) bool {
	containerInjected := false
	tempVolumeMountInjected := false

	for i, c := range containers {
		if c.Name == SidecarContainerName {
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
				*c.SecurityContext.RunAsUser == NobodyUID &&
				*c.SecurityContext.RunAsGroup == NobodyGID {
				containerInjected = true
			}

			for _, v := range c.VolumeMounts {
				if v.Name == SidecarContainerTmpVolumeName && v.MountPath == SidecarContainerTmpVolumeMountPath {
					tempVolumeMountInjected = true
				}
			}

			break
		}
	}

	if containerInjected && tempVolumeMountInjected {
		return true
	}

	return false
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
	// Checks that the temp volume is present in pod.
	tempVolumeInjected := func(pod *corev1.Pod) bool {
		tmpVolumeInjected := false
		for _, v := range pod.Spec.Volumes {
			if v.Name == SidecarContainerTmpVolumeName && v.VolumeSource.EmptyDir != nil {
				tmpVolumeInjected = true

				break
			}
		}

		return tmpVolumeInjected
	}

	// Check the sidecar container is present in regular or init container list.
	containerAndVolumeMountPresentInContainers := sidecarContainerPresent(pod.Spec.Containers, shouldInjectedByWebhook)
	containerAndVolumeMountPresentInInitContainers := sidecarContainerPresent(pod.Spec.InitContainers, shouldInjectedByWebhook)

	if containerAndVolumeMountPresentInContainers && containerAndVolumeMountPresentInInitContainers {
		klog.Errorf("sidecar present in containers and init containers... make sure only one sidecar is present.")
	}

	if !containerAndVolumeMountPresentInContainers && !containerAndVolumeMountPresentInInitContainers {
		return false, false
	}

	if !tempVolumeInjected(pod) {
		return false, false
	}

	return true, containerAndVolumeMountPresentInInitContainers
}

func prepareResourceList(c *Config) (corev1.ResourceList, corev1.ResourceList) {
	limitsResourceList := corev1.ResourceList{}
	requestsResourceList := corev1.ResourceList{}

	checkZeroQuantity := func(rl map[corev1.ResourceName]resource.Quantity, rn corev1.ResourceName, q resource.Quantity) {
		if !q.IsZero() {
			rl[rn] = q
		}
	}

	checkZeroQuantity(limitsResourceList, corev1.ResourceCPU, c.CPULimit)
	checkZeroQuantity(limitsResourceList, corev1.ResourceMemory, c.MemoryLimit)
	checkZeroQuantity(limitsResourceList, corev1.ResourceEphemeralStorage, c.EphemeralStorageLimit)
	checkZeroQuantity(requestsResourceList, corev1.ResourceCPU, c.CPURequest)
	checkZeroQuantity(requestsResourceList, corev1.ResourceMemory, c.MemoryRequest)
	checkZeroQuantity(requestsResourceList, corev1.ResourceEphemeralStorage, c.EphemeralStorageRequest)

	return limitsResourceList, requestsResourceList
}
