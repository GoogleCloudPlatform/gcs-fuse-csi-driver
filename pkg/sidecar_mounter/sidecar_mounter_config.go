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

package sidecarmounter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

const (
	GCSFuseAppName     = "gke-gcs-fuse-csi"
	TempDir            = "/temp-dir"
	unixSocketBasePath = "unix://"
	TokenFileName      = "token.sock" // #nosec G101
)

// MountConfig contains the information gcsfuse needs.
type MountConfig struct {
	FileDescriptor                 int                   `json:"-"`
	VolumeName                     string                `json:"volumeName,omitempty"`
	BucketName                     string                `json:"bucketName,omitempty"`
	BufferDir                      string                `json:"-"`
	CacheDir                       string                `json:"-"`
	TempDir                        string                `json:"-"`
	ConfigFile                     string                `json:"-"`
	Options                        []string              `json:"options,omitempty"`
	ErrWriter                      stderrWriterInterface `json:"-"`
	FlagMap                        map[string]string     `json:"-"`
	ConfigFileFlagMap              map[string]string     `json:"-"`
	TokenServerIdentityProvider    string                `json:"-"`
	HostNetworkKSAOptIn            bool                  `json:"-"`
	EnableCloudProfilerForSidecar  bool                  `json:"-"`
	PodNamespace                   string                `json:"-"`
	ServiceAccountName             string                `json:"-"`
	EnableSidecarBucketAccessCheck bool                  `json:"-"`
	TokenServerIdentityPool        string                `json:"-"`
	SidecarRetryConfig             sidecarRetryConfig    `json:"-"`
	FileCacheMedium                string                `json:"-"`
	GcsFuseNumaNode                int                   `json:"-"`
}

// sidecarRetryConfig controls the retry configurations for sidecarRetry behivior for storage service creation and bucket access check.
type sidecarRetryConfig struct {
	Duration time.Duration
	Factor   float64
	Cap      time.Duration
	Steps    int
	Jitter   float64
}

var prometheusPort = 62990

var disallowedFlags = map[string]bool{
	"temp-dir":             true,
	"config-file":          true,
	"foreground":           true,
	"log-file":             true,
	"log-format":           true,
	"key-file":             true,
	"token-url":            true,
	"reuse-token-from-url": true,
	"o":                    true,
	"cache-dir":            true,
	"prometheus-port":      true,
}

var boolFlags = map[string]bool{
	"implicit-dirs":                 true,
	"enable-nonexistent-type-cache": true,
	"debug_fuse_errors":             true,
	"debug_fuse":                    true,
	"debug_fs":                      true,
	"debug_gcs":                     true,
	"debug_http":                    true,
	"debug_invariants":              true,
	"debug_mutex":                   true,
	"disable-autoconfig":            true,
}

// Fetch the following information from a given socket path:
// 1. Pod volume name
// 2. The file descriptor
// 3. GCS bucket name
// 4. Mount options passing to gcsfuse (passed by the csi mounter).
func NewMountConfig(sp string, flagMapFromDriver map[string]string) *MountConfig {
	// socket path pattern: /gcsfuse-tmp/.volumes/<volume-name>/socket
	tempDir := filepath.Dir(sp)
	volumeName := filepath.Base(tempDir)
	mc := MountConfig{
		VolumeName: volumeName,
		BufferDir:  filepath.Join(webhook.SidecarContainerBufferVolumeMountPath, ".volumes", volumeName),
		CacheDir:   filepath.Join(webhook.SidecarContainerCacheVolumeMountPath, ".volumes", volumeName),
		TempDir:    tempDir,
		ConfigFile: filepath.Join(webhook.SidecarContainerTmpVolumeMountPath, ".volumes", volumeName, "config.yaml"),
		ErrWriter:  NewErrorWriter(filepath.Join(tempDir, "error")),
	}

	klog.Infof("connecting to socket %q", sp)
	c, err := net.Dial("unix", sp)
	if err != nil {
		mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to connect to the socket %q: %v", sp, err))

		return nil
	}

	fd, msg, err := util.RecvMsg(c)
	if err != nil {
		mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to receive mount options from the socket %q: %v", sp, err))

		return nil
	}
	// as we got all the information from the socket, closing the connection and deleting the socket
	c.Close()
	if err = syscall.Unlink(sp); err != nil && err != syscall.ENOENT {
		// ENOENT suggests, the file may have already been unliked and removed, since the listener is closed on the csi node driver when it has successfully responsed to the sidecar
		klog.Errorf("failed to unlink socket %q: %v", sp, err)
	}

	mc.FileDescriptor = fd

	if err := json.Unmarshal(msg, &mc); err != nil {
		mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to unmarshal the mount config: %v", err))

		return nil
	}

	if mc.BucketName == "" {
		mc.ErrWriter.WriteMsg("failed to fetch bucket name from CSI driver")

		return nil
	}

	mc.prepareMountArgs()
	mergeFlags(mc.ConfigFileFlagMap, flagMapFromDriver)
	if err := mc.prepareConfigFile(); err != nil {
		mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to create config file %q: %v", mc.ConfigFile, err))

		return nil
	}

	return &mc
}

