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

	profilesutil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
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
	ConfigurePodLister(ctx context.Context, nodeName string, eventHandlerFuncs *cache.ResourceEventHandlerFuncs)
	ConfigureNodeLister(ctx context.Context, nodeName string)
	ConfigurePVLister(ctx context.Context, eventHandlerFuncs *cache.ResourceEventHandlerFuncs)
	ConfigurePVCLister(ctx context.Context)
	ConfigureSCLister(ctx context.Context)
	ConfigurePodTemplateLister(ctx context.Context)
	GetPod(namespace, name string) (*corev1.Pod, error)
	GetMounterPod(namespace, name string) (*corev1.Pod, error)
	GetMounterPodsByName(name string) ([]*corev1.Pod, error)
	GetNode(name string) (*corev1.Node, error)
	GetPV(name string) (*corev1.PersistentVolume, error)
	GetPVC(namespace string, name string) (*corev1.PersistentVolumeClaim, error)
	GetSC(name string) (*storagev1.StorageClass, error)
	GetPodTemplate(namespace, name string) (*corev1.PodTemplate, error)
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
	mounterPodLister          listersv1.PodLister
	mounterPodInformer        cache.SharedIndexInformer
	nodeLister                listersv1.NodeLister
	pvLister                  listersv1.PersistentVolumeLister
	pvcLister                 listersv1.PersistentVolumeClaimLister
	scLister                  storagelisters.StorageClassLister
	podTemplateLister         listersv1.PodTemplateLister
	informerResyncDurationSec int
	runController             bool
	enableGCSFuseProfiles     bool
	enableSharedMount         bool
}

const (
	GkeMetaDataServerKey              = "iam.gke.io/gke-metadata-server-enabled"
	MachineTypeKey                    = "node.kubernetes.io/instance-type"
	GkeAppliedNodeLabelsAnnotationKey = "node.gke.io/last-applied-node-labels"
)

func (c *Clientset) K8sClient() kubernetes.Interface {
	return c.k8sClients
}

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

func trimMap(originalMap map[string]string, keysToKeep map[string]bool) map[string]string {
	if originalMap == nil {
		return nil
	}
	var trimmedMap map[string]string
	for key := range keysToKeep {
		if val, ok := originalMap[key]; ok {
			if trimmedMap == nil {
				trimmedMap = make(map[string]string)
			}
			trimmedMap[key] = val
		}
	}
	return trimmedMap
}

func (c *Clientset) ConfigurePVLister(ctx context.Context, eventHandlerFuncs *cache.ResourceEventHandlerFuncs) {
	var annotationsToKeep = map[string]bool{
		profilesutil.AnnotationStatus:          true,
		profilesutil.AnnotationNumObjects:      true,
		profilesutil.AnnotationTotalSize:       true,
		profilesutil.AnnotationLastUpdatedTime: true,
		profilesutil.AnnotationLocationType:    true,
		profilesutil.AnnotationHNSEnabled:      true,
	}
	var labelsToKeep = map[string]bool{
		webhook.GcsfuseProfilesManagedLabel: true,
	}

	trim := func(obj any) (any, error) {
		pvObj, ok := obj.(*corev1.PersistentVolume)
		if !ok || pvObj == nil {
			return obj, nil
		}

		// Fields shared between the Node and the Controller.
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:        pvObj.ObjectMeta.Name,
				Annotations: trimMap(pvObj.ObjectMeta.Annotations, annotationsToKeep), // Only keep relevant annotations to optimize memory.
				Labels:      trimMap(pvObj.ObjectMeta.Labels, labelsToKeep),           // Only keep relevant labels to optimize memory.
			},
			Spec: corev1.PersistentVolumeSpec{
				StorageClassName: pvObj.Spec.StorageClassName, // Required to map PV to SC.
			},
		}
		if pvObj.Spec.CSI != nil {
			pv.Spec.CSI = &corev1.CSIPersistentVolumeSource{}
			if pvObj.Spec.CSI.VolumeAttributes != nil {
				pv.Spec.CSI.VolumeAttributes = pvObj.Spec.CSI.VolumeAttributes // Required to find the bucket and profile configs.
			}
			if c.runController {
				pv.Spec.CSI.Driver = pvObj.Spec.CSI.Driver             // Required to check if it's a gcsfuse volume
				pv.Spec.CSI.VolumeHandle = pvObj.Spec.CSI.VolumeHandle // Required to identify the bucket
			}
		}

		// Fields only relevant to the Controller.
		if c.runController {
			pv.ObjectMeta.UID = pvObj.ObjectMeta.UID                         // Required for the event broadcaster.
			pv.ObjectMeta.ResourceVersion = pvObj.ObjectMeta.ResourceVersion // Required for the event broadcaster.
			pv.Spec.MountOptions = pvObj.Spec.MountOptions                   // Required to find only-dir
			pv.Spec.ClaimRef = pvObj.Spec.ClaimRef                           // Required to find the PVC and its namespace
		}

		return pv, nil
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		// To prevent OOMs, the Node driver uses a server-side filter to cache only profile-managed PVs since profiles is the only feature using GetPV.
		// Leaving the Controller unfiltered to discover and patch legacy volumes.
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			if !c.runController {
				options.LabelSelector = fmt.Sprintf("%s=%s", webhook.GcsfuseProfilesManagedLabel, util.TrueStr)
			}
		}),
		informers.WithTransform(trim),
	)
	pvLister := informerFactory.Core().V1().PersistentVolumes().Lister()

	if c.runController && eventHandlerFuncs != nil {
		informerFactory.Core().V1().PersistentVolumes().Informer().AddEventHandler(eventHandlerFuncs)
	}

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	c.pvLister = pvLister
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
				Annotations: annotations, // only store the mounter pod template annotation to optimize memory.
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				VolumeName: pvcObj.Spec.VolumeName,
			},
		}, nil
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		informers.WithTransform(trim),
	)

	pvcLister := informerFactory.Core().V1().PersistentVolumeClaims().Lister()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	c.pvcLister = pvcLister
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
				Labels: scObj.ObjectMeta.Labels, // Required by the gcsfuse profiles feature to know if the StorageClass is a profile.
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

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		c.k8sClients,
		time.Duration(c.informerResyncDurationSec)*time.Second,
		informers.WithTransform(trim),
	)
	podTemplateLister := informerFactory.Core().V1().PodTemplates().Lister()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	c.podTemplateLister = podTemplateLister
}

