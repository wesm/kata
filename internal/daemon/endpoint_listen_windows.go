//go:build windows

package daemon

import "net"

// Listen on Windows skips the flock-protected stale-socket cleanup
// path used on Unix. Windows lacks flock(2) and the kata daemon's
// runtime-files protocol assumes a Unix-only host; this implementation
// exists only so the package keeps compiling under GOOS=windows.
func (u unixEndpoint) Listen() (net.Listener, error) {
	return net.Listen("unix", u.path)
}
