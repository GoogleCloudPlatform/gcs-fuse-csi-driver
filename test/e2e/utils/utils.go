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

package utils

import (
	"os"
	"os/exec"

	"k8s.io/klog/v2"
)

type TestCase struct {
	Name string `yaml:"name"`
}
type TestPackage struct {
	// PackageName      string     `yaml:"packageName"`
	TestBucket       string       `yaml:"test_bucket"`
	MountedDirectory string       `yaml:"mounted_directory"`
	LogFile          string       `yaml:"log_file"`
	RunOnGke         bool         `yaml:"run_on_gke"`
	Configs          []TestConfig `yaml:"configs"`
}

type TestConfig struct {
	Flags      []string       `yaml:"flags"`
	Compatible TestBucketType `yaml:"compatible"`
}

type TestBucketType struct {
	Flat  bool `yaml:"flat"`
	HNS   bool `yaml:"hns"`
	Zonal bool `yaml:"zonal"`
}

type TestPackages map[string][]TestPackage

var LoadedYAMLTestConfigs TestPackages

func EnsureVariable(v *string, set bool, msgOnError string) {
	if set && len(*v) == 0 {
		klog.Fatal(msgOnError)
	} else if !set && len(*v) != 0 {
		klog.Fatal(msgOnError)
	}
}

func isVariableSet(v string) bool {
	return len(v) != 0
}

func runCommand(action string, cmd *exec.Cmd) error {
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	klog.Infof("%s", action)
	klog.Infof("cmd env=%v", cmd.Env)
	klog.Infof("cmd path=%v", cmd.Path)
	klog.Infof("cmd args=%s", cmd.Args)

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Wait()
}

// func loadTestSuiteConfig(path string) (*TestSuiteConfig, error) {
// 	data, err := os.ReadFile(path)
// 	if err != nil {
// 		return nil, err
// 	}
// 	var config TestSuiteConfig
// 	if err := yaml.Unmarshal(data, &config); err != nil {
// 		return nil, err
// 	}
// 	return &config, nil
// }

// func GetTestsForSuite(suiteName string) [string][]string {
// 	return suiteTestMap[suiteName]
// }

// func IsTestEnabled(suiteName, testName string) bool {
// 	for _, name := range suiteTestMap[suiteName] {
// 		if name == testName {
// 			return true
// 		}
// 	}
// 	return false
// }
