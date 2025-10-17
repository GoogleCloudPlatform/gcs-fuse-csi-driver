/*
Copyright 2025 Google LLC

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

package profiles

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	profilesutil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
)

const (
	// StorageClass param keys
	workloadTypeKey                          = "workloadType"
	workloadTypeInferenceKey                 = "inference"
	workloadTypeTrainingKey                  = "training"
	workloadTypeCheckpointingKey             = "checkpointing"
	fuseFileCacheMediumPriorityKey           = "fuseFileCacheMediumPriority"
	fuseMemoryAllocatableFactorKey           = "fuseMemoryAllocatableFactor"
	fuseEphemeralStorageAllocatableFactorKey = "fuseEphemeralStorageAllocatableFactor"

	// Node allocatable resource keys
	nvidiaGpuResourceName = corev1.ResourceName("nvidia.com/gpu")
	googleTpuResourceName = corev1.ResourceName("google.com/tpu")

	// Node types
	nodeTypeTPU            = "tpu"
	nodeTypeGPU            = "gpu"
	nodeTypeGeneralPurpose = "general_purpose"

	// gkeAppliedNodeLabelsAnnotationKey is the annotation key that stores a comma-separated list of node labels.
	gkeAppliedNodeLabelsAnnotationKey = "node.gke.io/last-applied-node-labels"
	// EphemeralStorageLocalSSDLabelKey is the specific label key we are looking for within the applied labels.
	ephemeralStorageLocalSSDLabelKey = "cloud.google.com/gke-ephemeral-storage-local-ssd"
)

// ProfileConfig holds the consolidated configuration for a volume profile,
// derived from PersistentVolume annotations and StorageClass parameters.
type ProfileConfig struct {
	pvDetails   *pvDetails   // Details extracted from the PersistentVolume.
	nodeDetails *nodeDetails // Details extracted from the Node.
	scDetails   *scDetails   // Details extracted from the SC.
	podDetails  *podDetails  // Details extracted from the Pod.

}

// pvDetails holds a parsed summary of information about a PersistentVolume that are relevant to the recommender.
type pvDetails struct {
	numObjects     int64  // The number of objects reported by the PV.
	totalSizeBytes int64  // The total size in bytes reported by the PV.
	name           string // The name of the PersistentVolume.
}

// nodeDetails holds a parsed summary of information about a Node that are relevant to the recommender.
type nodeDetails struct {
	nodeType                              string
	nodeAllocatables                      *parsedResourceList
	name                                  string // The name of the Node.
	hasLocalSSDEphemeralStorageAnnotation bool
}

// podDetaails holds a parsed summary of information about a Pod that are relevant to the recommender.
type podDetails struct {
	sidecarLimits *parsedResourceList
	namespace     string
	name          string // The name of the Pod.
}

// scDetails holds a parsed summary of information about a StorageClass that are relevant to the recommender.
type scDetails struct {
	fileCacheMediumPriority               map[string][]string // Parsed priority map for file cache mediums.
	fuseMemoryAllocatableFactor           float64             // Factor for calculating FUSE memory allocation.
	fuseEphemeralStorageAllocatableFactor float64             // Factor for calculating FUSE ephemeral storage allocation.
	VolumeAttributes                      map[string]string   // Arbitrary volume attributes sourced from the StorageClass.
	mountOptions                          []string            // Mount options sourced from the StorageClass.
}

// parsedResourceList holds the parsed resource values from a pod spec.
type parsedResourceList struct {
	memoryBytes           int64
	ephemeralStorageBytes int64
}

// BuildProfileConfigParams contains the parameters needed to build a profile configuration.
type BuildProfileConfigParams struct {
	targetPath          string
	clientset           clientset.Interface
	volumeAttributeKeys map[string]struct{}
	nodeName            string
	podNamespace        string
	podName             string
	isInitContainer     bool
}

// BuildProfileConfig constructs a ProfileConfig by fetching and validating
// information from the PV, Pod, SC, and Node relevant to the recommender.
// volumeAttributeKeys is used to filter parameters from the StorageClass that should be
// treated as volume attributes.
func BuildProfileConfig(params *BuildProfileConfigParams) (*ProfileConfig, error) {
	// Get the PV name from target path.
	_, volumeName, err := util.ParsePodIDVolumeFromTargetpath(params.targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get pv name from target path %q: %v", params.targetPath, err)
	}

	// Get the PV object using the PV name.
	pv, err := params.clientset.GetPV(volumeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "pv %q not found: %v", volumeName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get pv %q: %v", volumeName, err)
	}

	// Get the PV details from the PV object's annotations.
	pvDetails, err := buildPVDetails(pv)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get PV details from annotations for %q: %v", pv.Name, err)
	}

	// Get the StorageClassName from the PV object.
	scName := pv.Spec.StorageClassName
	if scName == "" {
		// Since the feature is opt-in, the user should specify a StorageClassName.
		return nil, status.Errorf(codes.InvalidArgument, "pv %q has empty StorageClassName", pv.Name)
	}

	// Get the StorageClass object from the StorageClassName.
	sc, err := params.clientset.GetSC(scName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "sc %q not found: %v", scName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get StorageClass %q: %v", scName, err)
	}

	// Get the scDetails from the StorageClass object.
	scDetails, err := buildSCDetails(sc, params.volumeAttributeKeys)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to get StorageClass details: %v", err)
	}

	// Get the Node object using the Node name.
	node, err := params.clientset.GetNode(params.nodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "node %q not found: %v", params.nodeName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get node %q: %v", params.nodeName, err)
	}

	// Get the nodeDetails from the Node object.
	nodeDetails, err := buildNodeDetails(node)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get node details: %v", err)
	}

	// Get the Pod object using the Pod namespace/name.
	pod, err := params.clientset.GetPod(params.podNamespace, params.podName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "pod %s/%s not found: %v", params.podNamespace, params.podName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get pod %s/%s: %v", params.podNamespace, params.podName, err)
	}

	// Get the podDetails from the Pod object.
	podDetails, err := buildPodDetails(params.isInitContainer, pod)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get pod details: %v", err)
	}

	return &ProfileConfig{
		pvDetails:   pvDetails,
		nodeDetails: nodeDetails,
		scDetails:   scDetails,
		podDetails:  podDetails,
	}, nil
}

// parseFloatParameterNonNegative extracts a parameter by key from the params map,
// parses it as a float64, and returns an error if the key is missing, the value
// is not a valid float, or the value is negative.
func parseFloatParameterNonNegative(params map[string]string, key string) (float64, error) {
	stringVal, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing %q", key)
	}
	floatVal, err := strconv.ParseFloat(stringVal, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %q: %w", key, err)
	}
	if floatVal < 0 {
		return 0, fmt.Errorf("%q is < 0", key)
	}
	return floatVal, nil
}

// parseFileCacheMediumPriority parses a comma-separated string of key:value pairs
// into a map. Each value can be a pipe-separated list of strings.
// Example input: "hdd:val1|val2,ssd:val3"
// Output: {"hdd": ["val1", "val2"], "ssd": ["val3"]}
// It returns an error if the input string is malformed.
func parseFileCacheMediumPriority(input string) (map[string][]string, error) {
	result := make(map[string][]string)
	if strings.TrimSpace(input) == "" {
		return result, nil
	}
	pairs := strings.Split(input, ",")
	for i, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed pair found at index %d: %q", i, pair)
		}
		key := strings.TrimSpace(parts[0])
		valueString := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("found pair with empty key at index %d: %q", i, pair)
		}
		var values []string
		if valueString != "" {
			rawValues := strings.Split(valueString, "|")
			for _, val := range rawValues {
				trimmedVal := strings.TrimSpace(val)
				if trimmedVal != "" {
					values = append(values, trimmedVal)
				}
			}
		}
		result[key] = values
	}
	return result, nil
}

// buildPVDetails extracts and parses the number of objects and total size
// annotations from the given PersistentVolume. It returns a pvDetails struct containing
// the parsed values. An error is returned if the required annotations are missing,
// empty, or cannot be parsed into their expected integer types.
func buildPVDetails(
	pv *corev1.PersistentVolume) (*pvDetails, error) {
	// Extract annotations
	pvAnnotations := pv.GetAnnotations()
	if pvAnnotations == nil {
		// Return internal error, since the mount shouldn't have started if the PV hasn't finished scanning.
		return nil, fmt.Errorf("PV %q has no annotations", pv.Name)
	}

	// Get annotation values as strings
	numObjectsStr, numObjectsFound := pvAnnotations[profilesutil.AnnotationNumObjects]
	totalSizeStr, totalSizeFound := pvAnnotations[profilesutil.AnnotationTotalSize]

	// Check for missing annotations
	var missingAnnotations []string
	if !numObjectsFound {
		missingAnnotations = append(missingAnnotations, profilesutil.AnnotationNumObjects)
	}
	if !totalSizeFound {
		missingAnnotations = append(missingAnnotations, profilesutil.AnnotationTotalSize)
	}

	if len(missingAnnotations) > 0 {
		// Return internal error, since the mount shouldn't have started if the PV hasn't finished scanning.
		return nil, fmt.Errorf("PV %q is missing required annotations: %s", pv.Name, strings.Join(missingAnnotations, ", "))
	}

	// Parse annotations into int64
	var parseErrors []string
	numObjects, err := strconv.ParseInt(numObjectsStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", profilesutil.AnnotationNumObjects, numObjectsStr, err))
	}
	totalSizeBytes, err := strconv.ParseInt(totalSizeStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", profilesutil.AnnotationTotalSize, totalSizeStr, err))
	}
	if len(parseErrors) > 0 {
		errorMsg := strings.Join(parseErrors, "; ")
		// Return internal error, since the annotations should have been validated either by GCW or the scanner controller.
		return nil, fmt.Errorf("invalid annotation format on PV %q: %s", pv.Name, errorMsg)
	}

	return &pvDetails{
		name:           pv.Name,
		numObjects:     numObjects,
		totalSizeBytes: totalSizeBytes,
	}, nil
}

// buildNodeDetails extracts and processes information from a Kubernetes Node object
// to populate and return a nodeDetails struct. It parses allocatable resources,
// determines the node type (General Purpose, GPU, TPU), and checks for specific
// annotations like local SSD ephemeral storage.
func buildNodeDetails(node *corev1.Node) (*nodeDetails, error) {
	// Parse the node allocatables from the node.
	nodeAllocatables, err := parseResourceList(node.Status.Allocatable)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node allocatable resources from node %q: %v", node.Name, err)
	}

	// Get the node type from the node.
	nodeType := nodeTypeGeneralPurpose
	if isGpuNodeByResource(node) {
		nodeType = nodeTypeGPU
	} else if isTpuNodeByResource(node) {
		nodeType = nodeTypeTPU
	}

	return &nodeDetails{
		nodeType:                              nodeType,
		nodeAllocatables:                      nodeAllocatables,
		name:                                  node.Name,
		hasLocalSSDEphemeralStorageAnnotation: hasLocalSSDEphemeralStorageAnnotation(node.Annotations),
	}, nil
}

// buildPodDetails extracts resource limit information for the gcsFuse sidecar container
// from a given corev1.Pod. The isInitContainer boolean specifies whether to look
// within the Pod's InitContainers or its Containers.
// It uses gcsFuseSidecarResourceRequirements to find the sidecar's resource requirements
// and then calls parseResourceList to process the Limits.
// It returns a pointer to a podDetails struct or an error if parsing the resource limits fails.
func buildPodDetails(isInitContainer bool, pod *corev1.Pod) (*podDetails, error) {
	// Get the sidecar limits from the Pod.
	sidecarLimits, err := parseResourceList(gcsFuseSidecarResourceRequirements(isInitContainer, pod).Limits)
	if err != nil {
		return nil, fmt.Errorf("failed to parse gcsfuse sidecar resource list from pod %q: %v", pod.Name, err)
	}

	return &podDetails{
		namespace:     pod.Namespace,
		name:          pod.Name,
		sidecarLimits: sidecarLimits,
	}, nil
}

// gcsFuseSidecarResourceRequirements finds the corev1.ResourceRequirements for the
// gcsFuse sidecar container within a provided corev1.Pod.
// The isInitContainer flag dictates whether the function searches in pod.Spec.InitContainers
// or pod.Spec.Containers.
func gcsFuseSidecarResourceRequirements(isInitContainer bool, pod *corev1.Pod) corev1.ResourceRequirements {
	if pod == nil {
		return corev1.ResourceRequirements{}
	}

	var containers []corev1.Container
	if isInitContainer {
		containers = pod.Spec.InitContainers
	} else {
		containers = pod.Spec.Containers
	}

	for _, container := range containers {
		if container.Name == webhook.GcsFuseSidecarName {
			return container.Resources
		}
	}
	return corev1.ResourceRequirements{}
}

// buildSCDetails extracts and validates parameters from a Kubernetes StorageClass
// object to populate and return an scDetails struct. It checks for required
// parameters like workloadType and fuseFileCacheMediumPriority, parses
// numeric factors, and selects volume attributes based on the provided
// volumeAttributeKeys. An error is returned if any mandatory parameters are
// missing or invalid.
func buildSCDetails(sc *v1.StorageClass, volumeAttributeKeys map[string]struct{}) (*scDetails, error) {
	// Get the parameters from the StorageClass.
	if sc.Parameters == nil {
		return nil, fmt.Errorf("sc %q found but has nil Parameters map", sc.Name)
	}

	// Validate the workloadType parameter for the profile.
	workloadType, ok := sc.Parameters[workloadTypeKey]
	if !ok {
		return nil, fmt.Errorf("missing workloadType parameter in StorageClass %q", sc.Name)
	}
	if err := validateWorkloadType(workloadType); err != nil {
		return nil, fmt.Errorf("failed to validate workloadType parameter in StorageClass %q: %v", sc.Name, err)
	}

	// Get the file cache medium priority from the StorageClass parameters.
	fileCacheMediumPriorityStr, ok := sc.Parameters[fuseFileCacheMediumPriorityKey]
	if !ok {
		return nil, fmt.Errorf("missing fuseFileCacheMediumPriority in StorageClass %q", sc.Name)
	}
	fileCacheMediumPriority, err := parseFileCacheMediumPriority(fileCacheMediumPriorityStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fuseFileCacheMediumPriority in StorageClass %q: %v", sc.Name, err)
	}

	// Get the fuse memory allocatable factor from the StorageClass parameters.
	fuseMemoryAllocatableFactor, err := parseFloatParameterNonNegative(sc.Parameters, fuseMemoryAllocatableFactorKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fuse memory allocatable factor param in StorageClass %q: %v", sc.Name, err)
	}

	// Get the fuse ephemeral storage allocatable factor from the StorageClass parameters.
	fuseEphemeralStorageAllocatableFactor, err := parseFloatParameterNonNegative(sc.Parameters, fuseEphemeralStorageAllocatableFactorKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fuse ephemeral storage allocatable factor param in StorageClass %q: %v", sc.Name, err)
	}

	// Parse volume attributes from the StorageClass parameters. Only select from keys recognized by the CSI Driver as volume attributes.
	volumeAttributes := selectFromMapIfKeysMatch(sc.Parameters, volumeAttributeKeys)

	return &scDetails{
		fileCacheMediumPriority:               fileCacheMediumPriority,
		fuseMemoryAllocatableFactor:           fuseMemoryAllocatableFactor,
		fuseEphemeralStorageAllocatableFactor: fuseEphemeralStorageAllocatableFactor,
		VolumeAttributes:                      volumeAttributes,
		mountOptions:                          sc.MountOptions,
	}, nil
}

// selectFromMapIfKeysMatch returns a new map containing entries from the 'target' map
// only if their keys are also present in the 'keys' map. The values
// in the returned map are taken from the 'target' map.
func selectFromMapIfKeysMatch(target map[string]string, keys map[string]struct{}) map[string]string {
	result := make(map[string]string)
	keysCaseInsensitive := map[string]struct{}{}
	for key := range keys {
		keysCaseInsensitive[strings.ToLower(key)] = struct{}{}
	}

	for key, val := range target {
		if _, keyExists := keysCaseInsensitive[strings.ToLower(key)]; keyExists {
			result[key] = val
		}
	}
	return result
}

// validateWorkloadType checks if the workload type is valid.
func validateWorkloadType(workloadType string) error {
	switch workloadType {
	case workloadTypeInferenceKey, workloadTypeTrainingKey, workloadTypeCheckpointingKey:
	default:
		return fmt.Errorf("invalid %q parameter %q", workloadTypeKey, workloadType)
	}
	return nil
}

// isGpuNodeByResource checks if the node has allocatable nvidia.com/gpu resources.
func isGpuNodeByResource(node *corev1.Node) bool {
	if node == nil || node.Status.Allocatable == nil {
		return false
	}
	gpuQuantity, exists := node.Status.Allocatable[nvidiaGpuResourceName]
	return exists && gpuQuantity.CmpInt64(0) > 0
}

// isTpuNodeByResource checks if the node has allocatable google.com/tpu resources.
func isTpuNodeByResource(node *corev1.Node) bool {
	if node == nil || node.Status.Allocatable == nil {
		return false
	}
	tpuQuantity, exists := node.Status.Allocatable[googleTpuResourceName]
	return exists && tpuQuantity.CmpInt64(0) > 0
}

func hasLocalSSDEphemeralStorageAnnotation(annotations map[string]string) bool {
	// Get the value of the 'node.gke.io/last-applied-node-labels' annotation.
	appliedLabelsStr, ok := annotations[gkeAppliedNodeLabelsAnnotationKey]
	if !ok || appliedLabelsStr == "" {
		return false
	}
	// Split the comma-separated string of labels into individual label strings.
	labelPairs := strings.Split(appliedLabelsStr, ",")
	// Iterate through each label string (e.g., "key=value").
	for _, labelPairStr := range labelPairs {
		// Split the label string into key and value.
		// Use SplitN to handle cases where a value might unexpectedly contain an '='.
		kv := strings.SplitN(labelPairStr, "=", 2)
		if len(kv) != 2 {
			continue
		}
		labelKey := strings.TrimSpace(kv[0])
		labelValue := strings.TrimSpace(kv[1])
		// Check if this is the label we're looking for and if its value is "true".
		if labelKey == ephemeralStorageLocalSSDLabelKey && labelValue == util.TrueStr {
			return true
		}
	}
	// If we've gone through all labels and haven't found the specific key-value pair,
	// then the node does not have the indicator.
	return false
}

// parseResource attempts to parse a specific resource quantity (e.g., memory, ephemeral-storage)
// from a corev1.ResourceList. It returns the value in bytes as an int64.
// If the resource name is not found in the list, it defaults to 0.
// An error is returned if the quantity is present but cannot be parsed as an int64.
func parseResource(name corev1.ResourceName, resourceList corev1.ResourceList) (int64, error) {
	quantity, ok := resourceList[name]
	if !ok {
		// If the resource quantity is unset, default to zero (e.g. sidecar limits).
		klog.V(6).Infof("key %q not found in resource list %+v, defaulting to 0", name, resourceList)
		return 0, nil
	}
	bytes, parsedOK := quantity.AsInt64()
	if parsedOK {
		klog.V(6).Infof("Successfully parsed resource %q: %d bytes (%s)", name, bytes, quantity.String())
		return bytes, nil
	}
	return 0, fmt.Errorf("could not parse resource %q quantity %q as int64 bytes", name, quantity.String())
}

// parseResourceList parses memory and ephemeral storage resources from a
// corev1.ResourceList and returns them in a parsedResourceList struct.
// It uses parseResource internally for each resource type. An error is returned
// if any of the individual resource parsing fails.
func parseResourceList(resourceList corev1.ResourceList) (*parsedResourceList, error) {
	// Parse memory
	memory, err := parseResource(corev1.ResourceMemory, resourceList)
	if err != nil {
		return nil, fmt.Errorf("could not parse resource memory, err: %v", err)
	}

	// Parse ephemeral storage
	ephemeralStorage, err := parseResource(corev1.ResourceEphemeralStorage, resourceList)
	if err != nil {
		return nil, fmt.Errorf("could not parse resource ephemeral storage, err: %v", err)
	}

	return &parsedResourceList{
		memoryBytes:           memory,
		ephemeralStorageBytes: ephemeralStorage,
	}, nil
}
