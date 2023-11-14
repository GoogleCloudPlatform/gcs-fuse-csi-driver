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

package driver

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestJoinMountOptions(t *testing.T) {
	t.Parallel()
	t.Run("parsing labels string into map", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name            string
			existingOptions []string
			newOptions      []string
			expectedOptions []string
		}{
			{
				name:            "should return deduplicated options",
				existingOptions: []string{"o=noexec", "o=sync", "rw"},
				newOptions:      []string{"o=noexec", "implicit-dirs", "rw"},
				expectedOptions: []string{"o=noexec", "o=sync", "rw", "implicit-dirs"},
			},
			{
				name:            "should return deduplicated options with overwritable options",
				existingOptions: []string{"o=noexec", "o=sync", "gid=3003", "file-mode=664", "dir-mode=775"},
				newOptions:      []string{"o=noexec", "uid=1001", "gid=2002", "file-mode=644", "dir-mode=755"},
				expectedOptions: []string{"o=noexec", "o=sync", "uid=1001", "gid=2002", "file-mode=644", "dir-mode=755"},
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			output := joinMountOptions(tc.existingOptions, tc.newOptions)

			less := func(a, b string) bool { return a > b }
			if diff := cmp.Diff(output, tc.expectedOptions, cmpopts.SortSlices(less)); diff != "" {
				t.Errorf("unexpected options args (-got, +want)\n%s", diff)
			}
		}
	})
}
