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
	v1 "k8s.io/api/core/v1"
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

func GetNativeSidecarContainerSpec(c *Config) v1.Container {
	container := GetSidecarContainerSpec(c)
	container.Env = append(container.Env, v1.EnvVar{Name: "NATIVE_SIDECAR", Value: "TRUE"})
	container.RestartPolicy = ptr.To(v1.ContainerRestartPolicyAlways)

	return container
}

func GetSidecarContainerSpec(c *Config) v1.Container {
	limits, requests := prepareResourceList(c)

	// The sidecar container follows Restricted Pod Security Standard,
	// see https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted
	container := v1.Container{
		Name:            SidecarContainerName,
		Image:           c.ContainerImage,
		ImagePullPolicy: v1.PullPolicy(c.ImagePullPolicy),
		SecurityContext: &v1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &v1.Capabilities{
				Drop: []v1.Capability{
					v1.Capability("ALL"),
				},
			},
			SeccompProfile: &v1.SeccompProfile{Type: v1.SeccompProfileTypeRuntimeDefault},
			RunAsNonRoot:   ptr.To(true),
			RunAsUser:      ptr.To(int64(NobodyUID)),
			RunAsGroup:     ptr.To(int64(NobodyGID)),
		},
		Args: []string{
			"--v=5",
		},
		Resources: v1.ResourceRequirements{
			Limits:   limits,
			Requests: requests,
		},
		VolumeMounts: []v1.VolumeMount{
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

func GetSidecarContainerVolumeSpec(existingVolumes []v1.Volume) []v1.Volume {
	var bufferVolumeExisted, cacheVolumeExisted bool

	for _, v := range existingVolumes {
		switch v.Name {
		case SidecarContainerBufferVolumeName:
			bufferVolumeExisted = true
		case SidecarContainerCacheVolumeName:
			cacheVolumeExisted = true
		}
	}

	volumes := []v1.Volume{
		{
			Name: SidecarContainerTmpVolumeName,
			VolumeSource: v1.VolumeSource{
				EmptyDir: &v1.EmptyDirVolumeSource{},
			},
		},
	}

	if !bufferVolumeExisted {
		volumes = append(volumes, v1.Volume{
			Name: SidecarContainerBufferVolumeName,
			VolumeSource: v1.VolumeSource{
				EmptyDir: &v1.EmptyDirVolumeSource{},
			},
		})
	}

	if !cacheVolumeExisted {
		volumes = append(volumes, v1.Volume{
			Name: SidecarContainerCacheVolumeName,
			VolumeSource: v1.VolumeSource{
				EmptyDir: &v1.EmptyDirVolumeSource{},
			},
		})
	}

	return volumes
}

func sidecarContainerPresent(containers []v1.Container, shouldInjectedByWebhook bool) bool {
	containerInjected := false
	tempVolumeMountInjected := false

	for i, c := range containers {
		if c.Name == SidecarContainerName {
			// if the sidecar container is injected by the webhook,
			// the sidecar container needs to be at 0 index.
			if shouldInjectedByWebhook && i != 0 {
				break
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
func ValidatePodHasSidecarContainerInjected(pod *v1.Pod, shouldInjectedByWebhook bool) (bool, bool) {
	// Checks that the temp volume is present in pod.
	tempVolumeInjected := func(pod *v1.Pod) bool {
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

func prepareResourceList(c *Config) (v1.ResourceList, v1.ResourceList) {
	limitsResourceList := v1.ResourceList{}
	requestsResourceList := v1.ResourceList{}

	checkZeroQuantity := func(rl map[v1.ResourceName]resource.Quantity, rn v1.ResourceName, q resource.Quantity) {
		if !q.IsZero() {
			rl[rn] = q
		}
	}

	checkZeroQuantity(limitsResourceList, v1.ResourceCPU, c.CPULimit)
	checkZeroQuantity(limitsResourceList, v1.ResourceMemory, c.MemoryLimit)
	checkZeroQuantity(limitsResourceList, v1.ResourceEphemeralStorage, c.EphemeralStorageLimit)
	checkZeroQuantity(requestsResourceList, v1.ResourceCPU, c.CPURequest)
	checkZeroQuantity(requestsResourceList, v1.ResourceMemory, c.MemoryRequest)
	checkZeroQuantity(requestsResourceList, v1.ResourceEphemeralStorage, c.EphemeralStorageRequest)

	return limitsResourceList, requestsResourceList
}
