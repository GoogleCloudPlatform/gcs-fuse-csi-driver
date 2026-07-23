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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
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

	pageSizeKB := max(minPageSizeKB, int64(os.Getpagesize()/util.KiB))

	testCases := []struct {
		name                        string
		inputMountOptions           []string
		expectedCsiMountOptions     []string
		expectedSidecarMountOptions []string
		expectedSysfsBDI            map[string]int64
		expectedFuseMaxPagesLimit   *int64
		expectErr                   bool
	}{
		{
			name:                        "should return valid options correctly with empty input",
			inputMountOptions:           []string{},
			expectedCsiMountOptions:     defaultCsiMountOptions,
			expectedSidecarMountOptions: []string{},
			expectedSysfsBDI:            map[string]int64{},
		},
		{
			name:                        "should return valid options correctly with CSI mount options only",
			inputMountOptions:           []string{"ro", "o=noexec", "o=noatime", "o=invalid"},
			expectedCsiMountOptions:     append(defaultCsiMountOptions, "ro", "noexec", "noatime"),
			expectedSidecarMountOptions: []string{},
			expectedSysfsBDI:            map[string]int64{},
		},
		{
			name:                        "should return valid options correctly with sidecar mount options only",
			inputMountOptions:           []string{"implicit-dirs", "max-conns-per-host=10"},
			expectedCsiMountOptions:     defaultCsiMountOptions,
			expectedSidecarMountOptions: []string{"implicit-dirs", "max-conns-per-host=10"},
			expectedSysfsBDI:            map[string]int64{},
		},
		{
			name:                        "should return valid options correctly with CSI and sidecar mount options",
			inputMountOptions:           []string{"ro", "implicit-dirs", "max-conns-per-host=10", "o=noexec", "o=noatime", "o=invalid"},
			expectedCsiMountOptions:     append(defaultCsiMountOptions, "ro", "noexec", "noatime"),
			expectedSidecarMountOptions: []string{"implicit-dirs", "max-conns-per-host=10"},
			expectedSysfsBDI:            map[string]int64{},
		},
		{
			name:                        "should return valid options correctly with CSI and sidecar mount options with read ahead configs",
			inputMountOptions:           []string{"ro", "implicit-dirs", "max-conns-per-host=10", "o=noexec", "o=noatime", "o=invalid", "read_ahead_kb=4096"},
			expectedCsiMountOptions:     append(defaultCsiMountOptions, "ro", "noexec", "noatime"),
			expectedSidecarMountOptions: []string{"implicit-dirs", "max-conns-per-host=10"},
			expectedSysfsBDI:            map[string]int64{"read_ahead_kb": 4096},
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
			name:                        "should return valid options correctly with node_fuse_max_request_limit_kb",
			inputMountOptions:           []string{"ro", "implicit-dirs", "node_fuse_max_request_limit_kb=8192"},
			expectedCsiMountOptions:     append(defaultCsiMountOptions, "ro"),
			expectedSidecarMountOptions: []string{"implicit-dirs"},
			expectedSysfsBDI:            map[string]int64{},
			expectedFuseMaxPagesLimit:   ptr(8192 / pageSizeKB),
		},
		{
			name:              "invalid node_fuse_max_request_limit_kb - not int",
			inputMountOptions: []string{"node_fuse_max_request_limit_kb=abc"},
			expectErr:         true,
		},
		{
			name:              "invalid node_fuse_max_request_limit_kb - negative",
			inputMountOptions: []string{"node_fuse_max_request_limit_kb=-100"},
			expectErr:         true,
		},
		{
			name:                        "should return valid options correctly with node_fuse_max_request_limit_kb - not multiple of page size (ceil up)",
			inputMountOptions:           []string{"node_fuse_max_request_limit_kb=8193"},
			expectedCsiMountOptions:     defaultCsiMountOptions,
			expectedSidecarMountOptions: []string{},
			expectedSysfsBDI:            map[string]int64{},
			expectedFuseMaxPagesLimit:   ptr(util.CeilDiv64(8193, pageSizeKB)),
		},
		{
			name:                        "should return valid options correctly with node_fuse_max_request_limit_kb at maximum allowed limit",
			inputMountOptions:           []string{fmt.Sprintf("node_fuse_max_request_limit_kb=%d", maxFuseMaxPagesLimit*pageSizeKB)},
			expectedCsiMountOptions:     defaultCsiMountOptions,
			expectedSidecarMountOptions: []string{},
			expectedSysfsBDI:            map[string]int64{},
			expectedFuseMaxPagesLimit:   ptr(int64(maxFuseMaxPagesLimit)),
		},
		{
			name:              "invalid node_fuse_max_request_limit_kb - exceeds max limit",
			inputMountOptions: []string{fmt.Sprintf("node_fuse_max_request_limit_kb=%d", maxFuseMaxPagesLimit*pageSizeKB+1)},
			expectErr:         true,
		},
		{
			name:                        "should return valid options correctly with node_fuse_max_request_limit_kb=0",
			inputMountOptions:           []string{"node_fuse_max_request_limit_kb=0"},
			expectedCsiMountOptions:     defaultCsiMountOptions,
			expectedSidecarMountOptions: []string{},
			expectedSysfsBDI:            map[string]int64{},
			expectedFuseMaxPagesLimit:   ptr(int64(0)),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("test case: %s", tc.name)

			c, s, sysfsBDI, fuseMaxPagesLimit, err := prepareMountOptions(tc.inputMountOptions)

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

			if !reflect.DeepEqual(countOptionOccurrence(c), countOptionOccurrence(tc.expectedCsiMountOptions)) {
				t.Errorf("Got options %v, but expected %v", c, tc.expectedCsiMountOptions)
			}

			if !reflect.DeepEqual(countOptionOccurrence(s), countOptionOccurrence(tc.expectedSidecarMountOptions)) {
				t.Errorf("Got options %v, but expected %v", s, tc.expectedSidecarMountOptions)
			}

			if !reflect.DeepEqual(sysfsBDI, tc.expectedSysfsBDI) {
				t.Errorf("Got sysfsBDI %v, expected %v", sysfsBDI, tc.expectedSysfsBDI)
			}

			expectedPages := util.CeilDiv64(defaultNodeFuseMaxRequestLimitKB, pageSizeKB)
			if tc.expectedFuseMaxPagesLimit != nil {
				expectedPages = *tc.expectedFuseMaxPagesLimit
			}
			if fuseMaxPagesLimit != expectedPages {
				t.Errorf("Got fuseMaxPagesLimit %d, expected %d", fuseMaxPagesLimit, expectedPages)
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

func ptr[T any](v T) *T {
	return &v
}
