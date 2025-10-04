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
	"strconv"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	nvidiaGpuResourceName = corev1.ResourceName("nvidia.com/gpu")
	googleTpuResourceName = corev1.ResourceName("google.com/tpu")

	metadataBytesPerObject = 1500

	annotationNumObjects     = "gke-gcsfuse/bucket-scan-num-objects"
	annotationTotalSizeBytes = "gke-gcsfuse/bucket-scan-total-size-bytes"

	// Node types
	nodeTypeTPU            = "tpu"
	nodeTypeGPU            = "gpu"
	nodeTypeGeneralPurpose = "general_purpose"

	// Standard medium names
	mediumRAM  = "ram"
	mediumLSSD = "lssd"

	// GKEAppliedNodeLabelsAnnotationKey is the annotation key that stores a comma-separated list of node labels.
	GKEAppliedNodeLabelsAnnotationKey = "node.gke.io/last-applied-node-labels"
	// EphemeralStorageLocalSSDLabelKey is the specific label key we are looking for within the applied labels.
	EphemeralStorageLocalSSDLabelKey = "cloud.google.com/gke-ephemeral-storage-local-ssd"
	// ExpectedEphemeralStorageLocalSSDLabelValue is the expected value for the label if local SSD is present.
	ExpectedEphemeralStorageLocalSSDLabelValue = "true"

	// storage class param keys
	fuseFileCacheMediumPriorityKey           = "fuseFileCacheMediumPriority"
	fuseMemoryAllocatableFactorKey           = "fuseMemoryAllocatableFactor"
	fuseEphemeralStorageAllocatableFactorKey = "fuseEphemeralStorageAllocatableFactor"
)

type GCSFuseRecommendations struct {
	MetadataCacheBytes                            int64
	FileCacheBytes                                int64
	FilecacheMedium                               string
	SignalNumObjects                              int64
	SignalTotalDataSizeBytes                      int64
	SignalNodeType                                string
	SignalNodeAllocatableBytesRam                 int64
	SignalNodeAllocatableBytesEpehemralStorage    int64
	SignalMaxFuseMemoryAllocatableBytes           int64
	SignalMaxFuseEphemeralStorageAllocatableBytes int64
}

type GCSFuseRecommendationsConfig struct {
	pv                                    *corev1.PersistentVolume
	pvDetails                             *PVDetails
	nodeName                              string
	nodeAllocatables                      *NodeAllocatables
	nodeType                              string
	fileCacheMediumPriority               map[string][]string
	hasLocalSSDEphemeralStorageAnnotation bool
	fuseMemoryAllocatableFactor           float64
	fuseEphemeralStorageAllocatableFactor float64
}

type PVDetails struct {
	NumObjects     int64
	TotalSizeBytes int64
}
type NodeAllocatables struct {
	MemoryBytes           int64
	EphemeralStorageBytes int64
}