func New(kubeconfigPath string, informerResyncDurationSec int, runController bool, kubeAPIBurst int, kubeAPIQPS float64, enableGCSFuseProfiles, enableSharedMount bool) (Interface, error) {
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

	if runController {
		rc.QPS = float32(kubeAPIQPS)
		rc.Burst = kubeAPIBurst
		klog.Infof("KubeClient QPS: %f, Burst: %d", rc.QPS, rc.Burst)
	}

	clientset, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to configure k8s client: %w", err)
	}

	return &Clientset{
		k8sClients:                clientset,
		informerResyncDurationSec: informerResyncDurationSec,
		runController:             runController,
		enableGCSFuseProfiles:     enableGCSFuseProfiles,
		enableSharedMount:         enableSharedMount}, nil
}

// PodNameIndexFunc is an IndexFunc that indexes pods by their name.
func PodNameIndexFunc(obj interface{}) ([]string, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return []string{}, nil
	}
	return []string{pod.Name}, nil
}

func (c *Clientset) configureControllerPodLister(ctx context.Context, nodeName string, eventHandlerFuncs *cache.ResourceEventHandlerFuncs) {
	// podListerForLabel creates a SharedInformerFactory that filters pods by the given label key.
	// This is the most memory efficient way to filter pods with different labels on the server side, since Kubernetes
	// doesn't support label selector OR statements.
	podListerForLabel := func(ctx context.Context, labelKey string, eventHandlerFuncs *cache.ResourceEventHandlerFuncs, indexers cache.Indexers) (listersv1.PodLister, cache.SharedIndexInformer) {
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
			if !ok || podObj == nil {
				return obj, nil
			}

			return &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              podObj.ObjectMeta.Name,
					Namespace:         podObj.ObjectMeta.Namespace,
					DeletionTimestamp: podObj.DeletionTimestamp, // Needed by shared mount to check if the pod is being deleted.
				},
				Spec: corev1.PodSpec{
					Volumes:         podObj.Spec.Volumes,         // Needed by profiles to check PVs requiring scans.
					SchedulingGates: podObj.Spec.SchedulingGates, // Needed by profiles to remove scheduling gates.
					NodeName:        podObj.Spec.NodeName,        // Needed by shared mount to check if the pod is scheduled.
				},
				Status: podObj.Status, // Needed by shared mount to check if the pod scheduled.
			}, nil
		}

		factory := informers.NewSharedInformerFactoryWithOptions(
			c.k8sClients,
			time.Duration(c.informerResyncDurationSec)*time.Second,
			informers.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.LabelSelector = fmt.Sprintf("%s=%s", labelKey, util.TrueStr)
			}),
			informers.WithTransform(trim),
		)
		if eventHandlerFuncs != nil {
			factory.Core().V1().Pods().Informer().AddEventHandler(eventHandlerFuncs)
		}
		if indexers != nil {
			if err := factory.Core().V1().Pods().Informer().AddIndexers(indexers); err != nil {
				klog.Fatalf("Failed to add indexers: %v", err)
			}
		}
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
		return factory.Core().V1().Pods().Lister(), factory.Core().V1().Pods().Informer()
	}

	if c.enableGCSFuseProfiles {
		c.podLister, _ = podListerForLabel(ctx, webhook.GcsfuseProfilesManagedLabel, eventHandlerFuncs, nil)
	}
	if c.enableSharedMount {
		c.mounterPodLister, c.mounterPodInformer = podListerForLabel(ctx, webhook.SharedMountLabel, nil, cache.Indexers{
			// Add an indexer to allow efficient lookup of pods by name across all namespaces.
			// This is used by the controller to quickly find specific pods (e.g., mounter pods)
			// by their known name without needing to iterate through all pods or know the namespace.
			PodNameIndex: PodNameIndexFunc,
		})
	}
}

