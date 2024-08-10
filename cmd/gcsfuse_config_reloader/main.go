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

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	sidecarmounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/sidecar_mounter"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

var (
	volumeName      = flag.String("volume-name", "", "volume name")
	volumeBasePath  = flag.String("volume-base-path", webhook.SidecarContainerTmpVolumeMountPath+"/.volumes", "volume base path")
	newConfigOption = flag.String("new-config-option", "", "new config option in the format <key>:<value>")
	// This is set at compile time.
	version = "unknown"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.Infof("Running Google Cloud Storage FUSE CSI driver GCSFuse conifg file reloader %v", version)
	configFilePath := fmt.Sprintf("%v/%v/config.yaml", *volumeBasePath, *volumeName)

	data, err := os.ReadFile(configFilePath)
	if err != nil {
		fmt.Println("Error reading YAML file:", err)
		return
	}

	configMap := make(map[string]interface{})
	err = yaml.Unmarshal(data, &configMap)
	if err != nil {
		fmt.Println("Error unmarshaling YAML:", err)
		return
	}

	klog.Infof("old config file: %v", configMap)

	arg := *newConfigOption
	i := strings.LastIndex(arg, ":")
	f, v := arg[:i], arg[i+1:]
	mc := sidecarmounter.MountConfig{
		ConfigFileFlagMap: map[string]string{f: v},
		ConfigFile:        configFilePath,
	}

	klog.Infof("new config option: %v", mc.ConfigFileFlagMap)
	err = mc.PrepareConfigFile(configMap)
	if err != nil {
		fmt.Println("Error overwritting YAML file:", err)
		return
	}

	data, err = os.ReadFile(configFilePath)
	if err != nil {
		fmt.Println("Error reading YAML file:", err)
		return
	}

	klog.Infof("new config file content:\n%v", string(data))
}