func (s *nodeServer) buildGCSFuseRecommendationsConfig(targetPath string, node *corev1.Node) (*GCSFuseRecommendationsConfig, error) {
	config := GCSFuseRecommendationsConfig{
		nodeName: node.Name,
	}

	// Get the PV name from target path.
	_, volumeName, err := util.ParsePodIDVolumeFromTargetpath(targetPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get pv name from target path %q: %v", targetPath, err)
	}

	// Get the PV object using the PV name.
	pv, err := s.k8sClients.GetPV(volumeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pv %q: %v", volumeName, err)
	}
	config.pv = pv

	// Get the PV details from the PV object's annotations.
	pvDetails, err := s.getPVDetailsFromAnnotations(pv)
	if err != nil {
		return nil, err
	}
	config.pvDetails = pvDetails

	// Get the StorageClassName from the PV object.
	scName := pv.Spec.StorageClassName
	if scName == "" {
		return nil, fmt.Errorf("pv %q has empty StorageClassName", pv.Name)
	}

	// Get the StorageClass object from the StorageClassName.
	sc, err := s.k8sClients.GetSC(scName)
	if err != nil {
		return nil, fmt.Errorf("failed to get StorageClass %q: %v", scName, err)
	}

	// Get the parameters from the StorageClass.
	if sc.Parameters == nil {
		return nil, fmt.Errorf("sc %q found but has nil Parameters map for volume %q", scName, pv.Name)
	}
	fileCacheMediumPriorityStr, ok := sc.Parameters[fuseFileCacheMediumPriorityKey]
	if !ok {
		return nil, fmt.Errorf("missing fuseFileCacheMediumPriority")
	}

	// Get the file cache medium priority from the StorageClass object.
	fileCacheMediumPriority, err := parseFileCacheMediumPriority(fileCacheMediumPriorityStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fileCacheMediumPriority: %v", err)
	}
	config.fileCacheMediumPriority = fileCacheMediumPriority

	// Get the fuse ephemeral storage allocatable factor from the StorageClass parameters.
	fuseEphemeralStorageAllocatableFactor, ok := sc.Parameters[fuseEphemeralStorageAllocatableFactorKey]
	if !ok {
		return nil, fmt.Errorf("missing fuseEphemeralStorageAllocatableFactor")
	}
	fuseEphemeralStorageAllocatableFactorVal, err := strconv.ParseFloat(fuseEphemeralStorageAllocatableFactor, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fuseEphemeralStorageAllocatableFactor: %v", err)
	}
	config.fuseEphemeralStorageAllocatableFactor = fuseEphemeralStorageAllocatableFactorVal

	// Get the fuse memory allocatable factor from the StorageClass parameters.
	fuseMemoryAllocatableFactor, ok := sc.Parameters[fuseMemoryAllocatableFactorKey]
	if !ok {
		return nil, fmt.Errorf("missing fuseMemoryAllocatableFactor")
	}
	fuseMemoryAllocatableFactorVal, err := strconv.ParseFloat(fuseMemoryAllocatableFactor, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fuseMemoryAllocatableFactor: %v", err)
	}
	config.fuseMemoryAllocatableFactor = fuseMemoryAllocatableFactorVal

	// Parse the node allocatables from the node.
	nodeAllocatables, err := parseNodeAllocatableResources(node.Status.Allocatable)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node allocatable resources from node %q: %v", node.Name, err)
	}
	config.nodeAllocatables = nodeAllocatables

	// Get the node type from the node.
	nodeType := nodeTypeGeneralPurpose
	if isGpuNodeByResource(node) {
		nodeType = nodeTypeGPU
	}
	if isTpuNodeByResource(node) {
		nodeType = nodeTypeTPU
	}
	config.nodeType = nodeType

	// Return the config.
	config.hasLocalSSDEphemeralStorageAnnotation = hasLocalSSDEphemeralStorageAnnotation(node.Annotations)
	return &config, nil
}

func (s *nodeServer) calculateGCSFuseRecommendations(targetPath string, node *corev1.Node) error {
	config, err := s.buildGCSFuseRecommendationsConfig(targetPath, node)
	if err != nil {
		return fmt.Errorf("failed to build GCSFuseRecommendationsConfig: %v", err)
	}

	recommendations, err := s.recommendGCSFuseCacheConfigs(config)
	if err != nil {
		return err
	}

	// Do something here with recommendations (probably pass to mount options). Log for now.
	klog.Infof("GCSFuseRecommendations: %+v", recommendations)
	return nil
}

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

func (s *nodeServer) getPVDetailsFromAnnotations(
	pv *corev1.PersistentVolume) (*PVDetails, error) {
	// Extract annotations
	pvAnnotations := pv.GetAnnotations()
	if pvAnnotations == nil {
		klog.Warningf("PV %q has no annotations.", pv.Name)
		return nil, nil
	}

	// Get annotation values as strings
	numObjectsStr, numObjectsFound := pvAnnotations[annotationNumObjects]
	totalSizeStr, totalSizeFound := pvAnnotations[annotationTotalSizeBytes]

	// Check for missing annotations
	var missingAnnotations []string
	if !numObjectsFound {
		missingAnnotations = append(missingAnnotations, annotationNumObjects)
	}
	if !totalSizeFound {
		missingAnnotations = append(missingAnnotations, annotationTotalSizeBytes)
	}

	if len(missingAnnotations) > 0 {
		klog.Warningf("PV %q is missing required annotations: %s", pv.Name, strings.Join(missingAnnotations, ", "))
		return nil, nil
	}

	// Parse annotations into int64
	var parseErrors []string
	var numObjects int64
	var totalSizeBytes int64
	numObjects, err := strconv.ParseInt(numObjectsStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", annotationNumObjects, numObjectsStr, err))
	}
	totalSizeBytes, err = strconv.ParseInt(totalSizeStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", annotationTotalSizeBytes, totalSizeStr, err))
	}
	if len(parseErrors) > 0 {
		errorMsg := strings.Join(parseErrors, "; ")
		klog.Errorf("PV %q has invalid annotation format: %s", pv.Name, errorMsg)
		return nil, fmt.Errorf("invalid annotation format on PV %q: %s", pv.Name, errorMsg)
	}

	return &PVDetails{
		NumObjects:     numObjects,
		TotalSizeBytes: totalSizeBytes,
	}, nil
}

