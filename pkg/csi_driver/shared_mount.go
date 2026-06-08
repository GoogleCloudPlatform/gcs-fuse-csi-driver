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
	"time"

	"context"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

const (
	mounterPodNamePrefix    = "gcsfusecsi-mount"
	mounterPodPriorityClass = "gcsfusecsi-mount-priority"
	mounterPodMountDir      = "mount-dir"
)

var (
	mounterPodPollInterval = 1 * time.Second
)

// mounterPodConfig holds the configuration parameters required to define and manage a mounter pod.
type mounterPodConfig struct {
	PodName   string // The name to assign to the mounter pod.
	NodeID    string // The specific node ID where the pod should be scheduled.
	Namespace string // The Kubernetes namespace in which to create the pod.
	image     string // The image for the mounter pod binary.
}

// sharedMount checks if the VolumeContext enables the shared node mount feature
// by checking the sharedMount: true volumeAttribute.
func sharedMount(vc map[string]string) bool {
	if v, ok := vc[VolumeContextSharedNodeMount]; ok && v == util.TrueStr {
		return true
	}
	return false
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
	return fmt.Sprintf("%s-%s", mounterPodNamePrefix, sha1Hash)
}

// pvFromVolumeID finds the PersistentVolume in the cluster that corresponds to the given CSI volumeID.
func pvFromVolumeID(clientset clientset.Interface, volumeID string) (*corev1.PersistentVolume, error) {
	if clientset == nil {
		return nil, fmt.Errorf("clientset is nil")
	}
	pvs, err := clientset.ListPV()
	if err != nil {
		return nil, err
	}
	for _, pv := range pvs {
		if pv != nil && pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle == volumeID {
			return pv, nil
		}
	}
	return nil, fmt.Errorf("no pv found for volumeID: %q", volumeID)
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
	pod, err := clientset.K8sClient().CoreV1().Pods(config.Namespace).Get(ctx, config.PodName, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return status.Errorf(codes.Internal, "failed to get mounter pod %s/%s: %v", config.Namespace, config.PodName, err)
	}
	// GET always returns a pointer to the pod, even if the pod doesn't exist.
	// Therefore, we cannot rely on a nil pointer to determine the pod's existence.
	if errors.IsNotFound(err) {
		podSpec := createMounterPodSpec(config)
		if _, err = clientset.K8sClient().CoreV1().Pods(config.Namespace).Create(ctx, podSpec, metav1.CreateOptions{}); err != nil {
			if errors.IsAlreadyExists(err) {
				klog.Infof("Mounter pod %s/%s already exists.", config.Namespace, config.PodName)
				return nil
			}
			return status.Errorf(codes.Internal, "failed to create mounter pod %s/%s: %v", config.Namespace, config.PodName, err)
		}
		return nil
	}
	// If the mounter pod is marked for deletion, prevent ControllerPublishVolume from succeeding.
	if pod == nil {
		return status.Errorf(codes.Internal, "mounter pod %s/%s was found but returned as nil", config.Namespace, config.PodName)
	}
	if pod.ObjectMeta.DeletionTimestamp != nil {
		return status.Errorf(
			codes.Aborted,
			"Mounter pod %s/%s is marked for deletion. Waiting for pod deletion to complete.",
			config.Namespace, config.PodName,
		)
	}
	klog.Infof("Mounter pod %s/%s already exists.", config.Namespace, config.PodName)
	return nil
}

