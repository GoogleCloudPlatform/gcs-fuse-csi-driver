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
	"slices"

	"cloud.google.com/go/compute/metadata"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
	listersv1 "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	GcsFuseVolumeEnableAnnotation           = "gke-gcsfuse/volumes"
	GcsFuseNativeSidecarEnableAnnotation    = "gke-gcsfuse/enable-native-sidecar"
	cpuLimitAnnotation                      = "gke-gcsfuse/cpu-limit"
	cpuRequestAnnotation                    = "gke-gcsfuse/cpu-request"
	memoryLimitAnnotation                   = "gke-gcsfuse/memory-limit"
	memoryRequestAnnotation                 = "gke-gcsfuse/memory-request"
	ephemeralStorageLimitAnnotation         = "gke-gcsfuse/ephemeral-storage-limit"
	ephemeralStorageRequestAnnotation       = "gke-gcsfuse/ephemeral-storage-request"
	metadataPrefetchMemoryLimitAnnotation   = "gke-gcsfuse/metadata-prefetch-memory-limit"
	metadataPrefetchMemoryRequestAnnotation = "gke-gcsfuse/metadata-prefetch-memory-request"
)

type SidecarInjector struct {
	Client client.Client
	// default sidecar container config values, can be overwritten by the pod annotations
	Config                 *Config
	MetadataPrefetchConfig *Config
	Decoder                admission.Decoder
	NodeLister             listersv1.NodeLister
	PvcLister              listersv1.PersistentVolumeClaimLister
	PvLister               listersv1.PersistentVolumeLister
	SCLister               storagelisters.StorageClassLister
	ServerVersion          *version.Version
}

// Handle injects a gcsfuse sidecar container and a emptyDir to incoming qualified pods.
func (si *SidecarInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	// Validate injection request
	pod := &corev1.Pod{}

	// GCSFuse Profiles TODO: if request is a PV, validate user has applied iam rules required for gcsfuse profiles.
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
	// Check support for native sidecar.
	injectAsNativeSidecar, err := si.injectAsNativeSidecar(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to verify native sidecar support: %w", err))
	}

	// Inject Fuse Side Car container.
	injected, _ := validatePodHasSidecarContainerInjected(GcsFuseSidecarName, pod, []corev1.Volume{tmpVolume}, []corev1.VolumeMount{TmpVolumeMount})
	if !injected {
		err = si.injectSidecarContainer(GcsFuseSidecarName, pod, injectAsNativeSidecar)
	}
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	// Inject service account volume
	if si.Config.ShouldInjectSAVolume && pod.Spec.HostNetwork {
		projectID, err := metadata.ProjectIDWithContext(ctx)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to get project id: %w", err))
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, GetSATokenVolume(projectID))
	}

	pod.Spec.Volumes = append(GetSidecarContainerVolumeSpec(pod.Spec.Volumes...), pod.Spec.Volumes...)

	// Inject metadata prefetch sidecar.
	injected, _ = validatePodHasSidecarContainerInjected(MetadataPrefetchSidecarName, pod, []corev1.Volume{}, []corev1.VolumeMount{})
	if !injected {
		err = si.injectSidecarContainer(MetadataPrefetchSidecarName, pod, injectAsNativeSidecar)
	}
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if si.Config.EnableGcsfuseProfiles {
		klog.Infof("GCSFuse profiles are enabled for Pod: Name %q, GenerateName %q, Namespace %q", pod.Name, pod.GenerateName, pod.Namespace)
		isUsingGcsfuseProfiles, err := si.IsGCSFuseProfilesEnabled(ctx, pod)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to check gcsfuse profiles support: %w", err))
		}

		// Handle gcsfuse profiles spec modifications if using gcsfuse profile enabled buckets.
		if isUsingGcsfuseProfiles {
			si.ModifyPodSpecForGCSFuseProfiles(ctx, pod)
		}
	}

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to marshal pod: %w", err))
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

