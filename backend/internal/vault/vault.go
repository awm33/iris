// Package vault resolves endpoint auth references to secrets. TDD §3.4: BYO
// keys live in a KMS-encrypted vault, decrypted ONLY in the dispatch/proxy
// path — never logged, never echoed, never stored plaintext outside dev.
//
// Dev resolver: "env:NAME" reads the orchestrator's environment (the same
// custody discipline as IRIS_PEXELS_API_KEY); anything else passes through
// verbatim for the existing dev rows. The KMS implementation replaces this
// behind the same interface.
package vault

import (
	"fmt"
	"os"
	"strings"
)

type Vault interface {
	// Resolve turns an auth_ref into the secret it names. Callers must not
	// log or persist the result.
	Resolve(authRef string) (string, error)
}

type Dev struct{}

func (Dev) Resolve(authRef string) (string, error) {
	if name, ok := strings.CutPrefix(authRef, "env:"); ok {
		v := os.Getenv(name)
		if v == "" {
			// The NAME is loggable; the value never is.
			return "", fmt.Errorf("vault: env ref %q is unset", name)
		}
		return v, nil
	}
	return authRef, nil // dev passthrough (plaintext rows predate the vault)
}
