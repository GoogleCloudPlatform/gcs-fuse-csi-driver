/*
Copyright 2026 Google LLC

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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// capacityAdvisorResponse is a sample API response.
const capacityAdvisorResponse = `{
  "recommendations": [
    {
      "scores": {
        "obtainability": 0.92
      },
      "shards": [
        {
          "zone": "https://www.googleapis.com/compute/beta/projects/prow-gob-internal-boskos-01/zones/us-central1-a",
          "machineType": "n2-standard-8",
          "instanceCount": 9,
          "provisioningModel": "STANDARD"
        }
      ]
    }
  ]
}`

// wantCapacityAdvisorRequest is the expected request for 9 n2-standard-8 nodes in the fake gcloud's zones.
const wantCapacityAdvisorRequest = `{
  "instanceProperties": {"scheduling": {"provisioningModel": "STANDARD"}},
  "instanceFlexibilityPolicy": {"instanceSelections": {"instance-selection-1": {"machineTypes": ["n2-standard-8"]}}},
  "distributionPolicy": {
    "targetShape": "ANY_SINGLE_ZONE",
    "zones": [{"zone": "zones/us-central1-a"}, {"zone": "zones/us-central1-b"}]
  },
  "size": 9
}`

// fakeGcloudScript prints a token and two zones.
const fakeGcloudScript = `case "$*" in
"auth print-access-token") echo test-token ;;
"compute regions describe "*) echo "us-central1-a;us-central1-b" ;;
*) exit 1 ;;
esac
`

// newFakeGcloud writes a fake gcloud that runs the given shell script, and returns its path.
func newFakeGcloud(t *testing.T, script string) string {
	t.Helper()

	gcloudPath := filepath.Join(t.TempDir(), "gcloud")
	//nolint:gosec // The fake gcloud must be executable.
	if err := os.WriteFile(gcloudPath, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("failed to write fake gcloud: %v", err)
	}

	return gcloudPath
}

//nolint:paralleltest // Overrides capacityAdvisorEndpoint.
func TestQueryCapacityAdvice(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		respBody   string
		wantZone   string
		wantScore  float64
		wantErr    string
	}{
		{
			name:       "should return the zone name and score of the first recommendation",
			statusCode: http.StatusOK,
			respBody:   capacityAdvisorResponse,
			wantZone:   "us-central1-a",
			wantScore:  0.92,
		},
		{
			name:       "should return the API error message when the request fails",
			statusCode: http.StatusBadRequest,
			respBody:   `{"error": {"code": 400, "message": "The service is not available for this project."}}`,
			wantErr:    "HTTP 400: The service is not available for this project.",
		},
		{
			name:       "should return a non-JSON error body on one line",
			statusCode: http.StatusBadGateway,
			respBody:   "<html>\n<body>Bad Gateway</body>\n</html>\n",
			wantErr:    "HTTP 502: <html> <body>Bad Gateway</body> </html>",
		},
		{
			name:       "should return an error when no recommendation is returned",
			statusCode: http.StatusOK,
			respBody:   `{}`,
			wantErr:    "no zone or score returned",
		},
		{
			name:       "should return an error when no zone has capacity",
			statusCode: http.StatusOK,
			respBody:   `{"recommendations": [{"scores": {"obtainability": 0.1}, "shards": [{"zone": "zones/us-central1-f"}]}]}`,
			wantErr:    "no zone has capacity (obtainability 0.10)",
		},
		{
			name:       "should return an error for a malformed response",
			statusCode: http.StatusOK,
			respBody:   `not json`,
			wantErr:    "failed to parse",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Must query the allowlisted project, not testParams.ProjectID.
				wantPath := "/projects/" + prowCapacityAdvisorProject + "/regions/us-central1/advice/capacity"
				if r.Method != http.MethodPost || r.URL.Path != wantPath {
					t.Errorf("got request %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
				}
				if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
					t.Errorf("got Authorization header %q, want %q", got, want)
				}

				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("failed to read request body: %v", err)
				}
				var got, want any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Errorf("failed to parse request body %q: %v", body, err)
				}
				if err := json.Unmarshal([]byte(wantCapacityAdvisorRequest), &want); err != nil {
					t.Errorf("failed to parse wantCapacityAdvisorRequest: %v", err)
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("unexpected request body (-want +got):\n%s", diff)
				}

				w.WriteHeader(tc.statusCode)
				fmt.Fprint(w, tc.respBody)
			}))
			defer server.Close()

			origEndpoint := capacityAdvisorEndpoint
			capacityAdvisorEndpoint = server.URL
			defer func() { capacityAdvisorEndpoint = origEndpoint }()

			testParams := &TestParameters{
				ProjectID:        "boskos-project",
				GkeGcloudCommand: newFakeGcloud(t, fakeGcloudScript),
				NodeMachineType:  "n2-standard-8",
				NumNodes:         9,
			}
			zone, score, err := queryCapacityAdvice(testParams, "us-central1", "test-token")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("queryCapacityAdvice() error = %v, want error containing %q", err, tc.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatalf("queryCapacityAdvice() returned unexpected error: %v", err)
			}
			if zone != tc.wantZone || score != tc.wantScore {
				t.Errorf("queryCapacityAdvice() = (%q, %v), want (%q, %v)", zone, score, tc.wantZone, tc.wantScore)
			}
		})
	}
}

//nolint:paralleltest // Parallel subtests exec freshly written scripts and hit ETXTBSY (golang/go#22315).
func TestGcloudAccessToken(t *testing.T) {
	testCases := []struct {
		name      string
		script    string
		wantToken string
		wantErr   string
	}{
		{
			name:      "should return the token without the trailing newline",
			script:    fakeGcloudScript,
			wantToken: "test-token",
		},
		{
			name:    "should include gcloud's stderr when gcloud fails",
			script:  "echo 'ERROR: (gcloud.auth.print-access-token) no credentialed accounts' >&2\nexit 1\n",
			wantErr: "failed to get access token: exit status 1, stderr: ERROR: (gcloud.auth.print-access-token) no credentialed accounts",
		},
		{
			name:    "should return an error when gcloud prints no token",
			script:  "echo\n",
			wantErr: "failed to get access token: gcloud returned an empty token",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			token, err := gcloudAccessToken(&TestParameters{GkeGcloudCommand: newFakeGcloud(t, tc.script)})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("gcloudAccessToken() error = %v, want error containing %q", err, tc.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatalf("gcloudAccessToken() returned unexpected error: %v", err)
			}
			if token != tc.wantToken {
				t.Errorf("gcloudAccessToken() = %q, want %q", token, tc.wantToken)
			}
		})
	}
}
