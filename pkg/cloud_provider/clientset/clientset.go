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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const (
	// PodNameIndex is the key for the indexer that indexes pods by their name.
	PodNameIndex = "podName"
)

type Interface interface {
	K8sClient() kubernetes.Interface
	ConfigurePodLister(ctx context.Context, nodeName string)
	ConfigureNodeLister(ctx context.Context, nodeName string)
	ConfigurePVLister(ctx context.Context)
	ConfigurePVCLister(ctx context.Context)
	ConfigureSCLister(ctx context.Context)
	ConfigurePodTemplateLister(ctx context.Context)
	GetPod(namespace, name string) (*corev1.Pod, error)
	GetPodsByName(name string) ([]*corev1.Pod, error)
	GetNode(name string) (*corev1.Node, error)
	GetPV(name string) (*corev1.PersistentVolume, error)
	GetPVC(namespace string, name string) (*corev1.PersistentVolumeClaim, error)
	GetSC(name string) (*storagev1.StorageClass, error)
	GetPodTemplate(namespace, name string) (*corev1.PodTemplate, error)
	ListPVs() ([]*corev1.PersistentVolume, error)
	CreateServiceAccountToken(ctx context.Context, namespace, name string, tokenRequest *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error)
	GetGCPServiceAccountName(ctx context.Context, namespace, name string) (string, error)

	// Informer methods
	PodInformer() cache.SharedIndexInformer
	PVInformer() cache.SharedIndexInformer
	PVCInformer() cache.SharedIndexInformer
	SCInformer() cache.SharedIndexInformer
	StartInformers(stopCh <-chan struct{})
	WaitForCacheSync(stopCh <-chan struct{}) bool
}

type PodInfo struct {
	Name      string
	Namespace string
}

type Clientset struct {
	k8sClients                kubernetes.Interface
	podLister                 listersv1.PodLister
	podInformer               cache.SharedIndexInformer
	nodeLister                listersv1.NodeLister
	pvLister                  listersv1.PersistentVolumeLister
	pvInformer                cache.SharedIndexInformer
	pvcLister                 listersv1.PersistentVolumeClaimLister
	pvcInformer               cache.SharedIndexInformer
	scLister                  storagelisters.StorageClassLister
	scInformer                cache.SharedIndexInformer
	podTemplateLister         listersv1.PodTemplateLister
	informerResyncDurationSec int
	runController             bool
	informerFactory           informers.SharedInformerFactory
}

const (
	GkeMetaDataServerKey              = "iam.gke.io/gke-metadata-server-enabled"
	MachineTypeKey                    = "node.kubernetes.io/instance-type"
	GkeAppliedNodeLabelsAnnotationKey = "node.gke.io/last-applied-node-labels"
)

func (c *Clientset) K8sClient() kubernetes.Interface {
	return c.k8sClients
}

func (c *Clientset) PodInformer() cache.SharedIndexInformer {
	return c.podInformer
}

func (c *Clientset) PVInformer() cache.SharedIndexInformer {
	return c.pvInformer
}

func (c *Clientset) PVCInformer() cache.SharedIndexInformer {
	return c.pvcInformer
}

func (c *Clientset) SCInformer() cache.SharedIndexInformer {
	return c.scInformer
}

func (c *Clientset) StartInformers(stopCh <-chan struct{}) {
	if c.informerFactory != nil {
		c.informerFactory.Start(stopCh)
	}
}

func (c *Clientset) WaitForCacheSync(stopCh <-chan struct{}) bool {
	if c.informerFactory == nil {
		return true
	}
	results := c.informerFactory.WaitForCacheSync(stopCh)
	for _, synced := range results {
		if !synced {
			return false
		}
	}
	return true
}

