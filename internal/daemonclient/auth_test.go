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

// writeAuthConfig writes a config.toml with [auth].token = tok under home.
func writeAuthConfig(home, tok string) error {
	return writeRawConfig(home, "[auth]\ntoken = \""+tok+"\"\n")
}

// writeRawConfig writes config.toml verbatim under home.
func writeRawConfig(home, body string) error {
	return os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600)
}
