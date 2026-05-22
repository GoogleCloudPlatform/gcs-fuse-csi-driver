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

package webhook

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetNativePrefetchOverrides(t *testing.T) {
	t.Parallel()

	const namespace = "test-namespace"

	testCases := []struct {
		name         string
		volumes      []corev1.Volume
		pvcs         []*corev1.PersistentVolumeClaim
		pvs          []*corev1.PersistentVolume
		wantEnabled  bool
		wantDisabled bool
	}{
		{
			name: "unspecified/no overrides",
			volumes: []corev1.Volume{
				{
					Name: "gcsfuse-inline",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           gcsFuseCsiDriverName,
							VolumeAttributes: map[string]string{"mountOptions": "rw,noatime"},
						},
					},
				},
			},
			wantEnabled:  false,
			wantDisabled: false,
		},
		{
			name: "inline csi: enable-metadata-prefetch=true",
			volumes: []corev1.Volume{
				{
					Name: "gcsfuse-inline",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           gcsFuseCsiDriverName,
							VolumeAttributes: map[string]string{"mountOptions": "metadata-cache:enable-metadata-prefetch:true"},
						},
					},
				},
			},
			wantEnabled:  true,
			wantDisabled: false,
		},
		{
			name: "inline csi: enable-metadata-prefetch=false",
			volumes: []corev1.Volume{
				{
					Name: "gcsfuse-inline",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           gcsFuseCsiDriverName,
							VolumeAttributes: map[string]string{"mountOptions": "metadata-cache:enable-metadata-prefetch:false"},
						},
					},
				},
			},
			wantEnabled:  false,
			wantDisabled: true,
		},
		{
			name: "multiple inline volumes: conflict (one true, one false)",
			volumes: []corev1.Volume{
				{
					Name: "gcsfuse-inline-1",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           gcsFuseCsiDriverName,
							VolumeAttributes: map[string]string{"mountOptions": "metadata-cache:enable-metadata-prefetch:true"},
						},
					},
				},
				{
					Name: "gcsfuse-inline-2",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           gcsFuseCsiDriverName,
							VolumeAttributes: map[string]string{"mountOptions": "metadata-cache:enable-metadata-prefetch:false"},
						},
					},
				},
			},
			wantEnabled:  true,
			wantDisabled: true,
		},
		{
			name: "pvc: pv enable-metadata-prefetch=true",
			volumes: []corev1.Volume{
				{
					Name: "gcsfuse-pvc",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "my-pvc",
						},
					},
				},
			},
			pvcs: []*corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: namespace},
					Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "my-pv"},
				},
			},
			pvs: []*corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pv"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{Driver: gcsFuseCsiDriverName},
						},
						MountOptions: []string{"metadata-cache:enable-metadata-prefetch:true"},
					},
				},
			},
			wantEnabled:  true,
			wantDisabled: false,
		},
		{
			name: "pvc: pv enable-metadata-prefetch=false",
			volumes: []corev1.Volume{
				{
					Name: "gcsfuse-pvc",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "my-pvc",
						},
					},
				},
			},
			pvcs: []*corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: namespace},
					Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "my-pv"},
				},
			},
			pvs: []*corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pv"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{Driver: gcsFuseCsiDriverName},
						},
						MountOptions: []string{"metadata-cache:enable-metadata-prefetch:false"},
					},
				},
			},
			wantEnabled:  false,
			wantDisabled: true,
		},
		{
			name: "multiple pvcs: conflict (one pv true, one pv false)",
			volumes: []corev1.Volume{
				{
					Name: "gcsfuse-pvc-1",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "my-pvc-1",
						},
					},
				},
				{
					Name: "gcsfuse-pvc-2",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "my-pvc-2",
						},
					},
				},
			},
			pvcs: []*corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pvc-1", Namespace: namespace},
					Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "my-pv-1"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pvc-2", Namespace: namespace},
					Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "my-pv-2"},
				},
			},
			pvs: []*corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pv-1"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{Driver: gcsFuseCsiDriverName},
						},
						MountOptions: []string{"metadata-cache:enable-metadata-prefetch:true"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pv-2"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{Driver: gcsFuseCsiDriverName},
						},
						MountOptions: []string{"metadata-cache:enable-metadata-prefetch:false"},
					},
				},
			},
			wantEnabled:  true,
			wantDisabled: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeClient := fake.NewSimpleClientset()
			for _, pv := range tc.pvs {
				_, err := fakeClient.CoreV1().PersistentVolumes().Create(context.TODO(), pv, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to create fake PV: %v", err)
				}
			}
			for _, pvc := range tc.pvcs {
				_, err := fakeClient.CoreV1().PersistentVolumeClaims(namespace).Create(context.TODO(), pvc, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to create fake PVC: %v", err)
				}
			}

			informerFactory := informers.NewSharedInformerFactoryWithOptions(fakeClient, time.Second*1, informers.WithNamespace(metav1.NamespaceAll))
			pvLister := informerFactory.Core().V1().PersistentVolumes().Lister()
			pvcLister := informerFactory.Core().V1().PersistentVolumeClaims().Lister()

			si := &SidecarInjector{
				PvLister:  pvLister,
				PvcLister: pvcLister,
			}

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())

			var pod *corev1.Pod
			if tc.volumes != nil {
				pod = &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
					Spec: corev1.PodSpec{
						Volumes: tc.volumes,
					},
				}
			}

			gotEnabled, gotDisabled := si.getNativePrefetchOverrides(pod)
			if gotEnabled != tc.wantEnabled || gotDisabled != tc.wantDisabled {
				t.Errorf("getNativePrefetchOverrides() got = (%v, %v), want = (%v, %v)", gotEnabled, gotDisabled, tc.wantEnabled, tc.wantDisabled)
			}
		})
	}
}
