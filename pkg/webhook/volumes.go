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
)

const (
	gcsFuseCsiDriverName = "gcsfuse.csi.storage.gke.io"
)

func (si *SidecarInjector) isGcsFuseCSIVolume(volume corev1.Volume, namespace string) (bool, map[string]string, error) {
	// Check if it is ephemeral volume.
	if volume.CSI != nil {
		if volume.CSI.Driver == gcsFuseCsiDriverName {
			return true, volume.CSI.VolumeAttributes, nil
		}

		return false, nil, nil
	}

	// Check if it's a persistent volume.
	pvc := volume.PersistentVolumeClaim
	if pvc == nil {
		return false, nil, nil
	}
	pvcName := pvc.ClaimName
	pvcObj, err := si.GetPVC(namespace, pvcName)
	if err != nil {
		return false, nil, err
	}

	// Check if the PVC is a preprovisioned parallelstore volume.
	isPreprovisionGcsFuseCsi, pv, err := si.IsPreprovisionCSIVolume(gcsFuseCsiDriverName, pvcObj)
	if err != nil {
		klog.Warningf("unable to determine if PVC %s/%s is a pre-provisioned gcsfuse volume: %v", namespace, pvcName, err)

		return false, nil, nil
	}
	if isPreprovisionGcsFuseCsi {
		return true, pv.Spec.CSI.VolumeAttributes, nil
	}

	klog.Infof("PVC %s/%s is not referring to a pre-provisioned gcsfuse volume", namespace, pvcName)

	return false, nil, nil
}
