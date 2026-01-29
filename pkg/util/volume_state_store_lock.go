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

package util

import (
	"context"
	"sync"
	"sync/atomic"
)

// VolumeStateStore provides a thread-safe map for storing volume states.
type VolumeStateStore struct {
	store sync.Map // Concurrent-safe map
	size  int32
}

type VolumeState struct {
	BucketAccessCheckPassed   bool
	GCSFuseKernelMonitorState GCSFuseKernelParamsMonitor
}

type GCSFuseKernelParamsMonitor struct {
	StartKernelParamsFileMonitorOnce sync.Once
	CancelFunc                       context.CancelFunc
}

// NewVolumeStateStore initializes the volume state store.
func NewVolumeStateStore() *VolumeStateStore {
	return &VolumeStateStore{}
}

// NewVolumeStateStore initializes the volume state store.
func (vss *VolumeStateStore) Size() int32 {
	return vss.size
}

// Store adds or updates a volume state.
func (vss *VolumeStateStore) Store(volumeID string, state *VolumeState) {
	vss.store.Store(volumeID, state)
	atomic.AddInt32(&vss.size, 1)
}

// Load retrieves the state of a volume.
func (vss *VolumeStateStore) Load(volumeID string) (*VolumeState, bool) {
	if value, ok := vss.store.Load(volumeID); ok {
		if v, ok := value.(*VolumeState); ok {
			return v, true
		}
	}

	return nil, false
}

// Delete removes a volume from the store.
func (vss *VolumeStateStore) Delete(volumeID string) {
	vss.store.Delete(volumeID)
	atomic.AddInt32(&vss.size, -1)
}
