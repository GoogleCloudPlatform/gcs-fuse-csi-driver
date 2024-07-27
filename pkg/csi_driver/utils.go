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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	pbSanitizer "github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const (
	CreateVolumeCSIFullMethod      = "/csi.v1.Controller/CreateVolume"
	DeleteVolumeCSIFullMethod      = "/csi.v1.Controller/DeleteVolume"
	NodePublishVolumeCSIFullMethod = "/csi.v1.Node/NodePublishVolume"

	VolumeContextKeyMountOptions              = "mountOptions"
	VolumeContextKeyFileCacheCapacity         = "fileCacheCapacity"
	VolumeContextKeyFileCacheForRangeRead     = "fileCacheForRangeRead"
	VolumeContextKeyMetadataStatCacheCapacity = "metadataStatCacheCapacity"
	VolumeContextKeyMetadataTypeCacheCapacity = "metadataTypeCacheCapacity"
	VolumeContextKeyMetadataCacheTTLSeconds   = "metadataCacheTTLSeconds"
	VolumeContextKeyGcsfuseLoggingSeverity    = "gcsfuseLoggingSeverity"
	VolumeContextKeySkipCSIBucketAccessCheck  = "skipCSIBucketAccessCheck"
	VolumeContextKeyEnableMetrics             = "enableMetrics"

	//nolint:revive,stylecheck
	VolumeContextKeyMetadataCacheTtlSeconds = "metadataCacheTtlSeconds"

	VolumeContextKeyServiceAccountName = "csi.storage.k8s.io/serviceAccount.name"
	//nolint:gosec
	VolumeContextKeyServiceAccountToken = "csi.storage.k8s.io/serviceAccount.tokens"
	VolumeContextKeyPodName             = "csi.storage.k8s.io/pod.name"
	VolumeContextKeyPodNamespace        = "csi.storage.k8s.io/pod.namespace"
	VolumeContextKeyEphemeral           = "csi.storage.k8s.io/ephemeral"
	VolumeContextKeyBucketName          = "bucketName"
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

var volumeAttributesToMountOptionsMapping = map[string]string{
	VolumeContextKeyFileCacheCapacity:         "file-cache:max-size-mb:",
	VolumeContextKeyFileCacheForRangeRead:     "file-cache:cache-file-for-range-read:",
	VolumeContextKeyMetadataStatCacheCapacity: "metadata-cache:stat-cache-max-size-mb:",
	VolumeContextKeyMetadataTypeCacheCapacity: "metadata-cache:type-cache-max-size-mb:",
	VolumeContextKeyMetadataCacheTTLSeconds:   "metadata-cache:ttl-secs:",
	VolumeContextKeyMetadataCacheTtlSeconds:   "metadata-cache:ttl-secs:",
	VolumeContextKeyGcsfuseLoggingSeverity:    "logging:severity:",
	VolumeContextKeySkipCSIBucketAccessCheck:  "",
	VolumeContextKeyEnableMetrics:             util.EnableMetricsForGKE + ":",
}

