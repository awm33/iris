// Dev mock of the Seedance API (recorded shapes) — loopback-bound like
// every other local model service.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/awm33/iris/backend/internal/mockseedance"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8905"
	}
	token := os.Getenv("MOCK_SEEDANCE_KEY")
	if token == "" {
		token = "dev-seedance-key"
	}
	log.Printf("mock-seedance listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mockseedance.New(token).Handler()))
}
