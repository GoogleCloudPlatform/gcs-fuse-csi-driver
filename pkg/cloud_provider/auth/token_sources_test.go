package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchTokenViaCertificate(t *testing.T) {
	// 1. Generate a self-signed certificate for testing
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test Org"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:         []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}

	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	// 2. Setup mock STS server
	expectedAccessToken := "mock-access-token"
	expectedExpiresIn := 3600

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/v1/token" {
			t.Errorf("Expected path /v1/token, got %s", r.URL.Path)
		}

		var reqBody struct {
			Audience           string `json:"audience"`
			GrantType          string `json:"grantType"`
			Scope              string `json:"scope"`
			RequestedTokenType string `json:"requestedTokenType"`
			SubjectTokenType   string `json:"subjectTokenType"`
			SubjectToken       string `json:"subjectToken"`
		}

		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatal(err)
		}

		// Verify SubjectToken contains the base64 encoded DER
		var subjectTokenList []string
		if err := json.Unmarshal([]byte(reqBody.SubjectToken), &subjectTokenList); err != nil {
			t.Fatal(err)
		}

		if len(subjectTokenList) != 1 {
			t.Errorf("Expected 1 cert in SubjectToken, got %d", len(subjectTokenList))
		}

		expectedDerBase64 := base64.StdEncoding.EncodeToString(derBytes)
		if subjectTokenList[0] != expectedDerBase64 {
			t.Errorf("Expected SubjectToken to contain %s, got %s", expectedDerBase64, subjectTokenList[0])
		}

		// Return mock response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": expectedAccessToken,
			"token_type":   "Bearer",
			"expires_in":   expectedExpiresIn,
		})
	}))
	defer server.Close()

	// 3. Run the test
	ts := &GCPTokenSource{
		certData: certPem,
		keyData:  keyPem,
		audience: "fake-audience",
		tokenURL: server.URL + "/v1/token", // Pass full URL, code should extract base URL
	}

	token, err := ts.fetchTokenViaCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if token.AccessToken != expectedAccessToken {
		t.Errorf("Expected access token %s, got %s", expectedAccessToken, token.AccessToken)
	}
}
