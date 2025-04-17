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
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
)

const (
	TraceStr = "trace"
)

func TestRemoveBucketSuffixIfPresentAndReturnVolumeId(t *testing.T) {
	t.Parallel()
	t.Run("removing bucket suffix if present and returning volume id", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name          string
			bucketName    string
			expectedValue string
		}{
			{
				name:          "should return bucket name without suffix, no special character",
				bucketName:    "bucket-name:1234567890123456789",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, special character",
				bucketName:    "bucket-name:1234567890123456789@us-central1",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, no colon with special character",
				bucketName:    "bucket-name@us-central1",
				expectedValue: "bucket-name@us-central1",
			},
			{
				name:          "should return bucket name without suffix, two colons",
				bucketName:    "bucket-name:1234567890123456789:12",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, no special character and no colon",
				bucketName:    "bucket-name",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, no bucket name",
				bucketName:    ":xyz",
				expectedValue: "",
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			actual := parseVolumeID(tc.bucketName)
			if actual != tc.expectedValue {
				t.Errorf("Got value %v, but expected %v", actual, tc.expectedValue)
			}
		}
	})
}
func TestJoinMountOptions(t *testing.T) {
	t.Parallel()
	t.Run("joining mount options into one", func(t *testing.T) {
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

func TestIsSidecarVersionSupportedForTokenServer(t *testing.T) {
	t.Parallel()
	t.Run("checking if sidecar version is supported for token server", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name              string
			imageName         string
			expectedSupported bool
		}{
			{
				name:              "should return true for supported sidecar version",
				imageName:         "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.12.3-gke.2@sha256:abcd",
				expectedSupported: true,
			},
			{
				name:              "should return true for supported sidecar version in staging gcr",
				imageName:         "gcr.io/gke-release-staging/gcs-fuse-csi-driver-sidecar-mounter:v1.12.2-gke.0@sha256:abcd",
				expectedSupported: true,
			},
			{
				name:              "should return false for unsupported sidecar version",
				imageName:         "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.8.7-gke.1@sha256:abcd",
				expectedSupported: false,
			},
			{
				name:              "should return false for private sidecar",
				imageName:         "customer.gcr.io/dir/gcs-fuse-csi-driver-sidecar-mounter:v1.12.2-gke.0@sha256:abcd",
				expectedSupported: false,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			actual := isSidecarVersionSupportedForTokenServer(tc.imageName)
			if actual != tc.expectedSupported {
				t.Errorf("Got supported %v, but expected %v", actual, tc.expectedSupported)
			}
		}
	})
}

func TestParseVolumeAttributes(t *testing.T) {
	t.Parallel()
	t.Run("parsing volume attributes into mount options", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name                            string
			volumeContext                   map[string]string
			expectedMountOptions            []string
			expectedSkipBucketAccessCheck   bool
			expectedEnableMetricsCollection bool
			expectedErr                     bool
		}{
			{
				name:                 "should return correct fileCacheCapacity 1",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheCapacity: "500Gi"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "512000"},
			},
			{
				name:                 "should return correct fileCacheCapacity 2",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheCapacity: "50000000"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct fileCacheCapacity 3",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheCapacity: "50e6"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct fileCacheCapacity 4",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheCapacity: "-1"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct fileCacheCapacity 5",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheCapacity: "-100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct fileCacheCapacity 6",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheCapacity: "0"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "0"},
			},
			{
				name:          "should throw error for invalid fileCacheCapacity",
				volumeContext: map[string]string{VolumeContextKeyFileCacheCapacity: "abc"},
				expectedErr:   true,
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 1",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "500Gi"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "512000"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 2",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "50000000"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 3",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "50e6"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 4",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "-1"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 5",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "-100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 6",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "0"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "0"},
			},
			{
				name:          "should throw error for invalid metadataStatCacheCapacity",
				volumeContext: map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "abc"},
				expectedErr:   true,
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 1",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "500Gi"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "512000"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 2",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "50000000"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 3",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "50e6"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 4",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "-1"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 5",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "-100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct metadataStatCacheCapacity 6",
				volumeContext:        map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "0"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "0"},
			},
			{
				name:          "should throw error for invalid metadataStatCacheCapacity",
				volumeContext: map[string]string{VolumeContextKeyMetadataStatCacheCapacity: "abc"},
				expectedErr:   true,
			},
			{
				name:                 "should return correct metadataTypeCacheCapacity 1",
				volumeContext:        map[string]string{VolumeContextKeyMetadataTypeCacheCapacity: "500Gi"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "512000"},
			},
			{
				name:                 "should return correct metadataTypeCacheCapacity 2",
				volumeContext:        map[string]string{VolumeContextKeyMetadataTypeCacheCapacity: "50000000"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct metadataTypeCacheCapacity 3",
				volumeContext:        map[string]string{VolumeContextKeyMetadataTypeCacheCapacity: "50e6"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "50"},
			},
			{
				name:                 "should return correct metadataTypeCacheCapacity 4",
				volumeContext:        map[string]string{VolumeContextKeyMetadataTypeCacheCapacity: "-1"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct metadataTypeCacheCapacity 5",
				volumeContext:        map[string]string{VolumeContextKeyMetadataTypeCacheCapacity: "-100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "-1"},
			},
			{
				name:                 "should return correct metadataTypeCacheCapacity 6",
				volumeContext:        map[string]string{VolumeContextKeyMetadataTypeCacheCapacity: "0"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "0"},
			},
			{
				name:          "should throw error for invalid metadataTypeCacheCapacity",
				volumeContext: map[string]string{VolumeContextKeyMetadataTypeCacheCapacity: "abc"},
				expectedErr:   true,
			},
			{
				name:                 "should return correct fileCacheForRangeRead 1",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheForRangeRead: util.TrueStr},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.TrueStr},
			},
			{
				name:                 "should return correct fileCacheForRangeRead 2",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheForRangeRead: "True"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.TrueStr},
			},
			{
				name:                 "should return correct fileCacheForRangeRead 3",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheForRangeRead: util.FalseStr},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.FalseStr},
			},
			{
				name:                 "should return correct fileCacheForRangeRead 4",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheForRangeRead: "False"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.FalseStr},
			},
			{
				name:                 "should return correct fileCacheForRangeRead 5",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheForRangeRead: "1"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.TrueStr},
			},
			{
				name:                 "should return correct fileCacheForRangeRead 6",
				volumeContext:        map[string]string{VolumeContextKeyFileCacheForRangeRead: "0"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.FalseStr},
			},
			{
				name:          "should throw error for invalid fileCacheForRangeRead",
				volumeContext: map[string]string{VolumeContextKeyFileCacheForRangeRead: "abc"},
				expectedErr:   true,
			},
			{
				name:                 "should return correct metadataCacheTTLSeconds 1",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTTLSeconds: "100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTTLSeconds] + "100"},
			},
			{
				name:                 "should return correct metadataCacheTTLSeconds 2",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTTLSeconds: "0"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTTLSeconds] + "0"},
			},
			{
				name:                 "should return correct metadataCacheTTLSeconds 3",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTTLSeconds: "-1"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTTLSeconds] + "-1"},
			},
			{
				name:                 "should return correct metadataCacheTTLSeconds 4",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTTLSeconds: "-100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTTLSeconds] + "-1"},
			},
			{
				name:          "should throw error for invalid metadataCacheTTLSeconds 1",
				volumeContext: map[string]string{VolumeContextKeyMetadataCacheTTLSeconds: "abc"},
				expectedErr:   true,
			},
			{
				name:          "should throw error for invalid metadataCacheTTLSeconds 2",
				volumeContext: map[string]string{VolumeContextKeyMetadataCacheTTLSeconds: "0.01"},
				expectedErr:   true,
			},
			{
				name:                 "should return correct metadataCacheTtlSeconds 1",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTtlSeconds: "100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTtlSeconds] + "100"},
			},
			{
				name:                 "should return correct metadataCacheTtlSeconds 2",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTtlSeconds: "0"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTtlSeconds] + "0"},
			},
			{
				name:                 "should return correct metadataCacheTtlSeconds 3",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTtlSeconds: "-1"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTtlSeconds] + "-1"},
			},
			{
				name:                 "should return correct metadataCacheTtlSeconds 4",
				volumeContext:        map[string]string{VolumeContextKeyMetadataCacheTtlSeconds: "-100"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTtlSeconds] + "-1"},
			},
			{
				name:          "should throw error for invalid metadataCacheTtlSeconds 1",
				volumeContext: map[string]string{VolumeContextKeyMetadataCacheTtlSeconds: "abc"},
				expectedErr:   true,
			},
			{
				name:          "should throw error for invalid metadataCacheTtlSeconds 2",
				volumeContext: map[string]string{VolumeContextKeyMetadataCacheTtlSeconds: "0.01"},
				expectedErr:   true,
			},
			{
				name:                 "should return correct gcsfuseLoggingSeverity",
				volumeContext:        map[string]string{VolumeContextKeyGcsfuseLoggingSeverity: "trace"},
				expectedMountOptions: []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyGcsfuseLoggingSeverity] + TraceStr},
			},
			{
				name: "should return correct mount options",
				volumeContext: map[string]string{
					VolumeContextKeyMountOptions:              "implicit-dirs,uid=1001",
					VolumeContextKeyGcsfuseLoggingSeverity:    "trace",
					VolumeContextKeyFileCacheCapacity:         "500Gi",
					VolumeContextKeyFileCacheForRangeRead:     util.TrueStr,
					VolumeContextKeyMetadataStatCacheCapacity: "-100",
					VolumeContextKeyMetadataTypeCacheCapacity: "0",
					VolumeContextKeyMetadataCacheTTLSeconds:   "3600",
				},
				expectedMountOptions: []string{
					"implicit-dirs",
					"uid=1001",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyGcsfuseLoggingSeverity] + "trace",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "512000",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.TrueStr,
					volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "-1",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "0",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTTLSeconds] + "3600",
				},
			},
			{
				name:                          "should return correct mount options, and skip bucket access check flag",
				expectedSkipBucketAccessCheck: true,
				volumeContext: map[string]string{
					VolumeContextKeyMountOptions:              "implicit-dirs,uid=1001",
					VolumeContextKeyGcsfuseLoggingSeverity:    "trace",
					VolumeContextKeyFileCacheCapacity:         "500Gi",
					VolumeContextKeyFileCacheForRangeRead:     util.TrueStr,
					VolumeContextKeyMetadataStatCacheCapacity: "-100",
					VolumeContextKeyMetadataTypeCacheCapacity: "0",
					VolumeContextKeyMetadataCacheTTLSeconds:   "3600",
					VolumeContextKeySkipCSIBucketAccessCheck:  util.TrueStr,
				},
				expectedMountOptions: []string{
					"implicit-dirs",
					"uid=1001",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyGcsfuseLoggingSeverity] + "trace",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheCapacity] + "512000",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyFileCacheForRangeRead] + util.TrueStr,
					volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataStatCacheCapacity] + "-1",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataTypeCacheCapacity] + "0",
					volumeAttributesToMountOptionsMapping[VolumeContextKeyMetadataCacheTTLSeconds] + "3600",
				},
			},
			{
				name:          "unexpected value for VolumeContextKeySkipCSIBucketAccessCheck",
				volumeContext: map[string]string{VolumeContextKeySkipCSIBucketAccessCheck: "blah"},
				expectedErr:   true,
			},
			{
				name:                 "value set to false for VolumeContextKeySkipCSIBucketAccessCheck",
				volumeContext:        map[string]string{VolumeContextKeySkipCSIBucketAccessCheck: util.FalseStr},
				expectedMountOptions: []string{},
			},
			{
				name:          "unexpected value for VolumeContextKeyDisableMetrics",
				volumeContext: map[string]string{VolumeContextKeyDisableMetrics: "blah"},
				expectedErr:   true,
			},
			{
				name:                            "value set to true for VolumeContextKeyDisableMetrics",
				volumeContext:                   map[string]string{VolumeContextKeyDisableMetrics: util.TrueStr},
				expectedMountOptions:            []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyDisableMetrics] + util.TrueStr},
				expectedEnableMetricsCollection: false,
			},
			{
				name:                            "value set to false for VolumeContextKeyDisableMetrics",
				volumeContext:                   map[string]string{VolumeContextKeyDisableMetrics: util.FalseStr},
				expectedMountOptions:            []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyDisableMetrics] + util.FalseStr},
				expectedEnableMetricsCollection: true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Logf("test case: %s", tc.name)
				output, skipCSIBucketAccessCheck, disableMetricsCollection, err := parseVolumeAttributes([]string{}, tc.volumeContext)
				if (err != nil) != tc.expectedErr {
					t.Errorf("Got error %v, but expected error %v", err, tc.expectedErr)
				}
				enableMetricsCollection := !disableMetricsCollection

				if tc.expectedErr {
					return
				}
				if tc.expectedSkipBucketAccessCheck != skipCSIBucketAccessCheck {
					t.Errorf("Got skipBucketAccessCheck %v, but expected %v", skipCSIBucketAccessCheck, tc.expectedSkipBucketAccessCheck)
				}
				if tc.expectedEnableMetricsCollection != enableMetricsCollection {
					t.Errorf("Got disableMetricsCollection %v, but expected %v", enableMetricsCollection, tc.expectedEnableMetricsCollection)
				}

				less := func(a, b string) bool { return a > b }
				if diff := cmp.Diff(output, tc.expectedMountOptions, cmpopts.SortSlices(less)); diff != "" {
					t.Errorf("unexpected options args (-got, +want)\n%s", diff)
				}
			})
		}
	})
}
