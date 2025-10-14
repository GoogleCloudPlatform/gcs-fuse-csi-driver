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

package profiles

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	testSCConfig = clientset.FakeSCConfig{
		Parameters: map[string]string{
			"workloadType":                          "training",
			"fuseFileCacheMediumPriority":           "gpu:ram|lssd,tpu:ram,general_purpose:ram|lssd",
			"fuseMemoryAllocatableFactor":           "0.7",
			"fuseEphemeralStorageAllocatableFactor": "0.85",
			"mountOptions":                          "1",
			"fileCacheCapacity":                     "2",
			"fileCacheForRangeRead":                 "3",
			"metadataStatCacheCapacity":             "4",
			"metadataTypeCacheCapacity":             "5",
			"metadataCacheTTLSeconds":               "6",
			"gcsfuseLoggingSeverity":                "7",
			"skipCSIBucketAccessCheck":              "8",
			"hostNetworkPodKSA":                     "9",
			"identityProvider":                      "10",
			"disableMetrics":                        "11",
			"identityPool":                          "12",
			"enableCloudProfilerForSidecar":         "13",
			"gcsfuseMetadataPrefetchOnMount":        "14",
		},
		MountOptions: []string{
			"implicit-dirs",
			"metadata-cache:negative-ttl-secs:0",
			"metadata-cache:ttl-secs:-1",
			"file-cache:cache-file-for-range-read:true",
			"file-system:kernel-list-cache-ttl-secs:-1",
			"read_ahead_kb=1024",
		},
	}

	testPVConfig = clientset.FakePVConfig{
		Annotations: map[string]string{
			"gke-gcsfuse/bucket-scan-status":            "completed",
			"gke-gcsfuse/bucket-scan-num-objects":       "1000",
			"gke-gcsfuse/bucket-scan-total-size-bytes":  "1000000000",
			"gke-gcsfuse/bucket-scan-last-updated-time": "2025-10-08T22:17:48Z",
			"gke-gcsfuse/bucket-scan-hns-enabled":       "true",
		},
		SCName: "gcsfusecsi-training",
	}

	csiDriverVolumeAttributeKeys = map[string]struct{}{
		"mountOptions":                   {},
		"fileCacheCapacity":              {},
		"fileCacheForRangeRead":          {},
		"metadataStatCacheCapacity":      {},
		"metadataTypeCacheCapacity":      {},
		"metadataCacheTTLSeconds":        {},
		"gcsfuseLoggingSeverity":         {},
		"skipCSIBucketAccessCheck":       {},
		"hostNetworkPodKSA":              {},
		"identityProvider":               {},
		"disableMetrics":                 {},
		"identityPool":                   {},
		"enableCloudProfilerForSidecar":  {},
		"gcsfuseMetadataPrefetchOnMount": {},
	}
)

const (
	testTargetPath = "/var/lib/kubelet/pods/pod-id/volumes/kubernetes.io~csi/test-pv/mount"
)

// PVConfigBuilder helps create FakePVConfig instances for tests.
type PVConfigBuilder struct {
	config clientset.FakePVConfig
}

// NewPVConfigBuilder initializes a builder with default testPVConfig values.
func NewPVConfigBuilder() *PVConfigBuilder {
	// Clone to avoid modifying the global variable
	cfg := testPVConfig
	cfg.Annotations = make(map[string]string)
	for k, v := range testPVConfig.Annotations {
		cfg.Annotations[k] = v
	}
	return &PVConfigBuilder{config: cfg}
}

// WithAnnotations sets the Annotations field.
func (b *PVConfigBuilder) WithAnnotations(annotations map[string]string) *PVConfigBuilder {
	b.config.Annotations = annotations
	return b
}

// WithSCName sets the SCName field.
func (b *PVConfigBuilder) WithSCName(scName string) *PVConfigBuilder {
	b.config.SCName = scName
	return b
}

// Build returns the final FakePVConfig.
func (b *PVConfigBuilder) Build() clientset.FakePVConfig {
	return b.config
}

// SCConfigBuilder helps create FakeSCConfig instances for tests.
type SCConfigBuilder struct {
	config clientset.FakeSCConfig
}

