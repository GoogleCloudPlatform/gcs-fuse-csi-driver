/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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
	"errors"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"
)

const resyncDuration = time.Second * 1

func TestIsPreprovisionCSIVolume(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		testName         string
		csiDriverName    string
		pvc              *corev1.PersistentVolumeClaim
		pvsInK8s         []corev1.PersistentVolume
		expectedResponse bool
		expectedError    error
	}{
		{
			testName:         "nil pvc passed into IsPreprovisionCSIVolume",
			csiDriverName:    "fake-csi-driver",
			pvc:              nil,
			expectedResponse: false,
			expectedError:    errors.New(`pvc is nil, cannot get pv`),
		},
		{
			testName:         "given blank csiDriver name",
			csiDriverName:    "",
			pvc:              &corev1.PersistentVolumeClaim{},
			expectedResponse: false,
			expectedError:    errors.New("csiDriver is empty, cannot verify storage type"),
		},
		{
			testName:         "not preprovisioned pvc",
			csiDriverName:    "fake-csi-driver",
			pvc:              &corev1.PersistentVolumeClaim{},
			expectedResponse: false,
			expectedError:    nil,
		},
		{
			testName:      "preprovisioned pvc volume not found",
			csiDriverName: "fake-csi-driver",
			pvc: &corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					VolumeName: "pv-1234",
				},
			},
			expectedResponse: false,
			expectedError:    errors.New(`persistentvolume "pv-1234" not found`),
		},
		{
			testName:      "preprovisioned pvc for different csi driver",
			csiDriverName: "fake-csi-driver",
			pvc: &corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					VolumeName: "pv-1234",
				},
			},
			pvsInK8s: []corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-1234",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver: "other-csi-driver",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-135",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver: "fake-csi-driver",
							},
						},
					},
				},
			},
			expectedResponse: false,
		},
		{
			testName:      "preprovisioned pvc for different csi driver",
			csiDriverName: "fake-csi-driver",
			pvc: &corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					VolumeName: "pv-1234",
				},
			},
			pvsInK8s: []corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-1234",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver: "fake-csi-driver",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-135",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver: "fake-csi-driver",
							},
						},
					},
				},
			},
			expectedResponse: true,
			expectedError:    nil,
		},
		{
			testName:      "preprovisioned pvc with no csi specified",
			csiDriverName: "fake-csi-driver",
			pvc: &corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					VolumeName: "pv-1234",
				},
			},
			pvsInK8s: []corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-1234",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{},
					},
				},
			},
			expectedResponse: false,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.testName, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fakeClient := fake.NewSimpleClientset()

			for _, pvInK8s := range testcase.pvsInK8s {
				_, err := fakeClient.CoreV1().PersistentVolumes().Create(context.TODO(), &pvInK8s, metav1.CreateOptions{})
				if err != nil {
					klog.Errorf("test setup failed: %v", err)
				}
			}

			csiGroupClient := SidecarInjector{}

			informer := informers.NewSharedInformerFactoryWithOptions(fakeClient, resyncDuration, informers.WithNamespace(metav1.NamespaceAll))
			csiGroupClient.PvLister = informer.Core().V1().PersistentVolumes().Lister()
			csiGroupClient.PvcLister = informer.Core().V1().PersistentVolumeClaims().Lister()

			informer.Start(ctx.Done())
			informer.WaitForCacheSync(ctx.Done())

			response, err := csiGroupClient.IsPreprovisionCSIVolume(testcase.csiDriverName, testcase.pvc)
			if err != nil && testcase.expectedError != nil {
				if err.Error() != testcase.expectedError.Error() {
					t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedError.Error(), " but got: ", err.Error())
				}
			} else if err != nil || testcase.expectedError != nil {
				// if one of them is nil, both must be nil to pass
				t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedError, " but got: ", err)
			}

			if response != testcase.expectedResponse {
				t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedResponse, " but got: ", response)
			}
		})
	}
}

