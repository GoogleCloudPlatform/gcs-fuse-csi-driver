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
	"sort"
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
		Labels: map[string]string{
			"gke-gcsfuse/profile": "true",
		},
		Parameters: map[string]string{
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
	cfg.Labels = make(map[string]string)
	for k, v := range testSCConfig.Labels {
		cfg.Labels[k] = v
	}
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
					volumeAttributes: map[string]string{
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
			name:       "TestBuildProfileConfig - Should return nil profile if SC missing",
			pvConfig:   NewPVConfigBuilder().WithSCName("").Build(),
			scConfig:   NewSCConfigBuilder().Build(),
			nodeConfig: NewNodeConfigBuilder().Build(),
			podConfig:  NewPodConfigBuilder().Build(),
			targetPath: testTargetPath,
			wantConfig: nil,
			wantErr:    false,
		},
		{
			name:       "TestBuildProfileConfig - Should return nil profile if PV missing",
			pvConfig:   clientset.FakePVConfig{},
			scConfig:   NewSCConfigBuilder().Build(),
			nodeConfig: NewNodeConfigBuilder().Build(),
			podConfig:  NewPodConfigBuilder().Build(),
			targetPath: testTargetPath,
			wantConfig: nil,
			wantErr:    false,
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
				TargetPath:          tt.targetPath,
				Clientset:           fakeClient,
				VolumeAttributeKeys: csiDriverVolumeAttributeKeys,
				NodeName:            tt.nodeName,
				PodNamespace:        tt.podNamespace,
				PodName:             tt.podName,
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
		pv      *corev1.PersistentVolume
		want    *scDetails
		wantErr bool
	}{
		{
			name: "TestBuildSCDetails - Valid StorageClass",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "test-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
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
				volumeAttributes: map[string]string{
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
				ObjectMeta: metav1.ObjectMeta{Name: "nil-params-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Missing fuseFileCacheMediumPriority",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "missing-priority-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters:   NewSCConfigBuilder().WithoutParameter("fuseFileCacheMediumPriority").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Invalid fuseFileCacheMediumPriority",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "invalid-priority-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters:   NewSCConfigBuilder().WithParameter("fuseFileCacheMediumPriority", "gpu:ram,:bad").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Missing fuseMemoryAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "missing-mem-factor-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters:   NewSCConfigBuilder().WithoutParameter("fuseMemoryAllocatableFactor").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - PV override",
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{Name: "missing-mem-factor-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters: NewSCConfigBuilder().
					WithoutParameter("fuseMemoryAllocatableFactor").
					WithoutParameter("fuseEphemeralStorageAllocatableFactor").
					WithoutParameter("fuseFileCacheMediumPriority").
					Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			pv: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: "csi.storage.gke.io",
							VolumeAttributes: map[string]string{
								"fuseMemoryAllocatableFactor":           "0.2",
								"fuseEphemeralStorageAllocatableFactor": "0.3",
								"fuseFileCacheMediumPriority":           "general_purpose:ram,gpu:ram,tpu:ram",
							},
						},
					},
				},
			},
			want: &scDetails{
				fileCacheMediumPriority: map[string][]string{
					"general_purpose": {"ram"},
					"gpu":             {"ram"},
					"tpu":             {"ram"},
				},
				fuseMemoryAllocatableFactor:           0.2,
				fuseEphemeralStorageAllocatableFactor: 0.3,
				volumeAttributes: map[string]string{
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
			name: "TestBuildSCDetails - Invalid fuseMemoryAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "invalid-mem-factor-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters:   NewSCConfigBuilder().WithParameter("fuseMemoryAllocatableFactor", "-0.1").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Missing fuseEphemeralStorageAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "missing-storage-factor-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters:   NewSCConfigBuilder().WithoutParameter("fuseEphemeralStorageAllocatableFactor").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Invalid fuseEphemeralStorageAllocatableFactor",
			sc: &storagev1.StorageClass{
				ObjectMeta:   metav1.ObjectMeta{Name: "invalid-storage-factor-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters:   NewSCConfigBuilder().WithParameter("fuseEphemeralStorageAllocatableFactor", "abc").Build().Parameters,
				MountOptions: defaultMountOptions,
			},
			wantErr: true,
		},
		{
			name: "TestBuildSCDetails - Only keep relevant volume attributes",
			sc: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{Name: "extra-params-sc", Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
				Parameters: map[string]string{
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
				volumeAttributes: map[string]string{
					"fileCacheCapacity": "10Gi",
				},
				mountOptions: defaultMountOptions,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildSCDetails(tt.pv, tt.sc, csiDriverVolumeAttributeKeys)
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

func TestBuildCacheRequirements(t *testing.T) {
	tests := []struct {
		name string
		pv   *pvDetails
		want *cacheRequirements
	}{
		{
			name: "Non-zero PV details - Should calculate all cache requirements",
			pv: &pvDetails{
				numObjects:     1000,
				totalSizeBytes: 10 * mib,
			},
			want: &cacheRequirements{
				metadataStatCacheBytes: 1000 * metadataStatCacheBytesPerObject,
				metadataTypeCacheBytes: 1000 * metadataTypeCacheBytesPerObject,
				fileCacheBytes:         10 * mib,
			},
		},
		{
			name: "Zero numObjects - Should result in zero metadata cache",
			pv: &pvDetails{
				numObjects:     0,
				totalSizeBytes: 5 * mib,
			},
			want: &cacheRequirements{
				metadataStatCacheBytes: 0,
				metadataTypeCacheBytes: 0,
				fileCacheBytes:         5 * mib,
			},
		},
		{
			name: "Zero totalSizeBytes - Should result in zero file cache",
			pv: &pvDetails{
				numObjects:     500,
				totalSizeBytes: 0,
			},
			want: &cacheRequirements{
				metadataStatCacheBytes: 500 * metadataStatCacheBytesPerObject,
				metadataTypeCacheBytes: 500 * metadataTypeCacheBytesPerObject,
				fileCacheBytes:         0,
			},
		},
		{
			name: "All zero PV details - Should result in all zero requirements",
			pv: &pvDetails{
				numObjects:     0,
				totalSizeBytes: 0,
			},
			want: &cacheRequirements{
				metadataStatCacheBytes: 0,
				metadataTypeCacheBytes: 0,
				fileCacheBytes:         0,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCacheRequirements(tt.pv)
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(cacheRequirements{})); diff != "" {
				t.Errorf("buildCacheRequirements(%v) returned unexpected diff (-want +got):\n%s", tt.pv, diff)
			}
		})
	}
}

func TestCalculateFuseResourceBudget(t *testing.T) {
	tests := []struct {
		name              string
		nodeAllocatable   int64
		allocatableFactor float64
		sidecarLimit      int64
		wantBudget        int64
	}{
		{
			name:              "Sidecar limit less than allocatable - Should use sidecar limit",
			nodeAllocatable:   1000,
			allocatableFactor: 0.5,
			sidecarLimit:      400,
			wantBudget:        200, // min(1000, 400) * 0.5 = 400 * 0.5
		},
		{
			name:              "Sidecar limit greater than allocatable - Should use node allocatable",
			nodeAllocatable:   1000,
			allocatableFactor: 0.5,
			sidecarLimit:      1200,
			wantBudget:        500, // min(1000, 1200) * 0.5 = 1000 * 0.5
		},
		{
			name:              "Sidecar limit is zero - Should use node allocatable",
			nodeAllocatable:   1000,
			allocatableFactor: 0.5,
			sidecarLimit:      0,
			wantBudget:        500, // nodeAllocatable * 0.5
		},
		{
			name:              "Factor is 1.0 - Should apply factor correctly",
			nodeAllocatable:   1000,
			allocatableFactor: 1.0,
			sidecarLimit:      600,
			wantBudget:        600, // min(1000, 600) * 1.0
		},
		{
			name:              "Factor is 0.0 - Should result in zero budget",
			nodeAllocatable:   1000,
			allocatableFactor: 0.0,
			sidecarLimit:      600,
			wantBudget:        0,
		},
		{
			name:              "Node allocatable is zero - Should result in zero budget",
			nodeAllocatable:   0,
			allocatableFactor: 0.5,
			sidecarLimit:      100,
			wantBudget:        0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateFuseResourceBudget(tt.nodeAllocatable, tt.allocatableFactor, tt.sidecarLimit)
			if got != tt.wantBudget {
				t.Errorf("calculateFuseResourceBudget(%d, %.2f, %d) = %d, want %d", tt.nodeAllocatable, tt.allocatableFactor, tt.sidecarLimit, got, tt.wantBudget)
			}
		})
	}
}

func TestCalculateResourceBudgets(t *testing.T) {
	tests := []struct {
		name          string
		config        *ProfileConfig
		wantMemory    int64
		wantEphemeral int64
	}{
		{
			name: "Standard case - Should calculate both budgets",
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           2 * 1024 * 1024 * 1024,  // 2Gi
						ephemeralStorageBytes: 20 * 1024 * 1024 * 1024, // 20Gi
					},
				},
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           0.7,
					fuseEphemeralStorageAllocatableFactor: 0.85,
				},
				podDetails: &podDetails{
					sidecarLimits: &parsedResourceList{
						memoryBytes:           1 * 1024 * 1024 * 1024,  // 1Gi
						ephemeralStorageBytes: 10 * 1024 * 1024 * 1024, // 10Gi
					},
				},
			},
			// Memory: min(2Gi, 1Gi) * 0.7 = 1Gi * 0.7 = 751619276
			wantMemory: 1073741824 * 7 / 10,
			// Ephemeral: min(20Gi, 10Gi) * 0.85 = 10Gi * 0.85 = 9126805504
			wantEphemeral: 10737418240 * 85 / 100,
		},
		{
			name: "Zero sidecar limits - Should base budgets on node allocatables",
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           2 * 1024 * 1024 * 1024,
						ephemeralStorageBytes: 20 * 1024 * 1024 * 1024,
					},
				},
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           0.5,
					fuseEphemeralStorageAllocatableFactor: 0.5,
				},
				podDetails: &podDetails{
					sidecarLimits: &parsedResourceList{
						memoryBytes:           0,
						ephemeralStorageBytes: 0,
					},
				},
			},
			// Memory: 2Gi * 0.5 = 1Gi = 1073741824
			wantMemory: 1073741824,
			// Ephemeral: 20Gi * 0.5 = 10Gi = 10737418240
			wantEphemeral: 10737418240,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMemory, gotEphemeral := calculateResourceBudgets(tt.config)
			if gotMemory != tt.wantMemory || gotEphemeral != tt.wantEphemeral {
				t.Errorf("calculateResourceBudgets() = (%d, %d), want (%d, %d) - Should match expected budgets", gotMemory, gotEphemeral, tt.wantMemory, tt.wantEphemeral)
			}
		})
	}
}

