/*
Copyright 2018 The Kubernetes Authors.
Copyright 2026 Google LLC

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
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSharedMount(t *testing.T) {
	testCases := []struct {
		name          string
		volumeContext map[string]string
		expected      bool
	}{
		{
			name:          "sharedMount true - should return true",
			volumeContext: map[string]string{"sharedMount": "true"},
			expected:      true,
		},
		{
			name:          "sharedMount false - should return false",
			volumeContext: map[string]string{"sharedMount": "false"},
			expected:      false,
		},
		{
			name:          "sharedMount missing - should return false",
			volumeContext: map[string]string{},
			expected:      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vc := tc.volumeContext
			result := sharedMount(vc)
			if result != tc.expected {
				t.Errorf("Expected %v, but got %v", tc.expected, result)
			}
		})
	}
}

func TestCreateMounterPodName(t *testing.T) {
	testCases := []struct {
		name     string
		nodeID   string
		volumeID string
		expected string
	}{
		{
			name:     "Basic test - should generate name correctly",
			nodeID:   testNodeID,
			volumeID: testVolumeID,
			expected: fmt.Sprintf("%s-f4d1ad31ce3ffcfcada13d5fe95e0f8ddc801bf7", mounterPodNamePrefix),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := createMounterPodName(tc.nodeID, tc.volumeID)
			// Verify that the generated name matches the expected name
			if actual != tc.expected {
				t.Errorf("Expected pod name %q, but got %q", tc.expected, actual)
			}
			// Verify that the generated name contains the prefix and the hash
			if len(actual) <= len(mounterPodNamePrefix) || actual[:len(mounterPodNamePrefix)] != mounterPodNamePrefix {
				t.Errorf("Expected pod name to start with %q, but got %q", mounterPodNamePrefix, actual)
			}
			if len(actual) != len(mounterPodNamePrefix+"-")+40 { // SHA1 hash is 40 characters long
				t.Errorf("Expected pod name to have a 40-character SHA1 hash, but got %q", actual)
			}
		})
	}
}

func TestPVFromVolumeID(t *testing.T) {
	// Helper to create the expected *corev1.PersistentVolume object for comparison
	makeExpectedPV := func(name, volumeHandle string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
	}

	testCases := []struct {
		name      string
		volumeID  string
		setupFake func() *clientset.FakeClientset
		wantPV    *corev1.PersistentVolume
		wantErr   bool
	}{
		{
			name:     "PV found - should return it",
			volumeID: "csi-vol-001",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: "pv1", VolumeHandle: "csi-vol-000"})
				fc.CreatePV(clientset.FakePVConfig{Name: "pv2", VolumeHandle: "csi-vol-001"})
				fc.CreatePV(clientset.FakePVConfig{Name: "pv3", VolumeHandle: "csi-vol-002"})
				return fc
			},
			wantPV:  makeExpectedPV("pv2", "csi-vol-001"),
			wantErr: false,
		},
		{
			name:     "PV not found - should return error",
			volumeID: "csi-vol-999",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: "pv1", VolumeHandle: "csi-vol-000"})
				fc.CreatePV(clientset.FakePVConfig{Name: "pv2", VolumeHandle: "csi-vol-001"})
				return fc
			},
			wantErr: true,
		},
		{
			name:     "ListPV returns error - should return error",
			volumeID: "csi-vol-001",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.ListPVErr = errors.New("failed to list PVs from apiserver")
				return fc
			},
			wantErr: true,
		},
		{
			name:     "Empty PV list - should return error",
			volumeID: "csi-vol-001",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				// No PVs created
				return fc
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCS := tc.setupFake()

			gotPV, err := pvFromVolumeID(fakeCS, tc.volumeID)

			if tc.wantErr {
				if err == nil {
					t.Errorf("pvFromVolumeID(%q) succeeded unexpectedly, want error", tc.volumeID)
				}
				return
			}

			if err != nil {
				t.Fatalf("pvFromVolumeID(%q) returned an unexpected error: %v", tc.volumeID, err)
			}

			if diff := cmp.Diff(tc.wantPV, gotPV); diff != "" {
				t.Errorf("pvFromVolumeID(%q) returned unexpected diff (-want +got):\n%s", tc.volumeID, diff)
			}
		})
	}
}
