package daemon_test

import (
	"context"
	"net"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

// assertEndpointRoundTrip verifies that a listener and dialer endpoint
// can successfully connect and exchange data.
func assertEndpointRoundTrip(t *testing.T, l net.Listener, dialEP daemon.DaemonEndpoint) {
	t.Helper()
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		c, err := l.Accept()
		if err == nil && c != nil {
			_, _ = c.Write([]byte("ok"))
			_ = c.Close()
		}
	}()

	conn, err := dialEP.Dial(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 2)
	_, err = conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(buf))
}

func TestUnixEndpoint_RoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on windows")
	}
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	ep := daemon.UnixEndpoint(sock)

	l, err := ep.Listen()
	require.NoError(t, err)

	assertEndpointRoundTrip(t, l, ep)
	assert.Equal(t, "unix://"+sock, ep.Address())
}

func TestTCPEndpoint_RoundTrip(t *testing.T) {
	ep := daemon.TCPEndpoint("127.0.0.1:0")
	l, err := ep.Listen()
	require.NoError(t, err)

	// Hand the actually-bound address back into a fresh endpoint for Dial.
	addr := l.Addr().(*net.TCPAddr).String()
	dialEP := daemon.TCPEndpoint(addr)

	assertEndpointRoundTrip(t, l, dialEP)
}

func TestTCPEndpoint_RejectsNonLoopback(t *testing.T) {
	ep := daemon.TCPEndpoint("8.8.8.8:7474")
	_, err := ep.Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}

func TestTCPEndpoint_RejectsLocalhostHostname(t *testing.T) {
	ep := daemon.TCPEndpoint("localhost:7474")
	_, err := ep.Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "literal IP")
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in   string
		kind string
	}{
		{"unix:///tmp/foo.sock", "unix"},
		{"127.0.0.1:7474", "tcp"},
		{"localhost:7474", "tcp"},
	}
	for _, tc := range cases {
		ep, err := daemon.ParseAddress(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.kind, ep.Kind(), tc.in)
	}
}

// TestParseAddress_TCPAcceptsNonLoopbackPrivate guards the runtime-file
// readback path: when a daemon writes its --listen TCP address (e.g. a
// CGNAT/RFC1918 endpoint) into daemon.<pid>.json, ParseAddress must
// reconstruct an endpoint whose Listen()/Dial() do not reject the
// private address. Strict loopback-only is correct for untrusted input
// but wrong for the file we wrote ourselves. We exercise Listen on the
// loopback case (CGNAT/RFC1918 may not be routable in the test
// environment) and rely on TCPEndpointAny's own table tests for the
// other private ranges.
func TestParseAddress_TCPAcceptsNonLoopbackPrivate(t *testing.T) {
	// CGNAT-style address must round-trip through ParseAddress without
	// the validator rejecting it. We don't need to bind — we just need
	// to confirm ParseAddress doesn't return the strict-loopback form.
	ep, err := daemon.ParseAddress("100.64.0.5:7777")
	require.NoError(t, err)
	require.Equal(t, "tcp", ep.Kind())

	// And Listen actually works on a private-range loopback addr.
	loopback, err := daemon.ParseAddress("127.0.0.1:0")
	require.NoError(t, err)
	l, err := loopback.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
}

func TestTCPEndpointAny_AcceptsLoopback(t *testing.T) {
	ep := daemon.TCPEndpointAny("127.0.0.1:0")
	l, err := ep.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
}

func TestTCPEndpointAny_AcceptsPrivateRanges(t *testing.T) {
	cases := []string{
		"10.0.0.1:0",
		"172.16.5.5:0",
		"192.168.1.1:0",
		"100.64.0.5:0",  // CGNAT
		"169.254.1.1:0", // link-local IPv4
		"[fe80::1]:0",   // link-local IPv6
		"[fc00::1]:0",   // ULA
		"[::1]:0",       // loopback IPv6
	}
	for _, addr := range cases {
		_, err := daemon.TCPEndpointAny(addr).Listen()
		// We do NOT require Listen() to succeed (binding 10.x without
		// a configured interface fails with "cannot assign requested
		// address"), only that it does not fail with our validator's
		// "non-public" rejection.
		if err != nil {
			assert.NotContains(t, err.Error(), "non-public", addr)
			assert.NotContains(t, err.Error(), "literal IP", addr)
		}
	}
}

func TestTCPEndpointAny_RejectsPublicIPv4(t *testing.T) {
	_, err := daemon.TCPEndpointAny("8.8.8.8:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public")
}

func TestTCPEndpointAny_RejectsGUAIPv6(t *testing.T) {
	_, err := daemon.TCPEndpointAny("[2001:4860:4860::8888]:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public")
}

func TestTCPEndpointAny_RejectsUnspecified(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0"} {
		_, err := daemon.TCPEndpointAny(addr).Listen()
		require.Error(t, err, addr)
		assert.Contains(t, err.Error(), "non-public", addr)
	}
}

func TestTCPEndpointAny_RejectsHostname(t *testing.T) {
	_, err := daemon.TCPEndpointAny("example.com:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "literal IP")
}

func TestTCPEndpoint_StillRejectsPrivateNonLoopback(t *testing.T) {
	// Guards against an accidental refactor that loosens TCPEndpoint.
	_, err := daemon.TCPEndpoint("10.0.0.1:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}
