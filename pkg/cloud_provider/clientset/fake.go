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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type FakeClientset struct {
	fakePod  *corev1.Pod
	fakeNode *corev1.Node
}

func (c *FakeClientset) ConfigurePodLister(_ string) {}

func (c *FakeClientset) ConfigureNodeLister(_ string) {}

func (c *FakeClientset) CreatePod(hostNetworkEnabled bool) {
	config := webhook.FakeConfig()
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

	if hostNetworkEnabled {
		c.fakePod.Spec.HostNetwork = true
	}
}

func (c *FakeClientset) CreateNode(isWorkloadIdentityEnabled bool) {
	c.fakeNode = &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "",
			Labels: map[string]string{},
		},
	}

	if isWorkloadIdentityEnabled {
		c.fakeNode.Labels[GkeMetaDataServerKey] = "true"
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
