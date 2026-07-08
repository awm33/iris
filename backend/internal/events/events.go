// Package events bridges pg NOTIFY channels to browsers via Server-Sent
// Events. Payloads are intentionally thin — {channel, id} — clients refetch
// through the normal API on receipt, so the bridge never becomes a second
// source of truth.
//
// Delivery model: per-client COALESCED dirty flags, not a queue. A slow
// client may miss intermediate events but can never miss "something changed
// on channel X" — the flag stays set until written. On pg reconnect the
// bridge broadcasts a synthetic event per channel (NOTIFYs fired during the
// gap are unrecoverable; clients resync by refetching). The client side
// mirrors this with an onopen invalidation and a slow poll while jobs are
// active, so SSE is an accelerator, never load-bearing for correctness.
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
	clients map[*client]struct{}
}

type client struct {
	mu      sync.Mutex
	pending map[string]string // channel -> latest payload (coalesced)
	kick    chan struct{}     // cap 1; wakes the writer
}

func (c *client) mark(channel, id string) {
	c.mu.Lock()
	c.pending[channel] = id
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default: // writer already signaled
	}
}

func (c *client) drain() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return nil
	}
	out := c.pending
	c.pending = map[string]string{}
	return out
}

type event struct {
	Channel string `json:"channel"`
	ID      string `json:"id,omitempty"`
}

const (
	maxClients        = 256
	keepaliveInterval = 25 * time.Second
	reconnectDelay    = 2 * time.Second
)

// Run listens on the pg channels and fans notifications out to connected
// SSE clients, reconnecting on connection loss until ctx is done.
func (b *Bridge) Run(ctx context.Context) {
	b.mu.Lock()
	if b.clients == nil {
		b.clients = map[*client]struct{}{}
	}
	b.mu.Unlock()

	first := true
	for ctx.Err() == nil {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectDelay):
			}
		}
		first = false
		if err := b.listenOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("event bridge connection lost; reconnecting", "err", err)
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
	// NOTIFYs fired while we were disconnected are gone (LISTEN has no
	// replay) — tell every client to resync each channel.
	for _, ch := range b.Channels {
		b.broadcast(ch, "")
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		b.broadcast(n.Channel, n.Payload)
	}
}

func (b *Bridge) broadcast(channel, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		c.mark(channel, id)
	}
}

// ServeHTTP is the SSE endpoint (GET /events).
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	c := &client{pending: map[string]string{}, kick: make(chan struct{}, 1)}
	b.mu.Lock()
	if b.clients == nil {
		b.clients = map[*client]struct{}{}
	}
	if len(b.clients) >= maxClients {
		b.mu.Unlock()
		http.Error(w, "too many event streams", http.StatusTooManyRequests)
		return
	}
	b.clients[c] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.clients, c)
		b.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no") // nginx-class proxies must not buffer SSE

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
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-c.kick:
			for channel, id := range c.drain() {
				msg, _ := json.Marshal(event{Channel: channel, ID: id})
				if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}
