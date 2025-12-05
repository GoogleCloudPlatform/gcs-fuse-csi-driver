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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
	boskosclient "sigs.k8s.io/boskos/client"
	"sigs.k8s.io/boskos/common"
)

var boskos, _ = boskosclient.NewClient(os.Getenv("JOB_NAME"), "http://boskos", "", "")

// getBoskosProject retries acquiring a boskos project until success or timeout.
func getBoskosProject(resourceType string) *common.Resource {
	timer := time.NewTimer(30 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-timer.C:
			klog.Fatalf("timed out trying to acquire boskos project")
		case <-ticker.C:
			p, err := boskos.Acquire(resourceType, "free", "busy")
			switch {
			case err != nil:
				klog.Warningf("boskos failed to acquire project: %v", err)
			case p == nil:
				klog.Warningf("boskos does not have a free %s at the moment", resourceType)
			default:
				return p
			}
		}
	}
}

func setupProwConfig(resourceType string) string {
	// Try to get a Boskos project
	klog.V(4).Infof("Running in PROW")
	klog.V(4).Infof("Fetching a Boskos loaned project")

	p := getBoskosProject(resourceType)
	project := p.Name

	go func(c *boskosclient.Client) {
		for range time.Tick(time.Minute * 5) {
			if err := c.UpdateOne(p.Name, "busy", nil); err != nil {
				klog.Warningf("[Boskos] Update %s failed with %v", p.Name, err)
			}
		}
	}(boskos)

	return project
}

func setEnvProject(project string) error {
	if out, err := exec.Command("gcloud", "config", "set", "project", project).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set gcloud project to %s: %s, err: %w", project, out, err)
	}

	return os.Setenv(ProjectEnvVar, project)
}

func setEnvProjectNumberUsingID(projectID string) (string, error) {
	cmd := exec.Command("gcloud", "projects", "describe", projectID, "--format=value(projectNumber)")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get project number for %s: %s, err: %w", projectID, out, err)
	}
	projectNumber := strings.TrimSpace(string(out))
	return projectNumber, os.Setenv(ProjectNumberEnvVar, projectNumber)
}