func TestRecommendMetadataCacheSize(t *testing.T) {
	config := &ProfileConfig{
		nodeDetails: &nodeDetails{name: "test-node"},
	}
	tests := []struct {
		name                string
		required            int64
		memoryBudget        int64
		wantRecommended     int64
		wantRemainingBudget int64
	}{
		{
			name:                "Required less than budget - Should return required size",
			required:            500,
			memoryBudget:        1000,
			wantRecommended:     500,
			wantRemainingBudget: 500,
		},
		{
			name:                "Required equals budget - Should return required size",
			required:            1000,
			memoryBudget:        1000,
			wantRecommended:     1000,
			wantRemainingBudget: 0,
		},
		{
			name:                "Required greater than budget - Should cap at budget",
			required:            1500,
			memoryBudget:        1000,
			wantRecommended:     1000,
			wantRemainingBudget: 0,
		},
		{
			name:                "Required is zero - Should return zero",
			required:            0,
			memoryBudget:        1000,
			wantRecommended:     0,
			wantRemainingBudget: 1000,
		},
		{
			name:                "Budget is zero - Should return zero",
			required:            500,
			memoryBudget:        0,
			wantRecommended:     0,
			wantRemainingBudget: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRecommended, gotRemainingBudget := recommendMetadataCacheSize(config, tt.required, tt.memoryBudget, "test")
			if gotRecommended != tt.wantRecommended || gotRemainingBudget != tt.wantRemainingBudget {
				t.Errorf("recommendMetadataCacheSize(%d, %d) = (%d, %d), want (%d, %d) - Should match recommended and remaining budgets", tt.required, tt.memoryBudget, gotRecommended, gotRemainingBudget, tt.wantRecommended, tt.wantRemainingBudget)
			}
		})
	}
}

