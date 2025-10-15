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
}

type FakeSCConfig struct {
	Parameters   map[string]string
	MountOptions []string
}

type FakeClientset struct {
	fakePod  *corev1.Pod
	fakeNode *corev1.Node
	fakePV   *corev1.PersistentVolume
	fakeSC   *storagev1.StorageClass
}

func NewFakeClientset() *FakeClientset {
	fakeClientSet := &FakeClientset{}
	// Default setting for most unit tests is pod doesn't use host network & workload identity is enabled on the node
	fakeClientSet.CreatePod(FakePodConfig{HostNetworkEnabled: false})
	fakeClientSet.CreateNode(FakeNodeConfig{IsWorkloadIdentityEnabled: true})
	fakeClientSet.CreatePV(FakePVConfig{})
	fakeClientSet.CreateSC(FakeSCConfig{})

	return fakeClientSet
}

func (c *FakeClientset) ConfigurePodLister(_ context.Context, _ string) {}

func (c *FakeClientset) ConfigureNodeLister(_ context.Context, _ string) {}

func (c *FakeClientset) ConfigurePVLister(_ context.Context) {}

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
				webhook.GetSidecarContainerSpec(config),
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
	c.fakePV = &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "",
			Labels: map[string]string{},
		},
	}

	c.fakePV.Annotations = pvConfig.Annotations
	c.fakePV.Spec.StorageClassName = pvConfig.SCName
}

func (c *FakeClientset) CreateSC(scConfig FakeSCConfig) {
	c.fakeSC = &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "",
			Labels: map[string]string{},
		},
	}

	c.fakeSC.MountOptions = scConfig.MountOptions
	c.fakeSC.Parameters = scConfig.Parameters
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
	c.fakePV.ObjectMeta.Name = name

	return c.fakePV, nil
}

func (c *FakeClientset) GetSC(name string) (*storagev1.StorageClass, error) {
	c.fakeSC.ObjectMeta.Name = name

	return c.fakeSC, nil
}

func (c *FakeClientset) CreateServiceAccountToken(_ context.Context, _, _ string, _ *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
	return &authenticationv1.TokenRequest{}, nil
}

func (c *FakeClientset) GetGCPServiceAccountName(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