func mergeFlags(mountConfigFlagMap map[string]string, driverFlagMap map[string]string) {
	for key, value := range driverFlagMap {
		_, ok := mountConfigFlagMap[key]
		// Only overwrite values not set in mountConfigMap
		if !ok {
			mountConfigFlagMap[key] = value
		}
	}
}

func (mc *MountConfig) prepareMountArgs() {
	flagMap := map[string]string{
		"app-name":        GCSFuseAppName,
		"temp-dir":        mc.BufferDir + TempDir,
		"config-file":     mc.ConfigFile,
		"foreground":      "",
		"uid":             "0",
		"gid":             "0",
		"prometheus-port": strconv.Itoa(prometheusPort),
	}
	// Use a new port each gcsfuse instance that we start.
	prometheusPort++

	configFileFlagMap := map[string]string{
		"logging:file-path": "/dev/fd/1", // redirect the output to cmd stdout
		"logging:format":    "json",
		"cache-dir":         "", // by default the gcsfuse file cache is disabled on GKE
	}

	invalidArgs := []string{}

	mc.GcsFuseNumaNode = -1
	for _, arg := range mc.Options {
		klog.Infof("processing mount arg %v", arg)

		// Config file flags are identified by not containing '=' and containing ':'.
		// e.g., "logging:severity:error"
		if !strings.Contains(arg, "=") && strings.Contains(arg, ":") && !strings.Contains(arg, "https") {
			var f, v string
			if strings.HasPrefix(arg, "gcs-connection:custom-endpoint:") {
				parts := strings.SplitN(arg, ":", 3)
				f = parts[0] + ":" + parts[1]
				v = parts[2]
			} else {
				i := strings.LastIndex(arg, ":")
				f, v = arg[:i], arg[i+1:]
			}
			if f == util.DisableMetricsForGKE {
				if v == util.TrueStr {
					flagMap["prometheus-port"] = "0"
				}

				continue
			}

			if disallowedFlags[f] {
				invalidArgs = append(invalidArgs, arg)
			} else {
				configFileFlagMap[f] = v
			}

			// if the value of flag file-cache:max-size-mb is not 0,
			// enable the file cache feature by passing the cache directory.
			if f == "file-cache:max-size-mb" && v != "0" {
				configFileFlagMap["cache-dir"] = mc.CacheDir
			}

			continue
		}

		argPair := strings.SplitN(arg, "=", 2)
		if len(argPair) == 0 {
			continue
		}

		flag := argPair[0]
		if disallowedFlags[flag] {
			invalidArgs = append(invalidArgs, arg)

			continue
		}

		value := ""
		if len(argPair) > 1 {
			value = argPair[1]
		}

		switch flag {
		case util.TokenServerIdentityProviderConst:
			mc.TokenServerIdentityProvider = value
			continue

		case util.TokenServerIdentityPoolConst:
			mc.TokenServerIdentityPool = value
			continue

		case util.OptInHnw:
			mc.HostNetworkKSAOptIn = value == util.TrueStr
			continue

		case util.EnableSidecarBucketAccessCheckConst:
			mc.EnableSidecarBucketAccessCheck = value == util.TrueStr
			continue

		case util.PodNamespaceConst:
			mc.PodNamespace = value
			continue

		case util.ServiceAccountNameConst:
			mc.ServiceAccountName = value
			continue

		case util.EnableCloudProfilerForSidecarConst:
			mc.EnableCloudProfilerForSidecar = value == util.TrueStr
			continue

		case util.FileCacheMediumConst:
			mc.FileCacheMedium = value
			continue

		case util.GCSFuseNumaNodeArg:
			idx, err := strconv.Atoi(value)
			if err != nil {
				invalidArgs = append(invalidArgs, arg)
			} else {
				mc.GcsFuseNumaNode = idx
			}
			continue
		}

		switch {
		case boolFlags[flag] && value != "":
			flag = flag + "=" + value
			if value == util.TrueStr || value == util.FalseStr {
				value = ""
			} else {
				invalidArgs = append(invalidArgs, flag)

				continue
			}
		case flag == "app-name":
			value = GCSFuseAppName + "-" + value
		}

		flagMap[flag] = value
	}

	if len(invalidArgs) > 0 {
		klog.Warningf("got invalid arguments for volume %q: %v. Will discard invalid args and continue to mount.",
			invalidArgs, mc.VolumeName)
	}

	// This flag can only be passed by the CSI driver if the following two things are true:
	//   1. User is using the gcsfuse profiles feature.
	//   2. User did not create a custom cache: https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-sidecar#configure-custom-read-cache-volume
	// This allows customers to override the caching medium while using the profiles feature, if they wish.
	if mc.FileCacheMedium != "" {
		cacheDir := ""
		switch mc.FileCacheMedium {
		case util.MediumRAM:
			cacheDir = webhook.SidecarContainerFileCacheRamDiskVolumeMountPath
		case util.MediumLSSD:
			cacheDir = webhook.SidecarContainerFileCacheEphemeralDiskVolumeMountPath
		default:
			klog.Warningf("got invalid value for %q: %q. Will discard and continue to mount.", util.FileCacheMediumConst, mc.FileCacheMedium)
		}
		// Ensure a unique directory is passed for each volume.
		cacheDir = filepath.Join(cacheDir, ".volumes", mc.VolumeName)
		configFileFlagMap["cache-dir"] = cacheDir
		mc.CacheDir = cacheDir
		klog.Infof("Overriding cache-dir with %q for medium %q", cacheDir, mc.FileCacheMedium)
	}

	mc.FlagMap = flagMap
	mc.ConfigFileFlagMap = configFileFlagMap
}

