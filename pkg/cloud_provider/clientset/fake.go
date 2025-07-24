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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type FakeNodeConfig struct {
	IsWorkloadIdentityEnabled bool
	MachineType               string
}

type FakeClientset struct {
	fakePod  *corev1.Pod
	fakeNode *corev1.Node
}

func NewFakeClientset() *FakeClientset {
	fakeClientSet := &FakeClientset{}
	// Default setting for most unit tests is pod doesn't use host network & workload identity is enabled on the node
	fakeClientSet.CreatePod(false /* hostNetworkEnabled */, false /* injectSATokenVolume */, "gcr.io/gke-release/gcs-fuse-csi-driver-sidecar-mounter:v9.9.9-gke.0", false)
	fakeClientSet.CreateNode(FakeNodeConfig{IsWorkloadIdentityEnabled: true})

	return fakeClientSet
}

func (c *FakeClientset) ConfigurePodLister(_ string) {}

func (c *FakeClientset) ConfigureNodeLister(_ string) {}

func (c *FakeClientset) CreatePod(hostNetworkEnabled bool, injectSATokenVolume bool, sidecarImage string, injectAsInitContainer bool) {
	config := webhook.FakeConfig()
	sidecarContainer := webhook.GetSidecarContainerSpec(config)
	sidecarContainer.Image = sidecarImage

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "",
			Namespace: "",
		},
		Spec: corev1.PodSpec{
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

	if injectAsInitContainer {
		pod.Spec.InitContainers = []corev1.Container{sidecarContainer}
		pod.Spec.Containers = []corev1.Container{
			{
				Name:  "dummy-container",
				Image: "dummy-image",
			},
		}
	} else {
		pod.Spec.Containers = []corev1.Container{sidecarContainer}
	}

	if hostNetworkEnabled {
		pod.Spec.HostNetwork = true
	}
	if injectSATokenVolume {
		pod.Spec.Volumes = append(pod.Spec.Volumes, webhook.GetSATokenVolume("test-project"))
	}
	c.fakePod = pod
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

func (c *FakeClientset) CreateServiceAccountToken(_ context.Context, _, _ string, _ *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
	return &authenticationv1.TokenRequest{}, nil
}

func (c *FakeClientset) GetGCPServiceAccountName(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
