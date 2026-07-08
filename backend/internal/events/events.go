// Package events bridges pg NOTIFY channels to browsers via Server-Sent
// Events. Payloads are intentionally thin — {channel, id} — clients refetch
// through the normal API on receipt, so the bridge never becomes a second
// source of truth.
//
// SSE (not WebSocket) is deliberate for job events: one-way, auto-reconnect
// built into EventSource, zero dependencies. The doc-sync service (M3) needs
// bidirectional WS and will be its own endpoint; this bridge stays as-is.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type Bridge struct {
	DSN      string
	Channels []string // pg NOTIFY channels to relay

	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

type event struct {
	Channel string `json:"channel"`
	ID      string `json:"id,omitempty"` // notify payload (job id), may be empty
}

const (
	clientBuffer      = 64
	keepaliveInterval = 25 * time.Second
	reconnectDelay    = 2 * time.Second
)

// Run listens on the pg channels and fans notifications out to connected
// SSE clients, reconnecting on connection loss until ctx is done.
func (b *Bridge) Run(ctx context.Context) {
	b.mu.Lock()
	if b.clients == nil {
		b.clients = map[chan []byte]struct{}{}
	}
	b.mu.Unlock()

	for ctx.Err() == nil {
		if err := b.listenOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("event bridge connection lost; reconnecting", "err", err)
			time.Sleep(reconnectDelay)
		}
	}
}

func (b *Bridge) listenOnce(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, b.DSN)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	for _, ch := range b.Channels {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
			return err
		}
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		msg, _ := json.Marshal(event{Channel: n.Channel, ID: n.Payload})
		b.broadcast(msg)
	}
}

func (b *Bridge) broadcast(msg []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default: // slow client: drop the event; the next one triggers refetch anyway
		}
	}
}

// ServeHTTP is the SSE endpoint (GET /events).
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, clientBuffer)
	b.mu.Lock()
	if b.clients == nil {
		b.clients = map[chan []byte]struct{}{}
	}
	b.clients[ch] = struct{}{}
	clientCount := len(b.clients)
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.clients, ch)
		b.mu.Unlock()
	}()
	slog.Debug("sse client connected", "clients", clientCount)

	// Tell EventSource we're live, then relay until the client goes away.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
