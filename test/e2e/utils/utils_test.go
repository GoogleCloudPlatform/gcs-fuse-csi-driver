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

	"github.com/google/go-cmp/cmp"
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

func TestParseConfigFlags(t *testing.T) {
	t.Parallel()
	t.Run("Testing ParseConfigFlags", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name           string
			flagStr        string
			expectedParsed ParsedConfig
		}{
			{
				name:    "should parse standard file cache flags correctly",
				flagStr: "--file-cache-max-size-mb=9,--file-cache-cache-file-for-range-read=true,--metadata-cache-ttl-secs=10,--o=ro,--log-severity=trace",
				expectedParsed: ParsedConfig{
					FileCacheCapacity: "9Mi",
					ReadOnly:          true,
					LogSeverity:       "trace",
					MountOptions: []string{
						"file-cache-cache-file-for-range-read=true",
						"metadata-cache-ttl-secs=10",
					},
				},
			},
			{
				name:    "should fallback to exact default values when flags are empty",
				flagStr: "",
				expectedParsed: ParsedConfig{
					FileCacheCapacity: "-1Mi",
					ReadOnly:          false,
					LogSeverity:       "trace",
					MountOptions:      []string{},
				},
			},
			{
				name:    "should correctly trim space around flags",
				flagStr: "  --file-cache-max-size-mb=100  ,  --metadata-cache-ttl-secs=0  ",
				expectedParsed: ParsedConfig{
					FileCacheCapacity: "100Mi",
					ReadOnly:          false,
					LogSeverity:       "trace",
					MountOptions: []string{
						"metadata-cache-ttl-secs=0",
					},
				},
			},
			{
				name:    "should translate configuration flags correctly",
				flagStr: "--log-file=/tmp/log,--file-cache-enable-parallel-downloads,--file-cache-enable-o-direct=false,--enable-kernel-reader=true,--log-format=text,--cache-dir=/tmp/cache",
				expectedParsed: ParsedConfig{
					FileCacheCapacity: "-1Mi",
					ReadOnly:          false,
					LogFilePath:       "/tmp/log",
					LogSeverity:       "trace",
					CacheDir:          "/tmp/cache",
					MountOptions: []string{
						"logging:file-path:/tmp/log",
						"file-cache-enable-parallel-downloads",
						"file-cache-enable-o-direct=false",
						"enable-kernel-reader=true",
						"logging:format:text",
					},
				},
			},
			{
				name:    "should swallow disallowed flags with no manual handling via default continue",
				flagStr: "--temp-dir=/tmp/foo,--prometheus-port=8080,--foreground",
				expectedParsed: ParsedConfig{
					FileCacheCapacity: "-1Mi",
					ReadOnly:          false,
					LogSeverity:       "trace",
					MountOptions:      []string{},
				},
			},
			{
				name:    "should correctly translate and pass log-format but swallow others",
				flagStr: "--log-format=json,--config-file=/etc/gcsfuse.yaml",
				expectedParsed: ParsedConfig{
					FileCacheCapacity: "-1Mi",
					ReadOnly:          false,
					LogSeverity:       "trace",
					MountOptions: []string{
						"logging:format:json",
					},
				},
			},
			{
				name:    "should correctly parse multiple space-separated flags",
				flagStr: "--stat-cache-ttl=0 --client-protocol=grpc",
				expectedParsed: ParsedConfig{
					FileCacheCapacity: "-1Mi",
					ReadOnly:          false,
					LogSeverity:       "trace",
					MountOptions: []string{
						"stat-cache-ttl=0",
						"client-protocol=grpc",
					},
				},
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			got := ParseConfigFlags(tc.flagStr)

			if diff := cmp.Diff(tc.expectedParsed, got); diff != "" {
				t.Errorf("ParseConfigFlags(%q) mismatch (-want +got):\n%s", tc.flagStr, diff)
			}
		}
	})
}
