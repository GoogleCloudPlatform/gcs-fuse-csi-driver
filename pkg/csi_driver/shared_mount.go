/*
Copyright 2018 The Kubernetes Authors.
Copyright 2026 Google LLC

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

package driver

import (
	"crypto/sha1"
	"fmt"
	"io"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	corev1 "k8s.io/api/core/v1"
)

const (
	mounterPodNamePrefix = "gcsfusecsi-mount"
)

// mounterPodConfig holds the configuration parameters required to define and manage a mounter pod.
type mounterPodConfig struct {
	podName       string // The name to assign to the mounter pod.
	nodeID        string // The specific node ID where the pod should be scheduled.
	namespace     string // The Kubernetes namespace in which to create the pod.
	priorityClass string // The name of the PriorityClass to apply to the pod.
}

// sharedMount checks if the VolumeContext enables the shared node mount feature
// by checking the sharedMount: true volumeAttribute.
func sharedMount(vc map[string]string) bool {
	if v, ok := vc[VolumeContextSharedNodeMount]; ok && v == util.TrueStr {
		return true
	}
	return false
}

// createMounterPodName returns a unique name for the mounter pod. The name is composed by
// the node and volume IDs, evaluated on a SHA1 hash for length shortening.
func createMounterPodName(nodeID, volumeID string) string {
	str := fmt.Sprintf("%s_%s", nodeID, volumeID)
	h := sha1.New()
	// Write the string to the hash
	io.WriteString(h, str)
	// Convert the byte slice to a hexadecimal string
	sha1Hash := fmt.Sprintf("%x", h.Sum(nil))
	return fmt.Sprintf("%s-%s", mounterPodNamePrefix, sha1Hash)
}

// pvFromVolumeID finds the PersistentVolume in the cluster that corresponds to the given CSI volumeID.
func pvFromVolumeID(clientset clientset.Interface, volumeID string) (*corev1.PersistentVolume, error) {
	if clientset == nil {
		return nil, fmt.Errorf("clientset is nil")
	}
	pvs, err := clientset.ListPV()
	if err != nil {
		return nil, err
	}
	for _, pv := range pvs {
		if pv != nil && pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle == volumeID {
			return pv, nil
		}
	}
	return nil, fmt.Errorf("no pv found for volumeID: %q", volumeID)
}