func (c *Clientset) ConfigurePodLister(ctx context.Context, nodeName string, eventHandlerFuncs *cache.ResourceEventHandlerFuncs) {
	if c.runController {
		c.configureControllerPodLister(ctx, nodeName, eventHandlerFuncs)
		return
	}

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
		if !ok || podObj == nil {
			return obj, nil
		}

		var newContainers []corev1.Container
		for _, cont := range podObj.Spec.Containers {
			if cont.Name == util.MounterPodNamePrefix {
				newContainers = append(newContainers, cont)
				continue
			}
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
			if nodeName != "" {
				options.FieldSelector = "spec.nodeName=" + nodeName
			}
		}),
		informers.WithTransform(trim),
	)

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())
	c.podLister = informerFactory.Core().V1().Pods().Lister()
}

// GetMounterPodsByName retrieves all mounter pods with the given name across all namespaces.
// This function is used to find mounter pods that are labeled with the shared mount label.
// It is important to note that this function only retrieves pods that are labeled with the shared mount label,
// and is currently only initialized in the controller. When getting the mounter pod from the node driver, call GetPod instead, which tracks
// all pods, including the mounter pods.
func (c *Clientset) GetMounterPodsByName(podName string) ([]*corev1.Pod, error) {
	if c.mounterPodInformer == nil {
		return nil, fmt.Errorf("mounterPodInformer is not initialized")
	}

	// objs is a slice of interface{}, each element is a *corev1.Pod
	objs, err := c.mounterPodInformer.GetIndexer().ByIndex(PodNameIndex, podName)
	if err != nil {
		return nil, err
	}

	pods := make([]*corev1.Pod, 0, len(objs))
	for _, obj := range objs {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			// This should not happen if the indexer is set up correctly,
			// but good practice to handle nonetheless.
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

// GetMounterPod retrieves a mounter pod from the informer cache by namespace and name.
// This function is used to check if a mounter pod exists and its status.
// It is important to note that this function only retrieves pods that are labeled with the shared mount label,
// and is currently only initialized in the controller. When getting the mounter pod from the node driver, call GetPod instead, which tracks
// all pods, including the mounter pods.
func (c *Clientset) GetMounterPod(namespace, name string) (*corev1.Pod, error) {
	if c.mounterPodLister == nil {
		return nil, errors.New("mounter pod informer is not ready")
	}

	return c.mounterPodLister.Pods(namespace).Get(name)
}

func (c *Clientset) GetNode(name string) (*corev1.Node, error) {
	if c.nodeLister == nil {
		return nil, errors.New("node informer is not ready")
	}

	return c.nodeLister.Get(name)
}

// GetPV retrieves a PersistentVolume from the informer cache by name.
// IMPORTANT: In the Node driver, the PV informer cache is intentionally filtered down
// to ONLY track PVs that utilize the GCS Fuse profiles feature (via the gke-gcsfuse/profile-managed label)
// to minimize the memory footprint in large-scale clusters.
// Do not attempt to use this function from the Node driver to look up generic PVs or non-profile GCS Fuse PVs,
// as they are explicitly prevented from entering the Node's cache.
func (c *Clientset) GetPV(name string) (*corev1.PersistentVolume, error) {
	if c.pvLister == nil {
		return nil, errors.New("pv informer is not ready")
	}

	return c.pvLister.Get(name)
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
