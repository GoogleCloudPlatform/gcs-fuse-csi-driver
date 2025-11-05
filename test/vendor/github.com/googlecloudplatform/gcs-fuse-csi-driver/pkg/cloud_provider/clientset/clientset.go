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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

type Interface interface {
	ConfigurePodLister(ctx context.Context, nodeName string)
	ConfigureNodeLister(ctx context.Context, nodeName string)
	ConfigurePVLister(ctx context.Context)
	ConfigureSCLister(ctx context.Context)
	GetPod(namespace, name string) (*corev1.Pod, error)
	GetNode(name string) (*corev1.Node, error)
	GetPV(name string) (*corev1.PersistentVolume, error)
	GetSC(name string) (*storagev1.StorageClass, error)
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
	nodeLister                listersv1.NodeLister
	pvLister                  listersv1.PersistentVolumeLister
	scLister                  storagelisters.StorageClassLister
	informerResyncDurationSec int
}

const (
	GkeMetaDataServerKey              = "iam.gke.io/gke-metadata-server-enabled"
	MachineTypeKey                    = "node.kubernetes.io/instance-type"
	GkeAppliedNodeLabelsAnnotationKey = "node.gke.io/last-applied-node-labels"
)

func (c *Clientset) ConfigureNodeLister(ctx context.Context, nodeName string) {
	trim := func(obj interface{}) (interface{}, error) {
		if accessor, err := meta.Accessor(obj); err == nil {
			if accessor.GetManagedFields() != nil {
				accessor.SetManagedFields(nil)
			}
		}

		// We are filtering only for relevant Node annotations to optimize memory usage.
		// Relevant info is for NodePublishVolume calls:
		// https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/547cab9a9aea4cdbda581885880020fb9266dc03/pkg/csi_driver/node.go#L85
		nodeObj, ok := obj.(*corev1.Node)
		if !ok {
			return obj, nil
		}

		newLabels := map[string]string{}
		isGkeMetaDataServerEnabled, ok := nodeObj.ObjectMeta.Labels[GkeMetaDataServerKey]
		if ok {
			newLabels[GkeMetaDataServerKey] = isGkeMetaDataServerEnabled
		}
		machineType, ok := nodeObj.ObjectMeta.Labels[MachineTypeKey]
		if ok {
			newLabels[MachineTypeKey] = machineType
		}

		newStatus := corev1.NodeStatus{
			// Used by the gcsfuse profiles feature to determine if the recommended cache fits in the node.
			Allocatable: nodeObj.Status.Allocatable,
		}

		newAnnotations := map[string]string{}

		appliedLabelsStr, ok := nodeObj.ObjectMeta.Annotations[GkeAppliedNodeLabelsAnnotationKey]
		if ok && appliedLabelsStr != "" {
			// Used by the gcsfuse profiles feature to determine if the node has the "cloud.google.com/gke-ephemeral-storage-local-ssd" label key.
			// TODO(urielguzman): This will not work in OSS K8S. We should consider either giving the customer a static label or
			// allowing the customer to pass their custom "node has LSSD" label into the gcsfuse profiles feature, most likely via a VolumeAttribute.
			newAnnotations[GkeAppliedNodeLabelsAnnotationKey] = appliedLabelsStr
		}

		nodeObj.Spec = corev1.NodeSpec{}
		nodeObj.Status = newStatus
		nodeObj.ObjectMeta.Annotations = newAnnotations
		nodeObj.ObjectMeta.Labels = newLabels

		return obj, nil
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = "metadata.name=" + nodeName
		}),
		informers.WithTransform(trim),
	)
	nodeLister := informerFactory.Core().V1().Nodes().Lister()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	c.nodeLister = nodeLister
}

