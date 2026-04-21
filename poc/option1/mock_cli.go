package main

import (
	"fmt"
)

func main() {
	fmt.Println(`{
  "version": 1,
  "success": true,
  "token_type": "urn:ietf:params:oauth:token-type:id_token",
  "id_token": "DUMMY_TOKEN_FROM_MOCK_CLI"
}`)
}
