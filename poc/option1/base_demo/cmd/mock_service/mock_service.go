package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/mint-cat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Return a dummy SAML assertion
		fmt.Fprint(w, "<saml:Assertion ID=\"_demo_assertion\">MOCK_SAML_TOKEN_CONTENT</saml:Assertion>")
		log.Println("Mock SAML token served")
	})

	http.HandleFunc("/sts-token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Return a dummy Google Access Token
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token": "MOCK_ACCESS_TOKEN", "issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "token_type": "Bearer", "expires_in": 3600}`)
		log.Println("Mock STS token served")
	})

	log.Println("Mock service listening on :5000")
	log.Fatal(http.ListenAndServe(":5000", nil))
}
