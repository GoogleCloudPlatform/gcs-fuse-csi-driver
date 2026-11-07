/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	protoAccept      = "application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited"
	protoContentType = "application/vnd.google.protobuf; proto=io.prometheus.client.MetricFamily; encoding=delimited"
	textContentType  = "text/plain; version=0.0.4; charset=utf-8"
)

// startFakeGcsfuse serves handler as the gcsfuse metrics endpoint and returns its URL.
func startFakeGcsfuse(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	gcsfuse := httptest.NewServer(handler)
	t.Cleanup(gcsfuse.Close)

	return gcsfuse.URL + "/metrics"
}

// startMetricsProxy serves newMetricsProxyHandler for metricEndpoint, as the sidecar does.
func startMetricsProxy(t *testing.T, metricEndpoint string) *httptest.Server {
	t.Helper()
	transport := metricsHTTPClient.Transport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	proxy := httptest.NewServer(newMetricsProxyHandler(metricEndpoint, client))
	t.Cleanup(proxy.Close)

	return proxy
}

func scrapeProxy(t *testing.T, proxy *httptest.Server, accept string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, proxy.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	if accept != "" {
		req.Header.Set(acceptHeader, accept)
	}

	return proxy.Client().Do(req)
}

func TestMetricsProxyHandlerNegotiation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		accept      string
		contentType string
	}{
		{name: "driver asks for protobuf", accept: protoAccept, contentType: protoContentType},
		{name: "gcsfuse falls back to text", accept: protoAccept, contentType: textContentType},
		{name: "older driver sends no Accept header", accept: "", contentType: textContentType},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			upstreamHeaders := make(chan http.Header, 1)
			proxy := startMetricsProxy(t, startFakeGcsfuse(t, func(w http.ResponseWriter, r *http.Request) {
				upstreamHeaders <- r.Header.Clone()
				w.Header().Set(contentTypeHeader, tc.contentType)
				_, _ = io.WriteString(w, "payload")
			}))

			resp, err := scrapeProxy(t, proxy, tc.accept)
			if err != nil {
				t.Fatalf("scrape failed: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read the response body: %v", err)
			}

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("got status %d (body %q), want %d", resp.StatusCode, body, http.StatusOK)
			}

			got := <-upstreamHeaders
			if accept := got.Get(acceptHeader); accept != tc.accept {
				t.Errorf("gcsfuse received Accept %q, want %q", accept, tc.accept)
			}
			// Guards DisableCompression: the loopback scrape must not be gzip-encoded.
			if encoding := got.Get("Accept-Encoding"); encoding != "" {
				t.Errorf("gcsfuse received Accept-Encoding %q, want none", encoding)
			}
			if contentType := resp.Header.Get(contentTypeHeader); contentType != tc.contentType {
				t.Errorf("got Content-Type %q, want %q", contentType, tc.contentType)
			}
			if string(body) != "payload" {
				t.Errorf("got body %q, want %q", body, "payload")
			}
		})
	}
}

func TestMetricsProxyHandlerUpstreamErrorsReturn500(t *testing.T) {
	t.Parallel()

	// Bind 127.0.0.2 so parallel httptest.NewServer instances (which bind 127.0.0.1:0)
	// cannot reuse the ephemeral port after closedListener is closed.
	closedListener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	unreachableEndpoint := "http://" + closedListener.Addr().String() + "/metrics"
	closedListener.Close()

	testCases := []struct {
		name     string
		endpoint func(t *testing.T) string
	}{
		{
			name:     "gcsfuse is unreachable",
			endpoint: func(*testing.T) string { return unreachableEndpoint },
		},
		{
			name: "gcsfuse returns a non-200 status",
			endpoint: func(t *testing.T) string {
				t.Helper()
				return startFakeGcsfuse(t, func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				})
			},
		},
		{
			name: "gcsfuse aborts before the headers",
			endpoint: func(t *testing.T) string {
				t.Helper()
				return startFakeGcsfuse(t, func(http.ResponseWriter, *http.Request) {
					panic(http.ErrAbortHandler)
				})
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp, err := scrapeProxy(t, startMetricsProxy(t, tc.endpoint(t)), protoAccept)
			if err != nil {
				t.Fatalf("scrape failed: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read the response body: %v", err)
			}

			if resp.StatusCode != http.StatusInternalServerError {
				t.Errorf("got status %d, want %d", resp.StatusCode, http.StatusInternalServerError)
			}
			if contentType := resp.Header.Get(contentTypeHeader); !strings.HasPrefix(contentType, "text/plain") {
				t.Errorf("got Content-Type %q, want a text/plain error", contentType)
			}
			if !strings.Contains(string(body), "failed to scrape metrics") {
				t.Errorf("got body %q, want the scrape error", body)
			}
		})
	}
}

