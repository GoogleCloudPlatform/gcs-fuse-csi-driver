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
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/oauth2/google"
	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
	"k8s.io/klog/v2"
	boskosclient "sigs.k8s.io/boskos/client"
	"sigs.k8s.io/boskos/common"
)

func ensureFlag(v *bool, setTo bool, msgOnError string) {
	if *v != setTo {
		klog.Fatal(msgOnError)
	}
}

func ensureVariable(v *string, set bool, msgOnError string) {
	if set && len(*v) == 0 {
		klog.Fatal(msgOnError)
	} else if !set && len(*v) != 0 {
		klog.Fatal(msgOnError)
	}
}

func ensureExactlyOneVariableSet(vars []*string, msgOnError string) {
	var count int
	for _, v := range vars {
		if len(*v) != 0 {
			count++
		}
	}

	if count != 1 {
		klog.Fatal(msgOnError)
	}
}

func isVariableSet(v *string) bool {
	return len(*v) != 0
}

func runCommand(action string, cmd *exec.Cmd) error {
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	klog.Infof("%s", action)
	klog.Infof("cmd env=%v", cmd.Env)
	klog.Infof("cmd args=%s", cmd.Args)

	err := cmd.Start()
	if err != nil {
		return err
	}

	err = cmd.Wait()
	if err != nil {
		return err
	}

	return nil
}

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
			if err != nil {
				klog.Warningf("boskos failed to acquire project: %w", err)
			} else if p == nil {
				klog.Warningf("boskos does not have a free %s at the moment", resourceType)
			} else {
				return p
			}
		}
	}
}

func SetupProwConfig(resourceType string) (project, serviceAccount string) {
	// Try to get a Boskos project
	klog.V(4).Infof("Running in PROW")
	klog.V(4).Infof("Fetching a Boskos loaned project")

	p := getBoskosProject(resourceType)
	project = p.Name

	go func(c *boskosclient.Client, proj string) {
		for range time.Tick(time.Minute * 5) {
			if err := c.UpdateOne(p.Name, "busy", nil); err != nil {
				klog.Warningf("[Boskos] Update %s failed with %v", p.Name, err)
			}
		}
	}(boskos, p.Name)

	// If we're on CI overwrite the service account
	klog.V(4).Infof("Fetching the default compute service account")

	c, err := google.DefaultClient(context.Background(), cloudresourcemanager.CloudPlatformScope)
	if err != nil {
		klog.Fatalf("Failed to get Google Default Client: %w", err)
	}

	cloudresourcemanagerService, err := cloudresourcemanager.New(c)
	if err != nil {
		klog.Fatalf("Failed to create new cloudresourcemanager: %w", err)
	}

	resp, err := cloudresourcemanagerService.Projects.Get(project).Do()
	if err != nil {
		klog.Fatalf("Failed to get project %v from Cloud Resource Manager: %w", project, err)
	}

	// Default Compute Engine service account
	// [PROJECT_NUMBER]-compute@developer.gserviceaccount.com
	serviceAccount = fmt.Sprintf("%v-compute@developer.gserviceaccount.com", resp.ProjectNumber)
	klog.Infof("Using project %v and service account %v", project, serviceAccount)

	return project, serviceAccount
}
