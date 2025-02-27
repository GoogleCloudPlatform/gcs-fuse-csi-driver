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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"cloud.google.com/go/compute/metadata"
	credentials "cloud.google.com/go/iam/credentials/apiv1"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/api/sts/v1"
	"k8s.io/klog/v2"
)

const metricEndpointFmt = "http://localhost:%v/metrics"

// Mounter will be used in the sidecar container to invoke gcsfuse.
type Mounter struct {
	mounterPath string
	WaitGroup   sync.WaitGroup
}

// New returns a Mounter for the current system.
// It provides an option to specify the path to gcsfuse binary.
func New(mounterPath string) *Mounter {
	return &Mounter{
		mounterPath: mounterPath,
	}
}

func (m *Mounter) Mount(ctx context.Context, mc *MountConfig) error {
	// Start the token server for HostNetwork enabled pods.
	if mc.TokenServerIdentityProvider != "" {
		tp := filepath.Join(mc.TempDir, TokenFileName)
		klog.Infof("Pod has hostNetwork enabled and token server feature is turned on. Starting Token Server on %s.", tp)
		go StartTokenServer(ctx, tp, mc.TokenServerIdentityProvider)
	}

	klog.Infof("start to mount bucket %q for volume %q", mc.BucketName, mc.VolumeName)

	if err := os.MkdirAll(mc.BufferDir+TempDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create temp dir %q: %w", mc.BufferDir+TempDir, err)
	}

	args := []string{}
	for k, v := range mc.FlagMap {
		args = append(args, "--"+k)
		if v != "" {
			args = append(args, v)
		}
	}

	args = append(args, mc.BucketName)
	// gcsfuse supports the `/dev/fd/N` syntax
	// the /dev/fuse is passed as ExtraFiles below, and will always be FD 3
	args = append(args, "/dev/fd/3")

	klog.Infof("gcsfuse mounting with args %v...", args)
	//nolint: gosec
	cmd := exec.CommandContext(ctx, m.mounterPath, args...)
	cmd.ExtraFiles = []*os.File{os.NewFile(uintptr(mc.FileDescriptor), "/dev/fuse")}
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, mc.ErrWriter)
	cmd.Cancel = func() error {
		klog.V(4).Infof("sending SIGTERM to gcsfuse process: %v", cmd)

		return cmd.Process.Signal(syscall.SIGTERM)
	}

	// when the ctx.Done() is closed,
	// the main workload containers have exited,
	// so it is safe to force kill the gcsfuse process.
	go func(cmd *exec.Cmd) {
		<-ctx.Done()
		time.Sleep(time.Second * 5)
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			klog.Warningf("after 5 seconds, process with id %v has not exited, force kill the process", cmd.Process.Pid)
			if err := cmd.Process.Kill(); err != nil {
				klog.Warningf("failed to force kill process with id %v", cmd.Process.Pid)
			}
		}
	}(cmd)

	m.WaitGroup.Add(1)
	go func() {
		defer m.WaitGroup.Done()
		if err := cmd.Start(); err != nil {
			mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to start gcsfuse with error: %v\n", err))

			return
		}

		klog.Infof("gcsfuse for bucket %q, volume %q started with process id %v", mc.BucketName, mc.VolumeName, cmd.Process.Pid)

		loggingSeverity := mc.ConfigFileFlagMap["logging:severity"]
		if loggingSeverity == "debug" || loggingSeverity == "trace" {
			go logMemoryUsage(ctx, cmd.Process.Pid)
			go logVolumeUsage(ctx, mc.BufferDir, mc.CacheDir)
		}

		promPort, ok := mc.FlagMap["prometheus-port"]
		if ok && promPort != "0" {
			klog.Infof("start to collect metrics from port %v for volume %q", promPort, mc.VolumeName)
			go collectMetrics(ctx, promPort, mc.TempDir)
		}

		// Since the gcsfuse has taken over the file descriptor,
		// closing the file descriptor to avoid other process forking it.
		syscall.Close(mc.FileDescriptor)
		if err := cmd.Wait(); err != nil {
			errMsg := fmt.Sprintf("gcsfuse exited with error: %v\n", err)
			if strings.Contains(errMsg, "signal: terminated") {
				klog.Infof("[%v] gcsfuse was terminated.", mc.VolumeName)
			} else {
				mc.ErrWriter.WriteMsg(errMsg)
			}
		} else {
			klog.Infof("[%v] gcsfuse exited normally.", mc.VolumeName)
		}
	}()

	return nil
}

// logMemoryUsage logs gcsfuse process VmRSS (Resident Set Size) usage every 30 seconds.
func logMemoryUsage(ctx context.Context, pid int) {
	ticker := time.NewTicker(30 * time.Second)
	filepath := fmt.Sprintf("/proc/%d/status", pid)
	file, err := os.Open(filepath)
	if err != nil {
		klog.Errorf("failed to open %v: %v", filepath, err)

		return
	}
	defer file.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := file.Seek(0, io.SeekStart)
			if err != nil {
				klog.Errorf("failed to seek to the file beginning: %v", err)

				return
			}

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "VmRSS:") {
					fields := strings.Fields(line)
					klog.Infof("gcsfuse with PID %v uses VmRSS: %v %v", pid, fields[1], fields[2])

					break
				}
			}
		}
	}
}

