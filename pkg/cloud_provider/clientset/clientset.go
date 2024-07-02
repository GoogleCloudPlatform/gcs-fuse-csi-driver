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
	"errors"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

type Interface interface {
	ConfigurePodLister(nodeName string)
	GetPod(namespace, name string) (*corev1.Pod, error)
	CreateServiceAccountToken(ctx context.Context, namespace, name string, tokenRequest *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error)
	GetGCPServiceAccountName(ctx context.Context, namespace, name string) (string, error)
}

type PodInfo struct {
	Name      string
	Namespace string
}

type Clientset struct {
	k8sClients                kubernetes.Interface
	podLister                 listersv1.PodLister
	informerResyncDurationSec int
}

func New(kubeconfigPath string, informerResyncDurationSec int) (Interface, error) {
	var err error
	var rc *rest.Config
	if kubeconfigPath != "" {
		klog.V(4).Infof("using kubeconfig path %q", kubeconfigPath)
		rc, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		klog.V(4).Info("using in-cluster kubeconfig")
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to configure k8s client: %w", err)
	}

	return &Clientset{k8sClients: clientset, informerResyncDurationSec: informerResyncDurationSec}, nil
}

func (c *Clientset) ConfigurePodLister(nodeName string) {
	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = "spec.nodeName=" + nodeName
		}),
	)
	podLister := informerFactory.Core().V1().Pods().Lister()

	ctx := context.Background()
	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	c.podLister = podLister
}

func (c *Clientset) GetPod(namespace, name string) (*corev1.Pod, error) {
	if c.podLister == nil {
		return nil, errors.New("pod informer is not ready")
	}

	return c.podLister.Pods(namespace).Get(name)
}

func (c *Clientset) CreateServiceAccountToken(ctx context.Context, namespace, name string, tokenRequest *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
	resp, err := c.k8sClients.
		CoreV1().
		ServiceAccounts(namespace).
		CreateToken(
			ctx,
			name,
			tokenRequest,
			metav1.CreateOptions{},
		)

	return resp, err
}

func (c *Clientset) GetGCPServiceAccountName(ctx context.Context, namespace, name string) (string, error) {
	resp, err := c.k8sClients.
		CoreV1().
		ServiceAccounts(namespace).
		Get(
			ctx,
			name,
			metav1.GetOptions{},
		)
	if err != nil {
		return "", fmt.Errorf("failed to call Kubernetes ServiceAccount.Get API: %w", err)
	}

	return resp.Annotations["iam.gke.io/gcp-service-account"], nil
}