func TestGetPV(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		testName         string
		pvName           string
		pvsInK8s         []corev1.PersistentVolume
		expectedResponse *corev1.PersistentVolume
		expectedError    error
	}{
		{
			testName:         "pv not in k8s",
			pvName:           "pvc-5678",
			pvsInK8s:         []corev1.PersistentVolume{},
			expectedResponse: nil,
			expectedError:    errors.New(`persistentvolume "pvc-5678" not found`),
		},
		{
			testName: "pv in k8s",
			pvName:   "pvc-12345",
			pvsInK8s: []corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pvc-13",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pvc-124",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pvc-12345",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pvc-135",
					},
				},
			},
			expectedResponse: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pvc-12345",
				},
			},
			expectedError: nil,
		},
	}

	for _, testcase := range testcases {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fakeClient := fake.NewSimpleClientset()
		for _, pvInK8s := range testcase.pvsInK8s {
			_, err := fakeClient.CoreV1().PersistentVolumes().Create(context.TODO(), &pvInK8s, metav1.CreateOptions{})
			if err != nil {
				klog.Errorf("test setup failed: %v", err)
			}
		}
		csiGroupClient := SidecarInjector{}
		informer := informers.NewSharedInformerFactoryWithOptions(fakeClient, resyncDuration, informers.WithNamespace(metav1.NamespaceAll))
		csiGroupClient.PvLister = informer.Core().V1().PersistentVolumes().Lister()
		csiGroupClient.PvcLister = informer.Core().V1().PersistentVolumeClaims().Lister()

		informer.Start(ctx.Done())
		informer.WaitForCacheSync(ctx.Done())

		response, err := csiGroupClient.GetPV(testcase.pvName)
		if err != nil && testcase.expectedError != nil {
			if err.Error() != testcase.expectedError.Error() {
				t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedError.Error(), " but got: ", err.Error())
			}
		} else if err != nil || testcase.expectedError != nil {
			// if one of them is nil, both must be nil to pass
			t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedError, " but got: ", err)
		}

		if response.String() != testcase.expectedResponse.String() {
			t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedResponse, " but got: ", response)
		}
	}
}

func TestGetPVC(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		testName         string
		pvcName          string
		pvcNamespace     string
		pvcsInK8s        []corev1.PersistentVolumeClaim
		expectedResponse *corev1.PersistentVolumeClaim
		expectedError    error
	}{
		{
			testName:         "pvc not in k8s",
			pvcName:          "pvc-5678",
			pvcNamespace:     metav1.NamespaceAll,
			pvcsInK8s:        []corev1.PersistentVolumeClaim{},
			expectedResponse: nil,
			expectedError:    errors.New(`persistentvolumeclaim "pvc-5678" not found`),
		},
		{
			testName:     "pvc in k8s on different namespace",
			pvcName:      "pvc-12345",
			pvcNamespace: metav1.NamespaceSystem,
			pvcsInK8s: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-13",
						Namespace: metav1.NamespaceDefault,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-12345",
						Namespace: metav1.NamespaceDefault,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-124",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			expectedResponse: nil,
			expectedError:    errors.New(`persistentvolumeclaim "pvc-12345" not found`),
		},
		{
			testName:     "pvc in k8s",
			pvcName:      "pvc-12345",
			pvcNamespace: metav1.NamespaceDefault,
			pvcsInK8s: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-13",
						Namespace: metav1.NamespaceDefault,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-12345",
						Namespace: metav1.NamespaceDefault,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-124",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			expectedResponse: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pvc-12345",
					Namespace: metav1.NamespaceDefault,
				},
			},
			expectedError: nil,
		},
	}

	for _, testcase := range testcases {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fakeClient := fake.NewSimpleClientset()
		for _, pvcInK8s := range testcase.pvcsInK8s {
			_, err := fakeClient.CoreV1().PersistentVolumeClaims(pvcInK8s.Namespace).Create(context.TODO(), &pvcInK8s, metav1.CreateOptions{})
			if err != nil {
				klog.Errorf("failed to setup test: %v", err)
			}
		}

		csiGroupClient := SidecarInjector{}

		informer := informers.NewSharedInformerFactoryWithOptions(fakeClient, resyncDuration, informers.WithNamespace(metav1.NamespaceAll))
		csiGroupClient.PvLister = informer.Core().V1().PersistentVolumes().Lister()
		csiGroupClient.PvcLister = informer.Core().V1().PersistentVolumeClaims().Lister()

		informer.Start(ctx.Done())
		informer.WaitForCacheSync(ctx.Done())

		response, err := csiGroupClient.GetPVC(testcase.pvcNamespace, testcase.pvcName)
		if err != nil && testcase.expectedError != nil {
			if err.Error() != testcase.expectedError.Error() {
				t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedError.Error(), " but got: ", err.Error())
			}
		} else if err != nil || testcase.expectedError != nil {
			// if one of them is nil, both must be nil to pass
			t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedError, " but got: ", err)
		}

		if response.String() != testcase.expectedResponse.String() {
			t.Error("for test: ", testcase.testName, ", want: ", testcase.expectedResponse, " but got: ", response)
		}
	}
}