func (c *Clientset) ConfigureNodeLister(ctx context.Context, nodeName string) {
	trim := func(obj interface{}) (interface{}, error) {
		if accessor, err := meta.Accessor(obj); err == nil {
			if accessor.GetManagedFields() != nil {
				accessor.SetManagedFields(nil)
			}
		}

		nodeObj, ok := obj.(*corev1.Node)
		if !ok {
			return obj, nil
		}

		newLabels := map[string]string{}
		if val, ok := nodeObj.ObjectMeta.Labels[GkeMetaDataServerKey]; ok {
			newLabels[GkeMetaDataServerKey] = val
		}
		if val, ok := nodeObj.ObjectMeta.Labels[MachineTypeKey]; ok {
			newLabels[MachineTypeKey] = val
		}

		newStatus := corev1.NodeStatus{
			Allocatable: nodeObj.Status.Allocatable,
		}

		newAnnotations := map[string]string{}
		if val, ok := nodeObj.ObjectMeta.Annotations[GkeAppliedNodeLabelsAnnotationKey]; ok && val != "" {
			newAnnotations[GkeAppliedNodeLabelsAnnotationKey] = val
		}

		nodeObj.Spec = corev1.NodeSpec{}
		nodeObj.Status = newStatus
		nodeObj.ObjectMeta.Annotations = newAnnotations
		nodeObj.ObjectMeta.Labels = newLabels

		return obj, nil
	}

	var factory informers.SharedInformerFactory
	if c.informerFactory != nil {
		factory = c.informerFactory
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(
			c.k8sClients,
			time.Duration(c.informerResyncDurationSec)*time.Second,
			informers.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.FieldSelector = "metadata.name=" + nodeName
			}),
			informers.WithTransform(trim),
		)
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
	}

	c.nodeLister = factory.Core().V1().Nodes().Lister()
}

func (c *Clientset) ConfigurePVLister(ctx context.Context) {
	trim := func(obj any) (any, error) {
		pvObj, ok := obj.(*corev1.PersistentVolume)
		if !ok || pvObj == nil {
			return obj, nil
		}
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:        pvObj.ObjectMeta.Name,
				Annotations: pvObj.ObjectMeta.Annotations,
				Labels:      pvObj.ObjectMeta.Labels,
			},
			Spec: corev1.PersistentVolumeSpec{
				StorageClassName: pvObj.Spec.StorageClassName,
				ClaimRef:         pvObj.Spec.ClaimRef,
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: pvObj.Spec.CSI,
				},
				MountOptions: pvObj.Spec.MountOptions,
			},
		}, nil
	}

	var factory informers.SharedInformerFactory
	if c.informerFactory != nil {
		factory = c.informerFactory
		c.pvInformer = factory.Core().V1().PersistentVolumes().Informer().(cache.SharedIndexInformer)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(
			c.k8sClients,
			time.Duration(c.informerResyncDurationSec)*time.Second,
			informers.WithTweakListOptions(func(options *metav1.ListOptions) {
				if !c.runController {
					options.LabelSelector = fmt.Sprintf("%s=%s", webhook.GcsfuseProfilesManagedLabel, util.TrueStr)
				}
			}),
			informers.WithTransform(trim),
		)
		// Request the informer to ensure it's tracked by the factory
		factory.Core().V1().PersistentVolumes().Informer()
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
	}
	c.pvLister = factory.Core().V1().PersistentVolumes().Lister()
}

func (c *Clientset) ConfigurePVCLister(ctx context.Context) {
	trim := func(obj any) (any, error) {
		pvcObj, ok := obj.(*corev1.PersistentVolumeClaim)
		if !ok || pvcObj == nil {
			return obj, nil
		}
		var annotations map[string]string
		if val, ok := pvcObj.ObjectMeta.Annotations[webhook.MounterPodTemplateAnnotation]; ok {
			annotations = map[string]string{
				webhook.MounterPodTemplateAnnotation: val,
			}
		}
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        pvcObj.ObjectMeta.Name,
				Namespace:   pvcObj.ObjectMeta.Namespace,
				Annotations: annotations,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				VolumeName: pvcObj.Spec.VolumeName,
			},
		}, nil
	}

	var factory informers.SharedInformerFactory
	if c.informerFactory != nil {
		factory = c.informerFactory
		c.pvcInformer = factory.Core().V1().PersistentVolumeClaims().Informer().(cache.SharedIndexInformer)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(
			c.k8sClients,
			time.Duration(c.informerResyncDurationSec)*time.Second,
			informers.WithTransform(trim),
		)
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
	}
	c.pvcLister = factory.Core().V1().PersistentVolumeClaims().Lister()
}