// recommendGCSFuseCacheConfigs calculates recommended cache sizes and medium.
// Adds specific logic for PD medium: caps at 64TiB and disables if requirement > 64TiB.
func (s *nodeServer) recommendGCSFuseCacheConfigs(config *GCSFuseRecommendationsConfig) (*GCSFuseRecommendations, error) {
	// Validate input.
	if config.pvDetails == nil {
		return nil, fmt.Errorf("pvDetails cannot be nil")
	}
	if config.nodeAllocatables == nil {
		return nil, fmt.Errorf("nodeAllocatables cannot be nil")
	}
	// Allow factors to be 0, but clamp negative values to 0 for calculation
	if config.fuseMemoryAllocatableFactor < 0 {
		return nil, fmt.Errorf("fuseMemoryAllocatableFactor is < 0")
	}
	if config.fuseEphemeralStorageAllocatableFactor < 0 {
		return nil, fmt.Errorf("fuseEphemeralStorageAllocatableFactor is < 0")
	}
	recommendations := &GCSFuseRecommendations{}

	// Calculate initial requirements
	metadataCacheRequired := config.pvDetails.NumObjects * metadataBytesPerObject
	fileCacheRequired := config.pvDetails.TotalSizeBytes
	recommendations.SignalNumObjects = config.pvDetails.NumObjects
	recommendations.SignalTotalDataSizeBytes = config.pvDetails.TotalSizeBytes
	klog.V(6).Infof("initial Requirements: MetadataCache=%d bytes, FileCache=%d bytes", metadataCacheRequired, fileCacheRequired)

	// Calculate max available resources & cap metadata cache
	// Use allocatable resources directly, ensure non-negative
	safeNodeMemory := config.nodeAllocatables.MemoryBytes
	safeNodeEphemeralStorage := config.nodeAllocatables.EphemeralStorageBytes
	recommendations.SignalNodeAllocatableBytesRam = safeNodeMemory
	recommendations.SignalNodeAllocatableBytesEpehemralStorage = safeNodeEphemeralStorage

	// Calculate budgets based on factors
	maxFuseMemory := int64(float64(safeNodeMemory) * config.fuseMemoryAllocatableFactor)
	maxFuseEphemeralStorage := int64(float64(safeNodeEphemeralStorage) * config.fuseEphemeralStorageAllocatableFactor)
	recommendations.SignalMaxFuseEphemeralStorageAllocatableBytes = maxFuseEphemeralStorage
	recommendations.SignalMaxFuseMemoryAllocatableBytes = maxFuseMemory

	klog.V(6).Infof("max Fuse Budgets: Memory=%d bytes, EphemeralStorage=%d bytes (Node Allocatable Mem: %d, Eph: %d; Factors Mem: %.2f, Eph: %.2f)",
		maxFuseMemory, maxFuseEphemeralStorage, safeNodeMemory, safeNodeEphemeralStorage, config.fuseMemoryAllocatableFactor, config.fuseEphemeralStorageAllocatableFactor)
	recommendations.MetadataCacheBytes = minInt64(metadataCacheRequired, maxFuseMemory)
	if recommendations.MetadataCacheBytes < metadataCacheRequired && metadataCacheRequired > 0 {
		klog.Warningf("For target node %s, required metadata cache %d bytes capped to available fuse memory budget %d bytes. This can impact perf due to increased GCS metadata API calls", config.nodeName, metadataCacheRequired, recommendations.MetadataCacheBytes)
	}

	// Calculate RAM remaining after allocating metadata cache
	availableRamForFileCache := maxInt64(0, maxFuseMemory-recommendations.MetadataCacheBytes)
	klog.V(6).Infof("available RAM for File Cache (after metadata): %d bytes", availableRamForFileCache)
	recommendations.SignalNodeType = config.nodeType

	// Determine priority list & perform node type specific checks
	priorityList, found := config.fileCacheMediumPriority[config.nodeType]
	if !found {
		return nil, fmt.Errorf("no file cache medium priority list found for nodeType %q", config.nodeType)
	}
	klog.V(6).Infof("Using file cache medium priority list for nodeType %q: %v", config.nodeType, priorityList)

	// TPU Check: Error if LSSD is in the priority list for TPU
	if config.nodeType == nodeTypeTPU {
		for _, mediumInList := range priorityList {
			if mediumInList == mediumLSSD {
				return nil, fmt.Errorf("LSSD medium is not supported/recommended for file cache on TPU node type (%q)", config.nodeType)
			}
		}
		klog.V(6).Infof("TPU node type check passed: LSSD not found in priority list.")
	}

	// Walk through file cache medium priority list
	foundSuitableMedium := false
	// Skip medium evaluation entirely if no file cache is required
	if fileCacheRequired <= 0 {
		klog.V(6).Infof("File cache not required (%d bytes). Skipping medium evaluation.", fileCacheRequired)
		recommendations.FileCacheBytes = 0
		recommendations.FilecacheMedium = ""
		foundSuitableMedium = true
	} else {
		// Only loop if file cache is actually needed
		for i, medium := range priorityList {
			isLastMedium := (i == len(priorityList)-1)
			klog.V(6).Infof("Evaluating medium %q (Priority %d/%d)", medium, i+1, len(priorityList))
			availableForMedium := int64(0)
			mediumAllowed := true
			// Determine availability and limits for the current medium
			switch {
			case medium == mediumRAM:
				availableForMedium = availableRamForFileCache
				if availableForMedium <= 0 {
					klog.V(6).Infof("Medium %q skipped: No RAM available/budgeted for file cache.", medium)
					mediumAllowed = false
				}
			case medium == mediumLSSD:
				if maxFuseEphemeralStorage <= 0 {
					klog.V(6).Infof("Medium %q skipped: No ephemeral storage budget available (maxFuseEphemeralStorage=%d).", medium, maxFuseEphemeralStorage)
					mediumAllowed = false
				} else if !config.hasLocalSSDEphemeralStorageAnnotation {
					// Warning/Skip logic for LSSD annotation missing (as before)
					if config.nodeType == nodeTypeGPU || config.nodeType == nodeTypeGeneralPurpose {
						klog.Warningf("For target node %q (type %q) does not have local SSD annotation %q set to true; skipping LSSD medium as a candidate", config.nodeName, config.nodeType, EphemeralStorageLocalSSDLabelKey)
					} else {
						klog.V(4).Infof("Medium %q skipped on node type %q: Node annotation %q is not 'true'.", medium, config.nodeType, EphemeralStorageLocalSSDLabelKey)
					}
					mediumAllowed = false
				} else {
					// LSSD Allowed: Use the ephemeral storage budget
					availableForMedium = maxFuseEphemeralStorage
				}
			default:
				return nil, fmt.Errorf("unkown storage medium")
			}

			// Process medium based on allowance and capacity
			if !mediumAllowed {
				if isLastMedium && !foundSuitableMedium {
					klog.Warningf("Last resort medium %q is not allowed/available.", medium)
					break
				}
				// Otherwise (not allowed, not last), just continue to next medium
				continue
			}

			// Medium is allowed, check if requirement fits
			klog.V(6).Infof("Medium %q is allowed. Checking if requirement %d bytes fits in available %d bytes.", medium, fileCacheRequired, availableForMedium)
			if fileCacheRequired <= availableForMedium {
				recommendations.FileCacheBytes = fileCacheRequired
				recommendations.FilecacheMedium = medium
				klog.V(6).Infof("Selected medium %q: File cache requirement %d bytes fits within available %d bytes.", medium, fileCacheRequired, availableForMedium)
				foundSuitableMedium = true
				break
			} else if !isLastMedium {
				klog.V(6).Infof("File cache requirement %d bytes does not fit in medium %q (available %d bytes), trying next priority.", fileCacheRequired, medium, availableForMedium)
			}
		}
	}

	// Post-loop checks & warnings
	if !foundSuitableMedium { // This only happens now if fileCacheRequired > 0 and no medium worked
		klog.Warningf("No suitable file cache medium found or requirement exceeded limits for all options based on priority list %v and available resources for node type %q. Disabling file cache.", priorityList, config.nodeType)
		recommendations.FileCacheBytes = 0
		recommendations.FilecacheMedium = ""
	}
	klog.V(6).Infof("Final Recommendations: MetadataCacheBytes=%d, FileCacheBytes=%d, FilecacheMedium=%q",
		recommendations.MetadataCacheBytes, recommendations.FileCacheBytes, recommendations.FilecacheMedium)
	return recommendations, nil
}