func (c *Clientset) ConfigurePVLister(ctx context.Context) {
	trim := func(obj any) (any, error) {
		pvObj, ok := obj.(*corev1.PersistentVolume)
		if !ok {
			return obj, nil
		}
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:        pvObj.ObjectMeta.Name,
				Annotations: pvObj.ObjectMeta.Annotations, // Required by the gcsfuse profiles feature to calculate smart cache recommendations.
			},
			Spec: corev1.PersistentVolumeSpec{
				StorageClassName: pvObj.Spec.StorageClassName, // Required by the gcsfuse profiles feature to map PV to SC.
			},
		}, nil
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		informers.WithTransform(trim),
	)
	pvLister := informerFactory.Core().V1().PersistentVolumes().Lister()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	c.pvLister = pvLister
}

func (c *Clientset) ConfigureSCLister(ctx context.Context) {
	trim := func(obj any) (any, error) {
		scObj, ok := obj.(*storagev1.StorageClass)
		if !ok {
			return obj, nil
		}
		return &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: scObj.ObjectMeta.Name,
			},
			MountOptions: scObj.MountOptions, // Required by the gcsfuse profiles feature to apply pre-bundled mount options.
			Parameters:   scObj.Parameters,   // Required by the gcsfuse profiles feature to get profile configs.
		}, nil
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		informers.WithTransform(trim),
	)
	scLister := informerFactory.Storage().V1().StorageClasses().Lister()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	c.scLister = scLister
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
	rc.ContentType = runtime.ContentTypeProtobuf

	clientset, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to configure k8s client: %w", err)
	}

	return &Clientset{k8sClients: clientset, informerResyncDurationSec: informerResyncDurationSec}, nil
}

func (c *Clientset) ConfigurePodLister(ctx context.Context, nodeName string) {
	trim := func(obj interface{}) (interface{}, error) {
		if accessor, err := meta.Accessor(obj); err == nil {
			if accessor.GetManagedFields() != nil {
				accessor.SetManagedFields(nil)
			}
		}

		// We are filtering only for relevant PodSpec info to optimize memory usage.
		// Relevant info is for NodePublishVolume calls:
		// https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/547cab9a9aea4cdbda581885880020fb9266dc03/pkg/csi_driver/node.go#L85
		podObj, ok := obj.(*corev1.Pod)
		if !ok {
			return obj, nil
		}

		var newContainers []corev1.Container
		for _, cont := range podObj.Spec.Containers {
			container := corev1.Container{
				Name:            cont.Name,
				SecurityContext: cont.SecurityContext,
				VolumeMounts:    cont.VolumeMounts,
			}
			newContainers = append(newContainers, container)
		}

		var newInitContainers []corev1.Container
		for _, cont := range podObj.Spec.InitContainers {
			if cont.Name == webhook.GcsFuseSidecarName {
				newInitContainers = append(newInitContainers, cont)

				continue
			}
			container := corev1.Container{
				Name:            cont.Name,
				SecurityContext: cont.SecurityContext,
				VolumeMounts:    cont.VolumeMounts,
			}
			newInitContainers = append(newInitContainers, container)
		}

		nodeName := podObj.Spec.NodeName
		volumes := podObj.Spec.Volumes
		restartPolicy := podObj.Spec.RestartPolicy
		hostNetwork := podObj.Spec.HostNetwork
		podObj.Spec = corev1.PodSpec{
			NodeName:       nodeName,
			Volumes:        volumes,
			Containers:     newContainers,
			InitContainers: newInitContainers,
			RestartPolicy:  restartPolicy,
			HostNetwork:    hostNetwork,
		}

		return obj, nil
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = "spec.nodeName=" + nodeName
		}),
		informers.WithTransform(trim),
	)
	podLister := informerFactory.Core().V1().Pods().Lister()

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

func (c *Clientset) GetNode(name string) (*corev1.Node, error) {
	if c.nodeLister == nil {
		return nil, errors.New("node informer is not ready")
	}

	return c.nodeLister.Get(name)
}

func (c *Clientset) GetPV(name string) (*corev1.PersistentVolume, error) {
	if c.pvLister == nil {
		return nil, errors.New("pv informer is not ready")
	}

	return c.pvLister.Get(name)
}

func (c *Clientset) GetSC(name string) (*storagev1.StorageClass, error) {
	if c.scLister == nil {
		return nil, errors.New("sc informer is not ready")
	}

	return c.scLister.Get(name)
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
