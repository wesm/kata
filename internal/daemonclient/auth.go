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
	// Per-request safety check: a baseURL-only check at construction can be
	// bypassed if the server redirects from a safe target (HTTPS / loopback)
	// to a plaintext non-loopback URL — http.Client follows the redirect via
	// the same transport, and we would attach the token to the redirected
	// request. Re-validating req.URL here covers both initial requests and
	// follow-up redirects without trusting the client redirect policy.
	if err := checkBearerTargetSafeURL(req.URL); err != nil {
		return nil, err
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
// would put the token on the wire in cleartext. Thin wrapper over
// checkBearerTargetSafeURL that accepts a string base URL — used at client
// construction time to fail fast before any request is built.
func checkBearerTargetSafe(baseURL string) error {
	if strings.HasPrefix(baseURL, UnixBase) {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("parse base URL %q for bearer-token safety check: %w", baseURL, err)
	}
	return checkBearerTargetSafeURL(u)
}

// unixSentinelHost is the host portion of UnixBase — the synthetic value
// http.Request sees when daemonclient dials a Unix socket. Treated as safe
// because the request never leaves the host.
const unixSentinelHost = "kata.invalid"

// checkBearerTargetSafeURL is the per-request form of the bearer-safety check.
// Safe targets are the Unix-socket sentinel host, HTTPS schemes, and HTTP
// loopback addresses (including "localhost", "127.0.0.1", "[::1]"). Defense
// in depth on top of checkAuthStartup so a redirect-following client cannot
// leak the token to a plaintext non-loopback URL.
func checkBearerTargetSafeURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("nil URL for bearer-token safety check")
	}
	if u.Host == unixSentinelHost {
		return nil
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
		"in a reverse proxy", u.Redacted())
}
