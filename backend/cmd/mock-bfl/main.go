// Dev mock of the BFL FLUX API (recorded shapes) — loopback-bound like
// every other local model service.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/awm33/iris/backend/internal/mockbfl"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8907"
	}
	token := os.Getenv("MOCK_BFL_KEY")
	if token == "" {
		token = "dev-bfl-key"
	}
	log.Printf("mock-bfl listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mockbfl.New(token).Handler()))
}