func (c *Clientset) ConfigureSCLister(ctx context.Context) {
	trim := func(obj any) (any, error) {
		scObj, ok := obj.(*storagev1.StorageClass)
		if !ok || scObj == nil {
			return obj, nil
		}
		return &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:   scObj.ObjectMeta.Name,
				Labels: scObj.ObjectMeta.Labels,
			},
			MountOptions: scObj.MountOptions,
			Parameters:   scObj.Parameters,
		}, nil
	}

	var factory informers.SharedInformerFactory
	if c.informerFactory != nil {
		factory = c.informerFactory
		c.scInformer = factory.Storage().V1().StorageClasses().Informer().(cache.SharedIndexInformer)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(
			c.k8sClients,
			time.Duration(c.informerResyncDurationSec)*time.Second,
			informers.WithTransform(trim),
		)
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
	}
	c.scLister = factory.Storage().V1().StorageClasses().Lister()
}

func (c *Clientset) ConfigurePodTemplateLister(ctx context.Context) {
	trim := func(obj any) (any, error) {
		podTemplateObj, ok := obj.(*corev1.PodTemplate)
		if !ok || podTemplateObj == nil {
			return obj, nil
		}
		return &corev1.PodTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podTemplateObj.ObjectMeta.Name,
				Namespace: podTemplateObj.Namespace,
			},
			Template: corev1.PodTemplateSpec{
				Spec: podTemplateObj.Template.Spec,
			},
		}, nil
	}

	var factory informers.SharedInformerFactory
	if c.informerFactory != nil {
		factory = c.informerFactory
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(
			c.k8sClients,
			time.Duration(c.informerResyncDurationSec)*time.Second,
			informers.WithTransform(trim),
		)
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
	}
	c.podTemplateLister = factory.Core().V1().PodTemplates().Lister()
}

func New(kubeconfigPath string, informerResyncDurationSec int, runController bool) (Interface, error) {
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

	k8sClients, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to configure k8s client: %w", err)
	}

	c := &Clientset{
		k8sClients:                k8sClients,
		informerResyncDurationSec: informerResyncDurationSec,
		runController:             runController,
	}

	if runController {
		c.informerFactory = informers.NewSharedInformerFactory(k8sClients, time.Duration(informerResyncDurationSec)*time.Second)
	}

	return c, nil
}

// PodNameIndexFunc is an IndexFunc that indexes pods by their name.
func PodNameIndexFunc(obj interface{}) ([]string, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return []string{}, nil
	}
	return []string{pod.Name}, nil
}

