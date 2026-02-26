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

package clientset

import (
	"context"
	"strconv"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type FakeNodeConfig struct {
	IsWorkloadIdentityEnabled bool
	MachineType               string
	Status                    corev1.NodeStatus
}

type FakePodConfig struct {
	HostNetworkEnabled bool
	SidecarLimits      corev1.ResourceList
}

type FakePVConfig struct {
	Annotations map[string]string
	SCName      string
	DriverName  string
	Name        string
}

type FakePVCConfig struct {
	Name       string
	VolumeName string
}

type FakeSCConfig struct {
	Labels       map[string]string
	Parameters   map[string]string
	MountOptions []string
}

type FakeClientset struct {
	fakePod  *corev1.Pod
	fakeNode *corev1.Node
	fakePVs  map[string]*corev1.PersistentVolume
	fakePVCs map[string]*corev1.PersistentVolumeClaim
	fakeSCs  map[string]*storagev1.StorageClass
}

func NewFakeClientset() *FakeClientset {
	fakeClientSet := &FakeClientset{
		fakePVs:  make(map[string]*corev1.PersistentVolume),
		fakePVCs: make(map[string]*corev1.PersistentVolumeClaim),
		fakeSCs:  make(map[string]*storagev1.StorageClass),
	}
	// Default setting for most unit tests is pod doesn't use host network & workload identity is enabled on the node
	fakeClientSet.CreatePod(FakePodConfig{HostNetworkEnabled: false})
	fakeClientSet.CreateNode(FakeNodeConfig{IsWorkloadIdentityEnabled: true})
	fakeClientSet.CreatePV(FakePVConfig{})
	fakeClientSet.CreatePVC(FakePVCConfig{})
	fakeClientSet.CreateSC(FakeSCConfig{})

	return fakeClientSet
}

func (c *FakeClientset) K8sClient() kubernetes.Interface {
	return nil
}

func (c *FakeClientset) ConfigurePodLister(_ context.Context, _ string) {}

func (c *FakeClientset) ConfigureNodeLister(_ context.Context, _ string) {}

func (c *FakeClientset) ConfigurePVLister(_ context.Context) {}

func (c *FakeClientset) ConfigurePVCLister(_ context.Context) {}

func (c *FakeClientset) ConfigureSCLister(_ context.Context) {}

func (c *FakeClientset) CreatePod(podConfig FakePodConfig) {
	config := webhook.FakeConfig()

	if podConfig.SidecarLimits != nil {
		config.MemoryLimit = podConfig.SidecarLimits[corev1.ResourceMemory]
		config.EphemeralStorageLimit = podConfig.SidecarLimits[corev1.ResourceEphemeralStorage]
	}

	c.fakePod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "",
			Namespace: "",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				webhook.GetSidecarContainerSpec(config, nil /*credentialConfig*/),
			},
			Volumes: webhook.GetSidecarContainerVolumeSpec(),
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: webhook.GcsFuseSidecarName,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			},
		},
	}

	if podConfig.HostNetworkEnabled {
		c.fakePod.Spec.HostNetwork = true
	}
}

func (c *FakeClientset) CreateNode(nodeConfig FakeNodeConfig) {
	c.fakeNode = &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "",
			Labels: map[string]string{},
		},
	}

	c.fakeNode.Labels[GkeMetaDataServerKey] = strconv.FormatBool(nodeConfig.IsWorkloadIdentityEnabled)
	c.fakeNode.Labels[MachineTypeKey] = nodeConfig.MachineType
	if c.fakeNode.Labels[MachineTypeKey] == "" {
		c.fakeNode.Labels[MachineTypeKey] = "e2-medium"
	}
	c.fakeNode.Status = nodeConfig.Status
}

func (c *FakeClientset) CreatePV(pvConfig FakePVConfig) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   pvConfig.Name,
			Labels: map[string]string{},
		},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: pvConfig.SCName,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: pvConfig.DriverName,
				},
			},
		},
	}

	c.fakePVs[pv.Name] = pv
}

func (c *FakeClientset) CreatePVC(pvcConfig FakePVCConfig) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvcConfig.Name,
			Labels: map[string]string{},
		},
	}

	pvc.Spec = corev1.PersistentVolumeClaimSpec{
		VolumeName: pvcConfig.VolumeName,
	}
	c.fakePVCs[pvc.Name] = pvc
}

func (c *FakeClientset) CreateSC(scConfig FakeSCConfig) {
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "",
			Labels: map[string]string{},
		},
	}

	sc.MountOptions = scConfig.MountOptions
	sc.Parameters = scConfig.Parameters
	sc.Labels = scConfig.Labels
	c.fakeSCs[sc.Name] = sc
}

func (c *FakeClientset) AddPodVolumes(volumes []corev1.Volume) {
	if c.fakePod != nil {
		c.fakePod.Spec.Volumes = append(c.fakePod.Spec.Volumes, volumes...)
	}
}

func (c *FakeClientset) GetPod(namespace, name string) (*corev1.Pod, error) {
	c.fakePod.ObjectMeta.Name = name
	c.fakePod.ObjectMeta.Namespace = namespace

	return c.fakePod, nil
}

func (c *FakeClientset) GetNode(name string) (*corev1.Node, error) {
	c.fakeNode.ObjectMeta.Name = name

	return c.fakeNode, nil
}

func (c *FakeClientset) GetPV(name string) (*corev1.PersistentVolume, error) {
	if pv, ok := c.fakePVs[name]; ok {
		return pv, nil
	}

	return c.fakePVs[""], nil
}

func (c *FakeClientset) GetPVC(namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	if pvc, ok := c.fakePVCs[name]; ok {
		return pvc, nil
	}

	return c.fakePVCs[""], nil
}

func (c *FakeClientset) GetSC(name string) (*storagev1.StorageClass, error) {
	if sc, ok := c.fakeSCs[name]; ok {
		return sc, nil
	}

	return c.fakeSCs[""], nil
}

func (c *FakeClientset) CreateServiceAccountToken(_ context.Context, _, _ string, _ *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
	return &authenticationv1.TokenRequest{}, nil
}

func (c *FakeClientset) GetGCPServiceAccountName(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
