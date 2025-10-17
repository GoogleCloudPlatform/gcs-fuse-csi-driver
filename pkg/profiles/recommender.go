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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
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
	// StorageClass param keys.
	workloadTypeKey                          = "workloadType"
	workloadTypeInferenceKey                 = "inference"
	workloadTypeTrainingKey                  = "training"
	workloadTypeCheckpointingKey             = "checkpointing"
	fuseFileCacheMediumPriorityKey           = "fuseFileCacheMediumPriority"
	fuseMemoryAllocatableFactorKey           = "fuseMemoryAllocatableFactor"
	fuseEphemeralStorageAllocatableFactorKey = "fuseEphemeralStorageAllocatableFactor"

	// Node allocatable resource keys.
	nvidiaGpuResourceName = corev1.ResourceName("nvidia.com/gpu")
	googleTpuResourceName = corev1.ResourceName("google.com/tpu")

	// Node types.
	nodeTypeTPU            = "tpu"
	nodeTypeGPU            = "gpu"
	nodeTypeGeneralPurpose = "general_purpose"

	// gkeAppliedNodeLabelsAnnotationKey is the annotation key that stores a comma-separated list of node labels.
	gkeAppliedNodeLabelsAnnotationKey = "node.gke.io/last-applied-node-labels"
	// EphemeralStorageLocalSSDLabelKey is the specific label key we are looking for within the applied labels.
	ephemeralStorageLocalSSDLabelKey = "cloud.google.com/gke-ephemeral-storage-local-ssd"

	// metadataStatCacheBytesPerObject is the average number of metadata stat cache bytes per object.
	metadataStatCacheBytesPerObject int64 = 1500
	// metadataTypeCacheBytesPerObject is the average number of metadata type cache bytes per object.
	metadataTypeCacheBytesPerObject int64 = 200
	// mib represents 1024 * 1024 bytes.
	mib int64 = 1024 * 1024

	// Mount option names.
	metadataStatCacheMaxSizeMiBMountOptionKey = "metadata-cache:stat-cache-max-size-mb"
	metadataTypeCacheMaxSizeMiBMountOptionKey = "metadata-cache:type-cache-max-size-mb"
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

// cacheRequirements defines the size constraints for various gcsfuse caches.
type cacheRequirements struct {
	// metadataStatCacheBytes is the maximum size (in bytes) for the metadata stat cache.
	metadataStatCacheBytes int64
	// metadataTypeCacheBytes is the maximum size (in bytes) for the metadata type cache.
	metadataTypeCacheBytes int64
	// fileCache is the maximum size (in bytes) for the file cache.
	fileCacheBytes int64
}

// recommendation suggests optimal cache sizes based on certain criteria.
type recommendation struct {
	// metadataStatCacheBytes is the recommended size in bytes for the metadata stat cache.
	metadataStatCacheBytes int64
	// metadataTypeCacheBytes is the recommended size in bytes for the metadata type cache.
	metadataTypeCacheBytes int64
	// TODO(urielguzman): Implement file cache bytes and medium recommendation.
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

// addRecommendationToMountOptions appends a mount option string to the given slice if the recommendationBytes is greater than 0.
// The option is formatted as "mountOptionKey:sizeMiB", where sizeMiB is the recommendationBytes converted to MiB.
func addRecommendationToMountOptions(mountOptions []string, mountOptionKey string, recommendationBytes int64) []string {
	result := mountOptions
	if recommendationBytes > 0 {
		result = mergeMountOptionsIfKeyUnset(result, []string{fmt.Sprintf("%s=%d", mountOptionKey, bytesToMiB(recommendationBytes))})
	}
	return result
}

// RecommendMountOptions generates a slice of recommended mount options for GCS FUSE based on the provided ProfileConfig.
// It calculates optimal cache configurations and translates them into gcsfuse mount option strings.
func RecommendMountOptions(config *ProfileConfig) ([]string, error) {
	recommendation, err := recommendCacheConfigs(config)
	// TODO(urielguzman): Log the decision summary into a human readable format via a container log / Pod event.
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to recommend cache configs: %v", err)
	}

	recommendedMountOptions := config.scDetails.mountOptions

	// Map the recommended metadata stat cache size to equivalent mount option.
	recommendedMountOptions = addRecommendationToMountOptions(recommendedMountOptions, metadataStatCacheMaxSizeMiBMountOptionKey, recommendation.metadataStatCacheBytes)

	// Map the recommended metadata type cache size to equivalent mount option.
	recommendedMountOptions = addRecommendationToMountOptions(recommendedMountOptions, metadataTypeCacheMaxSizeMiBMountOptionKey, recommendation.metadataTypeCacheBytes)

	// TODO(urielguzman): Map the recommended file cache size & medium to equivalent mount options.
	return recommendedMountOptions, nil
}