// createMounterPodSpec returns the pod spec for the mounter pod.
func createMounterPodSpec(config *mounterPodConfig) *corev1.Pod {
	spec := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.PodName,
			Namespace: config.Namespace,
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				// Use NodeSelector rather than NodeName in the mounter pod spec,
				// since NodeName will bypass kube-scheduler.
				"kubernetes.io/hostname": config.NodeID,
				"kubernetes.io/os":       "linux",
			},
			PriorityClassName: mounterPodPriorityClass,
			Containers: []corev1.Container{
				{
					Name:            mounterPodNamePrefix,
					Image:           config.image,
					ImagePullPolicy: corev1.PullAlways,
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr.To(true),
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:             mounterPodMountDir,
							MountPath:        util.KubeletDir,
							MountPropagation: ptr.To(corev1.MountPropagationBidirectional),
						},
						{
							Name:      util.SidecarContainerTmpVolumeName,
							MountPath: util.SidecarContainerTmpVolumePath,
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: mounterPodMountDir,
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: util.KubeletDir,
							Type: ptr.To(corev1.HostPathDirectoryOrCreate),
						},
					},
				},
				{
					Name: util.SidecarContainerTmpVolumeName,
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
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

// waitForMounterServer waits for the mounter pod to reach the Running phase and for the gRPC server to
// become fully operational by ensuring the socket file is available. It returns an error if the
// operation times out.
func waitForMounterServer(ctx context.Context, clientset clientset.Interface, mounterPodConfig *mounterPodConfig, mounterSocketFile string) error {
	klog.Infof("Waiting for mounter pod %s/%s to start running and the mounter pod socket file %q to become available. Polling every %s",
		mounterPodConfig.Namespace, mounterPodConfig.PodName, mounterSocketFile, pollInterval)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var pod *corev1.Pod
	var err error
	podRunning := false

	for {
		select {
		case <-ticker.C:
			// Check if the gRPC server socket file exists.
			if _, err = os.Stat(mounterSocketFile); err == nil {
				klog.Infof("Mounter pod %s/%s socket file %q is available.", mounterPodConfig.Namespace, mounterPodConfig.PodName, mounterSocketFile)
				return nil
			}

			if !os.IsNotExist(err) {
				return fmt.Errorf("error checking socket file %q for mounter pod %s/%s: %w", mounterSocketFile, mounterPodConfig.Namespace, mounterPodConfig.PodName, err)
			}

			if !podRunning {
				klog.Infof("Mounter pod socket file %q not found. Checking mounter pod %s/%s status.", mounterSocketFile, mounterPodConfig.Namespace, mounterPodConfig.PodName)
				// Get the current status of the mounter pod.
				pod, err = mustGetMounterPod(clientset, ctx, mounterPodConfig)
				if err != nil {
					return err
				}
				if pod.Status.Phase == corev1.PodRunning {
					klog.Infof("Mounter pod %s/%s is running.", mounterPodConfig.Namespace, mounterPodConfig.PodName)
					podRunning = true
				} else if pod.Status.Phase != corev1.PodPending {
					return status.Errorf(codes.Internal, "mounter pod %s/%s found in an unexpected status: %+v", mounterPodConfig.Namespace, mounterPodConfig.PodName, pod.Status)
				} else {
					klog.Infof("Mounter pod %s/%s found with status: %+v. Waiting for pod to start running...", mounterPodConfig.Namespace, mounterPodConfig.PodName, pod.Status)
				}
			}
		case <-ctx.Done():
			code := codes.DeadlineExceeded
			if ctx.Err() == context.Canceled {
				code = codes.Canceled
			}
			errMsg := fmt.Sprintf("timeout waiting for mounter pod %s/%s gRPC server to become available at %s: %v",
				mounterPodConfig.Namespace, mounterPodConfig.PodName, mounterSocketFile, ctx.Err())
			if pod != nil {
				// The pod may be nil if the timeout is too sudden (e.g. less than a second)
				errMsg = fmt.Sprintf("%s, pod status: %+v", errMsg, pod.Status)
			}
			return status.Error(code, errMsg)
		}
	}
}

// getMounterPod returns the mounter pod if it exists, or returns an error.
func getMounterPod(clientset clientset.Interface, ctx context.Context, config *mounterPodConfig) (*corev1.Pod, error) {
	pod, err := clientset.GetPod(config.Namespace, config.PodName)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return pod, nil
}

// mustGetMounterPod returns the pod if it exists or returns an error if it doesn't.
// This function strictly expects the mounter pod to exist.
func mustGetMounterPod(clientset clientset.Interface, ctx context.Context, config *mounterPodConfig) (*corev1.Pod, error) {
	pod, err := getMounterPod(clientset, ctx, config)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if pod == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "mounter pod %s/%s expected to exist but was not found", config.Namespace, config.PodName)
	}
	return pod, nil
}
