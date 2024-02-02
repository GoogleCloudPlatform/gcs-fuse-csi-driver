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

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	pbSanitizer "github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

const (
	CreateVolumeCSIFullMethod      = "/csi.v1.Controller/CreateVolume"
	DeleteVolumeCSIFullMethod      = "/csi.v1.Controller/DeleteVolume"
	NodePublishVolumeCSIFullMethod = "/csi.v1.Node/NodePublishVolume"
)

func NewVolumeCapabilityAccessMode(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability_AccessMode {
	return &csi.VolumeCapability_AccessMode{Mode: mode}
}

func NewControllerServiceCapability(c csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: c,
			},
		},
	}
}

func NewNodeServiceCapability(c csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
	return &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: c,
			},
		},
	}
}

func logGRPC(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	var strippedReq string
	switch info.FullMethod {
	case CreateVolumeCSIFullMethod:
		strippedReq = pbSanitizer.StripSecrets(req).String()
	case DeleteVolumeCSIFullMethod:
		strippedReq = pbSanitizer.StripSecrets(req).String()
	case NodePublishVolumeCSIFullMethod:
		if nodePublishReq, ok := req.(*csi.NodePublishVolumeRequest); ok {
			if token, ok := nodePublishReq.GetVolumeContext()[VolumeContextKeyServiceAccountToken]; ok {
				nodePublishReq.VolumeContext[VolumeContextKeyServiceAccountToken] = "***stripped***"
				strippedReq = fmt.Sprintf("%+v", nodePublishReq)
				nodePublishReq.VolumeContext[VolumeContextKeyServiceAccountToken] = token
			} else {
				strippedReq = fmt.Sprintf("%+v", req)
			}
		} else {
			klog.Errorf("failed to case req to *csi.NodePublishVolumeRequest")
		}
	default:
		strippedReq = fmt.Sprintf("%+v", req)
	}

	klog.V(4).Infof("%s called with request: %v", info.FullMethod, strippedReq)
	resp, err := handler(ctx, req)
	if err != nil {
		klog.Errorf("%s failed with error: %v", info.FullMethod, err)
	} else {
		if fmt.Sprintf("%v", resp) == "" {
			klog.V(4).Infof("%s succeeded.", info.FullMethod)
		} else {
			klog.V(4).Infof("%s succeeded with response: %s", info.FullMethod, resp)
		}
	}

	return resp, err
}

// joinMountOptions joins mount options eliminating duplicates.
func joinMountOptions(existingOptions []string, newOptions []string) []string {
	overwritableOptions := map[string]string{
		"gid":       "",
		"file-mode": "",
		"dir-mode":  "",
	}

	allMountOptions := sets.NewString()

	process := func(mountOption string) {
		if len(mountOption) > 0 {
			optionPair := strings.SplitN(mountOption, "=", 2)

			if len(optionPair) == 2 {
				if _, ok := overwritableOptions[optionPair[0]]; ok {
					overwritableOptions[optionPair[0]] = optionPair[1]

					return
				}
			}

			allMountOptions.Insert(mountOption)
		}
	}

	for _, mountOption := range existingOptions {
		process(mountOption)
	}

	for _, mountOption := range newOptions {
		process(mountOption)
	}

	for k, v := range overwritableOptions {
		if v != "" {
			allMountOptions.Insert(k + "=" + v)
		}
	}

	return allMountOptions.List()
}

func checkContainerIsStopped(ctx context.Context, containerdClient *containerd.Client, containerID string) (bool, error) {
	ctxWithNamespace := namespaces.WithNamespace(ctx, "k8s.io")

	container, err := containerdClient.LoadContainer(ctxWithNamespace, containerID)
	if err != nil {
		return false, fmt.Errorf("failed to get container %q: %w", containerID, err)
	}

	task, err := container.Task(ctxWithNamespace, nil)
	if err != nil {
		if strings.Contains(err.Error(), "no running task found") {
			return true, nil
		}

		return false, fmt.Errorf("failed to get container %q task: %w", containerID, err)
	}

	status, err := task.Status(ctxWithNamespace)
	if err != nil {
		return false, fmt.Errorf("failed to get container %q status: %w", containerID, err)
	}

	return status.Status == containerd.Stopped, nil
}

