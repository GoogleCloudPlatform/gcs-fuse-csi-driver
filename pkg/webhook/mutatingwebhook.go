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
	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	util "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
	listersv1 "k8s.io/client-go/listers/core/v1"
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
	GcsfuseProfilesAnnotation               = "gke-gcsfuse/profiles"
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
	ServerVersion          *version.Version
}

// Handle injects a gcsfuse sidecar container and a emptyDir to incoming qualified pods.
func (si *SidecarInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Kind.Kind == "PersistentVolume" && si.Config.EnableGcsfuseProfiles { // Currently only handling pvs for gcsfuse profiles
		pv := &corev1.PersistentVolume{}
		if err := si.Decoder.Decode(req, pv); err != nil {
			klog.Errorf("Could not decode PersistentVolume object: %v", err)
			return admission.Errored(http.StatusBadRequest, err)
		}
		klog.Infof("Mutating webhook is handling PersistentVolume: %s", pv.Name)

		if err := si.validatePersistentVolumeForGCSFuseProfiles(pv); err != nil {
			klog.Errorf("PersistentVolume validation failed: %v", err)
			return admission.Errored(http.StatusBadRequest, err)
		}
		return admission.Allowed(fmt.Sprintf("No mutation Required on PersistentVolume: %s", pv.Name))
	}

	// Validate injection request
	pod := &corev1.Pod{}

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
	if pod.Spec.HostNetwork && si.Config.ShouldInjectSAVolume {
		projectID, err := metadata.ProjectIDWithContext(ctx)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to get project id: %w", err))
		}
		audience := audienceForInjectedSATokenVolume(projectID, pod)
		pod.Spec.Volumes = append(pod.Spec.Volumes, GetSATokenVolume(audience))
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
		klog.Infof("GCSFuse profiles feature flag is enabled")
		// Handle gcsfuse profiles spec modifications if using gcsfuse profile enabled buckets
		areProfilesEnabled, err := IsGCSFuseProfilesEnabled(pod)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if areProfilesEnabled {
			err = ModifyPodSpecForGCSFuseProfiles(pod)
			if err != nil {
				return admission.Errored(http.StatusBadRequest, err)
			}
		}
	}

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to marshal pod: %w", err))
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

// validatePersistentVolumeForGCSFuseProfiles ensures it meets the requirements for gcsfuse CSI driver usage
// currently validations are only required by storage profiles, so this is a no-op if profiles are not enabled
func (si *SidecarInjector) validatePersistentVolumeForGCSFuseProfiles(pv *corev1.PersistentVolume) error {
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != util.GCSFuseCsiDriverName {
		// Not a gcsfuse CSI driver volume, skip validation
		return nil
	}
	if err := putil.ValidateStorageProfilesOverrideStatus(pv); err != nil {
		return err
	}
	return nil
}

// audienceForInjectedSATokenVolume determines the audience to use for the injected service account token volume.
// It searches through the pod's volumes to see if any of them have an identityProvider set in their VolumeAttributes.
// If one is found and it is a GKE cluster identityProvider, or if no identityProvider is set, it uses the default
// GKE identity provider format of projectID + ".svc.id.goog".
func audienceForInjectedSATokenVolume(projectID string, pod *corev1.Pod) string {
	var foundIdentityProvider string
	// Loop through the pod's volumes to find a better audience.
	for _, v := range pod.Spec.Volumes {
		if v.CSI != nil && v.CSI.Driver == util.GCSFuseCsiDriverName && v.CSI.VolumeAttributes != nil {
			if identityProvider, ok := v.CSI.VolumeAttributes["identityProvider"]; ok && identityProvider != "" {
				// If found, the identityProvider becomes the new audience.
				foundIdentityProvider = identityProvider
				klog.Infof("Found identityProvider=%s set in VolumeAttributes", foundIdentityProvider)
				break
			}
		}
	}

	if util.IsGKEIdentityProvider(foundIdentityProvider) || foundIdentityProvider == "" {
		return projectID + ".svc.id.goog"
	}
	return foundIdentityProvider
}

