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

package metrics

import (
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
)

func TestNewMetricsManager(t *testing.T) {
	t.Run("test the metrics manager creation", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name          string
			endpoint      string
			socketDir     string
			maxCollectors int
		}{
			{
				name:          "basic options test",
				endpoint:      "/test/metrics",
				socketDir:     "/tmp/test-sockets",
				maxCollectors: 5,
			},
			{
				name:          "no collectors test",
				endpoint:      ":9920",
				socketDir:     "/gcsfuse-tmp/socket",
				maxCollectors: 0,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)

			clientset := clientset.NewFakeClientset()
			manager := NewMetricsManager(tc.endpoint, tc.socketDir, tc.maxCollectors, clientset).(*manager)

			if manager.metricsEndpoint != tc.endpoint {
				t.Errorf("NewMetricsManager did not set metricsEndpoint correctly. Got %q, want %q", manager.metricsEndpoint, tc.endpoint)
			}
			if manager.fuseSocketDir != tc.socketDir {
				t.Errorf("NewMetricsManager did not set fuseSocketDir correctly. Got %q, want %q", manager.fuseSocketDir, tc.socketDir)
			}
			if manager.maximumNumberOfCollectors != tc.maxCollectors {
				t.Errorf("NewMetricsManager did not set maximumNumberOfCollectors correctly. Got %d, want %d", manager.maximumNumberOfCollectors, tc.maxCollectors)
			}
			if manager.registry == nil {
				t.Errorf("NewMetricsManager did not initialize registry")
			}
			if manager.volumePublishPathRegistered == nil {
				t.Errorf("NewMetricsManager did not initialize volumePublishPathRegistered")
			}
		}
	})
}
