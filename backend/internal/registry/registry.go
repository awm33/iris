// Package registry manages model endpoints: dev seeding, manifest fetch +
// refresh, health, and cached parsed manifests for validation and routing.
//
// Auth note (dev): endpoint tokens are stored directly in model_endpoints.
// auth_ref. The KMS-vault indirection (TDD §3.4) lands with BYO keys — the
// column name already reflects the destination.
package registry

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/inference"
)

type Endpoint struct {
	ID          string
	WorkspaceID string
	DisplayName string
	Kind        string // iris | openweight | commercial | mock
	BaseURL     string
	Token       string
	Manifest    *inference.Manifest
	ManifestRaw json.RawMessage
	Healthy     bool
}

type Registry struct {
	pool *pgxpool.Pool

	mu        sync.RWMutex
	endpoints map[string]*Endpoint // by id
}

func New(pool *pgxpool.Pool) *Registry {
	return &Registry{pool: pool, endpoints: map[string]*Endpoint{}}
}

// SeedDevEndpoints registers the dockerized mock endpoints for the dev
// workspace if absent (mirrors the dev-workspace seed).
func (r *Registry) SeedDevEndpoints(ctx context.Context, workspaceID string, seeds map[string]string) error {
	for name, baseURL := range seeds {
		_, err := r.pool.Exec(ctx, `
			INSERT INTO model_endpoints (id, workspace_id, display_name, kind, base_url, auth_ref)
			SELECT $1, $2, $3, 'mock', $4, 'dev'
			WHERE NOT EXISTS (
				SELECT 1 FROM model_endpoints WHERE workspace_id = $2 AND base_url = $4)`,
			ids.New("mep"), workspaceID, name, baseURL)
		if err != nil {
			return err
		}
	}
	return nil
}

// Refresh loads all endpoints from the DB and fetches each manifest,
// updating health + the cached manifest. Call at startup and on an interval.
func (r *Registry) Refresh(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `
		SELECT id, workspace_id, display_name, kind, base_url, COALESCE(auth_ref, '')
		FROM model_endpoints`)
	if err != nil {
		return err
	}
	type row struct{ id, ws, name, kind, url, token string }
	var eps []row
	for rows.Next() {
		var e row
		if err := rows.Scan(&e.id, &e.ws, &e.name, &e.kind, &e.url, &e.token); err != nil {
			rows.Close()
			return err
		}
		eps = append(eps, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	fresh := map[string]*Endpoint{}
	for _, e := range eps {
		ep := &Endpoint{
			ID: e.id, WorkspaceID: e.ws, DisplayName: e.name,
			Kind: e.kind, BaseURL: e.url, Token: e.token,
		}
		client := inference.New(e.url, e.token)
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		raw, err := client.GetManifest(fctx)
		cancel()
		if err == nil {
			// Schema validation first — a manifest that would fail every job
			// (empty profiles, negative pricing) marks the endpoint unhealthy
			// rather than parsing loosely and failing at dispatch.
			if verr := inference.ValidateManifestDocument(raw); verr != nil {
				slog.Warn("endpoint manifest fails schema", "endpoint", e.name, "err", verr)
			} else if m, perr := inference.ParseManifest(raw); perr == nil {
				ep.Manifest, ep.ManifestRaw, ep.Healthy = m, raw, true
			} else {
				slog.Warn("endpoint manifest invalid", "endpoint", e.name, "err", perr)
			}
		} else {
			slog.Warn("endpoint manifest fetch failed", "endpoint", e.name, "err", err)
		}

		if ep.Healthy {
			if _, err := r.pool.Exec(ctx, `
				UPDATE model_endpoints
				SET manifest = $2, manifest_fetched_at = now(), healthy = true
				WHERE id = $1`, e.id, ep.ManifestRaw); err != nil {
				return err
			}
		} else {
			// Keep the last-known-good manifest (DB and cache): a transient
			// endpoint blip must not erase capabilities and hard-fail queued
			// jobs — dispatch continues against the stale manifest and the
			// endpoint's own errors drive retry behavior.
			var stored []byte
			if err := r.pool.QueryRow(ctx,
				`SELECT manifest FROM model_endpoints WHERE id = $1`, e.id).Scan(&stored); err == nil && len(stored) > 0 {
				if m, perr := inference.ParseManifest(stored); perr == nil {
					ep.Manifest, ep.ManifestRaw = m, stored
				}
			}
			if _, err := r.pool.Exec(ctx, `
				UPDATE model_endpoints SET healthy = false WHERE id = $1`, e.id); err != nil {
				return err
			}
		}
		fresh[e.id] = ep
	}

	r.mu.Lock()
	r.endpoints = fresh
	r.mu.Unlock()
	return nil
}

// RefreshLoop refreshes on the interval until ctx is done.
func (r *Registry) RefreshLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Refresh(ctx); err != nil && ctx.Err() == nil {
				slog.Error("registry refresh failed", "err", err)
			}
		}
	}
}

func (r *Registry) Get(id string) (*Endpoint, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ep, ok := r.endpoints[id]
	return ep, ok
}

// GetOrRefresh returns the endpoint, refreshing the cache once on a miss —
// endpoints registered after this process's last refresh (startup races,
// newly added endpoints) resolve immediately instead of waiting a cycle.
func (r *Registry) GetOrRefresh(ctx context.Context, id string) (*Endpoint, bool) {
	if ep, ok := r.Get(id); ok {
		return ep, ok
	}
	if err := r.Refresh(ctx); err != nil {
		slog.Warn("on-demand registry refresh failed", "err", err)
		return nil, false
	}
	return r.Get(id)
}

func (r *Registry) List(workspaceID string) []*Endpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Endpoint
	for _, ep := range r.endpoints {
		if ep.WorkspaceID == workspaceID {
			out = append(out, ep)
		}
	}
	return out
}
