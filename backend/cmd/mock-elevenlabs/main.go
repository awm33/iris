// Dev mock of the ElevenLabs TTS API (recorded shapes) — loopback-bound.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/awm33/iris/backend/internal/mockelevenlabs"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8906"
	}
	key := os.Getenv("MOCK_ELEVENLABS_KEY")
	if key == "" {
		key = "dev-elevenlabs-key"
	}
	log.Printf("mock-elevenlabs listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mockelevenlabs.New(key).Handler()))
}