func putExitFile(ctx context.Context, containerdClient *containerd.Client, pod *v1.Pod, targetPath string) error {
	// Check if the Pod is owned by a Job
	isOwnedByJob := false
	for _, o := range pod.ObjectMeta.OwnerReferences {
		if o.Kind == "Job" {
			isOwnedByJob = true

			break
		}
	}

	podRestartPolicyIsNever := pod.Spec.RestartPolicy == v1.RestartPolicyNever
	podIsTerminating := pod.DeletionTimestamp != nil
	jobWithOnFailureRestartPolicy := isOwnedByJob && pod.Spec.RestartPolicy == v1.RestartPolicyOnFailure

	// Check if all the containers besides the sidecar container exited
	if jobWithOnFailureRestartPolicy || podRestartPolicyIsNever || podIsTerminating {
		if pod.Status.ContainerStatuses == nil || len(pod.Status.ContainerStatuses) == 0 {
			return nil
		}

		for _, cs := range pod.Status.ContainerStatuses {
			switch {
			// skip the sidecar container itself
			case cs.Name == webhook.SidecarContainerName:
				continue

			// If the Pod is terminating, call cri to check each container status.
			// The container status from Kubernetes API is not reliable when the Pod is terminating
			// because of the issue: https://github.com/kubernetes/kubernetes/issues/106896
			case podIsTerminating:
				ci := strings.Split(cs.ContainerID, "://")
				if ci[0] != "containerd" {
					return fmt.Errorf("container %q does not use containerd runtime", cs.ContainerID)
				}

				containerStopped, err := checkContainerIsStopped(ctx, containerdClient, ci[1])
				if err != nil {
					return err
				}

				if !containerStopped {
					return nil
				}

			// If any container is in Running or Waiting state,
			// do not terminate the gcsfuse sidecar container.
			case cs.State.Running != nil || cs.State.Waiting != nil:
				return nil

			// If the Pod belongs to a Job with OnFailure RestartPolicy,
			// when the container terminated with a non-zero exit code,
			// the container may restart. Do not terminate the gcsfuse sidecar container.
			// When the container restart count reaches Job backoffLimit,
			// the Pod will be directly terminated.
			case jobWithOnFailureRestartPolicy && cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
				return nil
			}
		}

		// Prepare the emptyDir path for the mounter to pass the file descriptor
		emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, true)
		if err != nil {
			return fmt.Errorf("failed to prepare emptyDir path: %w", err)
		}

		klog.V(4).Infof("[Pod %v/%v, UID %v] all the other containers terminated in the Pod, put the exit file.", pod.Namespace, pod.Name, pod.UID)
		exitFilePath := filepath.Dir(emptyDirBasePath) + "/exit"
		f, err := os.Create(exitFilePath)
		if err != nil {
			return fmt.Errorf("failed to put the exit file: %w", err)
		}
		f.Close()

		err = os.Chown(exitFilePath, webhook.NobodyUID, webhook.NobodyGID)
		if err != nil {
			return fmt.Errorf("failed to change ownership on the exit file: %w", err)
		}
	}

	return nil
}

func CheckVolumesAndPutExitFile(containerdClient *containerd.Client, clientset clientset.Interface, mounter mount.Interface) {
	mountPoints, err := mounter.List()
	if err != nil {
		klog.Errorf("failed to list mount points: %s", err)

		return
	}

	ctx := context.Background()
	for _, mp := range mountPoints {
		if mp.Type == FuseMountType {
			podID, _, err := util.ParsePodIDVolumeFromTargetpath(mp.Path)
			if err != nil {
				klog.Error(err)

				continue
			}

			pod, err := clientset.GetPodByUID(ctx, podID)
			if err != nil {
				klog.Errorf("the Pod may not consume gcsfuse volumes: %v", err)

				continue
			}

			if err := putExitFile(ctx, containerdClient, pod, mp.Path); err != nil {
				klog.Error(err)

				continue
			}
		}
	}
}
