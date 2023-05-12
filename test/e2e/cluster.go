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

package e2etest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	apimachineryversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func gkeLocationArgs(gceZone, gceRegion string) (locationArg, locationVal string, err error) {
	switch {
	case len(gceZone) > 0:
		locationArg = "--zone"
		locationVal = gceZone
	case len(gceRegion) > 0:
		locationArg = "--region"
		locationVal = gceRegion
	default:
		return "", "", fmt.Errorf("zone and region unspecified")
	}
	return
}

func isRegionalGKECluster(gceZone, gceRegion string) bool {
	return len(gceRegion) > 0
}

func clusterDownGKE(gceZone, gceRegion string) error {
	locationArg, locationVal, err := gkeLocationArgs(gceZone, gceRegion)
	if err != nil {
		return err
	}

	cmd := exec.Command("gcloud", "container", "clusters", "delete", *gkeTestClusterName,
		locationArg, locationVal, "--quiet")
	err = runCommand("Bringing Down E2E Cluster on GKE", cmd)
	if err != nil {
		return fmt.Errorf("failed to bring down kubernetes e2e cluster on gke: %v", err.Error())
	}
	return nil
}

func buildKubernetes(k8sDir, command string) error {
	cmd := exec.Command("make", "-C", k8sDir, command)
	cmd.Env = os.Environ()
	err := runCommand(fmt.Sprintf("Running command in kubernetes/kubernetes path=%s", k8sDir), cmd)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes: %w", err)
	}
	return nil
}

func clusterUpGKE(gceZone, gceRegion string, numNodes int, numWindowsNodes int, imageType string, useManagedDriver bool, projectID string) error {
	locationArg, locationVal, err := gkeLocationArgs(gceZone, gceRegion)
	if err != nil {
		return err
	}

	out, err := exec.Command("gcloud", "container", "clusters", "list",
		locationArg, locationVal, "--verbosity", "none", "--filter",
		fmt.Sprintf("name=%s", *gkeTestClusterName)).CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to check for previous test cluster: %v %s", err.Error(), out)
	}
	if len(out) > 0 {
		klog.Infof("Detected previous cluster %s. Deleting so a new one can be created...", *gkeTestClusterName)
		err = clusterDownGKE(gceZone, gceRegion)
		if err != nil {
			return err
		}
	}

	var cmd *exec.Cmd
	cmdParams := []string{"container", "clusters", "create", *gkeTestClusterName,
		locationArg, locationVal, "--num-nodes", strconv.Itoa(numNodes),
		"--quiet", "--machine-type", "n1-standard-2", "--image-type", imageType,
		"--workload-pool", fmt.Sprintf("%s.svc.id.goog", projectID),
	}

	if isVariableSet(gkeClusterVer) {
		cmdParams = append(cmdParams, "--cluster-version", *gkeClusterVer)
	} else {
		cmdParams = append(cmdParams, "--release-channel", *gkeReleaseChannel)
		// release channel based GKE clusters require autorepair to be enabled.
		cmdParams = append(cmdParams, "--enable-autorepair")
	}

	if isVariableSet(gkeNodeVersion) {
		cmdParams = append(cmdParams, "--node-version", *gkeNodeVersion)
	}

	if useManagedDriver {
		cmdParams = append(cmdParams, "--addons", "GcsFuseCsiDriver")
	}

	cmd = exec.Command("gcloud", cmdParams...)
	err = runCommand("Starting E2E Cluster on GKE", cmd)
	if err != nil {
		return fmt.Errorf("failed to bring up kubernetes e2e cluster on gke: %v", err.Error())
	}

	return nil
}

