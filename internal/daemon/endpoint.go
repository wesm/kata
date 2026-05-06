package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// DaemonEndpoint abstracts the listen / dial pair for either a Unix socket or
// TCP loopback. Address() returns a stable string representation.
//
//nolint:revive // DaemonEndpoint name is fixed by Plan 1 §2.2 public spec.
type DaemonEndpoint interface {
	Listen() (net.Listener, error)
	Dial(ctx context.Context) (net.Conn, error)
	Address() string
	Kind() string // "unix" | "tcp"
}

type unixEndpoint struct{ path string }

// staleSocketProbeTimeout caps how long the pre-bind probe waits
// before giving up. A short timeout is enough on a healthy host: a
// running daemon accepts immediately, and a stale socket whose owner
// is gone returns ECONNREFUSED in microseconds. The timeout only
// matters when the kernel is overloaded — and in that case the probe
// result is ambiguous, so we refuse to remove rather than guess.
const staleSocketProbeTimeout = 500 * time.Millisecond

// ErrSocketInUse is returned by unixEndpoint.Listen when the socket
// path already has a daemon accepting connections. The per-namespace
// runtime dir holds at most one daemon, so this is the launcher's
// signal that another instance is already serving and the new start
// should back off rather than clobber it.
var ErrSocketInUse = errors.New("socket already in use")

// Listen for unixEndpoint is OS-specific because the cleanup path
// uses flock(2) to serialize concurrent starters; see
// endpoint_listen_unix.go and endpoint_listen_windows.go.

func (u unixEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{}
	return d.DialContext(ctx, "unix", u.path)
}

func (u unixEndpoint) Address() string { return "unix://" + u.path }
func (u unixEndpoint) Kind() string    { return "unix" }

// UnixEndpoint constructs a Unix-socket endpoint at the given path.
func UnixEndpoint(path string) DaemonEndpoint { return unixEndpoint{path: path} }

type tcpEndpoint struct{ addr string }

func (t tcpEndpoint) Listen() (net.Listener, error) {
	if err := requireLoopback(t.addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", t.addr)
}

func (t tcpEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	if err := requireLoopback(t.addr); err != nil {
		return nil, err
	}
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", t.addr)
}

func (t tcpEndpoint) Address() string { return t.addr }
func (t tcpEndpoint) Kind() string    { return "tcp" }

// TCPEndpoint constructs a TCP-loopback endpoint at the given host:port.
func TCPEndpoint(addr string) DaemonEndpoint { return tcpEndpoint{addr: addr} }

// requireLoopback rejects any host that isn't a literal loopback IP. We do
// not accept the "localhost" hostname because /etc/hosts can map it to a
// non-loopback address, which would silently violate the loopback-only
// contract. Callers that want a hostname must resolve it themselves and pass
// the resulting literal IP.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("address %q is not a literal IP (resolve hostnames before calling)", addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("address %q is not loopback", addr)
	}
	return nil
}

type tcpAnyEndpoint struct{ addr string }

func (t tcpAnyEndpoint) Listen() (net.Listener, error) {
	if err := requireNonPublic(t.addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", t.addr)
}

func (t tcpAnyEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	if err := requireNonPublic(t.addr); err != nil {
		return nil, err
	}
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", t.addr)
}

func (t tcpAnyEndpoint) Address() string { return t.addr }
func (t tcpAnyEndpoint) Kind() string    { return "tcp" }

// TCPEndpointAny constructs a TCP endpoint that accepts any non-public
// address (loopback, RFC1918, CGNAT, link-local, ULA). Public IPv4,
// GUA IPv6, and unspecified (0.0.0.0 / ::) are rejected. Hostnames are
// rejected — callers must resolve to a literal IP.
func TCPEndpointAny(addr string) DaemonEndpoint { return tcpAnyEndpoint{addr: addr} }

// cgnatBlock is RFC6598 100.64.0.0/10 — the carrier-grade NAT range
// commonly used by tailscale and similar private overlays. Go's
// net.IP.IsPrivate() does not include it.
var cgnatBlock = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

// ValidateNonPublicAddress runs the same address rules used by
// TCPEndpointAny.Listen() / .Dial() without actually binding or
// dialing. Useful as a CLI preflight: a `kata daemon start --listen`
// caller wants the validation error before the server starts up,
// without a listen-then-close TOCTOU window where the bound port
// could be claimed by another process. Returns nil on success.
func ValidateNonPublicAddress(addr string) error { return requireNonPublic(addr) }

// requireNonPublic accepts loopback, RFC1918 (via IsPrivate), CGNAT,
// link-local, and ULA. Rejects public IPv4, GUA IPv6, the unspecified
// address (0.0.0.0 / ::), and any hostname.
func requireNonPublic(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("address %q is not a literal IP (resolve hostnames before calling)", addr)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("address %q is non-public: unspecified bind not allowed; use a private address (loopback, RFC1918, CGNAT, link-local, ULA)", addr)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnatBlock.Contains(ip) {
		return nil
	}
	return fmt.Errorf("address %q is non-public: use a private address (loopback, RFC1918, CGNAT, link-local, ULA)", addr)
}

// ParseAddress decodes a serialized form (unix:///path or host:port).
// TCP addresses are returned as TCPEndpointAny: this is the runtime-file
// readback path and the file is written by the daemon itself under
// $KATA_HOME (0700). A daemon started with --listen on a non-loopback
// private address (RFC1918, CGNAT, link-local, ULA) must be re-dialable
// from the same host's discovery code, so the strict loopback-only form
// is wrong here. Public addresses still get rejected on Listen/Dial.
func ParseAddress(s string) (DaemonEndpoint, error) {
	if strings.HasPrefix(s, "unix://") {
		return UnixEndpoint(strings.TrimPrefix(s, "unix://")), nil
	}
	if strings.Contains(s, ":") {
		return TCPEndpointAny(s), nil
	}
	return nil, fmt.Errorf("unrecognized address: %q", s)
}
