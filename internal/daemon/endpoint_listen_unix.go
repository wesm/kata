//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Listen binds the unix-socket endpoint, cleaning up after a crashed
// predecessor when needed.
//
// A stale socket file may remain on disk after a previous daemon
// crashed (SIGKILL, panic, OOM, host reboot mid-shutdown). Without
// removing it the next bind fails with "address already in use" and
// the auto-start launcher reports "kata: daemon failed to start
// within 5s". Pre-bind probe + remove keeps the path clean while
// refusing to clobber either a healthy concurrent daemon or an
// unrelated non-socket file that happens to share the name.
//
// The probe→remove→bind sequence is serialized under an exclusive
// flock on a sibling lock file. Without that lock two concurrent
// starters can both observe the socket as stale and race os.Remove,
// so the loser unlinks the winner's freshly-bound listener — leaving
// one daemon orphaned in the kernel while another claims its path.
func (u unixEndpoint) Listen() (net.Listener, error) {
	lock, err := acquireSocketLock(u.path)
	if err != nil {
		return nil, fmt.Errorf("lock socket %s: %w", u.path, err)
	}
	defer func() { _ = lock.Close() }()

	if err := removeStaleSocketAt(u.path); err != nil {
		return nil, err
	}
	return net.Listen("unix", u.path)
}

// removeStaleSocketAt is the pre-bind hygiene step. Caller MUST hold
// acquireSocketLock for the path; without it the probe→remove window
// races a concurrent Listen.
func removeStaleSocketAt(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("listen unix %s: path exists and is not a socket", path)
	}
	alive, probeErr := isUnixSocketAlive(path)
	if alive {
		return fmt.Errorf("listen unix %s: %w", path, ErrSocketInUse)
	}
	if probeErr != nil {
		return fmt.Errorf("listen unix %s: cannot determine socket state: %w", path, probeErr)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	return nil
}

// isUnixSocketAlive returns (alive, err) where:
//
//	(true,  nil) — dial succeeded; a daemon is accepting on the path.
//	(false, nil) — kernel returned ECONNREFUSED, proof that the file
//	               exists but no process is bound to it.
//	(false, err) — any other dial error (timeout, EACCES, ENOENT race).
//	               Caller treats this as ambiguous and refuses to
//	               remove rather than risk unlinking a live socket.
//
// Dial timeouts are NOT proof of staleness: a busy daemon with a full
// accept queue or a scheduling delay can time out without being dead.
func isUnixSocketAlive(path string) (bool, error) {
	d := net.Dialer{Timeout: staleSocketProbeTimeout}
	conn, err := d.Dial("unix", path)
	if err == nil {
		_ = conn.Close()
		return true, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return false, nil
	}
	return false, err
}

// acquireSocketLock takes a blocking exclusive flock(2) on a sibling
// lock file (<path>.lock). Released when the returned *os.File is
// Closed. The lock file is intentionally never unlinked: removing it
// while a peer holds it open would leave that peer locking an inode
// no longer reachable by path, so a fresh acquirer who recreates the
// file would hold a lock on a different inode and the two processes
// would no longer serialize.
func acquireSocketLock(path string) (*os.File, error) {
	//nolint:gosec // G304: path is daemon-controlled state-dir filename
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	//nolint:gosec // G115: fd fits int on every supported OS.
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock: %w", err)
	}
	return f, nil
}
