/*
Copyright 2025 Google LLC

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

package utils

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/version"
)

// TODO(amacaskill): Add test to presubmit by adding to make unit-test target.
func TestGCSFuseBranch(t *testing.T) {
	t.Parallel()
	t.Run("Testing GCSFuseBranch", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name              string
			gcsfuseVersionStr string
			expectedBranch    string
			expectedVersion   *version.Version
		}{
			{
				name:              "should return master for GCSFuse built from HEAD",
				gcsfuseVersionStr: "0.0.1-gcsfuse-git-master-abcdef",
				expectedBranch:    "master",
				expectedVersion:   nil,
			},
			{
				name:              "should return branch for not valid GCSFuse version v3.3.0-gke.1",
				gcsfuseVersionStr: "3.3.0-gke.1",
				expectedBranch:    "v3.3.0_release",
				expectedVersion:   version.MustParseSemantic("3.3.0-gke.1"),
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			v, branch := GCSFuseBranch(tc.gcsfuseVersionStr)
			if branch != tc.expectedBranch {
				t.Errorf("For branch, got value %q, but expected %q", branch, tc.expectedBranch)
			}

			if tc.expectedVersion == nil {
				if v != nil {
					t.Errorf("For version, got %v, but expected nil", v)
				}
			} else if v == nil {
				t.Errorf("For version, got %v, but expected %v", v, tc.expectedVersion)
			} else if v.String() != tc.expectedVersion.String() {
				t.Errorf("For version, got %q, but expected %q", v.String(), tc.expectedVersion.String())
			}
		}
	})
}
