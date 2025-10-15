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
	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

	testNodeConfig = clientset.FakeNodeConfig{
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceMemory:           resource.MustParse("2Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("20Gi"),
			},
		},
	}

	testPodConfig = clientset.FakePodConfig{
		SidecarLimits: corev1.ResourceList{
			corev1.ResourceMemory:           resource.MustParse("1Gi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("10Gi"),
		},
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

// WithParameter sets a specific key-value pair in the Parameters map.
func (b *SCConfigBuilder) WithParameter(key, value string) *SCConfigBuilder {
	if b.config.Parameters == nil {
		b.config.Parameters = make(map[string]string)
	}
	b.config.Parameters[key] = value
	return b
}

// Build returns the final FakeSCConfig.
func (b *SCConfigBuilder) Build() clientset.FakeSCConfig {
	return b.config
}

// NodeConfigBuilder helps create FakeNodeConfig instances for tests.
type NodeConfigBuilder struct {
	config clientset.FakeNodeConfig
}

// NewNodeConfigBuilder initializes a builder with default testNodeConfig values.
func NewNodeConfigBuilder() *NodeConfigBuilder {
	// Clone to avoid modifying the global variable
	cfg := testNodeConfig
	return &NodeConfigBuilder{config: cfg}
}

// Build returns the final FakeNodeConfig.
func (b *NodeConfigBuilder) Build() clientset.FakeNodeConfig {
	return b.config
}

// PodConfigBuilder helps create FakePodConfig instances for tests.
type PodConfigBuilder struct {
	config clientset.FakePodConfig
}

// NewPodConfigBuilder initializes a builder with default testPodConfig values.
func NewPodConfigBuilder() *PodConfigBuilder {
	// Clone to avoid modifying the global variable
	cfg := testPodConfig
	return &PodConfigBuilder{config: cfg}
}

// Build returns the final FakePodConfig.
func (b *PodConfigBuilder) Build() clientset.FakePodConfig {
	return b.config
}

func TestBuildProfileConfig(t *testing.T) {
	tests := []struct {
		name         string
		targetPath   string
		pvConfig     clientset.FakePVConfig
		scConfig     clientset.FakeSCConfig
		nodeName     string
		nodeConfig   clientset.FakeNodeConfig
		podNamespace string
		podName      string
		podConfig    clientset.FakePodConfig
		wantErr      bool
		wantConfig   *ProfileConfig
	}{
		{
			name:         "TestBuildProfileConfig - Should build config successfully",
			targetPath:   testTargetPath,
			wantErr:      false,
			pvConfig:     NewPVConfigBuilder().Build(),
			scConfig:     NewSCConfigBuilder().Build(),
			nodeName:     "test-node",
			nodeConfig:   NewNodeConfigBuilder().Build(),
			podNamespace: "default",
			podName:      "test-pod",
			podConfig:    NewPodConfigBuilder().Build(),
			wantConfig: &ProfileConfig{
				pvDetails: &pvDetails{
					name:           "test-pv",
					numObjects:     1000,
					totalSizeBytes: 1000000000,
				},
				scDetails: &scDetails{
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
				nodeDetails: &nodeDetails{
					name: "test-node",
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           2 * 1024 * 1024 * 1024,
						ephemeralStorageBytes: 20 * 1024 * 1024 * 1024,
					},
					nodeType: "general_purpose",
				},
				podDetails: &podDetails{
					namespace: "default",
					name:      "test-pod",
					sidecarLimits: &parsedResourceList{
						memoryBytes:           1024 * 1024 * 1024,
						ephemeralStorageBytes: 10 * 1024 * 1024 * 1024,
					},
				},
			},
		},
		{
			name:       "TestBuildProfileConfig - Should fail with invalid target path",
			targetPath: "/tmp/invalidpath",
			wantErr:    true,
			pvConfig:   NewPVConfigBuilder().Build(),
			scConfig:   NewSCConfigBuilder().Build(),
			nodeConfig: NewNodeConfigBuilder().Build(),
			podConfig:  NewPodConfigBuilder().Build(),
		},
		{
			name:       "TestBuildProfileConfig - Should fail if PV annotations missing",
			targetPath: testTargetPath,
			pvConfig:   NewPVConfigBuilder().WithAnnotations(nil).Build(),
			scConfig:   NewSCConfigBuilder().Build(),
			nodeConfig: NewNodeConfigBuilder().Build(),
			podConfig:  NewPodConfigBuilder().Build(),
			wantErr:    true,
		},
		{
			name:       "TestBuildProfileConfig - Should fail if SC name empty",
			pvConfig:   NewPVConfigBuilder().WithSCName("").Build(),
			scConfig:   NewSCConfigBuilder().Build(),
			nodeConfig: NewNodeConfigBuilder().Build(),
			podConfig:  NewPodConfigBuilder().Build(),
			targetPath: testTargetPath,
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
			if !reflect.DeepEqual(tt.nodeConfig, clientset.FakeNodeConfig{}) {
				fakeClient.CreateNode(tt.nodeConfig)
			}
			if !reflect.DeepEqual(tt.podConfig, clientset.FakePodConfig{}) {
				fakeClient.CreatePod(tt.podConfig)
			}

			got, err := BuildProfileConfig(&BuildProfileConfigParams{
				targetPath:          tt.targetPath,
				clientset:           fakeClient,
				volumeAttributeKeys: csiDriverVolumeAttributeKeys,
				nodeName:            tt.nodeName,
				podNamespace:        tt.podNamespace,
				podName:             tt.podName,
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildProfileConfig() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			opts := []cmp.Option{
				cmp.AllowUnexported(ProfileConfig{}, pvDetails{}, nodeDetails{}, parsedResourceList{}, scDetails{}, podDetails{}),
			}
			if diff := cmp.Diff(tt.wantConfig, got, opts...); diff != "" {
				t.Errorf("BuildProfileConfig() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildNodeDetails(t *testing.T) {
	tests := []struct {
		name    string
		node    *corev1.Node
		want    *nodeDetails
		wantErr bool
	}{
		{
			name: "TestBuildNodeDetails - General purpose node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceMemory:           resource.MustParse("2Gi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("20Gi"),
					},
				},
			},
			want: &nodeDetails{
				name:     "test-node",
				nodeType: nodeTypeGeneralPurpose,
				nodeAllocatables: &parsedResourceList{
					memoryBytes:           2 * 1024 * 1024 * 1024,
					ephemeralStorageBytes: 20 * 1024 * 1024 * 1024,
				},
			},
		},
		{
			name: "TestBuildNodeDetails - GPU node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu-node"},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceMemory:           resource.MustParse("2Gi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("20Gi"),
						nvidiaGpuResourceName:           resource.MustParse("1"),
					},
				},
			},
			want: &nodeDetails{
				name:     "gpu-node",
				nodeType: nodeTypeGPU,
				nodeAllocatables: &parsedResourceList{
					memoryBytes:           2 * 1024 * 1024 * 1024,
					ephemeralStorageBytes: 20 * 1024 * 1024 * 1024,
				},
			},
		},
		{
			name: "TestBuildNodeDetails - TPU node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "tpu-node"},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceMemory:           resource.MustParse("2Gi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("20Gi"),
						googleTpuResourceName:           resource.MustParse("1"),
					},
				},
			},
			want: &nodeDetails{
				name:     "tpu-node",
				nodeType: nodeTypeTPU,
				nodeAllocatables: &parsedResourceList{
					memoryBytes:           2 * 1024 * 1024 * 1024,
					ephemeralStorageBytes: 20 * 1024 * 1024 * 1024,
				},
			},
		},
		{
			name: "TestBuildNodeDetails - Node with Local SSD annotation",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "lssd-node",
					Annotations: map[string]string{
						gkeAppliedNodeLabelsAnnotationKey: ephemeralStorageLocalSSDLabelKey + "=" + util.TrueStr,
					},
				},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceMemory:           resource.MustParse("1Gi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("10Gi"),
					},
				},
			},
			want: &nodeDetails{
				name:     "lssd-node",
				nodeType: nodeTypeGeneralPurpose,
				nodeAllocatables: &parsedResourceList{
					memoryBytes:           1 * 1024 * 1024 * 1024,
					ephemeralStorageBytes: 10 * 1024 * 1024 * 1024,
				},
				hasLocalSSDEphemeralStorageAnnotation: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildNodeDetails(tt.node)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildNodeDetails() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(nodeDetails{}, parsedResourceList{})); diff != "" {
				t.Errorf("buildNodeDetails() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildPodDetails(t *testing.T) {
	validLimits := corev1.ResourceList{
		corev1.ResourceMemory:           resource.MustParse("256Mi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("5Gi"),
	}

	tests := []struct {
		name            string
		pod             *corev1.Pod
		isInitContainer bool
		want            *podDetails
		wantErr         bool
	}{
		{
			name: "TestBuildPodDetails - Should parse sidecar in Containers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "other-container"},
						{
							Name:      webhook.GcsFuseSidecarName,
							Resources: corev1.ResourceRequirements{Limits: validLimits},
						},
					},
				},
			},
			isInitContainer: false,
			want: &podDetails{
				namespace: "default",
				name:      "test-pod",
				sidecarLimits: &parsedResourceList{
					memoryBytes:           256 * 1024 * 1024,
					ephemeralStorageBytes: 5 * 1024 * 1024 * 1024,
				},
			},
		},
		{
			name: "TestBuildPodDetails - Should parse sidecar in InitContainers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "init-pod", Namespace: "kube-system"},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:      webhook.GcsFuseSidecarName,
							Resources: corev1.ResourceRequirements{Limits: validLimits},
						},
					},
					Containers: []corev1.Container{{Name: "main-container"}},
				},
			},
			isInitContainer: true,
			want: &podDetails{
				namespace: "kube-system",
				name:      "init-pod",
				sidecarLimits: &parsedResourceList{
					memoryBytes:           256 * 1024 * 1024,
					ephemeralStorageBytes: 5 * 1024 * 1024 * 1024,
				},
			},
		},
		{
			name: "TestBuildPodDetails - Should return empty sidecar limits if sidecar not found",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "no-sidecar-pod", Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "other-container"}},
				},
			},
			isInitContainer: false,
			want: &podDetails{
				namespace:     "default",
				name:          "no-sidecar-pod",
				sidecarLimits: &parsedResourceList{}, // Expect empty
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildPodDetails(tt.isInitContainer, tt.pod)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildPodDetails() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(podDetails{}, parsedResourceList{})); diff != "" {
				t.Errorf("buildPodDetails() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildSCDetails(t *testing.T) {
	defaultSC := NewSCConfigBuilder().Build()
	defaultParams := defaultSC.Parameters
	defaultMountOptions := defaultSC.MountOptions

	tests := []struct {
		name    string
		sc      *storagev1.StorageClass
		want    *scDetails
		wantErr bool
	}{
		{
			name: "TestBuildSCDetails - Valid StorageClass",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "test-sc"},
				Parameters:   defaultParams,
				MountOptions: defaultMountOptions,
			},
			want: &scDetails{
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
				mountOptions: defaultMountOptions,
			},
		},
		{
			name: "TestBuildSCDetails - Nil Parameters",
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{Name: "nil-params-sc"},
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Missing workloadType",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "missing-workload-sc"},
				Parameters:   NewSCConfigBuilder().WithoutParameter("workloadType").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Invalid workloadType",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "invalid-workload-sc"},
				Parameters:   NewSCConfigBuilder().WithParameter("workloadType", "invalid").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Missing fuseFileCacheMediumPriority",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "missing-priority-sc"},
				Parameters:   NewSCConfigBuilder().WithoutParameter("fuseFileCacheMediumPriority").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Invalid fuseFileCacheMediumPriority",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "invalid-priority-sc"},
				Parameters:   NewSCConfigBuilder().WithParameter("fuseFileCacheMediumPriority", "gpu:ram,:bad").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Missing fuseMemoryAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "missing-mem-factor-sc"},
				Parameters:   NewSCConfigBuilder().WithoutParameter("fuseMemoryAllocatableFactor").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Invalid fuseMemoryAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "invalid-mem-factor-sc"},
				Parameters:   NewSCConfigBuilder().WithParameter("fuseMemoryAllocatableFactor", "-0.1").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Missing fuseEphemeralStorageAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "missing-storage-factor-sc"},
				Parameters:   NewSCConfigBuilder().WithoutParameter("fuseEphemeralStorageAllocatableFactor").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Invalid fuseEphemeralStorageAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "invalid-storage-factor-sc"},
				Parameters:   NewSCConfigBuilder().WithParameter("fuseEphemeralStorageAllocatableFactor", "abc").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Only keep relevant volume attributes",
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{Name: "extra-params-sc"},
				Parameters: map[string]string{
					"workloadType":                          "inference",
					"fuseFileCacheMediumPriority":           "general_purpose:ram",
					"fuseMemoryAllocatableFactor":           "0.1",
					"fuseEphemeralStorageAllocatableFactor": "0.1",
					"fileCacheCapacity":                     "10Gi", // Keep
					"unknownParameter":                      "should-be-ignored",
				},
				MountOptions: defaultMountOptions,
			},
			want: &scDetails{
				fileCacheMediumPriority: map[string][]string{
					"general_purpose": {"ram"},
				},
				fuseMemoryAllocatableFactor:           0.1,
				fuseEphemeralStorageAllocatableFactor: 0.1,
				VolumeAttributes: map[string]string{
					"fileCacheCapacity": "10Gi",
				},
				mountOptions: defaultMountOptions,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildSCDetails(tt.sc, csiDriverVolumeAttributeKeys)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildSCDetails() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(scDetails{})); diff != "" {
				t.Errorf("buildSCDetails() returned unexpected diff (-want +got):\n%s", diff)
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

func TestBuildPVDetails(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        *pvDetails
		wantErr     bool
	}{
		{
			name: "TestBuildPVDetails - Should parse valid annotations",
			annotations: map[string]string{
				putil.AnnotationNumObjects: "12345",
				putil.AnnotationTotalSize:  "67890",
			},
			want:    &pvDetails{name: "test-pv", numObjects: 12345, totalSizeBytes: 67890},
			wantErr: false,
		},
		{
			name:        "TestBuildPVDetails - Should return error if annotations nil",
			annotations: nil,
			wantErr:     true,
		},
		{
			name: "TestBuildPVDetails - Should return error if numObjects missing",
			annotations: map[string]string{
				putil.AnnotationTotalSize: "67890",
			},
			wantErr: true,
		},
		{
			name: "TestBuildPVDetails - Should return error if totalSize missing",
			annotations: map[string]string{
				putil.AnnotationNumObjects: "12345",
			},
			wantErr: true,
		},
		{
			name: "TestBuildPVDetails - Should return error if numObjects invalid",
			annotations: map[string]string{
				putil.AnnotationNumObjects: "abc",
				putil.AnnotationTotalSize:  "67890",
			},
			wantErr: true,
		},
		{
			name: "TestBuildPVDetails - Should return error if totalSize invalid",
			annotations: map[string]string{
				putil.AnnotationNumObjects: "12345",
				putil.AnnotationTotalSize:  "def",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pv", Annotations: tt.annotations},
			}
			got, err := buildPVDetails(pv)
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

func TestIsGpuNodeByResource(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{
			name: "TestIsGpuNodeByResource - Should return true for GPU node",
			node: &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{nvidiaGpuResourceName: resource.MustParse("1")}}},
			want: true,
		},
		{
			name: "TestIsGpuNodeByResource - Should return false for non-GPU node",
			node: &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}},
			want: false,
		},
		{
			name: "TestIsGpuNodeByResource - Should return false for zero GPUs",
			node: &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{nvidiaGpuResourceName: resource.MustParse("0")}}},
			want: false,
		},
		{
			name: "TestIsGpuNodeByResource - Should return false for nil node",
			node: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGpuNodeByResource(tt.node); got != tt.want {
				t.Errorf("isGpuNodeByResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTpuNodeByResource(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{
			name: "TestIsTpuNodeByResource - Should return true for TPU node",
			node: &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{googleTpuResourceName: resource.MustParse("1")}}},
			want: true,
		},
		{
			name: "TestIsTpuNodeByResource - Should return false for non-TPU node",
			node: &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}},
			want: false,
		},
		{
			name: "TestIsTpuNodeByResource - Should return false for zero TPUs",
			node: &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{googleTpuResourceName: resource.MustParse("0")}}},
			want: false,
		},
		{
			name: "TestIsTpuNodeByResource - Should return false for nil node",
			node: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTpuNodeByResource(tt.node); got != tt.want {
				t.Errorf("isTpuNodeByResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGcsFuseSidecarResourceRequirements(t *testing.T) {
	limits := corev1.ResourceList{
		corev1.ResourceMemory:           resource.MustParse("1Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("10Gi"),
	}
	tests := []struct {
		name            string
		pod             *corev1.Pod
		want            corev1.ResourceRequirements
		isInitContainer bool
	}{
		{
			name: "TestGcsFuseSidecarResourceRequirements - Should find sidecar in InitContainers",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: webhook.GcsFuseSidecarName, Resources: corev1.ResourceRequirements{Limits: limits}}},
			}},
			isInitContainer: true,
			want:            corev1.ResourceRequirements{Limits: limits},
		},
		{
			name: "TestGcsFuseSidecarResourceRequirements - Should find sidecar in Containers",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: webhook.GcsFuseSidecarName, Resources: corev1.ResourceRequirements{Limits: limits}}},
			}},
			isInitContainer: false,
			want:            corev1.ResourceRequirements{Limits: limits},
		},
		{
			name: "TestGcsFuseSidecarResourceRequirements - Should return empty for nil pod",
			pod:  nil,
			want: corev1.ResourceRequirements{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gcsFuseSidecarResourceRequirements(tt.isInitContainer, tt.pod); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("gcsFuseSidecarResourceRequirements() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseResource(t *testing.T) {
	tests := []struct {
		name         string
		resourceName corev1.ResourceName
		resourceList corev1.ResourceList
		want         int64
		wantErr      bool
	}{
		{
			name:         "TestParseResource - Should parse memory",
			resourceName: corev1.ResourceMemory,
			resourceList: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			want:         1024 * 1024 * 1024,
			wantErr:      false,
		},
		{
			name:         "TestParseResource - Should parse ephemeral storage",
			resourceName: corev1.ResourceEphemeralStorage,
			resourceList: corev1.ResourceList{corev1.ResourceEphemeralStorage: resource.MustParse("10G")},
			want:         10 * 1000 * 1000 * 1000,
			wantErr:      false,
		},
		{
			name:         "TestParseResource - Should return 0 if resource not found",
			resourceName: corev1.ResourceCPU,
			resourceList: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			want:         0,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseResource(tt.resourceName, tt.resourceList)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseResource() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseResource() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasLocalSSDEphemeralStorageAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "TestHasLocalSSDEphemeralStorageAnnotation - Should return false for nil annotations",
			annotations: nil,
			want:        false,
		},
		{
			name:        "TestHasLocalSSDEphemeralStorageAnnotation - Should return false for empty annotations",
			annotations: map[string]string{},
			want:        false,
		},
		{
			name: "TestHasLocalSSDEphemeralStorageAnnotation - Should return false when key not present",
			annotations: map[string]string{
				"some-other-key": "value",
			},
			want: false,
		},
		{
			name: "TestHasLocalSSDEphemeralStorageAnnotation - Should return true when label is present and true",
			annotations: map[string]string{
				gkeAppliedNodeLabelsAnnotationKey: "foo=bar," + ephemeralStorageLocalSSDLabelKey + "=" + util.TrueStr + ",baz=qux",
			},
			want: true,
		},
		{
			name: "TestHasLocalSSDEphemeralStorageAnnotation - Should return false when label is present but not true",
			annotations: map[string]string{
				gkeAppliedNodeLabelsAnnotationKey: ephemeralStorageLocalSSDLabelKey + "=false",
			},
			want: false,
		},
		{
			name: "TestHasLocalSSDEphemeralStorageAnnotation - Should handle spaces around label",
			annotations: map[string]string{
				gkeAppliedNodeLabelsAnnotationKey: "  " + ephemeralStorageLocalSSDLabelKey + "  =  " + util.TrueStr + "  ",
			},
			want: true,
		},
		{
			name: "TestHasLocalSSDEphemeralStorageAnnotation - Should handle malformed pairs gracefully",
			annotations: map[string]string{
				gkeAppliedNodeLabelsAnnotationKey: "foo=bar,malformed," + ephemeralStorageLocalSSDLabelKey + "=" + util.TrueStr,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasLocalSSDEphemeralStorageAnnotation(tt.annotations); got != tt.want {
				t.Errorf("hasLocalSSDEphemeralStorageAnnotation() = %v, want %v", got, tt.want)
			}
		})
	}
}
