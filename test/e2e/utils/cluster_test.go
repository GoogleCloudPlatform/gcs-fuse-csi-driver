/*
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

package utils

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/version"
)

func TestClusterAtLeastMinVersion(t *testing.T) {
	testCases := []struct {
		name           string
		clusterVersion string
		nodeVersion    string
		minVersion     string
		expectSupport  bool
		expectErr      bool
	}{
		{
			name:           "should return true when cluster version is greater than min version 1.32.0",
			clusterVersion: "1.33.0",
			nodeVersion:    "1.33.0",
			minVersion:     "1.32.0",
			expectSupport:  true,
			expectErr:      false,
		},
		{
			name:           "should return false when cluster version is less than min version 1.32.0",
			clusterVersion: "1.31.0",
			nodeVersion:    "1.31.0",
			minVersion:     "1.32.0",
			expectSupport:  false,
			expectErr:      false,
		},
		{
			name:           "should return false when node version is less than min version 1.33.0",
			clusterVersion: "1.34.0",
			nodeVersion:    "1.32.0",
			minVersion:     "1.33.0",
			expectSupport:  false,
			expectErr:      false,
		},
		{
			name:           "should return true when cluster version with gke patch is equal to min version",
			clusterVersion: "1.34.1-gke.3084001",
			nodeVersion:    "1.34.1-gke.3084001",
			minVersion:     "1.34.1",
			expectSupport:  true,
			expectErr:      false,
		},
		{
			name:           "should return true when cluster gke patch version is greater than min gke patch version",
			clusterVersion: "1.34.1-gke.2991000",
			nodeVersion:    "1.34.1-gke.2991000",
			minVersion:     "1.34.1-gke.2890000",
			expectSupport:  true,
			expectErr:      false,
		},
		{
			// gke patch version is not considered for comparison so if major.minor.patch matches than gke.XXXXX doesn't matter
			name:           "should return true when cluster gke patch version is smaller than min gke patch version",
			clusterVersion: "1.34.1-gke.2890000",
			nodeVersion:    "",
			minVersion:     "1.34.1-gke.2990000",
			expectSupport:  true,
			expectErr:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			minVersion := version.MustParseGeneric(tc.minVersion)
			supports, err := ClusterAtLeastMinVersion(tc.clusterVersion, tc.nodeVersion, minVersion)

			if tc.expectErr {
				if err == nil {
					t.Errorf("Expected an error, but got none.")
				}
			} else {
				if err != nil {
					t.Errorf("Did not expect an error, but got: %v.", err)
				}
			}

			if supports != tc.expectSupport {
				t.Errorf("Expected support to be %v, but got %v.", tc.expectSupport, supports)
			}
		})
	}
}