// Modifies the pod spec to add gcsfuse profile related features. This includes adding a label, scheduling gate, and placeholder file cache volumes
func ModifyPodSpecForGCSFuseProfiles(pod *corev1.Pod) error {
	// Always apply the gcsfuse profile label when gcsfuse profiles are enabled for pod informer's Kubernetes API efficient filtering
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[GcsfuseProfilesManagedLabel] = "true"

	// Always apply the scheduling gate when the gcsfuse profiles are enabled. The controller will handle the logistics if its not needed
	profilesGate := corev1.PodSchedulingGate{Name: BucketScanPendingSchedulingGate}

	// Check if the gate is already present.
	gateExists := slices.ContainsFunc(pod.Spec.SchedulingGates, func(gate corev1.PodSchedulingGate) bool {
		return gate.Name == profilesGate.Name
	})

	// Only append the gate if it does not already exist
	if !gateExists {
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, profilesGate)
	} else {
		klog.Warningf("Pod %s/%s already has the %s scheduling gate, skipping injection of gcsfuse profiles scheduling gate.", pod.Namespace, pod.Name, BucketScanPendingSchedulingGate)
	}

	// Inject placeholder file cache volumes
	if !volumeExists(pod.Spec.Volumes, SidecarContainerFileCacheEphemeralDiskVolumeName) {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name:         SidecarContainerFileCacheEphemeralDiskVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	} else {
		klog.Warningf("Pod %s/%s already has a volume named %s, skipping injection of ephemeral file cache volume for gcsfuse sidecar.", pod.Namespace, pod.Name, SidecarContainerFileCacheEphemeralDiskVolumeName)
	}
	if !volumeExists(pod.Spec.Volumes, SidecarContainerFileCacheRamDiskVolumeName) {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: SidecarContainerFileCacheRamDiskVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		})
	} else {
		klog.Warningf("Pod %s/%s already has a volume named %s, skipping injection of ram file cache volume for gcsfuse sidecar.", pod.Namespace, pod.Name, SidecarContainerFileCacheRamDiskVolumeName)
	}

	// Apply file cache volume mounts to side car container
	mountsToAdd := []corev1.VolumeMount{ephemeralFileCacheVolumeMount, ramFileCacheVolumeMount}

	addMounts := func(c *corev1.Container) {
		for _, mount := range mountsToAdd {
			if !volumeMountExists(c.VolumeMounts, mount.Name) {
				c.VolumeMounts = append(c.VolumeMounts, mount)
			} else {
				klog.Warningf("Pod %s/%s gcsfuse sidecar container already has a volume mount named %s, skipping injection of file cache mount in gcsfuse sidecar.", pod.Namespace, pod.Name, mount.Name)
			}
		}
	}

	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == GcsFuseSidecarName {
			// Found the sidecar as an init container, now add mounts safely
			addMounts(&pod.Spec.InitContainers[i])
			return nil
		}
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == GcsFuseSidecarName {
			// Found the sidecar as a container, now add mounts safely
			addMounts(&pod.Spec.Containers[i])
			return nil
		}
	}
	klog.Errorf("Could not find gcsfuse sidecar container in pod %s/%s to add gcsfuse profile file cache mounts.", pod.Namespace, pod.Name)
	return fmt.Errorf("could not find gcsfuse sidecar container in pod %s/%s to add gcsfuse profile file cache mounts.", pod.Namespace, pod.Name)
}

// Checks if the pod has gcsfuse profiles annotation. returns true if the annotation is present and set to "true" (case insensitive), false otherwise
func IsGCSFuseProfilesEnabled(pod *corev1.Pod) (bool, error) {
	// Check if pod has gcsfuse profiles annotation set to true
	if pod.Annotations == nil {
		return false, nil
	}
	value, ok := pod.Annotations[GcsfuseProfilesAnnotation]
	if !ok {
		return false, nil
	}
	valueAsBool, err := ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("the acceptable values for %q are 'True', 'true', 'false' or 'False'", GcsfuseProfilesAnnotation)
	}
	return valueAsBool, nil
}

// volumeExists checks if a volume with a specific name already exists in the pod's volumes
func volumeExists(volumes []corev1.Volume, name string) bool {
	return slices.ContainsFunc(volumes, func(v corev1.Volume) bool {
		return v.Name == name
	})
}

// volumeMountExists checks if a volume mount with a specific name already exists in a slice of volume mounts
func volumeMountExists(volumeMounts []corev1.VolumeMount, name string) bool {
	return slices.ContainsFunc(volumeMounts, func(vm corev1.VolumeMount) bool {
		return vm.Name == name
	})
}