// NewSCConfigBuilder initializes a builder with default testSCConfig values.
func NewSCConfigBuilder() *SCConfigBuilder {
	// Clone to avoid modifying the global variable
	cfg := testSCConfig
	cfg.Parameters = make(map[string]string)
	for k, v := range testSCConfig.Parameters {
		cfg.Parameters[k] = v
	}
	cfg.MountOptions = append([]string(nil), testSCConfig.MountOptions...)
	return &SCConfigBuilder{config: cfg}
}

// WithParameters sets the Parameters field.
func (b *SCConfigBuilder) WithParameters(params map[string]string) *SCConfigBuilder {
	b.config.Parameters = params
	return b
}

// WithoutParameter removes a key from the Parameters map.
func (b *SCConfigBuilder) WithoutParameter(key string) *SCConfigBuilder {
	if b.config.Parameters != nil {
		delete(b.config.Parameters, key)
	}
	return b
}

// Build returns the final FakeSCConfig.
func (b *SCConfigBuilder) Build() clientset.FakeSCConfig {
	return b.config
}

func TestBuildProfileConfig(t *testing.T) {
	tests := []struct {
		name       string
		targetPath string
		pvConfig   clientset.FakePVConfig
		scConfig   clientset.FakeSCConfig
		wantErr    bool
		wantConfig *ProfileConfig
	}{
		{
			name:       "TestBuildProfileConfig - Should build config successfully",
			targetPath: testTargetPath,
			wantErr:    false,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().Build(),
			wantConfig: &ProfileConfig{
				pvDetails: &pvDetails{
					name:           "test-pv",
					numObjects:     1000,
					totalSizeBytes: 1000000000,
				},
				fileCacheMediumPriority: map[string][]string{
					"general_purpose": {"ram", "lssd"},
					"gpu":             {"ram", "lssd"},
					"tpu":             {"ram"},
				},
				fuseMemoryAllocatableFactor:           0.7,
				fuseEphemeralStorageAllocatableFactor: 0.85,
				VolumeAttributes: map[string]string{
					"mountOptions":                   "1",
					"fileCacheCapacity":              "2",
					"fileCacheForRangeRead":          "3",
					"metadataStatCacheCapacity":      "4",
					"metadataTypeCacheCapacity":      "5",
					"metadataCacheTTLSeconds":        "6",
					"gcsfuseLoggingSeverity":         "7",
					"skipCSIBucketAccessCheck":       "8",
					"hostNetworkPodKSA":              "9",
					"identityProvider":               "10",
					"disableMetrics":                 "11",
					"identityPool":                   "12",
					"enableCloudProfilerForSidecar":  "13",
					"gcsfuseMetadataPrefetchOnMount": "14",
				},
				mountOptions: []string{
					"implicit-dirs",
					"metadata-cache:negative-ttl-secs:0",
					"metadata-cache:ttl-secs:-1",
					"file-cache:cache-file-for-range-read:true",
					"file-system:kernel-list-cache-ttl-secs:-1",
					"read_ahead_kb=1024",
				},
			},
		},
		{
			name:       "TestBuildProfileConfig - Should fail with invalid target path",
			targetPath: "/tmp/invalidpath",
			wantErr:    true,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().Build(),
		},
		{
			name:       "TestBuildProfileConfig - Should fail if PV annotations missing",
			targetPath: testTargetPath,
			pvConfig:   NewPVConfigBuilder().WithAnnotations(nil).Build(),
			scConfig:   NewSCConfigBuilder().Build(),
			wantErr:    true,
		},
		{
			name:       "TestBuildProfileConfig - Should fail if SC name empty",
			pvConfig:   NewPVConfigBuilder().WithSCName("").Build(),
			scConfig:   NewSCConfigBuilder().Build(),
			targetPath: testTargetPath,
			wantErr:    true,
		},
		{
			name:       "TestBuildProfileConfig - Should fail if SC parameters nil",
			targetPath: testTargetPath,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().WithParameters(nil).Build(),
			wantErr:    true,
		},
		{
			name:       "testbuildprofileconfig - should fail if workloadType missing",
			targetPath: testTargetPath,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().WithoutParameter("workloadType").Build(),
			wantErr:    true,
		},
		{
			name:       "TestBuildProfileConfig - Should fail if fuseFileCacheMediumPriority missing",
			targetPath: testTargetPath,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().WithoutParameter("fuseFileCacheMediumPriority").Build(),
			wantErr:    true,
		},
		{
			name:       "TestBuildProfileConfig - Should fail if fuseMemoryAllocatableFactor missing",
			targetPath: testTargetPath,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().WithoutParameter("fuseMemoryAllocatableFactor").Build(),
			wantErr:    true,
		},
		{
			name:       "TestBuildProfileConfig - Should fail if fuseEphemeralStorageAllocatableFactor missing",
			targetPath: testTargetPath,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().WithoutParameter("fuseEphemeralStorageAllocatableFactor").Build(),
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := clientset.NewFakeClientset()
			// Check if default-like structs, to avoid creating empty PV/SC
			if !reflect.DeepEqual(tt.pvConfig, clientset.FakePVConfig{}) {
				fakeClient.CreatePV(tt.pvConfig)
			}
			if !reflect.DeepEqual(tt.scConfig, clientset.FakeSCConfig{}) {
				fakeClient.CreateSC(tt.scConfig)
			}

			got, err := BuildProfileConfig(tt.targetPath, fakeClient, csiDriverVolumeAttributeKeys)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildProfileConfig() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			opts := []cmp.Option{
				cmp.AllowUnexported(ProfileConfig{}, pvDetails{}),
			}
			if diff := cmp.Diff(tt.wantConfig, got, opts...); diff != "" {
				t.Errorf("BuildProfileConfig() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseFileCacheMediumPriority(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string][]string
		wantErr bool
	}{
		{
			name:    "TestParseFileCacheMediumPriority - Should handle empty string",
			input:   "",
			want:    map[string][]string{},
			wantErr: false,
		},
		{
			name:    "TestParseFileCacheMediumPriority - Should parse single type single medium",
			input:   "general_purpose:ram",
			want:    map[string][]string{"general_purpose": {"ram"}},
			wantErr: false,
		},
		{
			name:    "TestParseFileCacheMediumPriority - Should parse single type multiple mediums",
			input:   "general_purpose:ram|lssd",
			want:    map[string][]string{"general_purpose": {"ram", "lssd"}},
			wantErr: false,
		},
		{
			name:  "TestParseFileCacheMediumPriority - Should parse multiple types",
			input: "general_purpose:ram|lssd,gpu:ram,tpu:ram",
			want: map[string][]string{
				"general_purpose": {"ram", "lssd"},
				"gpu":             {"ram"},
				"tpu":             {"ram"},
			},
			wantErr: false,
		},
		{
			name:  "TestParseFileCacheMediumPriority - Should handle spaces",
			input: "  general_purpose : ram | lssd , gpu:ram ",
			want: map[string][]string{
				"general_purpose": {"ram", "lssd"},
				"gpu":             {"ram"},
			},
			wantErr: false,
		},
		{
			name:    "TestParseFileCacheMediumPriority - Should handle empty medium list",
			input:   "general_purpose:",
			want:    map[string][]string{"general_purpose": nil},
			wantErr: false,
		},
		{
			name:    "TestParseFileCacheMediumPriority - Should ignore empty mediums in list",
			input:   "general_purpose:ram||lssd",
			want:    map[string][]string{"general_purpose": {"ram", "lssd"}},
			wantErr: false,
		},
		{
			name:    "TestParseFileCacheMediumPriority - Should return error on missing colon",
			input:   "general_purposeram",
			wantErr: true,
		},
		{
			name:    "TestParseFileCacheMediumPriority - Should return error on empty key",
			input:   ":ram",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFileCacheMediumPriority(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseFileCacheMediumPriority() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseFileCacheMediumPriority() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPVDetailsFromAnnotations(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        *pvDetails
		wantErr     bool
	}{
		{
			name: "TestGetPVDetailsFromAnnotations - Should parse valid annotations",
			annotations: map[string]string{
				annotationNumObjects: "12345",
				annotationTotalSize:  "67890",
			},
			want:    &pvDetails{name: "test-pv", numObjects: 12345, totalSizeBytes: 67890},
			wantErr: false,
		},
		{
			name:        "TestGetPVDetailsFromAnnotations - Should return error if annotations nil",
			annotations: nil,
			wantErr:     true,
		},
		{
			name: "TestGetPVDetailsFromAnnotations - Should return error if numObjects missing",
			annotations: map[string]string{
				annotationTotalSize: "67890",
			},
			wantErr: true,
		},
		{
			name: "TestGetPVDetailsFromAnnotations - Should return error if totalSize missing",
			annotations: map[string]string{
				annotationNumObjects: "12345",
			},
			wantErr: true,
		},
		{
			name: "TestGetPVDetailsFromAnnotations - Should return error if numObjects invalid",
			annotations: map[string]string{
				annotationNumObjects: "abc",
				annotationTotalSize:  "67890",
			},
			wantErr: true,
		},
		{
			name: "TestGetPVDetailsFromAnnotations - Should return error if totalSize invalid",
			annotations: map[string]string{
				annotationNumObjects: "12345",
				annotationTotalSize:  "def",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pv", Annotations: tt.annotations},
			}
			got, err := pvDetailsFromAnnotations(pv)
			if (err != nil) != tt.wantErr {
				t.Fatalf("getPVDetailsFromAnnotations() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(pvDetails{})); diff != "" {
				t.Errorf("getPVDetailsFromAnnotations() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSelectFromMapIfKeysMatch(t *testing.T) {
	tests := []struct {
		name   string
		target map[string]string
		keys   map[string]struct{}
		want   map[string]string
	}{
		{
			name:   "target_empty - Should return empty map",
			target: map[string]string{},
			keys:   map[string]struct{}{"a": {}, "b": {}},
			want:   map[string]string{},
		},
		{
			name:   "keys_empty - Should return empty map",
			target: map[string]string{"a": "1", "b": "2"},
			keys:   map[string]struct{}{},
			want:   map[string]string{},
		},
		{
			name:   "both_empty - Should return empty map",
			target: map[string]string{},
			keys:   map[string]struct{}{},
			want:   map[string]string{},
		},
		{
			name:   "no_match - Should return empty map",
			target: map[string]string{"a": "1", "b": "2"},
			keys:   map[string]struct{}{"c": {}, "d": {}},
			want:   map[string]string{},
		},
		{
			name:   "some_match_exact - Should return matching entries",
			target: map[string]string{"a": "1", "b": "2", "c": "3"},
			keys:   map[string]struct{}{"a": {}, "c": {}},
			want:   map[string]string{"a": "1", "c": "3"},
		},
		{
			name:   "some_match_case_insensitive - Should return matching entries ignoring case",
			target: map[string]string{"Apple": "red", "banana": "yellow", "Cherry": "dark red"},
			keys:   map[string]struct{}{"apple": {}, "CHERRY": {}, "Durian": {}},
			want:   map[string]string{"Apple": "red", "Cherry": "dark red"},
		},
		{
			name:   "all_match_case_insensitive - Should return all target entries",
			target: map[string]string{"a": "1", "B": "2"},
			keys:   map[string]struct{}{"A": {}, "b": {}},
			want:   map[string]string{"a": "1", "B": "2"},
		},
		{
			name:   "keys_subset_of_target - Should return only entries in keys",
			target: map[string]string{"a": "1", "b": "2", "c": "3"},
			keys:   map[string]struct{}{"b": {}},
			want:   map[string]string{"b": "2"},
		},
		{
			name:   "target_subset_of_keys - Should return all target entries",
			target: map[string]string{"a": "1"},
			keys:   map[string]struct{}{"a": {}, "b": {}},
			want:   map[string]string{"a": "1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectFromMapIfKeysMatch(tc.target, tc.keys)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SelectFromMapIfKeysMatch(%v, %v) returned diff (-want +got):\n%s", tc.target, tc.keys, diff)
			}
		})
	}
}
