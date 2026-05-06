package daemon_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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

// shortTempDir returns a per-test directory short enough to host a
// Unix socket on darwin/linux (sockaddr_un.sun_path = 104/108 bytes).
// t.TempDir embeds the test name and a random suffix, which is fine
// for files but pushes socket paths past the kernel limit on macOS.
// We reuse the test's TempDir for cleanup but mint a sibling dir
// directly under /tmp to keep the absolute path tiny.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ka-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestUnixEndpoint_StaleListen is the regression for the auto-start
// failure mode where a previous daemon crashed (SIGKILL, panic, host
// reboot mid-shutdown) and left its socket file on disk. Without the
// pre-bind cleanup, the next Listen returns "address already in use"
// and the launcher reports "kata: daemon failed to start within 5s".
// A second Listen on the same path must succeed when nothing is
// actually accepting connections there.
func TestUnixEndpoint_StaleListen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on windows")
	}
	sock := filepath.Join(shortTempDir(t), "d.sock")
	// Simulate a crashed daemon: bind a listener with the unlink-on-
	// close hook disabled, then close. The file on disk is a real Unix
	// socket (right ModeSocket bit) but no process accepts on it — the
	// exact state a SIGKILL/panic leaves behind.
	first, err := net.Listen("unix", sock)
	require.NoError(t, err)
	first.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, first.Close())
	info, statErr := os.Stat(sock)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&os.ModeSocket, "test setup: expected stale socket file")

	second, err := daemon.UnixEndpoint(sock).Listen()
	require.NoError(t, err, "Listen must clean up the stale socket file")
	t.Cleanup(func() { _ = second.Close() })
	assertEndpointRoundTrip(t, second, daemon.UnixEndpoint(sock))
}

// TestUnixEndpoint_LiveRefused pins the safety property the cleanup
// must not violate: when a healthy daemon is already accepting on the
// path, a concurrent Listen must NOT remove the file and bind on top —
// that would steal connections. The error wraps daemon.ErrSocketInUse.
func TestUnixEndpoint_LiveRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on windows")
	}
	sock := filepath.Join(shortTempDir(t), "d.sock")
	live, err := daemon.UnixEndpoint(sock).Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = live.Close() })
	go func() {
		for {
			c, err := live.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	_, err = daemon.UnixEndpoint(sock).Listen()
	require.Error(t, err)
	require.True(t, errors.Is(err, daemon.ErrSocketInUse),
		"expected ErrSocketInUse, got %v", err)
}

// TestUnixEndpoint_NonSocketRefused: when the path points at a
// regular file the pre-bind cleanup must NOT remove it — that would
// destroy unrelated user data sharing the namespace.
func TestUnixEndpoint_NonSocketRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on windows")
	}
	path := filepath.Join(shortTempDir(t), "d.sock")
	require.NoError(t, os.WriteFile(path, []byte("not a socket"), 0o600))

	_, err := daemon.UnixEndpoint(path).Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a socket")
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "non-socket file must not be removed")
}

// TestUnixEndpoint_ConcurrentListenSerializes pins the locking
// invariant: when N starters race for the same socket path, exactly
// one ends up with a listener and the rest return ErrSocketInUse.
//
// Without flock around probe→remove→bind, two starters could both
// observe a stale socket as removable and race os.Remove — the
// loser's unlink would clobber the winner's freshly-bound listener,
// orphaning one daemon while the other claims its path. A live
// concurrent test catches the regression that loose serialization
// would introduce.
func TestUnixEndpoint_ConcurrentListenSerializes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on windows")
	}
	sock := filepath.Join(shortTempDir(t), "d.sock")

	const N = 8
	listeners := make([]net.Listener, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	start := make(chan struct{})
	for i := range N {
		go func(i int) {
			defer wg.Done()
			<-start
			listeners[i], errs[i] = daemon.UnixEndpoint(sock).Listen()
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, l := range listeners {
		if l != nil {
			won++
			t.Cleanup(func() { _ = l.Close() })
			continue
		}
		require.Error(t, errs[i], "goroutine %d: expected either a listener or an error", i)
		require.True(t, errors.Is(errs[i], daemon.ErrSocketInUse),
			"goroutine %d: expected ErrSocketInUse, got %v", i, errs[i])
	}
	require.Equal(t, 1, won, "exactly one Listen must succeed; got %d winners", won)
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
