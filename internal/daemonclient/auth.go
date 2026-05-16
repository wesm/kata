package daemonclient

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
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

// checkBearerTargetSafe refuses to attach a bearer token to a baseURL that
// would put the token on the wire in cleartext. Safe targets are the
// Unix-socket sentinel URL, HTTPS schemes, and HTTP loopback addresses
// (including "localhost", "127.0.0.1", "[::1]"). Defense in depth: the
// daemon-side guard in internal/daemon/auth.go already refuses to start in
// the unsafe shape, but a client pointed at an externally-administered
// daemon could still leak the token without this check.
func checkBearerTargetSafe(baseURL string) error {
	if strings.HasPrefix(baseURL, UnixBase) {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("parse base URL %q for bearer-token safety check: %w", baseURL, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" {
		return fmt.Errorf("unsupported URL scheme %q for bearer-token client", u.Scheme)
	}
	host := u.Hostname()
	if host == "" || host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to attach bearer token to plaintext non-loopback URL %q — "+
		"the daemon does not terminate TLS, so the token would travel in cleartext; "+
		"use a Unix socket or loopback address, tunnel via SSH, or terminate TLS "+
		"in a reverse proxy", baseURL)
}