func hasLocalSSDEphemeralStorageAnnotation(annotations map[string]string) bool {
	// Step 1: Get the value of the 'node.gke.io/last-applied-node-labels' annotation.
	appliedLabelsStr, ok := annotations[GKEAppliedNodeLabelsAnnotationKey]
	if !ok || appliedLabelsStr == "" {
		// The annotation key itself is missing or empty, so we can't determine.
		return false
	}
	// Step 2: Split the comma-separated string of labels into individual label strings.
	labelPairs := strings.Split(appliedLabelsStr, ",")
	// Step 3: Iterate through each label string (e.g., "key=value").
	for _, labelPairStr := range labelPairs {
		// Step 4: Split the label string into key and value.
		// Use SplitN to handle cases where a value might unexpectedly contain an '='.
		kv := strings.SplitN(labelPairStr, "=", 2)
		if len(kv) != 2 {
			// Malformed label pair, skip it.
			continue
		}
		labelKey := strings.TrimSpace(kv[0])
		labelValue := strings.TrimSpace(kv[1])
		// Step 5: Check if this is the label we're looking for and if its value is "true".
		if labelKey == EphemeralStorageLocalSSDLabelKey && labelValue == ExpectedEphemeralStorageLocalSSDLabelValue {
			return true
		}
	}
	// If we've gone through all labels and haven't found the specific key-value pair,
	// then the node does not have the indicator.
	return false
}

