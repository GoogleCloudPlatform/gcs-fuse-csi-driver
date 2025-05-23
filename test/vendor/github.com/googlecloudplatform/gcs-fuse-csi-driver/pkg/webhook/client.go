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

// IsPreprovisionCSIVolume checks whether the volume is a pre-provisioned volume for the desired csiDriver.
func (si *SidecarInjector) IsPreprovisionCSIVolume(csiDriver string, pvc *corev1.PersistentVolumeClaim) (bool, error) {
	_, ok, err := si.GetPreprovisionCSIVolume(csiDriver, pvc)

	return ok, err
}

// GetPreprovisionCSIVolume gets the pre-provisioned persistentVolume when backed by the desired csiDriver.
func (si *SidecarInjector) GetPreprovisionCSIVolume(csiDriver string, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolume, bool, error) {
	if csiDriver == "" {
		return nil, false, errors.New("csiDriver is empty, cannot verify storage type")
	}

	if pvc == nil {
		return nil, false, errors.New("pvc is nil, cannot get pv")
	}

	// We return a nil error because this volume is still valid.
	// A pvc can have this field missing when requesting a dynamically provisioned volume and said PV it is not yet bound.
	if pvc.Spec.VolumeName == "" {
		return nil, false, nil
	}

	// GetPV returns an error if pvc.Spec.VolumeName is not empty and the associated PV object is not found in the API server.
	pv, err := si.GetPV(pvc.Spec.VolumeName)
	if err != nil {
		return nil, false, err // no additional context needed for error.
	}

	if pv == nil {
		return nil, false, errors.New("pv is nil, cannot get storage type")
	}

	// Returns false when PV - PVC pair was created for a different csi driver or different storage type.
	if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == csiDriver {
		return pv, true, nil
	}

	return pv, false, nil
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