func TestMetricsProxyHandlerMidStreamAbort(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		prefix []byte
	}{
		{
			// The proxy has only buffered its own 200 headers when the upstream body fails, so
			// the driver sees the connection close without any response.
			name:   "gcsfuse aborts after flushing the 200 headers",
			prefix: nil,
		},
		{
			// 16 KiB exceeds net/http's response buffers, so the proxy has sent its 200 headers
			// and part of the body when the upstream body fails.
			name:   "gcsfuse aborts after flushing part of the body",
			prefix: bytes.Repeat([]byte{0x0a}, 16<<10),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			abort := make(chan struct{})
			if len(tc.prefix) == 0 {
				close(abort)
			}
			var healthy atomic.Bool
			proxy := startMetricsProxy(t, startFakeGcsfuse(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(contentTypeHeader, protoContentType)
				if healthy.Load() {
					_, _ = io.WriteString(w, "recovered-payload")
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(tc.prefix)
				// Flush, so that the proxy has received the 200 response before the body fails.
				_ = http.NewResponseController(w).Flush()
				select {
				case <-abort:
				case <-r.Context().Done():
				}
				panic(http.ErrAbortHandler)
			}))

			resp, err := scrapeProxy(t, proxy, protoAccept)
			if len(tc.prefix) == 0 {
				if err == nil {
					resp.Body.Close()
					t.Fatalf("got status %d, want the connection to be closed without a response", resp.StatusCode)
				}
				if !errors.Is(err, io.EOF) {
					t.Fatalf("got scrape error %v, want %v", err, io.EOF)
				}
			} else {
				if err != nil {
					close(abort)
					t.Fatalf("got scrape error %v, want the 200 headers before the abort", err)
				}
				close(abort)
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("got status %d, want %d", resp.StatusCode, http.StatusOK)
				}
				if !errors.Is(readErr, io.ErrUnexpectedEOF) {
					t.Fatalf("got body read error %v, want %v", readErr, io.ErrUnexpectedEOF)
				}
				if bytes.Contains(body, []byte("failed to scrape")) || !bytes.HasPrefix(tc.prefix, body) {
					t.Fatalf("got a corrupted body of %d bytes, want a prefix of the gcsfuse body", len(body))
				}
			}

			// The next scrape is served normally.
			healthy.Store(true)
			resp, err = scrapeProxy(t, proxy, protoAccept)
			if err != nil {
				t.Fatalf("scrape after the abort failed: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read the response body after the abort: %v", err)
			}
			if resp.StatusCode != http.StatusOK || string(body) != "recovered-payload" {
				t.Errorf("got status %d and body %q after the abort, want %d and %q", resp.StatusCode, body, http.StatusOK, "recovered-payload")
			}
		})
	}
}

func TestCollectMetricsUnixSocketLifecycle(t *testing.T) {
	t.Parallel()

	var blockScrape atomic.Bool
	inFlight := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	gcsfuseURL, err := url.Parse(startFakeGcsfuse(t, func(w http.ResponseWriter, r *http.Request) {
		if blockScrape.CompareAndSwap(true, false) {
			close(inFlight)
			<-r.Context().Done()
			close(upstreamCanceled)
			return
		}
		w.Header().Set(contentTypeHeader, textContentType)
		_, _ = io.WriteString(w, "payload")
	}))
	if err != nil {
		t.Fatalf("failed to parse the gcsfuse URL: %v", err)
	}

	// Keep the directory path short so the Unix domain socket path stays well below sun_path limits.
	tempDir, err := os.MkdirTemp("/tmp", "cm-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	socketPath := filepath.Join(tempDir, metrics.SocketName)
	// A stale socket file from a previous container must be replaced.
	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("failed to write the stale socket file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		collectMetrics(ctx, gcsfuseURL.Port(), tempDir)
	}()

	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	// Retry until collectMetrics has replaced the stale file and is listening.
	var resp *http.Response
	if pollErr := wait.PollUntilContextTimeout(context.Background(), 10*time.Millisecond, 5*time.Second, true, func(context.Context) (bool, error) {
		resp, err = client.Get("http://unix/metrics")
		return err == nil, nil
	}); pollErr != nil {
		t.Fatalf("scrape over %q failed: %v (last error: %v)", socketPath, pollErr, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("failed to read the response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get(contentTypeHeader) != textContentType || string(body) != "payload" {
		t.Errorf("got status %d, Content-Type %q and body %q, want %d, %q and %q",
			resp.StatusCode, resp.Header.Get(contentTypeHeader), body, http.StatusOK, textContentType, "payload")
	}

	// Block the next scrape at gcsfuse and cancel ctx while it is in flight.
	blockScrape.Store(true)
	scrapeErrCh := make(chan error, 1)
	go func() {
		r, getErr := client.Get("http://unix/metrics")
		if getErr != nil {
			scrapeErrCh <- getErr
			return
		}
		_, readErr := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			scrapeErrCh <- errors.New(r.Status)
			return
		}
		scrapeErrCh <- readErr
	}()

	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the scrape to reach gcsfuse")
	}

	cancel()

	select {
	case <-upstreamCanceled:
	case <-time.After(metricsScrapeTimeout / 2):
		t.Fatal("in-flight gcsfuse request context was not canceled on shutdown")
	}
	select {
	case scrapeErr := <-scrapeErrCh:
		if scrapeErr == nil {
			t.Error("expected in-flight scrape to fail when ctx was canceled, got nil")
		}
	case <-time.After(metricsScrapeTimeout / 2):
		t.Fatal("in-flight client scrape did not return after the context was canceled")
	}
	select {
	case <-done:
	case <-time.After(metricsScrapeTimeout / 2):
		t.Fatal("collectMetrics did not return after the context was canceled")
	}
	// Closing the listener removes the socket file.
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Errorf("expected %q to be removed on shutdown, got %v", socketPath, err)
	}
}