func TestRecommendFileCacheSizeAndMedium(t *testing.T) {
	// Constants for easier reading
	fileCacheReq := 10 * mib // 10 MiB required

	tests := []struct {
		name                   string
		cacheRequirements      *cacheRequirements
		config                 *ProfileConfig
		memoryBudget           int64
		ephemeralStorageBudget int64
		wantSize               int64
		wantMedium             string
		wantErr                bool
	}{
		{
			name:              "GPU node, RAM priority, Sufficient RAM - Should recommend file cache size and medium to RAM",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU, hasLocalSSDEphemeralStorageAnnotation: true},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumRAM, util.MediumLSSD}}},
			},
			memoryBudget:           20 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               fileCacheReq,
			wantMedium:             util.MediumRAM,
		},
		{
			name:              "GPU node, RAM priority, Insufficient RAM, Sufficient LSSD - Should recommend file cache size and medium to LSSD",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU, hasLocalSSDEphemeralStorageAnnotation: true},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumRAM, util.MediumLSSD}}},
			},
			memoryBudget:           5 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               fileCacheReq,
			wantMedium:             util.MediumLSSD,
		},
		{
			name:              "GPU node, RAM priority, Insufficient RAM & LSSD - Should disable file cache",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU, hasLocalSSDEphemeralStorageAnnotation: true},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumRAM, util.MediumLSSD}}},
			},
			memoryBudget:           5 * mib,
			ephemeralStorageBudget: 5 * mib,
			wantSize:               0,
			wantMedium:             "",
		},
		{
			name:              "GPU node, RAM priority, Sufficient RAM, LSSD not annotated - Should recommend file cache size and medium to RAM",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU, hasLocalSSDEphemeralStorageAnnotation: false},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumRAM, util.MediumLSSD}}},
			},
			memoryBudget:           20 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               fileCacheReq,
			wantMedium:             util.MediumRAM,
		},
		{
			name:              "GPU node, LSSD priority, Sufficient LSSD - Should recommend file cache size and medium to LSSD",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU, hasLocalSSDEphemeralStorageAnnotation: true},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumLSSD, util.MediumRAM}}},
			},
			memoryBudget:           5 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               fileCacheReq,
			wantMedium:             util.MediumLSSD,
		},
		{
			name:              "GPU node, LSSD priority, LSSD not annotated, Sufficient RAM - Should recommend file cache size and medium to RAM",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU, hasLocalSSDEphemeralStorageAnnotation: false},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumLSSD, util.MediumRAM}}},
			},
			memoryBudget:           20 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               fileCacheReq,
			wantMedium:             util.MediumRAM,
		},
		{
			name:              "TPU node, RAM priority, Sufficient RAM - Should recommend file cache size and medium to RAM",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeTPU},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeTPU: {util.MediumRAM}}},
			},
			memoryBudget:           20 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               fileCacheReq,
			wantMedium:             util.MediumRAM,
		},
		{
			name:              "TPU node, RAM priority, Insufficient RAM - Should recommend file cache size and medium to RAM",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeTPU},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeTPU: {util.MediumRAM}}},
			},
			memoryBudget:           5 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               0,
			wantMedium:             "",
		},
		{
			name:              "TPU node, LSSD in priority - Should fallback to RAM",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeTPU},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeTPU: {util.MediumRAM, util.MediumLSSD}}},
			},
			memoryBudget:           20 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               fileCacheReq,
			wantMedium:             util.MediumRAM,
		},
		{
			name:              "File cache not required (0 bytes) - Should not recommend file cache",
			cacheRequirements: &cacheRequirements{fileCacheBytes: 0},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU, hasLocalSSDEphemeralStorageAnnotation: true},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumRAM, util.MediumLSSD}}},
			},
			memoryBudget:           20 * mib,
			ephemeralStorageBudget: 20 * mib,
			wantSize:               0,
			wantMedium:             "",
		},
		{
			name:              "Unknown Node Type - Should error",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: "unknown"},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumRAM}}},
			},
			wantErr: true,
		},
		{
			name:              "Unknown Medium in Priority - Should error",
			cacheRequirements: &cacheRequirements{fileCacheBytes: fileCacheReq},
			config: &ProfileConfig{
				nodeDetails: &nodeDetails{nodeType: nodeTypeGPU},
				scDetails:   &scDetails{fileCacheMediumPriority: map[string][]string{nodeTypeGPU: {util.MediumRAM, "invalid-medium"}}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSize, gotMedium, err := recommendFileCacheSizeAndMedium(tt.cacheRequirements, tt.config, tt.memoryBudget, tt.ephemeralStorageBudget)
			if (err != nil) != tt.wantErr {
				t.Fatalf("recommendFileCacheSizeAndMedium() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if gotSize != tt.wantSize || gotMedium != tt.wantMedium {
				t.Errorf("recommendFileCacheSizeAndMedium() = (%d, %q), want (%d, %q)", gotSize, gotMedium, tt.wantSize, tt.wantMedium)
			}
		})
	}
}

