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
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testVolumeID           = "test-volume-id"
	testNodeID             = "test-node-id"
	testPV                 = "test-pv"
	testPVC                = "test-pvc"
	testMounterPodTemplate = "test-mounter-pod-template"
	testNamespace          = "test-namespace"
	testPod                = "test-pod"
	testImage              = "k8s.gcr.io/pause"
)

func initTestController(t *testing.T, clientset clientset.Interface) csi.ControllerServer {
	t.Helper()
	driver := initTestDriver(t, nil, clientset)
	cs, err := newControllerServer(driver, driver.config.StorageServiceManager, driver.config.FeatureOptions)
	if err != nil {
		t.Fatalf("newControllerServer failed: %v", err)
	}
	driver.config.FeatureOptions.FeatureGCSFuseProfiles.Enabled = true
	return cs
}

type fakeClientsetConfig struct {
	pvConfig  *clientset.FakePVConfig
	pvcConfig *clientset.FakePVCConfig
	ptConfig  *clientset.FakePodTemplateConfig
	podConfig *clientset.FakePodConfig
	scConfig  *clientset.FakeSCConfig
	cmConfig  *clientset.FakeConfigMapConfig
}

func setupFakeBase(cfg fakeClientsetConfig) *clientset.FakeClientset {
	fc := clientset.NewFakeClientset()
	if cfg.pvConfig != nil {
		fc.CreatePV(*cfg.pvConfig)
	}
	if cfg.pvcConfig != nil {
		fc.CreatePVC(*cfg.pvcConfig)
	}
	if cfg.ptConfig != nil {
		fc.CreatePodTemplate(*cfg.ptConfig)
	}
	if cfg.podConfig != nil {
		fc.CreatePod(*cfg.podConfig)
	}
	if cfg.scConfig != nil {
		fc.CreateSC(*cfg.scConfig)
	}
	if cfg.cmConfig != nil {
		fc.CreateConfigMap(*cfg.cmConfig)
	}
	return fc
}

func getDefaultFakeClientsetConfig() fakeClientsetConfig {
	return fakeClientsetConfig{
		pvConfig: &clientset.FakePVConfig{
			Name:         testPV,
			VolumeHandle: testVolumeID,
			ClaimRef: &corev1.ObjectReference{
				Name:      testPVC,
				Namespace: testNamespace,
			},
		},
		pvcConfig: &clientset.FakePVCConfig{
			Name:      testPVC,
			Namespace: testNamespace,
			Annotations: map[string]string{
				"gke-gcsfuse/mounter-pod-template": testMounterPodTemplate,
			},
		},
		ptConfig: &clientset.FakePodTemplateConfig{
			Name:      testMounterPodTemplate,
			Namespace: testNamespace,
		},
		cmConfig: &clientset.FakeConfigMapConfig{
			Name:      util.SidecarImageConfigMapName,
			Namespace: "gcs-fuse-csi-driver",
			Data: map[string]string{
				util.SidecarImageConfigMapKey: testImage,
			},
		},
	}
}

func TestCreateVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.CreateVolumeRequest
		resp      *csi.CreateVolumeResponse
		expectErr error
	}{
		{
			name: "valid defaults",
			req: &csi.CreateVolumeRequest{
				Name: testVolumeID,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Secrets: map[string]string{
					"projectID":               "test-project",
					"serviceAccountName":      "test-sa-name",
					"serviceAccountNamespace": "test-sa-namespace",
				},
			},
			resp: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					CapacityBytes: 1 * util.Mb,
					VolumeId:      testVolumeID,
				},
			},
		},
		{
			name: "empty name",
			req: &csi.CreateVolumeRequest{
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "CreateVolume name must be provided"),
		},
	}

	for _, test := range cases {
		cs := initTestController(t, clientset.NewFakeClientset())
		resp, err := cs.CreateVolume(context.TODO(), test.req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}
		if !reflect.DeepEqual(resp, test.resp) {
			t.Errorf("test %q failed:\ngot resp %+v,\nexpected resp %+v", test.name, resp, test.resp)
		}
	}
}

func TestDeleteVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.DeleteVolumeRequest
		resp      *csi.DeleteVolumeResponse
		expectErr error
	}{
		{
			name: "valid",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeID,
				Secrets: map[string]string{
					"projectID":               "test-project",
					"serviceAccountName":      "test-sa-name",
					"serviceAccountNamespace": "test-sa-namespace",
				},
			},
			resp: &csi.DeleteVolumeResponse{},
		},
		{
			name: "invalid id",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "abc",
				Secrets: map[string]string{
					"projectID":               "test-project",
					"serviceAccountName":      "test-sa-name",
					"serviceAccountNamespace": "test-sa-namespace",
				},
			},
			resp: &csi.DeleteVolumeResponse{},
		},
		{
			name:      "empty id",
			req:       &csi.DeleteVolumeRequest{},
			expectErr: status.Error(codes.InvalidArgument, "DeleteVolume volumeID must be provided"),
		},
	}

	for _, test := range cases {
		cs := initTestController(t, clientset.NewFakeClientset())
		resp, err := cs.DeleteVolume(context.TODO(), test.req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}
		if !reflect.DeepEqual(resp, test.resp) {
			t.Errorf("test %q failed:\ngot resp %+v,\nexpected resp %+v", test.name, resp, test.resp)
		}
	}
}

