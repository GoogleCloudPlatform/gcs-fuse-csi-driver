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
	"path/filepath"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

const (
	GcsFuseSidecarName                     = "gke-gcsfuse-sidecar"
	MetadataPrefetchSidecarName            = "gke-gcsfuse-metadata-prefetch"
	SidecarContainerTmpVolumeMountPath     = "/gcsfuse-tmp"
	SidecarContainerBufferVolumeName       = "gke-gcsfuse-buffer"
	SidecarContainerBufferVolumeMountPath  = "/gcsfuse-buffer"
	SidecarContainerCacheVolumeName        = "gke-gcsfuse-cache"
	SidecarContainerCacheVolumeMountPath   = "/gcsfuse-cache"
	SidecarContainerSATokenVolumeName      = "gcsfuse-sa-token"  // #nosec G101
	SidecarContainerSATokenVolumeMountPath = "/gcsfuse-sa-token" // #nosec G101
	K8STokenPath                           = "token"             // #nosec G101

	// Webhook relevant volume attributes.
	gcsFuseMetadataPrefetchOnMountVolumeAttribute = "gcsfuseMetadataPrefetchOnMount"

	// gcsfuse profiles constants
	GcsfuseProfilesManagedLabel                           = "gke-gcsfuse/profile-managed"
	BucketScanPendingSchedulingGate                       = "gke-gcsfuse/bucket-scan-pending"
	SidecarContainerFileCacheEphemeralDiskVolumeName      = "gcsfuse-file-cache-ephemeral-disk"
	SidecarContainerFileCacheEphemeralDiskVolumeMountPath = "/gcsfuse-file-cache-ephemeral-disk"
	SidecarContainerFileCacheRamDiskVolumeName            = "gcsfuse-file-cache-ram-disk"
	SidecarContainerFileCacheRamDiskVolumeMountPath       = "/gcsfuse-file-cache-ram-disk"

	// See the nonroot user discussion: https://github.com/GoogleContainerTools/distroless/issues/443
	NobodyUID           = 65534
	NobodyGID           = 65534
	tokenExpiryDuration = 3600
)

var (
	// gke-gcsfuse-sidecar volumes.
	tmpVolume = corev1.Volume{
		Name: util.SidecarContainerTmpVolumeName,
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

	// gke-gcsfuse-sidecar volumeMounts.
	TmpVolumeMount = corev1.VolumeMount{
		Name:      util.SidecarContainerTmpVolumeName,
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

	saTokenVolumeMount = corev1.VolumeMount{
		Name:      SidecarContainerSATokenVolumeName,
		MountPath: SidecarContainerSATokenVolumeMountPath,
	}

	// gcsfuse profiles related vars.
	ramFileCacheVolumeMount = corev1.VolumeMount{
		Name:      SidecarContainerFileCacheRamDiskVolumeName,
		MountPath: SidecarContainerFileCacheRamDiskVolumeMountPath,
	}

	ephemeralFileCacheVolumeMount = corev1.VolumeMount{
		Name:      SidecarContainerFileCacheEphemeralDiskVolumeName,
		MountPath: SidecarContainerFileCacheEphemeralDiskVolumeMountPath,
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

	volumeMounts := []corev1.VolumeMount{TmpVolumeMount, buffVolumeMount, cacheVolumeMount}
	// Inject SA token only for Host Network pods
	if c.PodHostNetworkSetting && c.ShouldInjectSAVolume {
		volumeMounts = append(volumeMounts, saTokenVolumeMount)
	}

	// The sidecar container follows Restricted Pod Security Standard,
	// see https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted
	container := corev1.Container{
		Name:            GcsFuseSidecarName,
		Image:           c.ContainerImage,
		ImagePullPolicy: corev1.PullPolicy(c.ImagePullPolicy),
		SecurityContext: GetSecurityContext(),
		Args: []string{
			"--v=5",
		},
		Resources: corev1.ResourceRequirements{
			Limits:   limits,
			Requests: requests,
		},
		VolumeMounts: volumeMounts,
	}

	return container
}

// GetSecurityContext ensures the sidecar that uses it follows Restricted Pod Security Standard.
// See https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted
func GetSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
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
	}
}

func (si *SidecarInjector) GetMetadataPrefetchSidecarContainerSpec(pod *corev1.Pod, c *Config) corev1.Container {
	if pod == nil {
		klog.Warning("failed to get metadata prefetch container spec: pod is nil")

		return corev1.Container{}
	}
	limits, requests := prepareResourceList(c)

	// The sidecar container follows Restricted Pod Security Standard,
	// see https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted
	container := corev1.Container{
		Name:            MetadataPrefetchSidecarName,
		Image:           c.ContainerImage,
		ImagePullPolicy: corev1.PullPolicy(c.ImagePullPolicy),
		SecurityContext: GetSecurityContext(),
		Resources: corev1.ResourceRequirements{
			Limits:   limits,
			Requests: requests,
		},
		VolumeMounts: []corev1.VolumeMount{},
	}

	for _, v := range pod.Spec.Volumes {
		isGcsFuseCSIVolume, isDynamicMount, volumeAttributes, err := si.isGcsFuseCSIVolume(v, pod.Namespace)
		if err != nil {
			klog.Errorf("failed to determine if %s is a GcsFuseCSI backed volume: %v", v.Name, err)
		}

		if isDynamicMount {
			klog.Warningf("dynamic mount set for %s, this is not supported for metadata prefetch. skipping volume", v.Name)

			continue
		}

		if isGcsFuseCSIVolume {
			enableMetaPrefetchRaw, ok := volumeAttributes[gcsFuseMetadataPrefetchOnMountVolumeAttribute]
			// We disable metadata prefetch by default, so we skip injection of volume mount when not set.
			if !ok {
				continue
			}

			enableMetaPrefetch, err := ParseBool(enableMetaPrefetchRaw)
			if err != nil {
				klog.Errorf(`failed to determine if metadata prefetch is needed for volume "%s": %v`, v.Name, err)
			}

			if enableMetaPrefetch {
				container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: v.Name, MountPath: filepath.Join("/volumes/", v.Name), ReadOnly: true})
			}
		}
	}

	return container
}

func GetSATokenVolume(audience string) corev1.Volume {
	saTokenVolume := corev1.Volume{
		Name: SidecarContainerSATokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Audience:          audience,
							ExpirationSeconds: &[]int64{tokenExpiryDuration}[0],
							Path:              K8STokenPath,
						},
					},
				},
			},
		},
	}

	return saTokenVolume
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
func ValidatePodHasSidecarContainerInjected(pod *corev1.Pod) (bool, bool) {
	return validatePodHasSidecarContainerInjected(GcsFuseSidecarName, pod, []corev1.Volume{tmpVolume}, []corev1.VolumeMount{TmpVolumeMount})
}

func sidecarContainerPresent(containerName string, containers []corev1.Container, volumeMounts []corev1.VolumeMount) bool {
	containerInjected := false
	volumeMountMap := map[string]string{}
	for _, vm := range volumeMounts {
		volumeMountMap[vm.Name] = vm.MountPath
	}

	for _, c := range containers {
		if c.Name == containerName {
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

func validatePodHasSidecarContainerInjected(containerName string, pod *corev1.Pod, volumes []corev1.Volume, volumeMounts []corev1.VolumeMount) (bool, bool) {
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
	containerAndVolumeMountPresentInContainers := sidecarContainerPresent(containerName, pod.Spec.Containers, volumeMounts)
	containerAndVolumeMountPresentInInitContainers := sidecarContainerPresent(containerName, pod.Spec.InitContainers, volumeMounts)

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