// Modifies the pod spec to add gcsfuse profile related features. This includes adding a label, scheduling gate, and placeholder file cache volumes.
func (si *SidecarInjector) ModifyPodSpecForGCSFuseProfiles(ctx context.Context, pod *corev1.Pod) {
	// Always apply the gcsfuse profile label when gcsfuse profiles are enabled for pod informer's Kubernetes API efficient filtering.
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[gcsfuseProfilesManagedLabel] = "true"

	// Always apply the scheduling gate when the gcsfuse profiles are enabled. The controller will handle the logistics if its not needed.
	profilesGate := corev1.PodSchedulingGate{Name: bucketScanPendingSchedulingGate}
	pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, profilesGate)

	// Inject placeholder file cache volumes.
	if !volumeExists(pod.Spec.Volumes, fileCacheEphemeralDiskVolumeName) {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name:         fileCacheEphemeralDiskVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	} else {
		klog.Warningf("Pod %s/%s already has a volume named %s, skipping injection of ephemeral file cache volume for gcsfuse sidecar.", pod.Namespace, pod.Name, fileCacheEphemeralDiskVolumeName)
	}
	if !volumeExists(pod.Spec.Volumes, fileCacheRamDiskVolumeName) {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: fileCacheRamDiskVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		})
	} else {
		klog.Warningf("Pod %s/%s already has a volume named %s, skipping injection of ram file cache volume for gcsfuse sidecar.", pod.Namespace, pod.Name, fileCacheRamDiskVolumeName)
	}

	// Apply file cache volume mounts to side car container.
	mountsToAdd := []corev1.VolumeMount{epehemeralFileCacheMount, ramFileCacheMount}

	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == GcsFuseSidecarName {
			// Found the sidecar as a init container, now add mounts safely
			for _, mount := range mountsToAdd {
				if !volumeMountExists(pod.Spec.InitContainers[i].VolumeMounts, mount.Name) {
					pod.Spec.InitContainers[i].VolumeMounts = append(pod.Spec.InitContainers[i].VolumeMounts, mount)
				} else {
					klog.Warningf("Pod %s/%s gcsfuse sidecar init container already has a volume mount named %s, skipping injection of file cache mount in gcsfuse sidecar.", pod.Namespace, pod.Name, mount.Name)
				}
			}
			return
		}
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == GcsFuseSidecarName {
			// Found the sidecar as a container, now add mounts safely
			for _, mount := range mountsToAdd {
				if !volumeMountExists(pod.Spec.Containers[i].VolumeMounts, mount.Name) {
					pod.Spec.Containers[i].VolumeMounts = append(pod.Spec.Containers[i].VolumeMounts, mount)
				} else {
					klog.Warningf("Pod %s/%s gcsfuse sidecar container already has a volume mount named %s, skipping injection of file cache mount in gcsfuse sidecar.", pod.Namespace, pod.Name, mount.Name)
				}
			}
			return
		}
	}
	klog.Warning("Could not find gcsfuse sidecar container to add gcsfuse profile file cache mounts.")
}

// Checks if the pod has ANY pvc backed by a gcsfuse profile enabled storage class. gcsfuse profile enabled means the storage class uses a valid workloadType parameter.
// If none are found, this implies gcsfuse profiles are not enabled for the pod.
func (si *SidecarInjector) IsGCSFuseProfilesEnabled(ctx context.Context, pod *corev1.Pod) (bool, error) {
	// Check if pod has volumes
	if pod.Spec.Volumes == nil {
		return false, nil
	}

	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil {
			pvcName := vol.PersistentVolumeClaim.ClaimName

			// Fetch the PVC from storage class informer
			pvc, err := si.GetPVC(pod.Namespace, pvcName)
			if err != nil {
				klog.Warningf("failed to get PVC %s in namespace %s while mutating pod %s: %v", pvcName, pod.Namespace, pod.Name, err)
				continue
			}
			if pvc.Spec.StorageClassName == nil {
				// If the PVC does not have a storage class, skip it.
				continue
			}
			sc, error := si.GetSC(*pvc.Spec.StorageClassName)
			if error != nil {
				klog.Warningf("failed to get StorageClass %s for pvc %s : %v", *pvc.Spec.StorageClassName, pvcName, error)
				continue
			}
			wt, ok := sc.Parameters[workloadTypeParamKey]
			if ok {
				if slices.Contains(workloadTypeParamValues, wt) {
					return true, nil
				} else {
					klog.Errorf("StorageClass: %s for pvc: %s has unsupported workloadType: %s", *pvc.Spec.StorageClassName, pvcName, wt)
					return true, fmt.Errorf("StorageClass: %s for pvc: %s has unsupported workloadType: %s", *pvc.Spec.StorageClassName, pvcName, wt)
				}
			}
		}
	}

	return false, nil
}

// volumeExists checks if a volume with a specific name already exists in the pod's volumes.
func volumeExists(volumes []corev1.Volume, name string) bool {
	for _, v := range volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

// volumeMountExists checks if a volume mount with a specific name already exists in a slice of volume mounts.
func volumeMountExists(volumeMounts []corev1.VolumeMount, name string) bool {
	for _, vm := range volumeMounts {
		if vm.Name == name {
			return true
		}
	}
	return false
}
