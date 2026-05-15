package daemonclient

import (
	"net/http"
	"os"
	"strings"

	"github.com/wesm/kata/internal/config"
)

// resolveAuthToken returns the auth token a client should attach to
// outgoing requests. Resolution mirrors the daemon side:
//
//  1. KATA_AUTH_TOKEN env (highest priority).
//  2. [auth].token in <KATA_HOME>/config.toml.
//  3. Empty (no header injected).
//
// Errors reading the TOML are not surfaced: a misformatted file should
// not silently strand the CLI on a no-auth path, but it also should not
// block discovery. Daemon startup (which always calls ReadDaemonConfig)
// is the surface that reports parse errors loudly. Here we degrade to
// "no token" so the request fails with a clean 401 rather than a noisy
// client-side decode error.
func resolveAuthToken() string {
	if v := strings.TrimSpace(os.Getenv("KATA_AUTH_TOKEN")); v != "" {
		return v
	}
	cfg, err := config.ReadDaemonConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Auth.Token
}

// bearerTransport wraps an http.RoundTripper and injects
// Authorization: Bearer <token> on every outgoing request unless the
// caller already supplied an Authorization header. Cloning the request
// keeps the upstream caller's *http.Request untouched, which matters
// when the same request is replayed (e.g. retry loops or SSE
// reconnects that recycle a parent request object).
//
// The /api/v1/ping and /api/v1/health endpoints do not require auth,
// but the daemon's middleware ignores the header on those paths. We
// inject unconditionally so a single transport works for discovery
// probes, normal API calls, and SSE streams alike.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token == "" || req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// withBearer wraps base with bearer-token injection when token is
// non-empty. When token is empty the base transport is returned
// unchanged so the no-auth daemon deployments incur zero extra cost.
// A nil base falls back to http.DefaultTransport — matching net/http's
// own zero-value behavior when *http.Client.Transport is nil.
func withBearer(base http.RoundTripper, token string) http.RoundTripper {
	if token == "" {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &bearerTransport{base: base, token: token}
}
