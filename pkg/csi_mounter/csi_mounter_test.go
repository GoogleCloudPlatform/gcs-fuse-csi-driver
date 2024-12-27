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
	"fmt"
	"os"
	"reflect"
	"testing"
)

var defaultCsiMountOptions = []string{
	"nodev",
	"nosuid",
	"allow_other",
	"default_permissions",
	"rootmode=40000",
	fmt.Sprintf("user_id=%d", os.Getuid()),
	fmt.Sprintf("group_id=%d", os.Getgid()),
}

func TestPrepareMountArgs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                       string
		inputMountOptions          []string
		expecteCsiMountOptions     []string
		expecteSidecarMountOptions []string
		expectedSysfsBDI           map[string]int64
		expectErr                  bool
	}{
		{
			name:                       "should return valid options correctly with empty input",
			inputMountOptions:          []string{},
			expecteCsiMountOptions:     defaultCsiMountOptions,
			expecteSidecarMountOptions: []string{},
			expectedSysfsBDI:           map[string]int64{},
		},
		{
			name:                       "should return valid options correctly with CSI mount options only",
			inputMountOptions:          []string{"ro", "o=noexec", "o=noatime", "o=invalid"},
			expecteCsiMountOptions:     append(defaultCsiMountOptions, "ro", "noexec", "noatime"),
			expecteSidecarMountOptions: []string{},
			expectedSysfsBDI:           map[string]int64{},
		},
		{
			name:                       "should return valid options correctly with sidecar mount options only",
			inputMountOptions:          []string{"implicit-dirs", "max-conns-per-host=10"},
			expecteCsiMountOptions:     defaultCsiMountOptions,
			expecteSidecarMountOptions: []string{"implicit-dirs", "max-conns-per-host=10"},
			expectedSysfsBDI:           map[string]int64{},
		},
		{
			name:                       "should return valid options correctly with CSI and sidecar mount options",
			inputMountOptions:          []string{"ro", "implicit-dirs", "max-conns-per-host=10", "o=noexec", "o=noatime", "o=invalid"},
			expecteCsiMountOptions:     append(defaultCsiMountOptions, "ro", "noexec", "noatime"),
			expecteSidecarMountOptions: []string{"implicit-dirs", "max-conns-per-host=10"},
			expectedSysfsBDI:           map[string]int64{},
		},
		{
			name:                       "should return valid options correctly with CSI and sidecar mount options with read ahead configs",
			inputMountOptions:          []string{"ro", "implicit-dirs", "max-conns-per-host=10", "o=noexec", "o=noatime", "o=invalid", "read_ahead_kb=4096", "max_ratio=100"},
			expecteCsiMountOptions:     append(defaultCsiMountOptions, "ro", "noexec", "noatime"),
			expecteSidecarMountOptions: []string{"implicit-dirs", "max-conns-per-host=10"},
			expectedSysfsBDI:           map[string]int64{"read_ahead_kb": 4096, "max_ratio": 100},
		},
		{
			name:              "invalid read ahead - not int",
			inputMountOptions: append(defaultCsiMountOptions, "read_ahead_kb=abc"),
			expectErr:         true,
		},
		{
			name:              "invalid read ahead - negative",
			inputMountOptions: append(defaultCsiMountOptions, "read_ahead_kb=-1"),
			expectErr:         true,
		},
		{
			name:              "invalid max ratio - not int",
			inputMountOptions: append(defaultCsiMountOptions, "max_ratio=abc"),
			expectErr:         true,
		},
		{
			name:              "invalid max ratio - negative",
			inputMountOptions: append(defaultCsiMountOptions, "max_ratio=-1"),
			expectErr:         true,
		},
		{
			name:              "invalid max ratio - greater than 100",
			inputMountOptions: append(defaultCsiMountOptions, "max_ratio=101"),
			expectErr:         true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("test case: %s", tc.name)

			c, s, sysfsBDI, err := prepareMountOptions(tc.inputMountOptions)

			if tc.expectErr && err == nil {
				t.Errorf("test %q failed: expected an error, but got nil", tc.name)

				return
			}
			if !tc.expectErr && err != nil {
				t.Errorf("test %q failed: unexpected error: %v", tc.name, err)

				return
			}
			if tc.expectErr {
				return
			}

			if !reflect.DeepEqual(countOptionOccurrence(c), countOptionOccurrence(tc.expecteCsiMountOptions)) {
				t.Errorf("Got options %v, but expected %v", c, tc.expecteCsiMountOptions)
			}

			if !reflect.DeepEqual(countOptionOccurrence(s), countOptionOccurrence(tc.expecteSidecarMountOptions)) {
				t.Errorf("Got options %v, but expected %v", s, tc.expecteSidecarMountOptions)
			}

			if !reflect.DeepEqual(sysfsBDI, tc.expectedSysfsBDI) {
				t.Errorf("Got sysfsBDI %v, expected %v", sysfsBDI, tc.expectedSysfsBDI)
			}
		})
	}
}

func countOptionOccurrence(options []string) map[string]int {
	dict := make(map[string]int)
	for _, o := range options {
		dict[o]++
	}

	return dict
}
