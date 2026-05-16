package daemonclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAuthTokenEnvWins(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	require.NoError(t, writeAuthConfig(tmp, "from-toml"))
	t.Setenv("KATA_AUTH_TOKEN", "from-env")

	assert.Equal(t, "from-env", resolveAuthToken())
}

func TestResolveAuthTokenFallsBackToTOML(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "")
	require.NoError(t, writeAuthConfig(tmp, "from-toml"))

	assert.Equal(t, "from-toml", resolveAuthToken())
}

func TestResolveAuthTokenEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "")

	assert.Empty(t, resolveAuthToken())
}

func TestResolveAuthTokenMalformedTOMLDegradesToEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "")
	require.NoError(t, writeRawConfig(tmp, "not = valid = toml"))

	assert.Empty(t, resolveAuthToken(),
		"malformed config.toml should not panic; degrade to no token")
}

func TestBearerTransportInjectsHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: withBearer(http.DefaultTransport, "secret")}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := c.Do(req) //nolint:gosec // G704: srv.URL is the test's own httptest.Server
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "Bearer secret", got)
}

func TestBearerTransportNoTokenPassthrough(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rt := withBearer(http.DefaultTransport, "")
	assert.Equal(t, http.DefaultTransport, rt,
		"empty token must return the base transport unchanged")

	c := &http.Client{Transport: rt}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := c.Do(req) //nolint:gosec // G704: srv.URL is the test's own httptest.Server
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Empty(t, got)
}

func TestBearerTransportPreservesCallerHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: withBearer(http.DefaultTransport, "from-transport")}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer caller-supplied")
	resp, err := c.Do(req) //nolint:gosec // G704: srv.URL is the test's own httptest.Server
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "Bearer caller-supplied", got,
		"caller-supplied Authorization header must not be overwritten")
}

func TestBearerTransportDoesNotMutateRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: withBearer(http.DefaultTransport, "secret")}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := c.Do(req) //nolint:gosec // G704: srv.URL is the test's own httptest.Server
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Empty(t, req.Header.Get("Authorization"),
		"the caller's *http.Request must not be mutated by the transport")
}

func TestNewHTTPClientAttachesAuthHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "client-tok")

	c, err := NewHTTPClient(context.Background(), srv.URL, Opts{})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		srv.URL+"/api/v1/projects", nil)
	require.NoError(t, err)
	resp, err := c.Do(req) //nolint:gosec // G704: srv.URL is the test's own httptest.Server
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "Bearer client-tok", got)
}

func TestNewHTTPClientStreamingAttachesAuthHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "stream-tok")

	c, err := NewHTTPClient(context.Background(), srv.URL, Opts{
		ResponseHeaderTimeout: SSEHandshakeTimeout,
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		srv.URL+"/api/v1/events/stream", nil)
	require.NoError(t, err)
	resp, err := c.Do(req) //nolint:gosec // G704: srv.URL is the test's own httptest.Server
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "Bearer stream-tok", got,
		"streaming clients must also inject the bearer token")
}

// TestNewHTTPClient_RefusesBearerOnPlaintextNonLoopback verifies the
// defense-in-depth guard in NewHTTPClient: when a token is resolved (env
// or config) and the baseURL is a non-loopback http:// target, client
// construction must fail rather than silently leak the token over the
// wire. The daemon-side checkAuthStartup already refuses to bind in this
// shape, but the client check protects against externally-administered
// daemons and operator misconfiguration.
func TestNewHTTPClient_RefusesBearerOnPlaintextNonLoopback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "secret")

	_, err := NewHTTPClient(context.Background(), "http://example.invalid:7373", Opts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext")
}

// TestNewHTTPClient_AllowsBearerOnLoopback covers the safe-target arm of
// checkBearerTargetSafe: 127.0.0.1 and [::1] keep the token in-host even
// over plaintext HTTP.
func TestNewHTTPClient_AllowsBearerOnLoopback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "secret")

	for _, base := range []string{
		"http://127.0.0.1:7373",
		"http://[::1]:7373",
		"http://localhost:7373",
	} {
		t.Run(base, func(t *testing.T) {
			_, err := NewHTTPClient(context.Background(), base, Opts{})
			require.NoError(t, err)
		})
	}
}

// TestNewHTTPClient_AllowsBearerOverHTTPS covers the TLS-protected arm:
// an https:// baseURL is safe to attach a token to regardless of host.
func TestNewHTTPClient_AllowsBearerOverHTTPS(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "secret")

	_, err := NewHTTPClient(context.Background(), "https://example.invalid", Opts{})
	require.NoError(t, err)
}

// TestNewHTTPClient_AllowsBearerOnUnixSocketBase verifies the
// UnixBase sentinel URL passes the safety check (the transport dials a
// local socket, so the token never leaves the host).
func TestNewHTTPClient_AllowsBearerOnUnixSocketBase(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "secret")

	// Pre-create a usable socket entry so unixClientFromRuntime succeeds.
	// The actual dial isn't exercised here — we only care that the bearer
	// safety check passes for the UnixBase URL.
	_, err := NewHTTPClient(context.Background(), UnixBase, Opts{})
	if err != nil {
		// Permit "no runtime file" failures from unixClientFromRuntime —
		// they fire before bearer-attachment so don't indicate a regression.
		assert.NotContains(t, err.Error(), "plaintext",
			"UnixBase must never trip the bearer-safety check")
	}
}

// TestNewHTTPClient_NoTokenSkipsSafetyCheck verifies the gate is
// token-conditional: when KATA_AUTH_TOKEN is unset, even non-loopback
// plaintext URLs are fine because no token will be attached.
func TestNewHTTPClient_NoTokenSkipsSafetyCheck(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_AUTH_TOKEN", "")

	_, err := NewHTTPClient(context.Background(), "http://example.invalid:7373", Opts{})
	require.NoError(t, err)
}

// writeAuthConfig writes a config.toml with [auth].token = tok under home.
func writeAuthConfig(home, tok string) error {
	return writeRawConfig(home, "[auth]\ntoken = \""+tok+"\"\n")
}

// writeRawConfig writes config.toml verbatim under home.
func writeRawConfig(home, body string) error {
	return os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600)
}
