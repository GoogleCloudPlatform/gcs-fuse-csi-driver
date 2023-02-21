/*
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
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type FakeClientset struct {
}

func (c *FakeClientset) GetPod(ctx context.Context, namespace, name string) (*v1.Pod, error) {
	config := webhook.FakeConfig()
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				webhook.GetSidecarContainerSpec(config),
			},
			Volumes: []v1.Volume{
				webhook.GetSidecarContainerVolumeSpec(),
			},
		},
	}
	return pod, nil
}

func (c *FakeClientset) GetDaemonSet(ctx context.Context, namespace, name string) (*appsv1.DaemonSet, error) {
	return nil, nil
}

func (c *FakeClientset) CreateServiceAccountToken(ctx context.Context, namespace, name string, tokenRequest *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
	return nil, nil
}

func (c *FakeClientset) GetGCPServiceAccountName(ctx context.Context, namespace, name string) (string, error) {
	return "", nil
}