func TestControllerPublishVolume(t *testing.T) {
	oldInterval := mounterPodPollInterval
	mounterPodPollInterval = 10 * time.Millisecond
	defer func() { mounterPodPollInterval = oldInterval }()

	cases := []struct {
		name               string
		req                *csi.ControllerPublishVolumeRequest
		setupFake          func() *clientset.FakeClientset
		podGetErr          error
		podCreateErr       error
		expectErr          bool
		expectErrCode      codes.Code
		podTemplateGetErr  error
		wantPublishContext map[string]string
		verifyCreatedPod   func(t *testing.T, pod *corev1.Pod)
	}{
		{
			name: "empty volume ID - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         "",
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "empty node ID - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "",
				VolumeCapability: testVolumeCapability,
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "empty volume capabilities - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: nil,
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing sharedMount - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    map[string]string{},
			},
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: false,
		},
		{
			name: "sharedMount false - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "false",
				},
			},
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: false,
		},
		{
			name: "sharedMount true - no pv found - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - claimRef nil - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.pvConfig.ClaimRef = nil
				cfg.pvcConfig = nil
				cfg.ptConfig = nil
				return setupFakeBase(cfg)
			},
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - mounter pod created successfully - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				return setupFakeBase(getDefaultFakeClientsetConfig())
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
		},
		{
			name: "sharedMount true - mounter pod with resource overrides - should create pod with overridden resources",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				// Modify the PodTemplate to include specific resources
				cfg.ptConfig.Template = corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  util.MounterPodNamePrefix,
								Image: mounterPodManagedImageKeyword,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("150m"),
										corev1.ResourceMemory: resource.MustParse("128Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("300m"),
										corev1.ResourceMemory: resource.MustParse("256Mi"),
									},
								},
							},
						},
					},
				}
				return setupFakeBase(cfg)
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				if len(pod.Spec.Containers) != 1 {
					t.Fatalf("Expected 1 container in created pod, got %d", len(pod.Spec.Containers))
				}
				container := pod.Spec.Containers[0]
				if container.Name != util.MounterPodNamePrefix {
					t.Errorf("Expected container name %q, got %q", util.MounterPodNamePrefix, container.Name)
				}

				expectedResources := corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:              resource.MustParse("150m"),
						corev1.ResourceMemory:           resource.MustParse("128Mi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("15Gi"), // Default
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("300m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				}

				if !reflect.DeepEqual(container.Resources, expectedResources) {
					t.Errorf("Mounter pod resources do not match expected overrides.\nGot: %+v\nWant: %+v", container.Resources, expectedResources)
				}

				// Default image set for testing when no custom image requested by user.
				expectedImage := testImage

				if !reflect.DeepEqual(container.Image, expectedImage) {
					t.Errorf("Mounter pod image does not match expected overrides.\nGot: %+v\nWant: %+v", container.Image, expectedImage)
				}
			},
		},
		{
			name: "sharedMount true - mounter pod with image override - should create pod with overridden image",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				// Modify the PodTemplate to include specific resources
				cfg.ptConfig.Template = corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  util.MounterPodNamePrefix,
								Image: "gcr.io/some-project/some-random-image/v1.2.3",
							},
						},
					},
				}
				return setupFakeBase(cfg)
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				if len(pod.Spec.Containers) != 1 {
					t.Fatalf("Expected 1 container in created pod, got %d", len(pod.Spec.Containers))
				}
				container := pod.Spec.Containers[0]
				if container.Name != util.MounterPodNamePrefix {
					t.Errorf("Expected container name %q, got %q", util.MounterPodNamePrefix, container.Name)
				}

				expectedImage := "gcr.io/some-project/some-random-image/v1.2.3"

				if !reflect.DeepEqual(container.Image, expectedImage) {
					t.Errorf("Mounter pod image does not match expected overrides.\nGot: %+v\nWant: %+v", container.Image, expectedImage)
				}
			},
		},
		{
			name: "sharedMount true - sidecar image from ConfigMap - should create pod with image from ConfigMap",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				fakeClient := setupFakeBase(cfg)
				fakeClient.CreateConfigMap(clientset.FakeConfigMapConfig{
					Name:      util.SidecarImageConfigMapName,
					Namespace: "gcs-fuse-csi-driver",
					Data: map[string]string{
						util.SidecarImageConfigMapKey: "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.0.0-from-configmap",
					},
				})
				return fakeClient
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				if len(pod.Spec.Containers) != 1 {
					t.Fatalf("Expected 1 container in created pod, got %d", len(pod.Spec.Containers))
				}
				container := pod.Spec.Containers[0]
				expectedImage := "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v1.0.0-from-configmap"
				if container.Image != expectedImage {
					t.Errorf("Expected image %q, got %q", expectedImage, container.Image)
				}
			},
		},
		{
			name: "sharedMount true - mounter pod with dnsPolicy override - should create pod with overridden dnsPolicy",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.ptConfig.Template.Spec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
				return setupFakeBase(cfg)
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				if pod.Spec.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
					t.Errorf("Expected DNSPolicy %q, got %q", corev1.DNSClusterFirstWithHostNet, pod.Spec.DNSPolicy)
				}
			},
		},
		{
			name: "sharedMount true - hostNetwork true with GKE IDP - should set SA token volume with identity pool audience",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":                     "true",
					util.VolumeContextKeyPVName:       testPV,
					VolumeContextKeyHostNetworkPodKSA: "true",
					VolumeContextKeyIdentityProvider:  "https://container.googleapis.com/v1/projects/my-proj/locations/us-central1/clusters/my-cluster",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				return setupFakeBase(getDefaultFakeClientsetConfig())
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				wantVolume := webhook.GetSATokenVolume("fake.identity.pool")
				found := false
				for _, v := range pod.Spec.Volumes {
					if v.Name == wantVolume.Name {
						found = true
						if !reflect.DeepEqual(v, wantVolume) {
							t.Errorf("SA token volume mismatch.\nGot: %+v\nWant: %+v", v, wantVolume)
						}
					}
				}
				if !found {
					t.Errorf("SA token volume %q not found in pod volumes", wantVolume.Name)
				}
			},
		},
		{
			name: "sharedMount true - hostNetwork true with custom IDP - should set SA token volume with custom IDP audience",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":                     "true",
					util.VolumeContextKeyPVName:       testPV,
					VolumeContextKeyHostNetworkPodKSA: "true",
					VolumeContextKeyIdentityProvider:  "https://custom.identity.provider",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				return setupFakeBase(getDefaultFakeClientsetConfig())
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				wantVolume := webhook.GetSATokenVolume("https://custom.identity.provider")
				found := false
				for _, v := range pod.Spec.Volumes {
					if v.Name == wantVolume.Name {
						found = true
						if !reflect.DeepEqual(v, wantVolume) {
							t.Errorf("SA token volume mismatch.\nGot: %+v\nWant: %+v", v, wantVolume)
						}
					}
				}
				if !found {
					t.Errorf("SA token volume %q not found in pod volumes", wantVolume.Name)
				}
			},
		},
		{
			name: "sharedMount true - hostNetwork true with empty IDP - should default SA token volume with identity pool audience",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":                     "true",
					util.VolumeContextKeyPVName:       testPV,
					VolumeContextKeyHostNetworkPodKSA: "true",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				return setupFakeBase(getDefaultFakeClientsetConfig())
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				wantVolume := webhook.GetSATokenVolume("fake.identity.pool")
				found := false
				for _, v := range pod.Spec.Volumes {
					if v.Name == wantVolume.Name {
						found = true
						if !reflect.DeepEqual(v, wantVolume) {
							t.Errorf("SA token volume mismatch.\nGot: %+v\nWant: %+v", v, wantVolume)
						}
					}
				}
				if !found {
					t.Errorf("SA token volume %q not found in pod volumes", wantVolume.Name)
				}
			},
		},

		{
			name: "sharedMount true - missing mounter pod template annotation - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.pvcConfig.Annotations = nil
				return setupFakeBase(cfg)
			},
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "sharedMount true - pod template doesn't exist - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.pvcConfig.Annotations["gke-gcsfuse/mounter-pod-template"] = "non-existent-template"
				return setupFakeBase(cfg)
			},
			expectErr:         true,
			expectErrCode:     codes.NotFound,
			podTemplateGetErr: apierrors.NewNotFound(schema.GroupResource{}, "pod template not found"),
		},
		{
			name: "sharedMount true - pod template get internal error - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.pvcConfig.Annotations["gke-gcsfuse/mounter-pod-template"] = "non-existent-template"
				return setupFakeBase(cfg)
			},
			expectErr:         true,
			expectErrCode:     codes.Internal,
			podTemplateGetErr: apierrors.NewInternalError(errors.New("internal error")),
		},
		{
			name: "sharedMount true - mounter pod already exists - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.podConfig = &clientset.FakePodConfig{
					NodeName: testNodeID,
					PodStatus: &corev1.PodStatus{
						Conditions: []corev1.PodCondition{
							{
								Type:   corev1.PodScheduled,
								Status: corev1.ConditionTrue,
							},
						},
					},
				}
				return setupFakeBase(cfg)
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			expectErr: false,
		},
		{
			name: "sharedMount true - mounter pod being deleted - should return Aborted error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.podConfig = &clientset.FakePodConfig{
					NodeName: testNodeID,
					PodStatus: &corev1.PodStatus{
						Conditions: []corev1.PodCondition{
							{
								Type:   corev1.PodScheduled,
								Status: corev1.ConditionTrue,
							},
						},
					},
					DeletionTimestamp: &metav1.Time{},
				}
				return setupFakeBase(cfg)
			},
			expectErr:     true,
			expectErrCode: codes.Aborted,
		},
		{
			name: "sharedMount true - mounter pod get error - should return Internal error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				return setupFakeBase(getDefaultFakeClientsetConfig())
			},
			podGetErr:     errors.New("simulated get error"),
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - mounter pod create error - should return Internal error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				return setupFakeBase(getDefaultFakeClientsetConfig())
			},
			podGetErr:     apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			podCreateErr:  errors.New("simulated create error"),
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - missing pv name in volume context - should return Internal",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - profiles feature enabled globally, storage class has profile label - should create pod with profile volumes",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount":               "true",
					util.VolumeContextKeyPVName: testPV,
				},
			},
			setupFake: func() *clientset.FakeClientset {
				cfg := getDefaultFakeClientsetConfig()
				cfg.pvConfig.SCName = "profile-sc"
				cfg.scConfig = &clientset.FakeSCConfig{
					Labels: map[string]string{
						"gke-gcsfuse/profile": "true",
					},
				}
				return setupFakeBase(cfg)
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			podGetErr: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, ""),
			expectErr: false,
			verifyCreatedPod: func(t *testing.T, pod *corev1.Pod) {
				// The profile volumes must be injected if profilesEnabled was true
				hasEphemeralVolume := webhook.VolumeExists(pod.Spec.Volumes, webhook.SidecarContainerFileCacheEphemeralDiskVolumeName)
				hasRamVolume := webhook.VolumeExists(pod.Spec.Volumes, webhook.SidecarContainerFileCacheRamDiskVolumeName)
				if !hasEphemeralVolume || !hasRamVolume {
					t.Errorf("Expected profile volumes to be injected, got: ephemeral=%t, ram=%t", hasEphemeralVolume, hasRamVolume)
				}
				container := pod.Spec.Containers[0]
				hasEphemeralMount := webhook.VolumeMountExists(container.VolumeMounts, webhook.SidecarContainerFileCacheEphemeralDiskVolumeName)
				hasRamMount := webhook.VolumeMountExists(container.VolumeMounts, webhook.SidecarContainerFileCacheRamDiskVolumeName)
				if !hasEphemeralMount || !hasRamMount {
					t.Errorf("Expected profile volume mounts to be injected, got: ephemeral=%t, ram=%t", hasEphemeralMount, hasRamMount)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fc := test.setupFake()
			s := initTestController(t, fc)

			fakeK8sClient := fc.K8sClient().(*fake.Clientset)
			var createdPod *corev1.Pod

			if test.podGetErr != nil {
				fc.GetPodErr = test.podGetErr
			}
			if test.podCreateErr != nil {
				fakeK8sClient.Fake.PrependReactor("create", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, test.podCreateErr
				})
			} else {
				fakeK8sClient.Fake.PrependReactor("create", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					createAction := action.(k8stesting.CreateAction)
					pod := createAction.GetObject().(*corev1.Pod)
					createdPod = pod.DeepCopy() // Capture the pod spec for verification

					// Simulate successful creation and scheduling
					pod.Spec.NodeName = testNodeID
					pod.Status.Conditions = []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					}

					fc.CreatePod(clientset.FakePodConfig{
						Name:      pod.Name,
						Namespace: pod.Namespace,
						NodeName:  testNodeID,
						PodStatus: &pod.Status,
					})

					if apierrors.IsNotFound(fc.GetPodErr) {
						fc.GetPodErr = nil // Clear any previous GetPodErr to simulate successful retrieval after creation
					}
					return false, pod, nil // Return false to allow default fake client handling to store the object
				})
			}

			if test.podTemplateGetErr != nil {
				fc.GetPodTemplateErr = test.podTemplateGetErr
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := s.ControllerPublishVolume(ctx, test.req)

			if test.expectErr {
				if err == nil {
					t.Fatalf("Expected error code %v, got nil", test.expectErrCode)
				}
				st, ok := status.FromError(err)
				if !ok {
					if test.expectErrCode != codes.Unknown {
						t.Fatalf("Expected status error with code %v, got non-status error: %v", test.expectErrCode, err)
					}
				} else if st.Code() != test.expectErrCode {
					t.Errorf("Expected error code %v, got %v (error: %v)", test.expectErrCode, st.Code(), err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
				if !reflect.DeepEqual(test.wantPublishContext, resp.PublishContext) {
					t.Errorf("Expected publish context: %v, got: %v", test.wantPublishContext, resp.PublishContext)
				}
				if test.verifyCreatedPod != nil {
					if createdPod == nil {
						t.Fatalf("Expected pod to be created, but it was not captured")
					}
					test.verifyCreatedPod(t, createdPod)
				}
			}
		})
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	oldInterval := mounterPodPollInterval
	mounterPodPollInterval = 10 * time.Millisecond
	defer func() { mounterPodPollInterval = oldInterval }()

	mounterPodName := createMounterPodName(testNodeID, testVolumeID)

	testCases := []struct {
		name string
		req  *csi.ControllerUnpublishVolumeRequest

		initialFCPods        []clientset.FakePodConfig
		listPodErr           error
		deleteReactionError  error // Injected error for the Delete call
		getPodErrAfterDelete error // Error for clientset.GetPod to return after Delete is called

		wantErr     bool
		wantErrCode codes.Code
	}{
		{
			name: "valid request - should delete pod successfully",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID,
				NodeId:   testNodeID,
			},
			initialFCPods: []clientset.FakePodConfig{
				{
					Name:      mounterPodName,
					Namespace: testNamespace,
				},
			},
			getPodErrAfterDelete: apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, mounterPodName),
			wantErr:              false,
		},
		{
			name: "pod not found - should succeed",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID,
				NodeId:   testNodeID,
			},
			initialFCPods: nil, // No mounter pod
			wantErr:       false,
		},
		{
			name: "empty volume ID - should return error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "",
				NodeId:   testNodeID,
			},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name: "empty node ID - should return error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID,
				NodeId:   "",
			},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name: "list pods fails - should return error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID,
				NodeId:   testNodeID,
			},
			listPodErr:  errors.New("simulated ListPods error"),
			wantErr:     true,
			wantErrCode: codes.Internal,
		},
		{
			name: "multiple mounter pods found - should return error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID,
				NodeId:   testNodeID,
			},
			initialFCPods: []clientset.FakePodConfig{
				{
					Name:      mounterPodName,
					Namespace: testNamespace,
				},
				{
					Name:      mounterPodName,
					Namespace: "another-namespace",
				},
			},
			wantErr:     true,
			wantErrCode: codes.Internal,
		},
		{
			name: "deleteMounterPod fails with API error - should return error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID,
				NodeId:   testNodeID,
			},
			initialFCPods: []clientset.FakePodConfig{
				{
					Name:      mounterPodName,
					Namespace: testNamespace,
				},
			},
			deleteReactionError: errors.New("simulated delete API error"),
			wantErr:             true,
			wantErrCode:         codes.Internal,
		},
		{
			name: "deleteMounterPod times out (pod never deleted) - should return error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID,
				NodeId:   testNodeID,
			},
			initialFCPods: []clientset.FakePodConfig{
				{
					Name:      mounterPodName,
					Namespace: testNamespace,
				},
			},
			getPodErrAfterDelete: nil, // GetPod continues to find the pod
			wantErr:              true,
			wantErrCode:          codes.DeadlineExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fc := clientset.NewFakeClientset()
			fc.ListPodErr = tc.listPodErr
			for _, pod := range tc.initialFCPods {
				fc.CreatePod(pod)
			}

			s := initTestController(t, fc)

			fakeK8sClient := fc.K8sClient().(*fake.Clientset)
			deleteCalled := false
			fakeK8sClient.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
				deleteCalled = true
				if tc.deleteReactionError != nil {
					return true, nil, tc.deleteReactionError
				}
				// Simulate effect on GetPod for the poller in deleteMounterPod
				fc.GetPodErr = tc.getPodErrAfterDelete
				return true, nil, nil
			})

			// Initial GetPodErr state for deleteMounterPod if delete is not called or fails early
			if len(tc.initialFCPods) == 0 {
				fc.GetPodErr = apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, mounterPodName)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, err := s.ControllerUnpublishVolume(ctx, tc.req)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Expected error code %v, got nil", tc.wantErrCode)
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("Expected status error with code %v, got non-status error: %v", tc.wantErrCode, err)
				}
				if st.Code() != tc.wantErrCode {
					t.Errorf("Expected error code %v, got %v (error: %v)", tc.wantErrCode, st.Code(), err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
				// Verify delete was called if a pod was expected to be found
				if len(tc.initialFCPods) > 0 && !deleteCalled && tc.deleteReactionError == nil {
					t.Errorf("Delete was not called on the mounter pod when it should have been")
				}
			}
		})
	}
}
