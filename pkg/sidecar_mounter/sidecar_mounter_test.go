/*
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

var defaultArgs = []string{
	"gcsfuse",
	"--implicit-dirs",
	"--app-name",
	GCSFUSE_APP_NAME,
	"--temp-dir",
	"test-temp-dir",
	"--foreground",
	"--log-file",
	"/dev/fd/1",
	"--log-format",
	"text",
}

var invalidArgs = []string{
	"implicit-dirs",
	"app-name",
	"temp-dir",
	"foreground",
	"log-file",
	"log-format",
	"key-file",
	"token-url",
	"reuse-token-from-url",
	"endpoint",
}

func TestPrepareMountArgs(t *testing.T) {
	testCases := []struct {
		name          string
		mc            *MountConfig
		expectedArgs  []string
		expectedError bool
	}{
		{
			name: "should return valid args correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				TempDir:    "test-temp-dir",
			},
			expectedArgs:  append(defaultArgs, "test-bucket", "/dev/fd/3"),
			expectedError: false,
		},
		{
			name: "should return valid args with options correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				TempDir:    "test-temp-dir",
				Options:    []string{"uid=100", "gid=200", "debug_gcs", "max-conns-per-host=100"},
			},
			expectedArgs:  append(defaultArgs, "--uid", "100", "--gid", "200", "--debug_gcs", "--max-conns-per-host", "100", "test-bucket", "/dev/fd/3"),
			expectedError: false,
		},
		{
			name: "should return valid args with error correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				TempDir:    "test-temp-dir",
				Options:    invalidArgs,
			},
			expectedArgs:  append(defaultArgs, "test-bucket", "/dev/fd/3"),
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		args, err := prepareMountArgs(tc.mc)
		if tc.expectedError && err == nil {
			t.Errorf("Expected error but got none")
		}
		if err != nil {
			if !tc.expectedError {
				t.Errorf("Did not expect error but got: %v", err)
			}
		}

		if !reflect.DeepEqual(args, tc.expectedArgs) {
			t.Errorf("Got args %v, but expected %v", args, tc.expectedArgs)
		}
	}
}
