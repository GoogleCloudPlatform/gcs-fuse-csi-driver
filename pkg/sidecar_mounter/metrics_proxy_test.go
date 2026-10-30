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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScrapeMetricsForwardsAcceptAndContentType(t *testing.T) {
	t.Parallel()

	const (
		protoAccept      = "application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited"
		protoContentType = "application/vnd.google.protobuf; proto=io.prometheus.client.MetricFamily; encoding=delimited"
		textContentType  = "text/plain; version=0.0.4; charset=utf-8"
	)

	testCases := []struct {
		name            string
		accept          string
		wantAccept      string
		contentType     string
		wantContentType string
	}{
		{
			name:            "driver asks for protobuf",
			accept:          protoAccept,
			wantAccept:      protoAccept,
			contentType:     protoContentType,
			wantContentType: protoContentType,
		},
		{
			name:            "older driver sends no Accept header",
			accept:          "",
			wantAccept:      "",
			contentType:     textContentType,
			wantContentType: textContentType,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotAccept string
			gcsfuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAccept = r.Header.Get(acceptHeader)
				w.Header().Set(contentTypeHeader, tc.contentType)
				_, _ = w.Write([]byte("payload"))
			}))
			defer gcsfuse.Close()

			rec := httptest.NewRecorder()
			if err := scrapeMetrics(context.Background(), gcsfuse.URL+"/metrics", tc.accept, rec); err != nil {
				t.Fatalf("scrapeMetrics failed: %v", err)
			}
			if gotAccept != tc.wantAccept {
				t.Errorf("gcsfuse received Accept %q, want %q", gotAccept, tc.wantAccept)
			}
			if got := rec.Header().Get(contentTypeHeader); got != tc.wantContentType {
				t.Errorf("response Content-Type %q, want %q", got, tc.wantContentType)
			}
			if rec.Body.String() != "payload" {
				t.Errorf("response body %q, want %q", rec.Body.String(), "payload")
			}
		})
	}
}

func TestScrapeMetricsErrorStatus(t *testing.T) {
	t.Parallel()

	gcsfuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer gcsfuse.Close()

	rec := httptest.NewRecorder()
	if err := scrapeMetrics(context.Background(), gcsfuse.URL+"/metrics", "", rec); err == nil {
		t.Fatalf("expected an error for a non-200 gcsfuse response")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected no body to be written on error, got %q", rec.Body.String())
	}
}
