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
	"fmt"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
)

const (
	TraceStr = "trace"
)

func TestExtractErrorFromGcsFuseErrorFile(t *testing.T) {
	t.Parallel()
	t.Run("modifies error code to be returned for slo parsing", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name         string
			errorMessage []byte
			err          error
			expectedCode codes.Code
		}{
			{
				name:         "bootstrap config error",
				errorMessage: []byte("ERROR: [xds] Attempt to set a bootstrap configuration even though one is already set via environment variables."),
				expectedCode: codes.Unavailable,
			},
			{
				name:         "deprecated flag error",
				errorMessage: []byte("Flag --stat-cache-ttl has been deprecated, This flag has been deprecated (starting v2.0) in favor of metadata-cache-ttl-secs."),
				expectedCode: codes.Unavailable,
			},
			{
				name:         "deprecated flag error 2",
				errorMessage: []byte("Flag --max-retry-duration has been deprecated, This is currently unused."),
				expectedCode: codes.Unavailable,
			},
			{
				name:         "bucket doesn't exist error",
				errorMessage: []byte("something went wrong: bucket doesn't exist"),
				expectedCode: codes.NotFound,
			},
			{
				name:         "authentication error",
				errorMessage: []byte("you set up gcsfuse without authentication, googleapi: Error 403: Anonymous users do not have storage.objects.list access to bucket, forbidden"),
				expectedCode: codes.PermissionDenied,
			},
			{
				name:         "killed",
				errorMessage: []byte("Something wen't wrong - signal: killed"),
				expectedCode: codes.ResourceExhausted,
			},
			{
				name:         "incrorrect usage error",
				errorMessage: []byte("Incorrect Usage: Missing argument 'BUCKET_NAME'"),
				expectedCode: codes.InvalidArgument,
			},
			{
				name:         "internal error",
				errorMessage: []byte("error that should be added to slo"),
				expectedCode: codes.Internal,
			},
			{
				name:         "internal error passed in",
				errorMessage: []byte(""),
				expectedCode: codes.Internal,
				err:          fmt.Errorf("Some error that occurred trying to read the gcsfuse error file"),
			},
			{
				name:         "no error",
				errorMessage: []byte(""),
				expectedCode: codes.OK,
				err:          os.ErrNotExist,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			actualCode, _ := extractErrorFromGcsFuseErrorFile(tc.errorMessage, tc.err)
			if actualCode != tc.expectedCode {
				t.Errorf("Got value %v, but expected %v", actualCode, tc.expectedCode)
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

func TestIsSidecarVersionSupportedForGivenFeature(t *testing.T) {
	t.Parallel()
	t.Run("checking if sidecar version is supported for given version and feature", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name                       string
			imageName                  string
			expectedSupported          bool
			minFeatureVersionSupported string
		}{
			{
				name:                       "should return true for supported sidecar version (managed driver image) for flag defaulting",
				imageName:                  "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.99.0-gke.2@sha256:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: MachineTypeAutoConfigSidecarMinVersion,
			},
			{
				name:                       "should return true for supported sidecar version in staging gcr for flag defaulting",
				imageName:                  "gcr.io/gke-release-staging/gcs-fuse-csi-driver-sidecar-mounter:v1.99.0-gke.0@sha256:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: MachineTypeAutoConfigSidecarMinVersion,
			},
			{
				name:                       "should return true for supported sidecar version (non-managed driver image) for flag defaulting",
				imageName:                  "gcr.io/prow-gob-internal-boskos-447/gcs-fuse-csi-driver/gcs-fuse-csi-driver-sidecar-mounter:v1.15.1-gke.0-14-gf752039e_linux_amd64@sha:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: MachineTypeAutoConfigSidecarMinVersion,
			},
			{
				name:                       "should return false for unsupported sidecar version for flag defaulting",
				imageName:                  "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.14.0-gke.1@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: MachineTypeAutoConfigSidecarMinVersion,
			},
			{
				name:                       "should return false for private sidecar for flag defaulting",
				imageName:                  "customer.gcr.io/dir/gcs-fuse-csi-driver-sidecar-mounter:v1.12.2-gke.0@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: MachineTypeAutoConfigSidecarMinVersion,
			},
			{
				name:                       "sidecar bucket access check - should return false for unsupported sidecar version",
				imageName:                  "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.7.1-gke.3@sha256:380bd2a716b936d9469d09e3a83baf22dddca1586a04a0060d7006ea78930cac",
				expectedSupported:          false,
				minFeatureVersionSupported: SidecarBucketAccessCheckMinVersion,
			},
			{
				name:                       "sidecar bucket access check - should return false for private sidecar",
				imageName:                  "customer.gcr.io/dir/gcs-fuse-csi-driver-sidecar-mounter:v1.12.2-gke.0@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: SidecarBucketAccessCheckMinVersion,
			},
			{
				name:                       "sidecar bucket access check - should return true for supported sidecar version (managed driver image)",
				imageName:                  "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.19.0-gke.2@sha256:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: SidecarCloudProfilerMinVersion,
			},
			{
				name:                       "cloud profiler - should return false for unsupported sidecar version",
				imageName:                  "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.7.1-gke.3@sha256:380bd2a716b936d9469d09e3a83baf22dddca1586a04a0060d7006ea78930cac",
				expectedSupported:          false,
				minFeatureVersionSupported: SidecarCloudProfilerMinVersion,
			},
			{
				name:                       "cloud profiler - should return false for private sidecar",
				imageName:                  "customer.gcr.io/dir/gcs-fuse-csi-driver-sidecar-mounter:v1.20.2-gke.0@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: SidecarCloudProfilerMinVersion,
			},
			{
				name:                       "cloud profiler - should return true for supported sidecar version (managed driver image)",
				imageName:                  "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.99.0-gke.2@sha256:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: SidecarCloudProfilerMinVersion,
			},
			{
				name:                       "Token server - should return true for supported sidecar version",
				imageName:                  "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.18.3-gke.2@sha256:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: TokenServerSidecarMinVersion,
			},
			{
				name:                       "Token server - should return true for supported sidecar version with container registory gke.gcr.io",
				imageName:                  "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.18.3-gke.2@sha256:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: TokenServerSidecarMinVersion,
			},
			{
				name:                       "Token server - should return false for unmanaged container registory with substring gke.gcr.io",
				imageName:                  "random-gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.18.3-gke.2@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: TokenServerSidecarMinVersion,
			},
			{
				name:                       "Token server - should return false for unmanaged container registory with subdir gke.gcr.io",
				imageName:                  "randomhost.gcr.io/gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.18.3-gke.2@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: TokenServerSidecarMinVersion,
			},
			{
				name:                       "Token server - should return true for supported sidecar version in staging gcr",
				imageName:                  "gcr.io/gke-release-staging/gcs-fuse-csi-driver-sidecar-mounter:v1.17.2-gke.0@sha256:abcd",
				expectedSupported:          true,
				minFeatureVersionSupported: TokenServerSidecarMinVersion,
			},
			{
				name:                       "Token server - should return false for unsupported sidecar version",
				imageName:                  "us-central1-artifactregistry.gcr.io/gke-release/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.16.7-gke.1@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: TokenServerSidecarMinVersion,
			},
			{
				name:                       "Token server - should return false for private sidecar",
				imageName:                  "customer.gcr.io/dir/gcs-fuse-csi-driver-sidecar-mounter:v1.17.2-gke.0@sha256:abcd",
				expectedSupported:          false,
				minFeatureVersionSupported: TokenServerSidecarMinVersion,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			actual := isSidecarVersionSupportedForGivenFeature(tc.imageName, tc.minFeatureVersionSupported)
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
			name                                  string
			volumeContext                         map[string]string
			expectedMountOptions                  []string
			expectedSkipBucketAccessCheck         bool
			expectedDisableMetricsCollection      bool
			expectedOptInHostNetworkKSA           bool
			expectedIdentityProvider              string
			expectedErr                           bool
			expectedEnableCloudProfilerForSidecar bool
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
				expectedOptInHostNetworkKSA: false,
				expectedIdentityProvider:    "",
			},
			{
				name: "should return correct mount options for hostnetwork pod ksa opt in",
				volumeContext: map[string]string{
					VolumeContextKeyHostNetworkPodKSA: util.TrueStr,
				},
				expectedMountOptions:        []string{},
				expectedOptInHostNetworkKSA: true,
				expectedIdentityProvider:    "",
			},
			{
				name: "should return correct mount options for private sidecar user's hostnetwork pod ksa opt in",
				volumeContext: map[string]string{
					VolumeContextKeyHostNetworkPodKSA: util.TrueStr,
					VolumeContextKeyIdentityProvider:  "https://container.googleapis.com/v1/projects/p/locations/us-central1/clusters/c",
				},
				expectedMountOptions:        []string{},
				expectedOptInHostNetworkKSA: true,
				expectedIdentityProvider:    "https://container.googleapis.com/v1/projects/p/locations/us-central1/clusters/c",
			},
			{
				name:          "unexpected value for VolumeContextKeySkipCSIBucketAccessCheck",
				volumeContext: map[string]string{VolumeContextKeySkipCSIBucketAccessCheck: "blah"},
				expectedErr:   true,
			},
			{
				name:          "unexpected value for VolumeContextKeyHostNetworkPodKSA",
				volumeContext: map[string]string{VolumeContextKeyHostNetworkPodKSA: "blah"},
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
				name:                             "value set to true for VolumeContextKeyDisableMetrics",
				volumeContext:                    map[string]string{VolumeContextKeyDisableMetrics: util.TrueStr},
				expectedMountOptions:             []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyDisableMetrics] + util.TrueStr},
				expectedDisableMetricsCollection: true,
			},
			{
				name:                             "value set to false for VolumeContextKeyDisableMetrics",
				volumeContext:                    map[string]string{VolumeContextKeyDisableMetrics: util.FalseStr},
				expectedMountOptions:             []string{volumeAttributesToMountOptionsMapping[VolumeContextKeyDisableMetrics] + util.FalseStr},
				expectedDisableMetricsCollection: false,
			},
			{
				name:                                  "value set to true for VolumeContextEnableCloudProfilerForSidecar",
				volumeContext:                         map[string]string{VolumeContextEnableCloudProfilerForSidecar: util.TrueStr},
				expectedMountOptions:                  []string{},
				expectedEnableCloudProfilerForSidecar: true,
			},
			{
				name:                     "value set to true for VolumeContextKeyIdentityProvider",
				volumeContext:            map[string]string{VolumeContextKeyIdentityProvider: "some-string"},
				expectedErr:              false,
				expectedMountOptions:     []string{},
				expectedIdentityProvider: "some-string",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Logf("test case: %s", tc.name)
				output, _, skipCSIBucketAccessCheck, disableMetricsCollection, _, enableCloudProfilerForSidecar, err := parseVolumeAttributes([]string{}, tc.volumeContext)
				if (err != nil) != tc.expectedErr {
					t.Errorf("Got error %v, but expected error %v", err, tc.expectedErr)
				}

				if tc.expectedErr {
					return
				}
				if tc.expectedSkipBucketAccessCheck != skipCSIBucketAccessCheck {
					t.Errorf("Got skipBucketAccessCheck %v, but expected %v", skipCSIBucketAccessCheck, tc.expectedSkipBucketAccessCheck)
				}
				if tc.expectedDisableMetricsCollection != disableMetricsCollection {
					t.Errorf("Got disableMetricsCollection %v, but expected %v", disableMetricsCollection, tc.expectedDisableMetricsCollection)
				}
				if tc.expectedEnableCloudProfilerForSidecar != enableCloudProfilerForSidecar {
					t.Errorf("Got enableCloudProfilerForSidecar %v, but expected %v", enableCloudProfilerForSidecar, tc.expectedEnableCloudProfilerForSidecar)
				}

				less := func(a, b string) bool { return a > b }
				if diff := cmp.Diff(output, tc.expectedMountOptions, cmpopts.SortSlices(less)); diff != "" {
					t.Errorf("unexpected options args (-got, +want)\n%s", diff)
				}
			})
		}
	})
}