// parseVolumeAttributes parses volume attributes and convert them to gcsfuse mount options.
func parseVolumeAttributes(fuseMountOptions []string, volumeContext map[string]string) ([]string, bool, bool, error) {
	if mountOptions, ok := volumeContext[VolumeContextKeyMountOptions]; ok {
		fuseMountOptions = joinMountOptions(fuseMountOptions, strings.Split(mountOptions, ","))
	}
	skipCSIBucketAccessCheck := false
	enableMetricsCollection := false
	for volumeAttribute, mountOption := range volumeAttributesToMountOptionsMapping {
		value, ok := volumeContext[volumeAttribute]
		if !ok {
			continue
		}

		var mountOptionWithValue string
		switch volumeAttribute {
		// parse Quantity volume attributes,
		// the input value should be a valid Quantity defined in https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/,
		// convert the input to a string representation in MB.
		case VolumeContextKeyFileCacheCapacity, VolumeContextKeyMetadataStatCacheCapacity, VolumeContextKeyMetadataTypeCacheCapacity:
			quantity, err := resource.ParseQuantity(value)
			if err != nil {
				return nil, skipCSIBucketAccessCheck, enableMetricsCollection, fmt.Errorf("volume attribute %v only accepts a valid Quantity value, got %q, error: %w", volumeAttribute, value, err)
			}

			megabytes := quantity.Value()
			switch {
			case megabytes < 0:
				value = "-1"
			case quantity.Format == resource.BinarySI:
				value = strconv.FormatInt(megabytes/1024/1024, 10)
			default:
				value = strconv.FormatInt(megabytes/1000/1000, 10)
			}

			mountOptionWithValue = mountOption + value

		// parse bool volume attributes
		case VolumeContextKeyFileCacheForRangeRead, VolumeContextKeySkipCSIBucketAccessCheck, VolumeContextKeyEnableMetrics:
			if boolVal, err := strconv.ParseBool(value); err == nil {
				if volumeAttribute == VolumeContextKeySkipCSIBucketAccessCheck {
					skipCSIBucketAccessCheck = boolVal

					// The skipCSIBucketAccessCheck volume attribute is only for CSI driver,
					// and there is no translation to GCSFuse mount options.
					continue
				}

				if volumeAttribute == VolumeContextKeyEnableMetrics {
					enableMetricsCollection = boolVal
				}

				mountOptionWithValue = mountOption + strconv.FormatBool(boolVal)
			} else {
				return nil, skipCSIBucketAccessCheck, enableMetricsCollection, fmt.Errorf("volume attribute %v only accepts a valid bool value, got %q", volumeAttribute, value)
			}

		// parse int volume attributes
		case VolumeContextKeyMetadataCacheTTLSeconds, VolumeContextKeyMetadataCacheTtlSeconds:
			if intVal, err := strconv.Atoi(value); err == nil {
				if intVal < 0 {
					intVal = -1
				}

				mountOptionWithValue = mountOption + strconv.Itoa(intVal)
			} else {
				return nil, skipCSIBucketAccessCheck, enableMetricsCollection, fmt.Errorf("volume attribute %v only accepts a valid int value, got %q", volumeAttribute, value)
			}

		default:
			mountOptionWithValue = mountOption + value
		}

		fuseMountOptions = joinMountOptions(fuseMountOptions, []string{mountOptionWithValue})
	}

	return fuseMountOptions, skipCSIBucketAccessCheck, enableMetricsCollection, nil
}

// parseRequestArguments parses arguments from given NodePublishVolumeRequest.
func parseRequestArguments(req *csi.NodePublishVolumeRequest) (string, string, []string, bool, bool, error) {
	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return "", "", nil, false, false, errors.New("NodePublishVolume target path must be provided")
	}

	vc := req.GetVolumeContext()
	bucketName := req.GetVolumeId()
	if vc[VolumeContextKeyEphemeral] == util.TrueStr {
		bucketName = vc[VolumeContextKeyBucketName]
		if len(bucketName) == 0 {
			return "", "", nil, false, false, fmt.Errorf("NodePublishVolume VolumeContext %q must be provided for ephemeral storage", VolumeContextKeyBucketName)
		}
	}

	fuseMountOptions := []string{}
	if req.GetReadonly() {
		fuseMountOptions = joinMountOptions(fuseMountOptions, []string{"ro"})
	}

	if capMount := req.GetVolumeCapability().GetMount(); capMount != nil {
		// Delegate fsGroup to CSI Driver
		// Set gid, file-mode, and dir-mode for gcsfuse.
		// Allow users to overwrite these flags.
		if capMount.GetVolumeMountGroup() != "" {
			fuseMountOptions = joinMountOptions(fuseMountOptions, []string{"gid=" + capMount.GetVolumeMountGroup(), "file-mode=664", "dir-mode=775"})
		}
		fuseMountOptions = joinMountOptions(fuseMountOptions, capMount.GetMountFlags())
	}

	fuseMountOptions, skipCSIBucketAccessCheck, enableMetricsCollection, err := parseVolumeAttributes(fuseMountOptions, vc)
	if err != nil {
		return "", "", nil, false, false, err
	}

	return targetPath, bucketName, fuseMountOptions, skipCSIBucketAccessCheck, enableMetricsCollection, nil
}

