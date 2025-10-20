/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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
)

const (
	gcsFuseCsiDriverName = "gcsfuse.csi.storage.gke.io"
)

// isGcsFuseCSIVolume checks if the given volume is backed by gcsfuse csi driver.
//
// Returns the following (in order):
//   - isGcsFuseCSIVolume - (bool) whether volume is backed by gcsfuse csi driver.
//   - isDynamicMount - (bool) True if volume is attempting to mount all of the buckets in project.
//   - volumeAttributes (map[string]string)
//   - pv - if volume is a pv we return (*corev1.PersistentVolume) otherwise nil
//   - error - if check failed
func (si *SidecarInjector) isGcsFuseCSIVolume(volume corev1.Volume, namespace string) (bool, bool, map[string]string, *corev1.PersistentVolume, error) {
	var isDynamicMount bool

	// Check if it is ephemeral volume.
	if volume.CSI != nil {
		if volume.CSI.Driver == gcsFuseCsiDriverName {
			// Ephemeral volume is using dynamic mounting,
			// See details: https://cloud.google.com/storage/docs/gcsfuse-mount#dynamic-mount
			if val, ok := volume.CSI.VolumeAttributes["bucketName"]; ok && val == "_" {
				isDynamicMount = true
			}

			return true, isDynamicMount, volume.CSI.VolumeAttributes, nil, nil
		}

		return false, false, nil, nil, nil
	}

	// Check if it's a persistent volume.
	pvc := volume.PersistentVolumeClaim
	if pvc == nil {
		return false, false, nil, nil, nil
	}
	pvcName := pvc.ClaimName
	pvcObj, err := si.GetPVC(namespace, pvcName)
	if err != nil {
		return false, false, nil, nil, err
	}

	// Check if the PVC is a preprovisioned gcsfuse volume.
	pv, ok, err := si.GetPreprovisionCSIVolume(gcsFuseCsiDriverName, pvcObj)
	if err != nil || pv == nil {
		klog.Warningf("unable to determine if PVC %s/%s is a pre-provisioned gcsfuse volume: %v", namespace, pvcName, err)

		return false, false, nil, nil, nil
	}

	if ok {
		if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle == "_" {
			isDynamicMount = true
		}

		return true, isDynamicMount, pv.Spec.CSI.VolumeAttributes, pv, nil
	}

	return false, false, nil, nil, nil
}