func TestGetVolumesStorageClass(t *testing.T) {
	t.Parallel()

	scFound := storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sc-found",
		},
	}
	scOther := storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sc-other",
		},
	}

	testcases := []struct {
		testName         string
		inputVolume      *corev1.PersistentVolume
		scsInK8s         []storagev1.StorageClass
		expectedResponse *storagev1.StorageClass
		expectedError    error
	}{
		{
			testName:         "nil volume",
			inputVolume:      nil,
			scsInK8s:         []storagev1.StorageClass{},
			expectedResponse: nil,
			expectedError:    nil,
		},
		{
			testName: "volume with empty storageclass name",
			inputVolume: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					StorageClassName: "",
				},
			},
			scsInK8s:         []storagev1.StorageClass{scOther},
			expectedResponse: nil,
			expectedError:    nil,
		},
		{
			testName: "storageclass not found",
			inputVolume: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					StorageClassName: "sc-not-found",
				},
			},
			scsInK8s:         []storagev1.StorageClass{scOther},
			expectedResponse: nil,
			expectedError:    errors.New(`storageclass.storage.k8s.io "sc-not-found" not found`),
		},
		{
			testName: "storageclass found",
			inputVolume: &corev1.PersistentVolume{
				Spec: corev1.PersistentVolumeSpec{
					StorageClassName: "sc-found",
				},
			},
			scsInK8s:         []storagev1.StorageClass{scOther, scFound},
			expectedResponse: &scFound,
			expectedError:    nil,
		},
	}

	for _, testcase := range testcases {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fakeClient := fake.NewSimpleClientset()
		for _, scInK8s := range testcase.scsInK8s {
			sc := scInK8s
			_, err := fakeClient.StorageV1().StorageClasses().Create(context.TODO(), &sc, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to setup test: %v", err)
			}
		}

		csiGroupClient := SidecarInjector{}
		const resyncDuration = 0 * time.Second

		informer := informers.NewSharedInformerFactory(fakeClient, resyncDuration)
		csiGroupClient.ScLister = informer.Storage().V1().StorageClasses().Lister()

		informer.Start(ctx.Done())
		informer.WaitForCacheSync(ctx.Done())

		response, err := csiGroupClient.GetVolumesStorageClass(testcase.inputVolume)

		if err != nil && testcase.expectedError != nil {
			if err.Error() != testcase.expectedError.Error() {
				t.Error("for test: ", testcase.testName, ", want error: ", testcase.expectedError.Error(), " but got: ", err.Error())
			}
		} else if err != nil || testcase.expectedError != nil {
			t.Error("for test: ", testcase.testName, ", want error: ", testcase.expectedError, " but got: ", err)
		}

		if !reflect.DeepEqual(response, testcase.expectedResponse) {
			t.Error("for test: ", testcase.testName, ", want response: ", testcase.expectedResponse, " but got: ", response)
		}
	}
}
