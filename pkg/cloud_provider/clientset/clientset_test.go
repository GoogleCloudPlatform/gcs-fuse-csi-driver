/*
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

package clientset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func TestConfigurePodLister(t *testing.T) {
	t.Parallel()

	var wantFSGroup int64 = 1000
	wantSA := "custom-sa"

	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-ns",
		},
		Spec: corev1.PodSpec{
			NodeName:           "test-node",
			ServiceAccountName: wantSA,
			SecurityContext: &corev1.PodSecurityContext{
				FSGroup: ptr.To(wantFSGroup),
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(testPod)
	c := &Clientset{
		k8sClients:    fakeClient,
		runController: false,
	}

	c.ConfigurePodLister(t.Context(), "test-node", nil)

	gotPod, err := c.GetPod("test-ns", "test-pod")
	if err != nil {
		t.Fatalf("c.GetPod unexpected error: %v", err)
	}

	if gotPod.Spec.ServiceAccountName != wantSA {
		t.Errorf("got ServiceAccountName %q, want %q", gotPod.Spec.ServiceAccountName, wantSA)
	}

	if gotPod.Spec.SecurityContext == nil || gotPod.Spec.SecurityContext.FSGroup == nil || *gotPod.Spec.SecurityContext.FSGroup != wantFSGroup {
		t.Errorf("got SecurityContext %+v, want FSGroup %d", gotPod.Spec.SecurityContext, wantFSGroup)
	}
}

func TestConfigurePVLister(t *testing.T) {
	t.Parallel()

	labeledPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "labeled-gcsfuse-pv",
			Labels: map[string]string{
				webhook.GcsfuseProfilesManagedLabel: util.TrueStr,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: "gcsfuse.csi.storage.gke.io",
				},
			},
		},
	}

	unlabeledPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unlabeled-pv",
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: "pd.csi.storage.gke.io",
				},
			},
		},
	}

	multiLabeledPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "multi-labeled-gcsfuse-pv",
			Labels: map[string]string{
				webhook.GcsfuseProfilesManagedLabel: util.TrueStr,
				"some-label":                        "some-value",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: "gcsfuse.csi.storage.gke.io",
				},
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(labeledPV, unlabeledPV, multiLabeledPV)
	c := &Clientset{
		k8sClients: fakeClient,
		// We want to test the Node driver behavior (which filters the PV), so we set runController to false.
		runController: false,
	}

	c.ConfigurePVLister(t.Context(), nil)

	pvs, err := c.pvLister.List(labels.Everything())
	if err != nil {
		t.Fatalf("c.pvLister.List unexpected error: %v", err)
	}

	var gotPVNames []string
	for _, pv := range pvs {
		gotPVNames = append(gotPVNames, pv.Name)
	}

	wantPVNames := []string{"labeled-gcsfuse-pv", "multi-labeled-gcsfuse-pv"}
	less := func(a, b string) bool { return a < b }
	if diff := cmp.Diff(wantPVNames, gotPVNames, cmpopts.SortSlices(less)); diff != "" {
		t.Errorf("c.pvLister.List PV names mismatch (-want +got):\n%s", diff)
	}
}
