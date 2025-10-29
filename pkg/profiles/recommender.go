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
	"slices"
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
	// StorageClass param keys.
	workloadTypeKey                          = "workloadType"
	workloadTypeServingKey                   = "serving"
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

	// Standard medium names
	mediumRAM  = "ram"
	mediumLSSD = "lssd"

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
	fileCacheSizeMiBMountOptionKey            = "file-cache:max-size-mb"
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
	labels        map[string]string
}

// scDetails holds a parsed summary of information about a StorageClass that are relevant to the recommender.
type scDetails struct {
	fileCacheMediumPriority               map[string][]string // Parsed priority map for file cache mediums.
	fuseMemoryAllocatableFactor           float64             // Factor for calculating FUSE memory allocation.
	fuseEphemeralStorageAllocatableFactor float64             // Factor for calculating FUSE ephemeral storage allocation.
	volumeAttributes                      map[string]string   // Arbitrary volume attributes sourced from the StorageClass.
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
	// metadataTypeCacheBytes is the recommended size in bytes for the file cache.
	fileCacheBytes int64
	// fileCacheMedium is the recommended medium for file cache (ram or lssd).
	fileCacheMedium string
}

// BuildProfileConfigParams contains the parameters needed to build a profile configuration.
type BuildProfileConfigParams struct {
	TargetPath          string
	Clientset           clientset.Interface
	VolumeAttributeKeys map[string]struct{}
	NodeName            string
	PodNamespace        string
	PodName             string
	IsInitContainer     bool
}

// MergeVolumeAttributesOnRecommendedMissingKeys returns the a copy of the input map, merged with the profile's.
// recommended VolumeAttributes. This function respects the input's keys in the case of duplication.
func (c *ProfileConfig) MergeVolumeAttributesOnRecommendedMissingKeys(input map[string]string) map[string]string {
	if c == nil || c.scDetails == nil {
		return nil
	}
	return mergeMapsOnMissingKeys(input, c.scDetails.volumeAttributes)
}

// BuildProfileConfig constructs a ProfileConfig by fetching and validating
// information from the PV, Pod, SC, and Node relevant to the recommender.
// volumeAttributeKeys is used to filter parameters from the StorageClass that should be
// treated as volume attributes.
func BuildProfileConfig(params *BuildProfileConfigParams) (*ProfileConfig, error) {
	// Get the PV name from target path.
	_, volumeName, err := util.ParsePodIDVolumeFromTargetpath(params.TargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get pv name from target path %q: %v", params.TargetPath, err)
	}

	// Get the PV object using the PV name.
	pv, err := params.Clientset.GetPV(volumeName)
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
	sc, err := params.Clientset.GetSC(scName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "sc %q not found: %v", scName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get StorageClass %q: %v", scName, err)
	}

	// Get the scDetails from the StorageClass object.
	scDetails, err := buildSCDetails(sc, params.VolumeAttributeKeys)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to get StorageClass details: %v", err)
	}

	// Get the Node object using the Node name.
	node, err := params.Clientset.GetNode(params.NodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "node %q not found: %v", params.NodeName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get node %q: %v", params.NodeName, err)
	}

	// Get the nodeDetails from the Node object.
	nodeDetails, err := buildNodeDetails(node)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get node details: %v", err)
	}

	// Get the Pod object using the Pod namespace/name.
	pod, err := params.Clientset.GetPod(params.PodNamespace, params.PodName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "pod %s/%s not found: %v", params.PodNamespace, params.PodName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get pod %s/%s: %v", params.PodNamespace, params.PodName, err)
	}

	// Get the podDetails from the Pod object.
	podDetails, err := buildPodDetails(params.IsInitContainer, pod)
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

// shouldSkipCacheRecommendations returns true if the user provided cache mount options or configured their own custom cache medium.
func (config *ProfileConfig) shouldSkipCacheRecommendations(userMountOptions []string) bool {
	if cacheCreatedByUser, ok := config.podDetails.labels[webhook.GcsfuseCacheCreatedByUserLabel]; ok && cacheCreatedByUser == util.TrueStr {
		klog.Warning("Detected custom cache medium provided by user, skipping smart cache recommendation to allow override")
		return true
	}
	// https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-sidecar#configure-custom-read-cache-volume
	// This will require adding a label to the Pod if a pre-existing gke-gcsfuse-cache volume is found before injection.
	for _, option := range userMountOptions {
		if isCacheMountOptionKey(option) {
			klog.Warningf("Detected pre-existing cache size mount option: %q, skipping smart cache recommendation to allow override", option)
			return true
		}
	}
	return false
}

