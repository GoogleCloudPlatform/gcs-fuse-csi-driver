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

func TestCheckFeatureSupport(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		clusterVersion      string
		useGKEManagedDriver bool
		wantSupported       bool
		wantErr             bool
		minVersion          *version.Version
	}{
		{
			name:                "feature is supported with unmanaged driver regardless of version",
			clusterVersion:      "1.32.0-gke.0",
			minVersion:          sidecarBucketAccessCheckMinimumVersion,
			useGKEManagedDriver: false,
			wantSupported:       true,
			wantErr:             false,
		},
		{
			name:                "feature is not supported on lower cluster version than min version",
			clusterVersion:      "1.33.5-gke.1162000",
			minVersion:          sidecarBucketAccessCheckMinimumVersion,
			useGKEManagedDriver: true,
			wantSupported:       false,
			wantErr:             false,
		},
		{
			name:                "feature is supported on higher patch version",
			clusterVersion:      "1.34.2-gke.100",
			minVersion:          sidecarBucketAccessCheckMinimumVersion,
			useGKEManagedDriver: true,
			wantSupported:       true,
			wantErr:             false,
		},
		{
			name:                "feature is not supported on lower patch version",
			clusterVersion:      "1.34.1-gke.2930000",
			minVersion:          sidecarBucketAccessCheckMinimumVersion,
			useGKEManagedDriver: true,
			wantSupported:       false,
			wantErr:             false,
		},
		{
			name:                "feature is supported on exact min version",
			clusterVersion:      "1.34.1-gke.2931000",
			minVersion:          sidecarBucketAccessCheckMinimumVersion,
			useGKEManagedDriver: true,
			wantSupported:       true,
			wantErr:             false,
		},
		{
			name:                "should return error for invalid cluster version",
			clusterVersion:      "invalid-version",
			minVersion:          sidecarBucketAccessCheckMinimumVersion,
			useGKEManagedDriver: true,
			wantSupported:       false,
			wantErr:             true,
		},
		// {
		// 	name:                "feature is not supported on lower patch version",
		// 	clusterVersion:      "1.34.1-gke.2909000",
		// 	minVersion:          sidecarBucketAccessCheckMinimumVersion,
		// 	useGKEManagedDriver: true,
		// 	wantSupported:       false,
		// 	wantErr:             false,
		// },
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			supported, err := checkFeatureSupport(tc.clusterVersion, "", tc.minVersion, tc.useGKEManagedDriver, "test-feature")

			if (err != nil) != tc.wantErr {
				t.Errorf("checkFeatureSupport() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if supported != tc.wantSupported {
				t.Errorf("checkFeatureSupport() = %v, want %v", supported, tc.wantSupported)
			}
		})
	}
}
