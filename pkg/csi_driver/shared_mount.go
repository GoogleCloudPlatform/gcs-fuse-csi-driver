/*
Copyright 2018 The Kubernetes Authors.
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

package driver

import (
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"context"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

const (
	mounterPodPriorityClass       = "gcsfusecsi-mount-priority"
	mounterPodMountDir            = "mount-dir"
	mounterPodSocketFile          = "mounter.sock"
	mounterPodManagedImageKeyword = "managed"
	mounterPodSocketDir           = "mount-socket"
)

var (
	mounterPodPollInterval = 1 * time.Second
)

// mounterPodConfig holds the configuration parameters required to define and manage a mounter pod.
type mounterPodConfig struct {
	podName            string                       // The name to assign to the mounter pod.
	nodeID             string                       // The specific node ID where the pod should be scheduled.
	namespace          string                       // The Kubernetes namespace in which to create the pod.
	image              string                       // The image for the mounter pod binary.
	serviceAccountName string                       // The KSA name for the mounter pod.
	resources          *corev1.ResourceRequirements // The resource requirements for the mounter pod container.
	volumes            []corev1.Volume              // The volumes for the mounter pod.
	profilesEnabled    bool                         // Whether the profiles feature is enabled.
	hostNetworkEnabled bool                         // Whether hostNetwork is enabled for the mounter pod.
	tokenAudience      string                       // Token audience for projected service account volume.
	dnsPolicy          corev1.DNSPolicy             // The DNS policy for the mounter pod.
}

// sharedMount checks if the VolumeContext enables the shared node mount feature
// by checking the sharedMount: true volumeAttribute.
func (driver *GCSDriver) sharedMount(vc map[string]string) bool {
	if driver.config.FeatureOptions == nil ||
		driver.config.FeatureOptions.SharedMountOptions == nil ||
		!driver.config.FeatureOptions.SharedMountOptions.Enabled {
		return false
	}
	if v, ok := vc[VolumeContextSharedNodeMount]; ok && v == util.TrueStr {
		return true
	}
	return false
}

// mounterPodImage extracts the container image specified for the mounter pod container
// with name matching MounterPodNamePrefix from the given Pod.
func mounterPodImage(pod *corev1.Pod) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("pod is nil")
	}
	for _, container := range pod.Spec.Containers {
		if strings.HasPrefix(container.Name, util.MounterPodNamePrefix) {
			return container.Image, nil
		}
	}
	return "", fmt.Errorf("failed to find mounter container with prefix %q", util.MounterPodNamePrefix)
}

// createMounterPodName returns a unique name for the mounter pod. The name is composed by
// the node and volume IDs, evaluated on a SHA1 hash for length shortening.
func createMounterPodName(nodeID, volumeID string) string {
	str := fmt.Sprintf("%s_%s", nodeID, volumeID)
	h := sha1.New()
	// Write the string to the hash
	io.WriteString(h, str)
	// Convert the byte slice to a hexadecimal string
	sha1Hash := fmt.Sprintf("%x", h.Sum(nil))
	return fmt.Sprintf("%s-%s", util.MounterPodNamePrefix, sha1Hash)
}

// createMounterPod handles the creation of the mounter pod using the Kubernetes API client.
func createMounterPod(clientset clientset.Interface, ctx context.Context, config *mounterPodConfig) error {
	if clientset == nil || clientset.K8sClient() == nil {
		return status.Error(codes.Internal, "kubernetes client is uninitialized")
	}
	if config == nil {
		return status.Error(codes.Internal, "mounter pod config cannot be nil")
	}
	// Check if the mounter pod already exists, but was marked for deletion.
	// This requires calling the API server directly to retrieve the most up-to-date pod status.
	pod, err := clientset.K8sClient().CoreV1().Pods(config.namespace).Get(ctx, config.podName, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return status.Errorf(codes.Internal, "failed to get mounter pod %s/%s: %v", config.namespace, config.podName, err)
	}
	// GET always returns a pointer to the pod, even if the pod doesn't exist.
	// Therefore, we cannot rely on a nil pointer to determine the pod's existence.
	if errors.IsNotFound(err) {
		podSpec := createMounterPodSpec(config)
		if _, err = clientset.K8sClient().CoreV1().Pods(config.namespace).Create(ctx, podSpec, metav1.CreateOptions{}); err != nil {
			if errors.IsAlreadyExists(err) {
				klog.Infof("Mounter pod %s/%s already exists.", config.namespace, config.podName)
				return nil
			}
			return status.Errorf(codes.Internal, "failed to create mounter pod %s/%s: %v", config.namespace, config.podName, err)
		}
		return nil
	}
	// If the mounter pod is marked for deletion, prevent ControllerPublishVolume from succeeding.
	if pod == nil {
		return status.Errorf(codes.Internal, "mounter pod %s/%s was found but returned as nil", config.namespace, config.podName)
	}
	if pod.ObjectMeta.DeletionTimestamp != nil {
		return status.Errorf(
			codes.Aborted,
			"Mounter pod %s/%s is marked for deletion. Waiting for pod deletion to complete.",
			config.namespace, config.podName,
		)
	}
	klog.Infof("Mounter pod %s/%s already exists.", config.namespace, config.podName)
	return nil
}

// createMounterPodSpec returns the pod spec for the mounter pod.
func createMounterPodSpec(config *mounterPodConfig) *corev1.Pod {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:             mounterPodMountDir,
			MountPath:        util.KubeletDir,
			MountPropagation: ptr.To(corev1.MountPropagationBidirectional),
		},
		{
			Name:      util.SidecarContainerTmpVolumeName,
			MountPath: util.SidecarContainerTmpVolumePath,
		},
		{
			Name:      webhook.SidecarContainerBufferVolumeName,
			MountPath: webhook.SidecarContainerBufferVolumeMountPath,
		},
		{
			Name:      webhook.SidecarContainerCacheVolumeName,
			MountPath: webhook.SidecarContainerCacheVolumeMountPath,
		},
	}
	if config.profilesEnabled {
		if !webhook.VolumeMountExists(volumeMounts, webhook.SidecarContainerFileCacheEphemeralDiskVolumeName) {
			volumeMounts = append(volumeMounts, webhook.EphemeralFileCacheVolumeMount)
		}
		if !webhook.VolumeMountExists(volumeMounts, webhook.SidecarContainerFileCacheRamDiskVolumeName) {
			volumeMounts = append(volumeMounts, webhook.RamFileCacheVolumeMount)
		}
	}

	if config.hostNetworkEnabled {
		volumeMounts = append(volumeMounts, webhook.SATokenVolumeMount)
	}

	labels := map[string]string{
		webhook.SharedMountLabel: util.TrueStr,
	}
	if webhook.VolumeExists(config.volumes, webhook.SidecarContainerCacheVolumeName) {
		labels[webhook.GcsfuseCacheCreatedByUserLabel] = util.TrueStr
	}

	spec := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.podName,
			Namespace: config.namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				// Use NodeSelector rather than NodeName in the mounter pod spec,
				// since NodeName will bypass kube-scheduler.
				"kubernetes.io/hostname": config.nodeID,
				"kubernetes.io/os":       "linux",
			},
			PriorityClassName: mounterPodPriorityClass,
			HostNetwork:       config.hostNetworkEnabled,
			Containers: []corev1.Container{
				{
					Name:            util.MounterPodNamePrefix,
					Image:           config.image,
					ImagePullPolicy: corev1.PullAlways,
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr.To(true),
					},
					VolumeMounts: volumeMounts,
				},
			},
			Volumes: mounterPodVolumes(config),
			Tolerations: []corev1.Toleration{
				{
					//  https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/
					//  See "special case". This will tolerate everything. Mounter pod should
					//  be possible to be scheduled on all nodes where the workloads are scheduled.
					Operator: corev1.TolerationOpExists,
				},
			},
		},
	}

	if config.serviceAccountName != "" {
		spec.Spec.ServiceAccountName = config.serviceAccountName
	}

	if config.dnsPolicy != "" {
		spec.Spec.DNSPolicy = config.dnsPolicy
	}

	spec.Spec.Containers[0].Resources = *mounterPodResources(config)

	return spec
}

// waitForMounterPodScheduled wait for mounter pod to be scheduled.
func waitForMounterPodScheduled(clientset clientset.Interface, ctx context.Context, namespace, podName, nodeID string) error {
	if clientset == nil {
		return status.Error(codes.Internal, "kubernetes client is uninitialized")
	}

	checkIfScheduled := func() (bool, error) {
		pod, err := clientset.GetPod(namespace, podName)
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, status.Errorf(codes.Internal, "failed to get mounter pod %s/%s: %v", namespace, podName, err)
		}
		if pod != nil && pod.Spec.NodeName != "" {
			if pod.Spec.NodeName != nodeID {
				return false, status.Errorf(codes.Internal, "mounter pod %s/%s expected to be scheduled to node %q, instead, was scheduled to node %q", namespace, podName, nodeID, pod.Spec.NodeName)
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionTrue {
					klog.Infof("Mounter pod %s/%s has been scheduled to node %s", namespace, podName, pod.Spec.NodeName)
					return true, nil
				}
			}
		}
		return false, nil
	}

	// Check immediately to avoid waiting if the pod is already scheduled.
	if scheduled, err := checkIfScheduled(); err != nil || scheduled {
		return err
	}

	klog.Infof("Waiting for mounter pod %s/%s to be scheduled. Polling every %v", namespace, podName, mounterPodPollInterval)

	ticker := time.NewTicker(mounterPodPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if scheduled, err := checkIfScheduled(); err != nil || scheduled {
				return err
			}
		case <-ctx.Done():
			code := codes.DeadlineExceeded
			if ctx.Err() == context.Canceled {
				code = codes.Canceled
			}
			return status.Errorf(code, "timeout waiting for mounter pod %s/%s to be scheduled to node %q: %v", namespace, podName, nodeID, ctx.Err())
		}
	}
}

// mounterPodTemplate retrieves the referenced mounter PodTemplate for a PVC if the annotation is present.
func mounterPodTemplate(clientset clientset.Interface, pvc *corev1.PersistentVolumeClaim) (*corev1.PodTemplate, error) {
	if pvc == nil {
		return nil, status.Errorf(codes.InvalidArgument, "pvc cannot be nil")
	}
	if pvc.Annotations == nil {
		return nil, status.Errorf(codes.InvalidArgument, "mounter pod template annotation must be provided")
	}
	templateName, ok := pvc.Annotations[webhook.MounterPodTemplateAnnotation]
	if !ok || templateName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "mounter pod template annotation must be provided")
	}

	podTemplate, err := clientset.GetPodTemplate(pvc.Namespace, templateName)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return podTemplate, nil
}

// deleteMounterPod handles the deletion of the mounter pod, if it exists.
// It will retry until it is deleted or return an error if the number of retries have exceeded the limit.
// deleteMounterPod returns only after mounter pod has terminated and deleted from the API server.
func deleteMounterPod(ctx context.Context, clientset clientset.Interface, podNamespace, podName string) error {
	if clientset == nil || clientset.K8sClient() == nil {
		return status.Error(codes.Internal, "kubernetes client is uninitialized")
	}
	if err := clientset.K8sClient().CoreV1().Pods(podNamespace).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
		if errors.IsNotFound(err) {
			// If the pod is not found, it's not an error
			klog.Infof("Mounter pod %s/%s was already deleted.", podNamespace, podName)
			return nil
		}
		return status.Errorf(codes.Internal, "failed to delete pod %s/%s: %v", podNamespace, podName, err)
	}
	pollInterval := mounterPodPollInterval
	klog.Infof("Waiting for mounter pod %s/%s deletion to complete. Polling every %s",
		podNamespace, podName, pollInterval)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			code := codes.DeadlineExceeded
			if ctx.Err() == context.Canceled {
				code = codes.Canceled
			}
			return status.Errorf(code, "timed out waiting for mounter pod %s/%s to be deleted", podNamespace, podName)
		case <-ticker.C:
			if _, err := clientset.GetPod(podNamespace, podName); err != nil {
				if errors.IsNotFound(err) {
					klog.Infof("Mounter pod %s/%s was successfully deleted.", podNamespace, podName)
					return nil
				}
				return status.Errorf(codes.Internal, "failed to get pod %s/%s: %v", podNamespace, podName, err)
			}
		}
	}
}

func mounterPodResources(config *mounterPodConfig) *corev1.ResourceRequirements {
	// Instantiate maps to avoid nil map assignment panics
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			// TODO: Change the defaults once we profile the feature.
			corev1.ResourceMemory:           resource.MustParse("768Mi"),
			corev1.ResourceCPU:              resource.MustParse("750m"),
			corev1.ResourceEphemeralStorage: resource.MustParse("15Gi"),
		},
		Limits: corev1.ResourceList{},
	}

	// Guard against nil configs or nil resources
	if config == nil || config.resources == nil {
		return &resources
	}

	// Set customer overrides, if defined.
	for resourceName := range config.resources.Requests {
		setResource(&resources.Requests, config.resources.Requests, resourceName)
	}
	for resourceName := range config.resources.Limits {
		setResource(&resources.Limits, config.resources.Limits, resourceName)
	}

	return &resources
}

// mounterPodVolumes returns the list of volumes required by the mounter pod.
// This includes standard GCS FUSE volumes and the host path to the kubelet directory.
func mounterPodVolumes(config *mounterPodConfig) []corev1.Volume {
	// Get the gke-gcsfuse-tmp, gke-gcsfuse-buffer, and gke-gcsfuse-cache volumes, and allow
	// the buffer and cache to be overridden by the PodTemplate volumes.
	volumes := []corev1.Volume{}
	volumes = append(volumes, config.volumes...) // Make a copy to avoid mutating the PodTemplate volumes.
	volumes = append(volumes, webhook.GetSidecarContainerVolumeSpec(config.volumes...)...)

	// Set the /var/lib/kubelet host path, so the mounter pod can mount the staging path to the node.
	volumes = append(volumes, corev1.Volume{Name: mounterPodMountDir,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				// TODO(urielguzman): Check if we can use /var/lib/kubelet/plugins/gcsfuse.csi.storage.gke.io/
				// instead, to decrease the host path scope.
				Path: util.KubeletDir,
				Type: ptr.To(corev1.HostPathDirectoryOrCreate),
			},
		}})

	if config.hostNetworkEnabled {
		volumes = append(volumes, webhook.GetSATokenVolume(config.tokenAudience))
	}

	if config.profilesEnabled {
		if !webhook.VolumeExists(volumes, webhook.SidecarContainerFileCacheEphemeralDiskVolumeName) {
			volumes = append(volumes, webhook.EphemeralFileCacheVolume)
		}
		if !webhook.VolumeExists(volumes, webhook.SidecarContainerFileCacheRamDiskVolumeName) {
			volumes = append(volumes, webhook.RamFileCacheVolume)
		}
	}
	return volumes
}

func setResource(target *corev1.ResourceList, override corev1.ResourceList, resourceName corev1.ResourceName) {
	if target == nil {
		return
	}
	val, ok := override[resourceName]
	if !ok {
		return // No value provided, skip setting
	}

	// Initialize the underlying map if it's nil and we are inserting a value
	if *target == nil && !val.IsZero() {
		*target = make(corev1.ResourceList)
	}

	// Remove the value if "0" is specified.
	if val.IsZero() {
		if *target != nil {
			delete(*target, resourceName)
		}
	} else {
		(*target)[resourceName] = val
	}
}

// waitForMounterServer waits for the mounter pod's gRPC server to be ready.
func waitForMounterServer(ctx context.Context, clientset clientset.Interface, mounterPodNamespace, mounterPodName, podUID string, emptyDirBasePath func(string) string) error {
	if clientset == nil {
		return status.Error(codes.Internal, "kubernetes client is uninitialized")
	}
	if emptyDirBasePath == nil {
		return status.Error(codes.Internal, "emptyDirBasePath function is uninitialized")
	}

	// Construct the mounter socket file path.
	mounterSocketFilePath := filepath.Join(emptyDirBasePath(podUID), mounterPodSocketFile)
	var pod *corev1.Pod
	var err error

	checkIfReady := func() (bool, error) {
		// Get the mounter pod status.
		if pod, err = clientset.GetPod(mounterPodNamespace, mounterPodName); err != nil {
			// Mounter Pod should exist by this point.
			if errors.IsNotFound(err) {
				return false, status.Errorf(codes.FailedPrecondition, "mounter pod %s/%s expected to exist but was not found", mounterPodNamespace, mounterPodName)
			}
			return false, status.Errorf(codes.Internal, "failed to get mounter pod %s/%s: %v", mounterPodNamespace, mounterPodName, err)
		}

		// If the pod is not pending or running, it's in an unexpected state.
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			return false, status.Errorf(codes.Internal, "mounter pod %s/%s found with an unexpected status: %s", mounterPodNamespace, mounterPodName, pod.Status.Phase)
		}

		// If the pod is pending, retry.
		if pod.Status.Phase == corev1.PodPending {
			klog.Infof("Mounter pod %s/%s is still Pending. Retrying...", mounterPodNamespace, mounterPodName)
			return false, nil
		}

		// Mounter pod is running, check if the socket file is available.
		// TODO(FUECHR): Investigate symlink traversal vulnerabilities for os.Stat vs os.Lstat.
		if _, err = os.Stat(mounterSocketFilePath); err == nil {
			klog.Infof("Mounter pod %s/%s is running and socket file %q is available.", mounterPodNamespace, mounterPodName, mounterSocketFilePath)
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, status.Errorf(codes.Internal, "error checking socket file %q for mounter pod %s/%s: %v", mounterSocketFilePath, mounterPodNamespace, mounterPodName, err)
		}
		klog.Infof("Mounter pod %s/%s is running but socket file %q is not yet available. Retrying...", mounterPodNamespace, mounterPodName, mounterSocketFilePath)
		return false, nil
	}

	// Check immediately to avoid waiting if the pod and socket are already ready.
	if ready, err := checkIfReady(); err != nil || ready {
		return err
	}

	klog.Infof("Waiting for mounter pod %s/%s to start running and the mounter pod socket file %q to become available. Polling every %s",
		mounterPodNamespace, mounterPodName, mounterSocketFilePath, mounterPodPollInterval)

	ticker := time.NewTicker(mounterPodPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if ready, err := checkIfReady(); err != nil || ready {
				return err
			}
		case <-ctx.Done():
			code := codes.DeadlineExceeded
			if ctx.Err() == context.Canceled {
				code = codes.Canceled
			}
			errMsg := fmt.Sprintf("timeout waiting for mounter pod %s/%s gRPC server to become available at %s: %v",
				mounterPodNamespace, mounterPodName, mounterSocketFilePath, ctx.Err())
			if pod != nil { // Catches the case where context is cancelled before GetPod is first called.
				errMsg += fmt.Sprintf(", pod status: %+v", pod.Status)
			}
			return status.Error(code, errMsg)
		}
	}
}

func checkMounterPodErrorFile(emptyDirPath string) (codes.Code, error) {
	if emptyDirPath == "" {
		return codes.Internal, fmt.Errorf("empty dir path is empty")
	}
	errorFilePath := filepath.Join(emptyDirPath, util.ErrorFileName)
	errMsg, err := extractErrorMsgFromGcsFuseErrorFile(errorFilePath)
	if err != nil {
		return codes.Internal, err
	}

	return extractErrorFromGcsFuseErrorFile(errMsg)
}
