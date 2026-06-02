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

import "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"

// sharedMount checks if the VolumeContext enables the shared node mount feature
// by checking the sharedMount: true volumeAttribute.
func sharedMount(vc map[string]string) bool {
	if v, ok := vc[VolumeContextSharedNodeMount]; ok && v == util.TrueStr {
		return true
	}
	return false
}
