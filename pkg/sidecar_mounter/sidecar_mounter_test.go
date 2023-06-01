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

package sidecarmounter

import (
	"reflect"
	"testing"
)

var (
	defaultFlagMap = map[string]string{
		"app-name":   GCSFuseAppName,
		"temp-dir":   "test-temp-dir",
		"foreground": "",
		"log-file":   "/dev/fd/1",
		"log-format": "text",
		"uid":        "0",
		"gid":        "0",
	}

	invalidArgs = []string{
		"app-name",
		"temp-dir",
		"foreground",
		"log-file",
		"log-format",
		"key-file",
		"token-url",
		"reuse-token-from-url",
		"o",
	}
)

func TestPrepareMountArgs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		mc           *MountConfig
		expectedArgs map[string]string
	}{
		{
			name: "should return valid args correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				TempDir:    "test-temp-dir",
			},
			expectedArgs: defaultFlagMap,
		},
		{
			name: "should return valid args with options correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				TempDir:    "test-temp-dir",
				Options:    []string{"uid=100", "gid=200", "debug_gcs", "max-conns-per-host=10", "implicit-dirs"},
			},
			expectedArgs: map[string]string{
				"implicit-dirs":      "",
				"app-name":           GCSFuseAppName,
				"temp-dir":           "test-temp-dir",
				"foreground":         "",
				"log-file":           "/dev/fd/1",
				"log-format":         "text",
				"uid":                "100",
				"gid":                "200",
				"debug_gcs":          "",
				"max-conns-per-host": "10",
			},
		},
		{
			name: "should return valid args with bool options correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				TempDir:    "test-temp-dir",
				Options:    []string{"uid=100", "gid=200", "debug_gcs", "max-conns-per-host=10", "implicit-dirs=true", "enable-storage-client-library=false"},
			},
			expectedArgs: map[string]string{
				"implicit-dirs=true":                  "",
				"enable-storage-client-library=false": "",
				"app-name":                            GCSFuseAppName,
				"temp-dir":                            "test-temp-dir",
				"foreground":                          "",
				"log-file":                            "/dev/fd/1",
				"log-format":                          "text",
				"uid":                                 "100",
				"gid":                                 "200",
				"debug_gcs":                           "",
				"max-conns-per-host":                  "10",
			},
		},
		{
			name: "should return valid args with error correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				TempDir:    "test-temp-dir",
				Options:    invalidArgs,
			},
			expectedArgs: defaultFlagMap,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)

		flagMap := tc.mc.PrepareMountArgs()
		if !reflect.DeepEqual(flagMap, tc.expectedArgs) {
			t.Errorf("Got args %v, but expected %v", flagMap, tc.expectedArgs)
		}
	}
}