// logVolumeUsage logs gcsfuse process buffer and cache volume usage every 30 seconds.
func logVolumeUsage(ctx context.Context, bufferDir, cacheDir string) {
	ticker := time.NewTicker(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// TODO: this method does not work for the buffer dir
			logVolumeTotalSize(bufferDir)
			logVolumeTotalSize(cacheDir)
		}
	}
}

// logVolumeTotalSize logs the total volume size of dirPath.
// Warning: this func uses filepath.Walk func that is less efficient when dealing with very large directory trees.
func logVolumeTotalSize(dirPath string) {
	var totalSize int64

	err := filepath.Walk(dirPath, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}

		return nil
	})

	if err != nil {
		klog.Errorf("failed to calculate volume total size for %q: %v", dirPath, err)
	} else {
		klog.Infof("total volume size of %v: %v bytes", dirPath, totalSize)
	}
}

// collectMetrics collects metrics from the gcsfuse instance.
// Meanwhile, a server is created for each gcsfuse instance,
// exposing a unix domain socket for CSI driver to connect.
func collectMetrics(ctx context.Context, port, tempDir string) {
	metricEndpoint := fmt.Sprintf(metricEndpointFmt, port)

	// Create a unix domain socket and listen for incoming connections.
	socketPath := filepath.Join(tempDir, metrics.SocketName)
	socket, err := net.Listen("unix", socketPath)
	if err != nil {
		klog.Errorf("failed to create socket %q: %v", socketPath, err)

		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		if err := scrapeMetrics(timeoutCtx, metricEndpoint, w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	server := http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := server.Serve(socket); err != nil {
		klog.Errorf("failed to start the metrics server for %q: %v", socketPath, err)
	}
}

// scrapeMetrics connects to the metrics endpoint and scrapes latest metrics sample.
// The response is written to a new http.ResponseWriter.
func scrapeMetrics(ctx context.Context, metricEndpoint string, w http.ResponseWriter) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricEndpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request to %q: %w", metricEndpoint, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make HTTP request to %q: %w", metricEndpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status: %v", resp.Status)
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("failed to copy response: %w", err)
	}

	return nil
}

func getK8sTokenFromFile(tokenPath string) (string, error) {
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("error reading token file: %w", err)
	}

	return strings.TrimSpace(string(token)), nil
}

func fetchIdentityBindingToken(ctx context.Context, k8sSAToken string, identityProvider string) (*oauth2.Token, error) {
	stsService, err := sts.NewService(ctx, option.WithHTTPClient(&http.Client{}))
	if err != nil {
		return nil, fmt.Errorf("new STS service error: %w", err)
	}

	audience, err := getAudienceFromContextAndIdentityProvider(ctx, identityProvider)
	if err != nil {
		return nil, fmt.Errorf("failed to get audience from the context: %w", err)
	}

	stsRequest := &sts.GoogleIdentityStsV1ExchangeTokenRequest{
		Audience:           audience,
		GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
		Scope:              credentials.DefaultAuthScopes()[0],
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:jwt",
		SubjectToken:       k8sSAToken,
	}

	stsResponse, err := stsService.V1.Token(stsRequest).Do()
	if err != nil {
		return nil, fmt.Errorf("IdentityBindingToken exchange error with audience %q: %w", audience, err)
	}

	return &oauth2.Token{
		AccessToken: stsResponse.AccessToken,
		TokenType:   stsResponse.TokenType,
		Expiry:      time.Now().Add(time.Second * time.Duration(stsResponse.ExpiresIn)),
	}, nil
}

func getAudienceFromContextAndIdentityProvider(ctx context.Context, identityProvider string) (string, error) {
	projectID, err := metadata.ProjectIDWithContext(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get project ID: %w", err)
	}

	audience := fmt.Sprintf(
		"identitynamespace:%s.svc.id.goog:%s",
		projectID,
		identityProvider,
	)
	klog.Infof("audience: %s", audience)

	return audience, nil
}

func StartTokenServer(ctx context.Context, tokenURLSocketPath string, identityProvider string) {
	// Create a unix domain socket and listen for incoming connections.
	tokenSocketListener, err := net.Listen("unix", tokenURLSocketPath)
	if err != nil {
		klog.Errorf("failed to create socket %q: %v", tokenURLSocketPath, err)

		return
	}
	klog.Infof("created a listener using the socket path %s", tokenURLSocketPath)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		k8stoken, err := getK8sTokenFromFile(webhook.SidecarContainerSATokenVolumeMountPath + "/" + webhook.K8STokenPath)
		var stsToken *oauth2.Token
		if err != nil {
			klog.Errorf("failed to get k8s token from path %v", err)
			w.WriteHeader(http.StatusInternalServerError)

			return
		}
		stsToken, err = fetchIdentityBindingToken(ctx, k8stoken, identityProvider)
		if err != nil {
			klog.Errorf("failed to get sts token from path %v", err)
			w.WriteHeader(http.StatusInternalServerError)

			return
		}
		// Marshal the oauth2.Token object to JSON
		jsonToken, err := json.Marshal(stsToken)
		if err != nil {
			klog.Errorf("failed to marshal token to JSON: %v", err)
			w.WriteHeader(http.StatusInternalServerError)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, string(jsonToken))
	})

	server := http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.Serve(tokenSocketListener); !errors.Is(err, http.ErrServerClosed) {
		klog.Errorf("Server for %q returns unexpected error: %v", tokenURLSocketPath, err)
	}
}