func putExitFile(pod *corev1.Pod, targetPath string) error {
	podIsTerminating := pod.DeletionTimestamp != nil
	podRestartPolicyIsNever := pod.Spec.RestartPolicy == corev1.RestartPolicyNever
	podRestartPolicyIsOnFailure := pod.Spec.RestartPolicy == corev1.RestartPolicyOnFailure

	// Check if all the containers besides the sidecar container exited
	if podRestartPolicyIsOnFailure || podRestartPolicyIsNever || podIsTerminating {
		if pod.Status.ContainerStatuses == nil || len(pod.Status.ContainerStatuses) == 0 {
			return nil
		}

		for _, cs := range pod.Status.ContainerStatuses {
			switch {
			// skip the sidecar container itself
			case cs.Name == webhook.SidecarContainerName:
				continue

			// If the Pod is terminating, the container status from Kubernetes API is not reliable
			// because of the issue: https://github.com/kubernetes/kubernetes/issues/106896,
			// so container status checking is skipped.
			// Directly pulling the container status from CRI is not acceptable due to security concerns.
			// This will cause the issue https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/issues/168.
			// The issue will be solved by the Kubernetes native sidecar container feature.
			case podIsTerminating:
				return nil

			// If any container is in Running or Waiting state,
			// do not terminate the gcsfuse sidecar container.
			case cs.State.Running != nil || cs.State.Waiting != nil:
				return nil

			// If the Pod RestartPolicy is OnFailure,
			// when the container terminated with a non-zero exit code,
			// the container may restart. Do not terminate the gcsfuse sidecar container.
			// When the Pod belongs to a Job, and the container restart count reaches the Job backoffLimit,
			// the Pod will be directly terminated, which goes to the first case.
			case podRestartPolicyIsOnFailure && cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
				return nil
			}
		}

		klog.V(4).Infof("[Pod %v/%v, UID %v] all the other containers terminated in the Pod, put the exit file.", pod.Namespace, pod.Name, pod.UID)
		emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, false)
		if err != nil {
			return fmt.Errorf("failed to get emptyDir path: %w", err)
		}

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

func checkGcsFuseErr(isInitContainer bool, pod *corev1.Pod, targetPath string) (codes.Code, error) {
	code := codes.Internal
	cs, err := getSidecarContainerStatus(isInitContainer, pod)
	if err != nil {
		return code, err
	}

	// the sidecar container has not started, skip the check
	if cs.State.Waiting != nil {
		return codes.OK, nil
	}

	emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, false)
	if err != nil {
		return code, fmt.Errorf("failed to get emptyDir path: %w", err)
	}

	errMsg, err := os.ReadFile(emptyDirBasePath + "/error")
	if err != nil && !os.IsNotExist(err) {
		return code, fmt.Errorf("failed to open error file %q: %w", emptyDirBasePath+"/error", err)
	}
	if err == nil && len(errMsg) > 0 {
		errMsgStr := string(errMsg)
		code := codes.Internal
		if strings.Contains(errMsgStr, "Incorrect Usage") {
			code = codes.InvalidArgument
		}

		if strings.Contains(errMsgStr, "signal: killed") {
			code = codes.ResourceExhausted
		}

		if strings.Contains(errMsgStr, "signal: terminated") {
			code = codes.Canceled
		}

		if strings.Contains(errMsgStr, "googleapi: Error 403") ||
			strings.Contains(errMsgStr, "IAM returned 403 Forbidden: Permission") ||
			strings.Contains(errMsgStr, "google: could not find default credentials") {
			code = codes.PermissionDenied
		}

		if strings.Contains(errMsgStr, "bucket doesn't exist") {
			code = codes.NotFound
		}

		return code, fmt.Errorf("gcsfuse failed with error: %v", errMsgStr)
	}

	return codes.OK, nil
}

func checkSidecarContainerErr(isInitContainer bool, pod *corev1.Pod) (codes.Code, error) {
	code := codes.Internal
	cs, err := getSidecarContainerStatus(isInitContainer, pod)
	if err != nil {
		return code, err
	}

	var reason string
	var exitCode int32
	if cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil {
		reason = cs.LastTerminationState.Terminated.Reason
		exitCode = cs.LastTerminationState.Terminated.ExitCode
	} else if cs.State.Terminated != nil {
		reason = cs.State.Terminated.Reason
		exitCode = cs.State.Terminated.ExitCode
	}

	if exitCode != 0 {
		if reason == "OOMKilled" || exitCode == 137 {
			code = codes.ResourceExhausted
		}

		return code, fmt.Errorf("the sidecar container terminated due to %v, exit code: %v", reason, exitCode)
	}

	return codes.OK, nil
}

func getSidecarContainerStatus(isInitContainer bool, pod *corev1.Pod) (*corev1.ContainerStatus, error) {
	var containerStatusList []corev1.ContainerStatus
	// Use ContainerStatuses or InitContainerStatuses
	if isInitContainer {
		containerStatusList = pod.Status.InitContainerStatuses
	} else {
		containerStatusList = pod.Status.ContainerStatuses
	}

	for _, cs := range containerStatusList {
		if cs.Name == webhook.SidecarContainerName {
			return &cs, nil
		}
	}

	return nil, errors.New("the sidecar container was not found")
}