// buildCacheRequirements constructs a cacheRequirements struct based on the provided pvDetails.
// It calculates the ideal sizes for metadata stat, metadata type, and file caches.
func buildCacheRequirements(pvDetails *pvDetails) *cacheRequirements {
	return &cacheRequirements{
		metadataStatCacheBytes: pvDetails.numObjects * metadataStatCacheBytesPerObject,
		metadataTypeCacheBytes: pvDetails.numObjects * metadataTypeCacheBytesPerObject,
		fileCacheBytes:         pvDetails.totalSizeBytes,
	}
}

// calculateFuseResourceBudget determines the memory or ephemeral storage budget available for GCS FUSE.
// It takes the node's allocatable capacity, a factor for FUSE allocation, and an optional sidecar limit.
func calculateFuseResourceBudget(nodeAllocatable int64, allocatableFactor float64, sidecarLimit int64) int64 {
	budget := nodeAllocatable
	// If the sidecar has "0" resource limit, then we assume the entire node allocatable is available.
	// Otherwise, cap the max fuse resource to the sidecar's memory limit.
	if sidecarLimit > 0 && sidecarLimit < nodeAllocatable {
		budget = sidecarLimit
	}
	// Return x% of the available budget, determined by the allocatable factor variable.
	return int64(float64(budget) * allocatableFactor)
}

// calculateResourceBudgets computes the memory and ephemeral storage budgets available for GCS FUSE
// based on the node and pod resource configurations in the ProfileConfig.
func calculateResourceBudgets(config *ProfileConfig) (int64, int64) {
	// Calculate memory budget
	memoryBudget := calculateFuseResourceBudget(
		config.nodeDetails.nodeAllocatables.memoryBytes,
		config.scDetails.fuseMemoryAllocatableFactor,
		config.podDetails.sidecarLimits.memoryBytes,
	)

	// Calculate ephemeral storage budget
	ephemeralStorageBudget := calculateFuseResourceBudget(
		config.nodeDetails.nodeAllocatables.ephemeralStorageBytes,
		config.scDetails.fuseEphemeralStorageAllocatableFactor,
		config.podDetails.sidecarLimits.ephemeralStorageBytes,
	)

	return memoryBudget, ephemeralStorageBudget
}

// recommendCacheConfigs calculates recommended cache sizes and medium.
func recommendCacheConfigs(config *ProfileConfig) (*recommendation, error) {
	// Validate input.
	if config.pvDetails == nil {
		return nil, errors.New("pvDetails cannot be nil")
	}
	if config.scDetails == nil {
		return nil, errors.New("scDetails cannot be nil")
	}
	if config.nodeDetails == nil {
		return nil, errors.New("nodeDetails cannot be nil")
	}
	if config.podDetails == nil {
		return nil, errors.New("podDetails cannot be nil")
	}

	recommendation := &recommendation{}

	// Calculate metadata and file cache requirements.
	cacheRequirements := buildCacheRequirements(config.pvDetails)

	// Calculate memory and ephemeral storage budgets.
	// TODO(urielguzman): Use ephemeralStorageBudget to calculate file cache size and medium.
	memoryBudget, _ := calculateResourceBudgets(config)

	// Calculate the recommended metadata cache sizes. The memoryBudget gets decreased after each recommendation.
	recommendation.metadataStatCacheBytes, memoryBudget = recommendMetadataCacheSize(config, cacheRequirements.metadataStatCacheBytes, memoryBudget, "stat")
	recommendation.metadataTypeCacheBytes, _ = recommendMetadataCacheSize(config, cacheRequirements.metadataTypeCacheBytes, memoryBudget, "type")

	// TODO(urielguzman): Calculate the recommended file cache size and medium.
	return recommendation, nil
}

