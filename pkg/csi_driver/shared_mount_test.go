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

import "testing"

func TestSharedMount(t *testing.T) {
	testCases := []struct {
		name          string
		volumeContext map[string]string
		expected      bool
	}{
		{
			name:          "sharedMount true - should return true",
			volumeContext: map[string]string{"sharedMount": "true"},
			expected:      true,
		},
		{
			name:          "sharedMount false - should return false",
			volumeContext: map[string]string{"sharedMount": "false"},
			expected:      false,
		},
		{
			name:          "sharedMount missing - should return false",
			volumeContext: map[string]string{},
			expected:      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vc := tc.volumeContext
			result := sharedMount(vc)
			if result != tc.expected {
				t.Errorf("Expected %v, but got %v", tc.expected, result)
			}
		})
	}
}