func TestRecommendCacheConfigs(t *testing.T) {
	// Constants for easier reading
	objPerStat := metadataStatCacheBytesPerObject
	objPerType := metadataTypeCacheBytesPerObject
	reqStat := 1000 * objPerStat // 1.5MiB
	reqType := 1000 * objPerType // 0.2MiB
	reqFile := 100 * mib         // 100 MiB

	// Example config components
	defaultPV := &pvDetails{numObjects: 1000, totalSizeBytes: reqFile}
	defaultSC := &scDetails{fuseMemoryAllocatableFactor: 1.0, fuseEphemeralStorageAllocatableFactor: 1.0}
	defaultPod := &podDetails{sidecarLimits: &parsedResourceList{memoryBytes: 0, ephemeralStorageBytes: 0}}
	defaultNode := &nodeDetails{nodeType: "general_purpose"}

	tests := []struct {
		name    string
		config  *ProfileConfig
		want    *recommendation
		wantErr bool
	}{
		{
			name: "Sufficient budget for all caches (GPU, RAM) - Should recommend RAM",
			config: &ProfileConfig{
				pvDetails: defaultPV,
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + reqFile,
						ephemeralStorageBytes: reqFile,
					},
					name: "test-gpu-node",
				},
				podDetails: defaultPod,
			},
			want: &recommendation{
				metadataStatCacheBytes: reqStat,
				metadataTypeCacheBytes: reqType,
				fileCacheBytes:         reqFile,
				fileCacheMedium:        util.MediumRAM,
			},
		},
		{
			name: "Sufficient budget (GPU, LSSD) - Should recommend LSSD",
			config: &ProfileConfig{
				pvDetails: defaultPV,
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumLSSD}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType,
						ephemeralStorageBytes: reqFile,
					},
					hasLocalSSDEphemeralStorageAnnotation: true,
					name:                                  "test-gpu-lssd-node",
				},
				podDetails: defaultPod,
			},
			want: &recommendation{
				metadataStatCacheBytes: reqStat,
				metadataTypeCacheBytes: reqType,
				fileCacheBytes:         reqFile,
				fileCacheMedium:        util.MediumLSSD,
			},
		},
		{
			name: "Limited memory caps stat cache (GPU, RAM) - Should cap stat cache",
			config: &ProfileConfig{
				pvDetails: defaultPV,
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           1 * mib, // Less than reqStat
						ephemeralStorageBytes: reqFile,
					},
					name: "test-gpu-node",
				},
				podDetails: defaultPod,
			},
			want: &recommendation{
				metadataStatCacheBytes: 1 * mib,
				metadataTypeCacheBytes: 0,
				fileCacheBytes:         0, // Not enough RAM for file cache after metadata
			},
		},
		{
			name: "Limited memory caps type cache (GPU, RAM) - Should recommend stat cache and cap type cache",
			config: &ProfileConfig{
				pvDetails: defaultPV,
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + (reqType / 2), // Enough for stat, half for type
						ephemeralStorageBytes: reqFile,
					},
					name: "test-gpu-node",
				},
				podDetails: defaultPod,
			},
			want: &recommendation{
				metadataStatCacheBytes: reqStat,
				metadataTypeCacheBytes: reqType / 2,
				fileCacheBytes:         0, // Not enough RAM for file cache
			},
		},
		{
			name: "Insufficient Ephemeral Storage for LSSD (GPU, LSSD priority) - Should disable file cache",
			config: &ProfileConfig{
				pvDetails: defaultPV,
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumLSSD}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + reqFile,
						ephemeralStorageBytes: reqFile / 2, // Insufficient for file cache
					},
					hasLocalSSDEphemeralStorageAnnotation: true,
					name:                                  "test-gpu-lssd-node",
				},
				podDetails: defaultPod,
			},
			want: &recommendation{
				metadataStatCacheBytes: reqStat,
				metadataTypeCacheBytes: reqType,
				fileCacheBytes:         0, // LSSD failed, RAM not in priority
			},
		},
		{
			name: "File cache bytes 0 - Should recommend zero file cache",
			config: &ProfileConfig{
				pvDetails: &pvDetails{numObjects: 1000, totalSizeBytes: 0},
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + reqFile,
						ephemeralStorageBytes: reqFile,
					},
					name: "test-gpu-node",
				},
				podDetails: defaultPod,
			},
			want: &recommendation{
				metadataStatCacheBytes: reqStat,
				metadataTypeCacheBytes: reqType,
				fileCacheBytes:         0,
				fileCacheMedium:        "",
			},
		},
		{
			name: "TPU node with LSSD in priority - Should fallback to RAM",
			config: &ProfileConfig{
				pvDetails: defaultPV,
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeTPU: {util.MediumRAM, util.MediumLSSD}}, // Invalid for TPU
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeTPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + reqFile,
						ephemeralStorageBytes: reqFile,
					},
					name: "test-tpu-node",
				},
				podDetails: defaultPod,
			},
			want: &recommendation{
				metadataStatCacheBytes: reqStat,
				metadataTypeCacheBytes: reqType,
				fileCacheBytes:         reqFile,
				fileCacheMedium:        util.MediumRAM,
			},
		},
		{
			name: "Unknown Node Type in SC Priority - Should error",
			config: &ProfileConfig{
				pvDetails: defaultPV,
				scDetails: &scDetails{
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{"unknown-type": {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU, // Does not match SC priority key
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + reqFile,
						ephemeralStorageBytes: reqFile,
					},
					name: "test-gpu-node",
				},
				podDetails: defaultPod,
			},
			wantErr: true,
		},
		{
			name:    "Nil pvDetails - Should return error",
			config:  &ProfileConfig{pvDetails: nil, scDetails: defaultSC, nodeDetails: defaultNode, podDetails: defaultPod},
			wantErr: true,
		},
		{
			name:    "Nil scDetails - Should return error",
			config:  &ProfileConfig{pvDetails: defaultPV, scDetails: nil, nodeDetails: defaultNode, podDetails: defaultPod},
			wantErr: true,
		},
		{
			name:    "Nil nodeDetails - Should return error",
			config:  &ProfileConfig{pvDetails: defaultPV, scDetails: defaultSC, nodeDetails: nil, podDetails: defaultPod},
			wantErr: true,
		},
		{
			name:    "Nil podDetails - Should return error",
			config:  &ProfileConfig{pvDetails: defaultPV, scDetails: defaultSC, nodeDetails: defaultNode, podDetails: nil},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recommendCacheConfigs(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("recommendCacheConfigs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(recommendation{})); diff != "" {
				t.Errorf("recommendCacheConfigs() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMergeRecommendedMountOptionsOnMissingKeys(t *testing.T) {
	// Constants for easier reading
	objPerStat := metadataStatCacheBytesPerObject
	objPerType := metadataTypeCacheBytesPerObject
	reqStat := 1000 * objPerStat // 1.5MiB -> 2 MiB
	reqType := 1000 * objPerType // 0.2MiB -> 1 MiB
	fileCacheSize := 100 * mib   // 100 MiB

	// Base config components
	basePV := &pvDetails{numObjects: 1000, totalSizeBytes: fileCacheSize}
	baseSC := &scDetails{
		mountOptions:                          []string{"implicit-dirs"},
		fuseMemoryAllocatableFactor:           1.0,
		fuseEphemeralStorageAllocatableFactor: 1.0,
		fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM, util.MediumLSSD}},
	}
	basePod := &podDetails{sidecarLimits: &parsedResourceList{memoryBytes: 0, ephemeralStorageBytes: 0}}
	baseNode := &nodeDetails{nodeType: "general_purpose"}

	tests := []struct {
		name             string
		config           *ProfileConfig
		wantOptions      []string
		wantErr          bool
		userMountOptions []string
	}{
		{
			name: "Sufficient memory for all caches (GPU, RAM) - Should recommend all caches to RAM",
			config: &ProfileConfig{
				pvDetails: basePV,
				scDetails: baseSC,
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + fileCacheSize,
						ephemeralStorageBytes: fileCacheSize,
					},
					name: "test-gpu-node",
				},
				podDetails: basePod,
			},
			wantOptions: []string{
				"implicit-dirs",
				"metadata-cache:stat-cache-max-size-mb:2",
				"metadata-cache:type-cache-max-size-mb:1",
				"file-cache:max-size-mb:100",
				"file-cache-medium=ram",
			},
		},
		{
			name: "Sufficient memory for LSSD only (GPU, LSSD) - Should recommend file cache to LSSD",
			config: &ProfileConfig{
				pvDetails: basePV,
				scDetails: baseSC,
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType, // Not enough RAM for file cache
						ephemeralStorageBytes: fileCacheSize,
					},
					hasLocalSSDEphemeralStorageAnnotation: true,
					name:                                  "test-gpu-lssd-node",
				},
				podDetails: basePod,
			},
			wantOptions: []string{
				"implicit-dirs",
				"metadata-cache:stat-cache-max-size-mb:2",
				"metadata-cache:type-cache-max-size-mb:1",
				"file-cache:max-size-mb:100",
				"file-cache-medium=lssd",
			},
		},
		{
			name: "Insufficient budget - Should not recommend file cache",
			config: &ProfileConfig{
				pvDetails: basePV,
				scDetails: baseSC,
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType,
						ephemeralStorageBytes: fileCacheSize / 2, // Insufficient LSSD
					},
					hasLocalSSDEphemeralStorageAnnotation: true,
					name:                                  "test-gpu-node",
				},
				podDetails: basePod,
			},
			wantOptions: []string{
				"file-cache:max-size-mb:0",
				"implicit-dirs",
				"metadata-cache:stat-cache-max-size-mb:2",
				"metadata-cache:type-cache-max-size-mb:1",
			},
		},
		{
			name: "Zero file cache required - Should not recommend file cache",
			config: &ProfileConfig{
				pvDetails: &pvDetails{numObjects: 1000, totalSizeBytes: 0},
				scDetails: baseSC,
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + fileCacheSize,
						ephemeralStorageBytes: fileCacheSize,
					},
					name: "test-gpu-node",
				},
				podDetails: basePod,
			},
			wantOptions: []string{
				"implicit-dirs",
				"file-cache:max-size-mb:0",
				"metadata-cache:stat-cache-max-size-mb:2",
				"metadata-cache:type-cache-max-size-mb:1",
			},
		},
		{
			name: "Zero stat cache required - Should disable stat cache",
			config: &ProfileConfig{
				pvDetails: &pvDetails{numObjects: 1000, totalSizeBytes: 0},
				scDetails: baseSC,
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           0,
						ephemeralStorageBytes: 0,
					},
					name: "test-gpu-node",
				},
				podDetails: basePod,
			},
			wantOptions: []string{
				"implicit-dirs",
				"file-cache:max-size-mb:0",
				"metadata-cache:stat-cache-max-size-mb:0",
				"metadata-cache:type-cache-max-size-mb:0",
			},
		},
		{
			name: "Zero type cache required - Should disable type cache",
			config: &ProfileConfig{
				pvDetails: &pvDetails{numObjects: 1000, totalSizeBytes: 0},
				scDetails: baseSC,
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat,
						ephemeralStorageBytes: 0,
					},
					name: "test-gpu-node",
				},
				podDetails: basePod,
			},
			wantOptions: []string{
				"implicit-dirs",
				"file-cache:max-size-mb:0",
				"metadata-cache:stat-cache-max-size-mb:2",
				"metadata-cache:type-cache-max-size-mb:0",
			},
		},
		{
			name: "Pre-existing mount options for file cache in config - Should not recommend cache configs",
			config: &ProfileConfig{
				pvDetails: basePV,
				scDetails: &scDetails{
					mountOptions:                          []string{"implicit-dirs", "file-cache:max-size-mb:50", "file-cache-medium=fake"},
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + fileCacheSize,
						ephemeralStorageBytes: fileCacheSize,
					},
					name: "test-gpu-node",
				},
				podDetails: basePod,
			},
			wantOptions: []string{
				"implicit-dirs",
				"file-cache:max-size-mb:50",
				"file-cache-medium=fake",
			},
		},
		{
			name: "Pre-existing mount options for metadata cache in user mount options - Should not recommend cache configs",
			config: &ProfileConfig{
				pvDetails: basePV,
				scDetails: &scDetails{
					mountOptions:                          []string{"implicit-dir"},
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + fileCacheSize,
						ephemeralStorageBytes: fileCacheSize,
					},
					name: "test-gpu-node",
				},
				podDetails: basePod,
			},
			userMountOptions: []string{"stat-cache-max-size-mb=2"},
			wantOptions: []string{
				"implicit-dir",
				"stat-cache-max-size-mb=2",
			},
		},
		{
			name: "Pre-existing cache in customer's pod spec - Should not recommend cache configs",
			config: &ProfileConfig{
				pvDetails: basePV,
				scDetails: &scDetails{
					mountOptions:                          []string{"implicit-dir"},
					fuseMemoryAllocatableFactor:           1.0,
					fuseEphemeralStorageAllocatableFactor: 1.0,
					fileCacheMediumPriority:               map[string][]string{nodeTypeGPU: {util.MediumRAM}},
				},
				nodeDetails: &nodeDetails{
					nodeType: nodeTypeGPU,
					nodeAllocatables: &parsedResourceList{
						memoryBytes:           reqStat + reqType + fileCacheSize,
						ephemeralStorageBytes: fileCacheSize,
					},
					name: "test-gpu-node",
				},
				podDetails: &podDetails{labels: map[string]string{"gke-gcsfuse/cache-created-by-user": "true"}},
			},
			wantOptions: []string{
				"implicit-dir",
				// No cache recommendations.
			},
		},
		{
			name: "Error from recommendCacheConfigs - Should propagate error",
			config: &ProfileConfig{
				pvDetails:   nil, // Causes error in recommendCacheConfigs
				scDetails:   baseSC,
				nodeDetails: baseNode,
				podDetails:  basePod,
			},
			wantOptions: nil,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.config.MergeRecommendedMountOptionsOnMissingKeys(tt.userMountOptions)
			if (err != nil) != tt.wantErr {
				t.Fatalf("MergeRecommendedMountOptionsOnMissingKeys() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			// Sort both slices for comparison
			sort.Strings(got)
			sort.Strings(tt.wantOptions)
			if diff := cmp.Diff(tt.wantOptions, got); diff != "" {
				t.Errorf("RecommendMountOptions() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIsValidMountOption(t *testing.T) {
	tests := []struct {
		name string
		opt  string
		want bool
	}{
		{"empty - should be invalid", "", false},
		{"key_only_a - should be valid", "a", true},
		{"key_only_a-b - should be valid", "a-b", true},
		{"key_only_a:b - should be valid", "a:b", true},
		{"a=b - should be valid", "a=b", true},
		{"a-b=c - should be valid", "a-b=c", true},
		{"a=b-c - should be valid", "a=b-c", true},
		{"a:b - should be valid", "a:b", true},
		{"a:b:c - should be valid", "a:b:c", true},
		{"a-b:c - should be valid", "a-b:c", true},
		{"a:b-c - should be valid", "a:b-c", true},
		{"a=b:c - should be valid", "a=b:c", true},

		{"a=b=c - should be invalid due to multiple equals", "a=b=c", false},
		{"a=b=c=d - should be invalid due to multiple equals", "a=b=c=d", false},

		{"=a - should be invalid because it starts with an equals", "=a", false},
		{"a= - should be invalid because it ends with an equals", "a=", false},

		{":a - should be invalid because it starts with colon", ":a", false},
		{"a: - should be invalid because it ends with colon", "a:", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidMountOption(tc.opt)
			if got != tc.want {
				t.Errorf("isValidMountOption(%q) = %v, want %v", tc.opt, got, tc.want)
			}
		})
	}
}

func TestGetMountOptionKey(t *testing.T) {
	tests := []struct {
		name string
		opt  string
		want string
	}{
		{"key_only_a - should extract entire string", "a", "a"},
		{"key_only_a-b - should extract entire string", "a-b", "a-b"},
		{"colon_a:b - should extract before last colon", "a:b", "a"},
		{"colon_a:b:c - should extract before last colon", "a:b:c", "a:b"},
		{"colon_a-b:c - should extract before last colon", "a-b:c", "a-b"},
		{"equals_a=b - should extract before first equals", "a=b", "a"},
		{"equals_a-b=c - should extract before first equals", "a-b=c", "a-b"},
		{"equals_a=b:c - should extract before first equals", "a=b:c", "a"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getMountOptionKey(tc.opt)
			if got != tc.want {
				t.Errorf("getMountOptionKey(%q) = %q, want %q", tc.opt, got, tc.want)
			}
		})
	}
}

func TestMergeMountOptionsOnMissingKeys(t *testing.T) {
	tests := []struct {
		name     string
		dstOpts  []string
		srcOpts  []string
		wantOpts []string
		wantErr  bool
	}{
		{
			name:     "empty src - should return dstOpts",
			dstOpts:  []string{"a=b"},
			srcOpts:  []string{},
			wantOpts: []string{"a=b"},
		},
		{
			name:     "empty dst - should return srcOpts",
			dstOpts:  []string{},
			srcOpts:  []string{"a=b"},
			wantOpts: []string{"a=b"},
		},
		{
			name:     "new key added - should append srcOpt",
			dstOpts:  []string{"key1=val1"},
			srcOpts:  []string{"key2=val2"},
			wantOpts: []string{"key1=val1", "key2=val2"},
		},
		{
			name:     "existing key not added - should skip srcOpt",
			dstOpts:  []string{"mykey=val1"},
			srcOpts:  []string{"mykey=val2"},
			wantOpts: []string{"mykey=val1"},
		},
		{
			name:     "case insensitive key - should treat keys as case insensitive",
			dstOpts:  []string{"MyKey=val1"},
			srcOpts:  []string{"mykey=val2"},
			wantOpts: []string{"MyKey=val1"},
		},
		{
			name:     "colon key in dst, different key in src - should add srcOpt",
			dstOpts:  []string{"a:b:c"},
			srcOpts:  []string{"b=d"},
			wantOpts: []string{"a:b:c", "b=d"},
		},
		{
			name:     "invalid src opts - should return InvalidArgument error",
			dstOpts:  []string{"valid=a"},
			srcOpts:  []string{"invalid=b=c"}, // Invalid
			wantOpts: nil,
			wantErr:  true,
		},
		{
			name:     "another invalid src opts - should return InvalidArgument error",
			dstOpts:  []string{"valid=a"},
			srcOpts:  []string{"valid2=b", ":bad"}, // Invalid ":bad"
			wantOpts: nil,
			wantErr:  true,
		},
		{
			name:     "invalid dst opts - should return InvalidArgument error",
			dstOpts:  []string{"valid=a", "bad=opt=x"}, // Invalid "bad:opt=x"
			srcOpts:  []string{"valid2=b"},
			wantOpts: nil,
			wantErr:  true,
		},
		{
			name:    "complex merge with only valid - should merge correctly",
			dstOpts: []string{"a=1", "b:c"},
			srcOpts: []string{"a=2", "b:d", "g=h"},
			// Valid dstOpts keys (lower): {"a": true, "b": true}
			// "a=2": Key "a" exists. Skip.
			// "b:d": Key "b" exists. Skip.
			// "g=h": Key "g" is new. Add "g=h".
			wantOpts: []string{"a=1", "b:c", "g=h"},
		},
		{
			name:     "complex merge with invalid in dst - should error",
			dstOpts:  []string{"a=1", "b:c", "d=e=f"}, // "d=e=f" is invalid
			srcOpts:  []string{"g=h"},
			wantOpts: nil,
			wantErr:  true,
		},
		{
			name:     "complex merge with invalid in src - should error",
			dstOpts:  []string{"a=1", "b:c"},
			srcOpts:  []string{"a=2", "b:d", "g=h", "b=c=x"}, // "b=c=x" is invalid
			wantOpts: nil,
			wantErr:  true,
		},
		{
			name:     "key overlap equals vs colon - should not add srcOpt",
			dstOpts:  []string{"a=b"}, // Key: "a", Normalized: "a"
			srcOpts:  []string{"a:c"}, // Key: "a", Normalized: "a"
			wantOpts: []string{"a=b"},
		},
		{
			name:     "colon key contains colon, no overlap with hyphen key - should add srcOpt",
			dstOpts:  []string{"key:key-sub:val1"},
			srcOpts:  []string{"key-sub=val2"},
			wantOpts: []string{"key:key-sub:val1", "key-sub=val2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotOpts, gotErr := mergeMountOptionsOnMissingKeys(tc.dstOpts, tc.srcOpts)

			if gotErr == nil && tc.wantErr {
				t.Errorf("mergeMountOptionsIfKeyUnset(%v, %v) got error %v, want error %v", tc.dstOpts, tc.srcOpts, gotErr, tc.wantErr)
			}
			if !reflect.DeepEqual(gotOpts, tc.wantOpts) {
				t.Errorf("mergeMountOptionsIfKeyUnset(%v, %v) got options \n%v, want \n%v", tc.dstOpts, tc.srcOpts, gotOpts, tc.wantOpts)
			}
		})
	}
}

func TestMergeMapsOnMissingKeys(t *testing.T) {
	tests := []struct {
		name string
		src  map[string]string
		dst  map[string]string
		want map[string]string
	}{
		{
			name: "dst_nil - Should return src",
			src:  map[string]string{"a": "1"},
			dst:  nil,
			want: map[string]string{"a": "1"},
		},
		{
			name: "src_nil - Should return dst",
			src:  nil,
			dst:  map[string]string{"b": "2"},
			want: map[string]string{"b": "2"},
		},
		{
			name: "both_nil - Should return nil",
			src:  nil,
			dst:  nil,
			want: nil,
		},
		{
			name: "dst_empty - Should return src elements",
			src:  map[string]string{"a": "1", "b": "2"},
			dst:  map[string]string{},
			want: map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "src_empty - Should return dst elements",
			src:  map[string]string{},
			dst:  map[string]string{"a": "1", "b": "2"},
			want: map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "no_overlap - Should merge all elements",
			src:  map[string]string{"a": "1"},
			dst:  map[string]string{"b": "2"},
			want: map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "overlap_exact_key - Should keep dst value",
			src:  map[string]string{"a": "1", "b": "new"},
			dst:  map[string]string{"b": "2", "c": "3"},
			want: map[string]string{"a": "1", "b": "2", "c": "3"},
		},
		{
			name: "overlap_case_insensitive - Should keep dst value based on case-insensitive key",
			src:  map[string]string{"A": "1", "B": "new"},
			dst:  map[string]string{"a": "2", "c": "3"},
			want: map[string]string{"a": "2", "B": "new", "c": "3"},
		},
		{
			name: "mixed_overlap_and_case - Should merge non-conflicting keys",
			src:  map[string]string{"Key1": "src1", "Key2": "src2", "Key3": "src3"},
			dst:  map[string]string{"key1": "dst1", "KEY2": "dst2", "Other": "dstOther"},
			want: map[string]string{"key1": "dst1", "KEY2": "dst2", "Key3": "src3", "Other": "dstOther"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dstOriginal := make(map[string]string)
			if tc.dst != nil {
				for k, v := range tc.dst {
					dstOriginal[k] = v
				}
			}

			got := mergeMapsOnMissingKeys(tc.dst, tc.src)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mergeMapsOnMissingKeys(%v, %v) returned diff (-want +got):\n%s", tc.src, dstOriginal, diff)
			}

			// Verify dst was not modified in place
			if tc.dst != nil {
				if diff := cmp.Diff(dstOriginal, tc.dst); diff != "" {
					t.Errorf("mergeMapsOnMissingKeys(%v, %v) unexpectedly modified dst in place:\n%s", tc.src, dstOriginal, diff)
				}
			}
		})
	}
}