func parseNodeAllocatableResources(nodeAllocatable corev1.ResourceList) (*NodeAllocatables, error) {
	var nodeAllocatables NodeAllocatables

	// Parse memory
	if memQuantity, ok := nodeAllocatable[corev1.ResourceMemory]; ok {
		memBytes, parsedOK := memQuantity.AsInt64()
		if parsedOK {
			nodeAllocatables.MemoryBytes = memBytes
			klog.V(6).Infof("Successfully parsed allocatable memory: %d bytes (%s)", memBytes, memQuantity.String())
		} else {
			return nil, fmt.Errorf("could not parse node allocatable memory quantity %q as int64 bytes", memQuantity.String())
		}
	} else {
		return nil, fmt.Errorf("node status allocatable map does not contain resource key %q", corev1.ResourceMemory)
	}

	// Parse ephemeral storage
	if storageQuantity, ok := nodeAllocatable[corev1.ResourceEphemeralStorage]; ok {
		storageBytes, parsedOK := storageQuantity.AsInt64()
		if parsedOK {
			nodeAllocatables.EphemeralStorageBytes = storageBytes
			klog.V(6).Infof("Successfully parsed allocatable ephemeral storage: %d bytes (%s)", storageBytes, storageQuantity.String())
		} else {
			return nil, fmt.Errorf("could not parse node allocatable ephemeral storage quantity %q as int64 bytes", storageQuantity.String())
		}
	} else {
		return nil, fmt.Errorf("node status allocatable map does not contain resource key %q", corev1.ResourceEphemeralStorage)
	}

	return &nodeAllocatables, nil
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
	// Check if the key exists and the quantity is greater than 0
	return exists && tpuQuantity.CmpInt64(0) > 0
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