// MergeRecommendedMountOptionsOnMissingKeys generates a slice of recommended mount options for GCS FUSE based on the provided ProfileConfig.
// It calculates optimal cache configurations and translates them into gcsfuse mount option strings.
// If the user provides any of the following mount options:
//   - "metadata-cache:stat-cache-max-size-mb"
//   - "metadata-cache:type-cache-max-size-mb"
//   - "file-cache:max-size-mb"
//
// the cache recommendation will be skipped, and only the pre-bundled mount options will be merged with the
// user's mount options, respecting the user's mount options in the case of duplication.
func (config *ProfileConfig) MergeRecommendedMountOptionsOnMissingKeys(userMountOptions []string) ([]string, error) {
	if config == nil {
		return nil, status.Errorf(codes.Internal, "config cannot be nil")
	}

	// Start with merging the pre-bundled StorageClass mount option recommendations into the user's mount options,
	// respecting user's mount options in the case of duplicates.
	recommendedMountOptions, err := mergeMountOptionsOnMissingKeys(userMountOptions, config.scDetails.mountOptions)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to merge mount options: %v", err)
	}

	// Skip the smart cache recommendation and only recommend pre-bundled mount options.
	// Note: Taking into account user mount options and medium as input for the recommender can be a future improvement,
	// but it requires careful thinking for corner cases (e.g. Should we pick a medium for unlimited cache sizes?
	// Should we cap if they exceed allocatable? What if their medium doesn't have enough capacity?)
	if config.shouldSkipCacheRecommendations(recommendedMountOptions) {
		return recommendedMountOptions, nil
	}

	recommendation, err := recommendCacheConfigs(config)
	// TODO(urielguzman): Log the decision summary into a human readable format via a container log / Pod event.
	if err != nil {
		return nil, fmt.Errorf("failed to recommend cache configs: %v", err)
	}

	cacheOptions := []string{}

	// Map the recommended metadata stat cache size to equivalent mount option.
	if recommendation.metadataStatCacheBytes > 0 {
		cacheOptions = append(cacheOptions, fmt.Sprintf("%s:%d", metadataStatCacheMaxSizeMiBMountOptionKey, bytesToMiB(recommendation.metadataStatCacheBytes)))
	}

	// Map the recommended metadata type cache size to equivalent mount option.
	if recommendation.metadataTypeCacheBytes > 0 {
		cacheOptions = append(cacheOptions, fmt.Sprintf("%s:%d", metadataTypeCacheMaxSizeMiBMountOptionKey, bytesToMiB(recommendation.metadataTypeCacheBytes)))
	}

	// Map the recommended file cache size & medium to equivalent mount options.
	if recommendation.fileCacheBytes > 0 && recommendation.fileCacheMedium != "" {
		cacheOptions = append(cacheOptions, fmt.Sprintf("%s:%d", fileCacheSizeMiBMountOptionKey, bytesToMiB(recommendation.fileCacheBytes)))
		// Note: File cache medium *must* be delimeted with an "=" sign, since it's an internal CSI flag.
		// TODO(urielguzman): Add a sidecar version check in the driver before passing this flag down to the sidecar mounter.
		cacheOptions = append(cacheOptions, fmt.Sprintf("%s=%s", util.FileCacheMediumConst, recommendation.fileCacheMedium))
	}

	// Merge the final cache mount options to the recommended mount options, respecting the user's mount options in the case of duplication.
	recommendedMountOptions, err = mergeMountOptionsOnMissingKeys(recommendedMountOptions, cacheOptions)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to merge mount options: %v", err)
	}
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
		return nil, status.Errorf(codes.Internal, "pvDetails cannot be nil")
	}
	if config.scDetails == nil {
		return nil, status.Errorf(codes.Internal, "scDetails cannot be nil")
	}
	if config.nodeDetails == nil {
		return nil, status.Errorf(codes.Internal, "nodeDetails cannot be nil")
	}
	if config.podDetails == nil {
		return nil, status.Errorf(codes.Internal, "podDetails cannot be nil")
	}

	recommendation := &recommendation{}

	// Calculate metadata and file cache requirements.
	cacheRequirements := buildCacheRequirements(config.pvDetails)

	// Calculate memory and ephemeral storage budgets.
	memoryBudget, ephemeralStorageBudget := calculateResourceBudgets(config)

	// Calculate the recommended metadata cache sizes. The memoryBudget gets decreased after each recommendation.
	recommendation.metadataStatCacheBytes, memoryBudget = recommendMetadataCacheSize(config, cacheRequirements.metadataStatCacheBytes, memoryBudget, "stat")
	recommendation.metadataTypeCacheBytes, memoryBudget = recommendMetadataCacheSize(config, cacheRequirements.metadataTypeCacheBytes, memoryBudget, "type")

	// Calculate the recommended file cache size and medium.
	var err error
	recommendation.fileCacheBytes, recommendation.fileCacheMedium, err = recommendFileCacheSizeAndMedium(cacheRequirements, config, memoryBudget, ephemeralStorageBudget)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to recommend file cache: %v", err)
	}
	return recommendation, nil
}