func (mc *MountConfig) prepareConfigFile() error {
	if mc.ConfigFileFlagMap == nil {
		return errors.New("got empty config file flag map")
	}

	configMap := map[string]interface{}{}

	for f, v := range mc.ConfigFileFlagMap {
		curLevel := configMap
		tokens := strings.Split(f, ":")
		for i, t := range tokens {
			if i == len(tokens)-1 {
				if _, ok := curLevel[t].(map[string]interface{}); ok {
					return fmt.Errorf("invalid config file flag: %q", f)
				}

				if intVal, err := strconv.ParseInt(v, 10, 64); err == nil {
					curLevel[t] = intVal
				} else if boolVal, err := strconv.ParseBool(v); err == nil {
					curLevel[t] = boolVal
				} else {
					curLevel[t] = v
				}

				break
			}

			if _, ok := curLevel[t]; !ok {
				curLevel[t] = map[string]interface{}{}
			}

			if nextLevel, ok := curLevel[t].(map[string]interface{}); ok {
				curLevel = nextLevel
			} else {
				return fmt.Errorf("invalid config file flag: %q", f)
			}
		}
	}
	if mc.TokenServerIdentityProvider != "" && mc.HostNetworkKSAOptIn {
		configMap["gcs-auth"] = map[string]interface{}{
			"token-url": unixSocketBasePath + filepath.Join(mc.TempDir, TokenFileName),
		}
	}

	yamlData, err := yaml.Marshal(&configMap)
	if err != nil {
		return err
	}

	klog.Infof("gcsfuse config file content: %v", configMap)

	return os.WriteFile(mc.ConfigFile, yamlData, 0o400)
}
