// Package mockseedance replicates the RECORDED Seedance API shapes (Ark v3
// contents/generations) so the adapter develops and tests against local
// docker with zero real-remote traffic (environment stance, plan §0).
// Behaviors mocked: bearer auth, task lifecycle queued→running→succeeded,
// a result URL served by this mock, content-policy failures ("unsafe" in
// the prompt), and cancellation.
package mockseedance

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed canned.mp4
var cannedMP4 []byte

type task struct {
	ID      string
	Status  string
	Created time.Time
	Fail    bool
}

type Server struct {
	mu    sync.Mutex
	tasks map[string]*task
	seq   int
	token string
}

func New(token string) *Server {
	return &Server{tasks: map[string]*task{}, token: token}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/ping", s.auth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.HandleFunc("POST /api/v3/contents/generations/tasks", s.auth(s.create))
	mux.HandleFunc("GET /api/v3/contents/generations/tasks/{id}", s.auth(s.get))
	mux.HandleFunc("DELETE /api/v3/contents/generations/tasks/{id}", s.auth(s.cancel))
	// Wildcards must span whole segments — the handler serves any
	// /results/<file>; task ids are embedded in the filename.
	mux.HandleFunc("GET /results/{file}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(cannedMP4)
	})
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, `{"error":{"code":"auth_failed","message":"invalid api key"}}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" || len(body.Content) == 0 {
		http.Error(w, `{"error":{"code":"invalid_parameter","message":"model and content are required"}}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.seq++
	t := &task{ID: fmt.Sprintf("cgt-%08d", s.seq), Status: "queued", Created: time.Now()}
	for _, c := range body.Content {
		// Recorded behavior: policy violations fail the TASK, not the create.
		if c.Type == "text" && strings.Contains(strings.ToLower(c.Text), "unsafe") {
			t.Fail = true
		}
	}
	s.tasks[t.ID] = t
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{"id": t.ID})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	t := s.tasks[r.PathValue("id")]
	s.mu.Unlock()
	if t == nil {
		http.Error(w, `{"error":{"code":"not_found","message":"no such task"}}`, http.StatusNotFound)
		return
	}
	resp := map[string]any{"id": t.ID}
	age := time.Since(t.Created)
	switch {
	case t.Status == "cancelled":
		resp["status"] = "cancelled"
	case age < 400*time.Millisecond:
		resp["status"] = "queued"
	case age < 1200*time.Millisecond:
		resp["status"] = "running"
	case t.Fail:
		resp["status"] = "failed"
		resp["error"] = map[string]string{"code": "content_policy_violation", "message": "prompt rejected by content policy"}
	default:
		resp["status"] = "succeeded"
		resp["content"] = map[string]string{"video_url": "http://" + r.Host + "/results/" + t.ID + ".mp4"}
		resp["usage"] = map[string]int64{"completion_tokens": 24000}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if t := s.tasks[r.PathValue("id")]; t != nil {
		t.Status = "cancelled"
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}
