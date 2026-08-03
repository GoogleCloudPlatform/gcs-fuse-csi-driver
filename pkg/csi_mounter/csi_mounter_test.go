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

package csimounter

import (
	"testing"
)

func TestCheckForKernelReader(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		options  []string
		expected bool
	}{
		{
			name:     "empty options",
			options:  []string{},
			expected: false,
		},
		{
			name:     "unrelated options",
			options:  []string{"ro", "implicit-dirs", "node_fuse_max_request_limit_kb=8192"},
			expected: false,
		},
		{
			name:     "enable-kernel-reader flag only",
			options:  []string{"enable-kernel-reader"},
			expected: true,
		},
		{
			name:     "enable-kernel-reader=true flag",
			options:  []string{"enable-kernel-reader=true"},
			expected: true,
		},
		{
			name:     "enable-kernel-reader=false flag",
			options:  []string{"enable-kernel-reader=false"},
			expected: false,
		},
		{
			name:     "file-system:enable-kernel-reader:true config",
			options:  []string{"file-system:enable-kernel-reader:true"},
			expected: true,
		},
		{
			name:     "file-system:enable-kernel-reader:false config",
			options:  []string{"file-system:enable-kernel-reader:false"},
			expected: false,
		},
		{
			name:     "multiple options with enable-kernel-reader",
			options:  []string{"ro", "implicit-dirs", "enable-kernel-reader=true", "node_fuse_max_request_limit_kb=8192"},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkForKernelReader(tc.options)
			if got != tc.expected {
				t.Errorf("checkForKernelReader() = %v, expected %v", got, tc.expected)
			}
		})
	}
}
