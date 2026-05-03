package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

var url = "http://mock-token-service:5000/mint-cat"
const audience = "//iam.googleapis.com/projects/332244367552/locations/global/workloadIdentityPools/custom-simulation-pool/providers/custom-saml-provider"

type Payload struct {
	Audience string `json:"audience"`
}

type Output struct {
	Version        int    `json:"version"`
	Success        bool   `json:"success"`
	TokenType      string `json:"token_type"`
	SAMLResponse   string `json:"saml_response"`
	ExpirationTime int64  `json:"expiration_time"`
}

type ErrorOutput struct {
	Version int    `json:"version"`
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {
	client := &http.Client{}

	payload := Payload{Audience: audience}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		exitWithError(err.Error())
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		exitWithError(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		exitWithError(err.Error())
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		exitWithError(err.Error())
	}

	if resp.StatusCode != http.StatusOK {
		exitWithError(fmt.Sprintf("HTTP error: %d, body: %s", resp.StatusCode, string(body)))
	}

	samlB64 := base64.StdEncoding.EncodeToString(body)
	exp := time.Now().Unix() + 3600

	out := Output{
		Version:        1,
		Success:        true,
		TokenType:      "urn:ietf:params:oauth:token-type:saml2",
		SAMLResponse:   samlB64,
		ExpirationTime: exp,
	}

	jsonOut, err := json.Marshal(out)
	if err != nil {
		exitWithError(err.Error())
	}

	fmt.Println(string(jsonOut))
	os.Exit(0)
}

func exitWithError(msg string) {
	errOut := ErrorOutput{
		Version: 1,
		Success: false,
		Code:    "500",
		Message: msg,
	}
	jsonErr, _ := json.Marshal(errOut)
	fmt.Println(string(jsonErr))
	os.Exit(1)
}