func downloadKubernetesSource(pkgDir, k8sIoDir, kubeVersion string) error {
	k8sDir := filepath.Join(k8sIoDir, "kubernetes")
	klog.Infof("Downloading Kubernetes source v=%s to path=%s", kubeVersion, k8sIoDir)

	if err := os.MkdirAll(k8sIoDir, 0777); err != nil {
		return err
	}
	if err := os.RemoveAll(k8sDir); err != nil {
		return err
	}

	// We clone rather than download from release archives, because the file naming has not been
	// stable.  For example, in late 2021 it appears that archives of minor versions (eg v1.21.tgz)
	// stopped and was replaced with just patch version.
	if kubeVersion == "master" {
		// Clone of master. We cannot use a shallow clone, because the k8s version is not set, and
		// in order to find the revision git searches through the tags, and tags are not fetched in
		// a shallow clone. Not using a shallow clone adds about 700M to the ~5G archive directory,
		// after make quick-release, so this is not disastrous.
		klog.Info("cloning k8s master")
		out, err := exec.Command("git", "clone", "https://github.com/kubernetes/kubernetes", k8sDir).CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to clone kubernetes master: %s, err: %v", out, err.Error())
		}
	} else {
		// Shallow clone of a release branch.
		vKubeVersion := "v" + kubeVersion
		klog.Infof("shallow clone of k8s %s", vKubeVersion)
		out, err := exec.Command("git", "clone", "--depth", "1", "https://github.com/kubernetes/kubernetes", k8sDir).CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to clone kubernetes %s: %s, err: %v", vKubeVersion, out, err.Error())
		}
	}
	return nil
}

func getNormalizedVersion(kubeVersion, gkeVersion string) (string, error) {
	if kubeVersion != "" && gkeVersion != "" {
		return "", fmt.Errorf("both kube version (%s) and gke version (%s) specified", kubeVersion, gkeVersion)
	}
	if kubeVersion == "" && gkeVersion == "" {
		return "", errors.New("neither kube version nor gke version specified")
	}
	var v string
	if kubeVersion != "" {
		v = kubeVersion
	} else if gkeVersion != "" {
		v = gkeVersion
	}
	if v == "master" || v == "latest" {
		// Ugh
		return v, nil
	}
	toks := strings.Split(v, ".")
	if len(toks) < 2 || len(toks) > 3 {
		return "", fmt.Errorf("got unexpected number of tokens in version string %s - wanted 2 or 3", v)
	}
	return strings.Join(toks[:2], "."), nil

}

func getKubeClusterVersion() (string, error) {
	out, err := exec.Command("kubectl", "version", "-o=json").Output()
	if err != nil {
		return "", fmt.Errorf("failed to obtain cluster version, error: %v; output was %s", err.Error(), out)
	}
	type version struct {
		ClientVersion *apimachineryversion.Info `json:"clientVersion,omitempty" yaml:"clientVersion,omitempty"`
		ServerVersion *apimachineryversion.Info `json:"serverVersion,omitempty" yaml:"serverVersion,omitempty"`
	}

	var v version
	err = json.Unmarshal(out, &v)
	if err != nil {
		return "", fmt.Errorf("Failed to parse kubectl version output, error: %v", err.Error())
	}

	return v.ServerVersion.GitVersion, nil
}

func mustGetKubeClusterVersion() string {
	ver, err := getKubeClusterVersion()
	if err != nil {
		klog.Fatalf("Error: %v", err.Error())
	}
	return ver
}

// getKubeConfig returns the full path to the
// kubeconfig file set in $KUBECONFIG env.
// If unset, then it defaults to $HOME/.kube/config
func getKubeConfig() (string, error) {
	config, ok := os.LookupEnv("KUBECONFIG")
	if ok {
		return config, nil
	}
	homeDir, ok := os.LookupEnv("HOME")
	if !ok {
		return "", fmt.Errorf("HOME env not set")
	}
	return filepath.Join(homeDir, ".kube/config"), nil
}

// getKubeClient returns a Kubernetes client interface
// for the test cluster
func getKubeClient() (kubernetes.Interface, error) {
	kubeConfig, err := getKubeConfig()
	if err != nil {
		return nil, err
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create config: %v", err.Error())
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %v", err.Error())
	}
	return kubeClient, nil
}