// recommendFileCacheSizeAndMedium determines the optimal file cache size and medium.
// It iterates through a prioritized list of storage mediums (e.g., RAM, LSSD) based on the `config.nodeDetails.nodeType`.
// For each medium, it checks if the `cacheRequirements.fileCacheBytes` can be satisfied within the provided
// `memoryBudget` or `ephemeralStorageBudget`, also considering node-specific limitations like LSSD availability.
// The first suitable medium found in the priority list is returned along with the required cache size.
// If no medium can satisfy the requirements, it returns 0 bytes and an empty string, indicating the file cache should be disabled.
func recommendFileCacheSizeAndMedium(cacheRequirements *cacheRequirements, config *ProfileConfig, memoryBudget, ephemeralStorageBudget int64) (int64, string, error) {
	// Determine priority list & perform node type specific checks
	priorityList, found := config.scDetails.fileCacheMediumPriority[config.nodeDetails.nodeType]
	if !found {
		return 0, "", fmt.Errorf("no file cache medium priority list found for node type %q", config.nodeDetails.nodeType)
	}
	klog.V(6).Infof("Using file cache medium priority list for node type %q: %v", config.nodeDetails.nodeType, priorityList)

	// Error if LSSD is in the priority list for TPU
	// TODO(urielguzman): Re-consider emitting a warning instead of failing here.
	if config.nodeDetails.nodeType == nodeTypeTPU && slices.Contains(priorityList, util.MediumLSSD) {
		return 0, "", fmt.Errorf("LSSD medium is not supported for file cache on TPU node type %q", config.nodeDetails.nodeType)
	}

	// Skip medium evaluation if no file cache is required
	if cacheRequirements.fileCacheBytes <= 0 {
		klog.V(6).Infof("File cache not required (%d bytes). Skipping medium evaluation", cacheRequirements.fileCacheBytes)
		return 0, "", nil
	}

	for i, medium := range priorityList {
		klog.V(6).Infof("Evaluating medium %q (Priority %d/%d)", medium, i+1, len(priorityList))
		switch medium {
		case util.MediumRAM:
			// Check if the required file cache bytes fit into the available memory (RAM) medium.
			if cacheRequirements.fileCacheBytes <= memoryBudget {
				return cacheRequirements.fileCacheBytes, util.MediumRAM, nil
			}
			// Check if the required file cache bytes fir into the available ephemeral storage medium.
		case util.MediumLSSD:
			if !config.nodeDetails.hasLocalSSDEphemeralStorageAnnotation {
				klog.Warningf("Medium %q skipped on node type %q: Node annotation %q is not 'true'.", medium, config.nodeDetails.nodeType, ephemeralStorageLocalSSDLabelKey)
				break
			}
			if cacheRequirements.fileCacheBytes <= ephemeralStorageBudget {
				return cacheRequirements.fileCacheBytes, util.MediumLSSD, nil
			}
		default:
			return 0, "", fmt.Errorf("unkown storage medium: %q", medium)
		}
	}

	klog.Warningf("No suitable file cache medium found or requirement exceeded limits for all options based on priority list %v and available resources for node type %q. Disabling file cache.", priorityList, config.nodeDetails.nodeType)
	return 0, "", nil
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
		labels:        pod.Labels,
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
		volumeAttributes:                      volumeAttributes,
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
	case workloadTypeServingKey, workloadTypeTrainingKey, workloadTypeCheckpointingKey:
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

// isValidMountOption checks if a mount option string follows the allowed formats.
// An option is valid unless it meets one of the following prohibited conditions:
//  1. It is empty.
//  2. It starts/ends with a colon (e.g., ":a", "a:").
//  3. It starts/ends with an equal sign (e.g., "=a", "a=").
//  4. It contains more than one equals sign (e.g., "a=b=c").
//  5. It contains an equals sign and a colon  (e.g., "a:b=c", "a:b:c=d", "a=b:c", "a=b:c=d").
func isValidMountOption(opt string) bool {
	if opt == "" {
		return false
	}

	if strings.HasPrefix(opt, ":") || strings.HasSuffix(opt, ":") {
		return false // Prohibited: Starts with ":" or ends with ":"
	}

	if strings.HasPrefix(opt, "=") || strings.HasSuffix(opt, "=") {
		return false // Prohibited: Starts with "=" or ends with "="
	}

	equalsCount := strings.Count(opt, "=")
	if strings.Count(opt, "=") > 1 {
		return false // Prohibited: Multiple '=' signs
	}

	hasColon := strings.Contains(opt, ":")
	hasEquals := equalsCount == 1

	if hasColon && hasEquals {
		return false // Prohibited: ':' can't appear in the same string as '='
	}

	// If none of the invalid conditions are met, the option is valid.
	return true
}

// getMountOptionKey extracts the key part of a mount option string.
// This function assumes the input `opt` has already been validated by isValidMountOption.
// Formats supported and precedence:
// 1. key=value  (Key is everything before the first '=')
// 2. ...:key:value (Key is everything before the last ':')
// 3. key        (Key is the entire string)
func getMountOptionKey(opt string) string {
	if strings.Contains(opt, "=") {
		// isValidMountOption ensures at most one '=', so SplitN is safe.
		parts := strings.SplitN(opt, "=", 2)
		return parts[0]
	}
	if strings.Contains(opt, ":") {
		lastColon := strings.LastIndex(opt, ":")
		// isValidMountOption ensures it doesn't start with ':', so lastColon > 0 if ':' exists.
		return opt[:lastColon]
	}
	// No '=' or ':', the entire valid string is the key.
	return opt
}

// isCacheMountOptionKey returns true if the key is a managed cache mount option.
// The function checks if it's either in the "config-file" format or the "gcsfuse CLI"
// format.
func isCacheMountOptionKey(opt string) bool {
	key := getMountOptionKey(opt)
	switch key {
	// The mapping is hardcoded because there does not exist a programatic way
	// of normalizing the keys, due to grouping inconsistencies.
	case metadataStatCacheMaxSizeMiBMountOptionKey, "stat-cache-max-size-mb",
		metadataTypeCacheMaxSizeMiBMountOptionKey, "type-cache-max-size-mb",
		fileCacheSizeMiBMountOptionKey, "file-cache-max-size-mb":
		return true
	default:
		return false
	}
}

// mergeMountOptionsOnMissingKeys merges mount options from srcOpts into dstOpts.
// It returns an error if any option in dstOpts or srcOpts is invalid.
// An option from srcOpts is added only if the key is not already
// present in the keys of options within dstOpts.
//
// Note: Complete deduplication is impossible, since there doesn't exist
// any consistent mapping between gcsfuse config-file groups and CLI options.
// In the rare scenario that this happens, the user's CLI option will
// take precedence by the gcsfuse process hierarchy downstream. The CSI recommender
// should always ensure to only recommend config-file to respect this behavior.
func mergeMountOptionsOnMissingKeys(dstOpts, srcOpts []string) ([]string, error) {
	var mountOptions []string
	existingKeys := make(map[string]bool)

	// Validate and collect options from dstOpts.
	for _, opt := range dstOpts {
		if !isValidMountOption(opt) {
			return nil, fmt.Errorf("invalid mount option in dstOpts: %q", opt)
		}
		mountOptions = append(mountOptions, opt)
		existingKeys[strings.ToLower(getMountOptionKey(opt))] = true
	}

	if len(srcOpts) == 0 {
		return mountOptions, nil
	}

	// Validate and process srcOpts, adding only valid options with new normalized keys.
	for _, opt := range srcOpts {
		if !isValidMountOption(opt) {
			return nil, fmt.Errorf("invalid mount option in srcOpts: %q", opt)
		}

		key := strings.ToLower(getMountOptionKey(opt))
		if !existingKeys[key] {
			mountOptions = append(mountOptions, opt)
			existingKeys[key] = true
		}
	}
	return mountOptions, nil
}

// mergeMapsOnMissingKeys merges key/value pairs from src into dst.
// A key/value pair from src is added to dst only if the key
// does NOT already exist in dst.
func mergeMapsOnMissingKeys(dst, src map[string]string) map[string]string {
	if dst == nil {
		return src
	}
	if src == nil {
		return dst
	}
	result := map[string]string{}
	caseInsensitiveDstKeys := map[string]struct{}{}
	for k, v := range dst {
		result[k] = v
		caseInsensitiveDstKeys[strings.ToLower(k)] = struct{}{}
	}
	// Check if the case-insensitive keys match before adding the source key/val pair to the destination map.
	for key, value := range src {
		caseInsensitiveSrcKey := strings.ToLower(key)
		if _, exists := caseInsensitiveDstKeys[caseInsensitiveSrcKey]; !exists {
			result[key] = value
			caseInsensitiveDstKeys[caseInsensitiveSrcKey] = struct{}{}
		}
	}
	return result
}
