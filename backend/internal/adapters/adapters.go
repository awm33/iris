// Package adapters normalizes model endpoints behind one client surface
// (TDD §3.4): submit, poll, cancel, manifest — the orchestrator and the
// registry speak this interface and never care whether the far side is our
// inference-api spec (iris fleet, open-weight, mocks) or a commercial API
// translated in-process. Commercial calls always proxy server-side: key
// custody, artifact download→re-upload, error-taxonomy mapping.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/awm33/iris/backend/internal/inference"
	"github.com/awm33/iris/backend/internal/vault"
)

// V resolves auth references at client construction — the ONLY place
// endpoint secrets materialize (TDD §3.4). Swapped for KMS later.
var V vault.Vault = vault.Dev{}

type Client interface {
	GetManifest(ctx context.Context) (json.RawMessage, error)
	CreateJob(ctx context.Context, req *inference.CreateJobRequest) (*inference.JobStatus, error)
	GetJob(ctx context.Context, id string) (*inference.JobStatus, error)
	CancelJob(ctx context.Context, id string) error
}

// For selects the adapter by endpoint kind and resolves the auth reference
// through the vault. "inference" (and the legacy dev kinds) speak our spec
// natively; commercial kinds translate.
func For(kind, baseURL, authRef string) (Client, error) {
	token, err := V.Resolve(authRef)
	if err != nil {
		return nil, fmt.Errorf("endpoint auth: %w", err)
	}
	switch kind {
	case "bfl":
		return newBFL(baseURL, token), nil
	case "seedance":
		return newSeedance(baseURL, token), nil
	case "elevenlabs":
		return newElevenLabs(baseURL, token), nil
	case "", "inference", "iris", "http", "mock", "openweight", "commercial":
		return inference.New(baseURL, token), nil
	default:
		return nil, fmt.Errorf("unknown endpoint kind %q", kind)
	}
}

// InProcess reports whether this kind's adapter executes inside the
// orchestrator process (commercial translators) rather than as a separate
// service speaking our spec. In-process adapters must receive INTERNAL
// blob URLs — the external advertisement (host.docker.internal) exists for
// containerized endpoints and doesn't resolve from the host.
func InProcess(kind string) bool {
	return kind == "seedance" || kind == "elevenlabs" || kind == "bfl"
}

// isPostSendTimeout: the request left the socket and the wait timed out —
// paid work may have happened remotely. Dial refusals (nothing sent) are
// excluded; they keep the orchestrator's free unreachable classification.
func isPostSendTimeout(err error) bool {
	var uerr *url.Error
	if !errorsAs(err, &uerr) {
		return false
	}
	return uerr.Timeout() && !isDialError(uerr)
}

func isDialError(uerr *url.Error) bool {
	var operr *net.OpError
	if errorsAs(uerr.Err, &operr) {
		return operr.Op == "dial"
	}
	return false
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