// recommendMetadataCacheSize determines the recommended size for a specific metadata cache type
// (e.g., "stat" or "type"), ensuring it does not exceed the available memoryBudget.
// It returns the recommended size and the remaining memory budget.
func recommendMetadataCacheSize(config *ProfileConfig, required, memoryBudget int64, cacheType string) (int64, int64) {
	recommended := minInt64(required, memoryBudget)
	if recommended < required && required > 0 {
		// TODO(urielguzman): Log this in a Kubernetes Pod event warning.
		klog.Warningf("For target node %s, required metadata %s size %d bytes capped to available fuse memory budget %d bytes. This can impact perf due to increased GCS metadata API calls", config.nodeDetails.name, cacheType, required, recommended)
	}
	memoryBudget = maxInt64(0, memoryBudget-recommended)
	klog.V(6).Infof("available memory after metadata %s cache: %d bytes", cacheType, memoryBudget)
	return recommended, memoryBudget
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
	numObjectsStr, numObjectsFound := pvAnnotations[annotationNumObjects]
	totalSizeStr, totalSizeFound := pvAnnotations[annotationTotalSize]

	// Check for missing annotations
	var missingAnnotations []string
	if !numObjectsFound {
		missingAnnotations = append(missingAnnotations, annotationNumObjects)
	}
	if !totalSizeFound {
		missingAnnotations = append(missingAnnotations, annotationTotalSize)
	}

	if len(missingAnnotations) > 0 {
		// Return internal error, since the mount shouldn't have started if the PV hasn't finished scanning.
		return nil, fmt.Errorf("PV %q is missing required annotations: %s", pv.Name, strings.Join(missingAnnotations, ", "))
	}

	// Parse annotations into int64
	var parseErrors []string
	numObjects, err := strconv.ParseInt(numObjectsStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", annotationNumObjects, numObjectsStr, err))
	}
	totalSizeBytes, err := strconv.ParseInt(totalSizeStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", annotationTotalSize, totalSizeStr, err))
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

// minInt64 returns the smaller of two int64 values.
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// maxInt64 returns the larger of two int64 values.
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// bytesToMiB converts a byte count to mebibytes (MiB), rounding up to the nearest whole MiB.
func bytesToMiB(a int64) int64 {
	return ceilDiv64(a, mib)
}

// ceilDiv64 performs integer division of 'a' by 'b', rounding the result up.
func ceilDiv64(a, b int64) int64 {
	return (a + b - 1) / b
}

// getMountOptionKey extracts the key part of a mount option string.
// Formats supported and precedence:
// 1. key=value  (Key is everything before the first '=')
// 2. ...:key:value (Key is everything before the last ':')
// 3. key        (Key is the entire string)
func getMountOptionKey(opt string) string {
	if strings.Contains(opt, "=") {
		parts := strings.SplitN(opt, "=", 2)
		return parts[0]
	}
	if strings.Contains(opt, ":") {
		lastColon := strings.LastIndex(opt, ":")
		// If lastColon > 0, the key is the part before the last colon.
		// Examples: "a:b" -> "a", "a:b:c" -> "a:b"
		if lastColon > 0 {
			return opt[:lastColon]
		}
	}
	return opt
}

// MergeMountOptionsIfKeyUnset merges mount options from srcOpts into dstOpts.
// An option from srcOpts is added only if its key is not already
// present in the keys of options within dstOpts.
func mergeMountOptionsIfKeyUnset(dstOpts, srcOpts []string) []string {
	if len(srcOpts) == 0 {
		return dstOpts
	}

	if len(dstOpts) == 0 {
		return srcOpts
	}

	mountOptions := make([]string, len(dstOpts))
	copy(mountOptions, dstOpts)

	existingKeys := make(map[string]bool)
	for _, opt := range mountOptions {
		key := strings.ToLower(getMountOptionKey(opt))
		existingKeys[key] = true
	}

	for _, opt := range srcOpts {
		key := strings.ToLower(getMountOptionKey(opt))
		if !existingKeys[key] {
			mountOptions = append(mountOptions, opt)
			existingKeys[key] = true
		}
	}
	return mountOptions
}
