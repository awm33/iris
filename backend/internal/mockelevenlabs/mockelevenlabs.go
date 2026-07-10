// Package mockelevenlabs replicates the RECORDED ElevenLabs TTS shapes so
// the adapter develops against local docker with zero real-remote traffic.
// Mocked: xi-api-key auth, synchronous audio response, voices listing,
// empty-text 422, rate-limit simulation via the X-Mock-Overload header.
package mockelevenlabs

import (
	_ "embed"
	"encoding/json"
	"net/http"
)

//go:embed canned.mp3
var cannedMP3 []byte

// Voices the mock recognizes — the elevenlabs adapter's manifest enum must
// stay a subset (a drift test in the adapter package enforces it).
var Voices = []string{"iris-narrator", "mara"}

func knownVoice(v string) bool {
	for _, k := range Voices {
		if k == v {
			return true
		}
	}
	return false
}

type Server struct{ key string }

func New(key string) *Server { return &Server{key: key} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/voices", s.auth(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"voices": []map[string]string{
				{"voice_id": "iris-narrator", "name": "Iris Narrator"},
				{"voice_id": "mara", "name": "Mara"},
			},
		})
	}))
	mux.HandleFunc("POST /v1/text-to-speech/{voice}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if !knownVoice(r.PathValue("voice")) {
			http.Error(w, `{"detail":{"status":"voice_not_found"}}`, http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Mock-Overload") != "" {
			http.Error(w, `{"detail":{"status":"too_many_concurrent_requests"}}`, http.StatusTooManyRequests)
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
			http.Error(w, `{"detail":{"status":"invalid_text","message":"text is required"}}`, http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(cannedMP3)
	}))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.key != "" && r.Header.Get("xi-api-key") != s.key {
			http.Error(w, `{"detail":{"status":"invalid_api_key"}}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
