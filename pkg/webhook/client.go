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
	"errors"

	corev1 "k8s.io/api/core/v1"
)

func (si *SidecarInjector) IsPreprovisionCSIVolume(csiDriver string, pvc *corev1.PersistentVolumeClaim) (bool, *corev1.PersistentVolume, error) {
	// IsPreprovisionCSIVolume checks whether the volume is a pre-provisioned volume for the desired csiDriver.
	if csiDriver == "" {
		return false, nil, errors.New("failed to check IsPreprovisionCSIVolume, csiDriver is empty")
	}

	if pvc == nil {
		return false, nil, errors.New("failed to check IsPreprovisionCSIVolume, pvsi is nil")
	}

	if pvc.Spec.VolumeName == "" {
		return false, nil, nil
	}

	// GetPV returns an error if pvc.Spec.VolumeName is not empty and the associated PV object is not found in the API server.
	pv, err := si.GetPV(pvc.Spec.VolumeName)
	if err != nil {
		return false, nil, err // no additional context needed for error.
	}

	if pv.Spec.CSI == nil {
		return false, nil, nil
	}

	// PV is using dynamic mounting. See details: https://cloud.google.com/storage/docs/gcsfuse-mount#dynamic-mount
	if pv.Spec.CSI.VolumeHandle == "_" {
		return false, nil, nil
	}

	if pv.Spec.CSI.Driver == csiDriver {
		return true, pv, nil
	}

	// Returns false when PV - PVC pair was created for a different csi driver or different storage type.
	return false, nil, nil
}

func (si *SidecarInjector) GetPV(name string) (*corev1.PersistentVolume, error) {
	pv, err := si.PvLister.Get(name)
	if err != nil {
		return nil, err
	}

	return pv, nil
}

func (si *SidecarInjector) GetPVC(namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	pvc, err := si.PvcLister.PersistentVolumeClaims(namespace).Get(name)
	if err != nil {
		return nil, err
	}

	return pvc, nil
}