func (c *Clientset) ConfigurePodLister(ctx context.Context, nodeName string) {
	trim := func(obj interface{}) (interface{}, error) {
		if accessor, err := meta.Accessor(obj); err == nil {
			if accessor.GetManagedFields() != nil {
				accessor.SetManagedFields(nil)
			}
		}

		podObj, ok := obj.(*corev1.Pod)
		if !ok {
			return obj, nil
		}

		if c.runController {
			// For controller, we trim a bit more than nothing but keep what scanner needs.
			return &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        podObj.Name,
					Namespace:   podObj.Namespace,
					UID:         podObj.UID,
					Labels:      podObj.Labels,
					Annotations: podObj.Annotations,
				},
				Spec: corev1.PodSpec{
					Volumes:         podObj.Spec.Volumes,
					SchedulingGates: podObj.Spec.SchedulingGates,
					NodeName:        podObj.Spec.NodeName,
				},
				Status: corev1.PodStatus{
					Phase: podObj.Status.Phase,
				},
			}, nil
		}

		var newContainers []corev1.Container
		for _, cont := range podObj.Spec.Containers {
			if cont.Name == util.MounterPodNamePrefix {
				newContainers = append(newContainers, cont)
				continue
			}
			newContainers = append(newContainers, corev1.Container{
				Name:            cont.Name,
				SecurityContext: cont.SecurityContext,
				VolumeMounts:    cont.VolumeMounts,
			})
		}

		var newInitContainers []corev1.Container
		for _, cont := range podObj.Spec.InitContainers {
			if cont.Name == webhook.GcsFuseSidecarName {
				newInitContainers = append(newInitContainers, cont)
				continue
			}
			newInitContainers = append(newInitContainers, corev1.Container{
				Name:            cont.Name,
				SecurityContext: cont.SecurityContext,
				VolumeMounts:    cont.VolumeMounts,
			})
		}

		podObj.Spec = corev1.PodSpec{
			NodeName:       podObj.Spec.NodeName,
			Volumes:        podObj.Spec.Volumes,
			Containers:     newContainers,
			InitContainers: newInitContainers,
			RestartPolicy:  podObj.Spec.RestartPolicy,
			HostNetwork:    podObj.Spec.HostNetwork,
		}

		return obj, nil
	}

	var factory informers.SharedInformerFactory
	if c.informerFactory != nil {
		factory = c.informerFactory
		// Use transform on the shared factory's informer if possible, but informers.SharedInformerFactory doesn't support setting transform after creation easily.
		// Actually, we can use informers.WithTransform when creating the factory.
		// Since we want to optimize memory, let's just use the factory as is.
		// TODO(urielguzman): Consider if we should have a global transform for all pods in the controller.
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(
			c.k8sClients,
			time.Duration(c.informerResyncDurationSec)*time.Second,
			informers.WithTweakListOptions(func(options *metav1.ListOptions) {
				if nodeName != "" {
					options.FieldSelector = "spec.nodeName=" + nodeName
				}
			}),
			informers.WithTransform(trim),
		)
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
	}

	c.podLister = factory.Core().V1().Pods().Lister()
	c.podInformer = factory.Core().V1().Pods().Informer().(cache.SharedIndexInformer)

	if c.runController {
		err := c.podInformer.AddIndexers(cache.Indexers{
			PodNameIndex: PodNameIndexFunc,
		})
		if err != nil {
			klog.Fatalf("Failed to add podName indexer: %v", err)
		}
	}
}

func (c *Clientset) GetPodsByName(podName string) ([]*corev1.Pod, error) {
	if c.podInformer == nil {
		return nil, fmt.Errorf("podInformer is not initialized")
	}

	objs, err := c.podInformer.GetIndexer().ByIndex(PodNameIndex, podName)
	if err != nil {
		return nil, err
	}

	pods := make([]*corev1.Pod, 0, len(objs))
	for _, obj := range objs {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return nil, fmt.Errorf("unexpected type in indexer: got %T, want *corev1.Pod", obj)
		}
		pods = append(pods, pod)
	}

	return pods, nil
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

func (c *Clientset) ListPVs() ([]*corev1.PersistentVolume, error) {
	if c.pvLister == nil {
		return nil, errors.New("pv informer is not ready")
	}
	return c.pvLister.List(labels.Everything())
}

func (c *Clientset) GetPVC(namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	if c.pvcLister == nil {
		return nil, errors.New("pvc informer is not ready")
	}
	return c.pvcLister.PersistentVolumeClaims(namespace).Get(name)
}

func (c *Clientset) GetSC(name string) (*storagev1.StorageClass, error) {
	if c.scLister == nil {
		return nil, errors.New("sc informer is not ready")
	}
	return c.scLister.Get(name)
}

func (c *Clientset) GetPodTemplate(namespace, name string) (*corev1.PodTemplate, error) {
	if c.podTemplateLister == nil {
		return nil, errors.New("pod template informer is not ready")
	}
	return c.podTemplateLister.PodTemplates(namespace).Get(name)
}

func (c *Clientset) CreateServiceAccountToken(ctx context.Context, namespace, name string, tokenRequest *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
	resp, err := c.k8sClients.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, name, tokenRequest, metav1.CreateOptions{})
	return resp, err
}

func (c *Clientset) GetGCPServiceAccountName(ctx context.Context, namespace, name string) (string, error) {
	resp, err := c.k8sClients.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to call Kubernetes ServiceAccount.Get API: %w", err)
	}
	return resp.Annotations["iam.gke.io/gcp-service-account"], nil
}
