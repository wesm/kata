package daemon

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/config"
)

const (
	authBearerPrefix     = "Bearer "
	authHeader           = "Authorization"
	pathPing             = "/api/v1/ping"
	pathHealth           = "/api/v1/health"
	pathEventsStreamPath = "/api/v1/events/stream"
)

// authPolicy is the resolved auth posture at daemon start. Token == "" disables
// bearer auth; InsecureReadonly is the dev escape hatch that allows GETs on
// non-loopback TCP without a token. Both fields are also surfaced through
// ServerConfig (see Task B3); this struct exists so the middleware itself
// does not depend on ServerConfig.
type authPolicy struct {
	Token            string
	InsecureReadonly bool
}

// requireBearer returns an HTTP middleware that enforces bearer-token auth
// per the spec matrix:
//
//	Token == "" && !InsecureReadonly  -> no-op (local-socket / loopback deployments)
//	Token == "" &&  InsecureReadonly  -> GETs pass; mutations + SSE return 401
//	Token != ""                       -> all non-health paths require Bearer == Token
//
// /api/v1/ping and /api/v1/health bypass unconditionally so health-check probes
// do not need credentials.
func requireBearer(p authPolicy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == pathPing || r.URL.Path == pathHealth {
				next.ServeHTTP(w, r)
				return
			}
			if p.Token == "" {
				if !p.InsecureReadonly {
					next.ServeHTTP(w, r)
					return
				}
				if r.Method != http.MethodGet || strings.HasPrefix(r.URL.Path, pathEventsStreamPath) {
					api.WriteEnvelope(w, http.StatusUnauthorized, "auth_required",
						"mutations and event stream require authentication; daemon is in --insecure-readonly mode")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			got := r.Header.Get(authHeader)
			if !strings.HasPrefix(got, authBearerPrefix) {
				api.WriteEnvelope(w, http.StatusUnauthorized, "auth_required",
					"Authorization: Bearer <token> required")
				return
			}
			presented := strings.TrimPrefix(got, authBearerPrefix)
			if subtle.ConstantTimeCompare([]byte(presented), []byte(p.Token)) != 1 {
				api.WriteEnvelope(w, http.StatusForbidden, "auth_invalid", "token mismatch")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// checkAuthStartup refuses startup when the listen address is non-loopback
// TCP, no token is configured, and the operator has not opted in to
// --insecure-readonly. listen uses the same convention as runDaemonWithListen:
// "" means Unix socket; "host:port" means TCP.
func checkAuthStartup(listen string, p authPolicy) error {
	if !isNonLoopbackTCP(listen) {
		return nil
	}
	if p.Token == "" && !p.InsecureReadonly {
		return fmt.Errorf("non-loopback TCP listen %q requires a token: "+
			"set KATA_AUTH_TOKEN or [auth].token in <KATA_HOME>/config.toml, "+
			"or pass --insecure-readonly to opt out (DEV ONLY)", listen)
	}
	return nil
}

// CheckAuthStartup is the exported form used by the CLI entry point.
func CheckAuthStartup(listen string, auth config.AuthConfig, insecureReadonly bool) error {
	return checkAuthStartup(listen, authPolicy{
		Token: auth.Token, InsecureReadonly: insecureReadonly,
	})
}

// isNonLoopbackTCP reports whether listen designates a TCP bind on a
// non-loopback host. Empty listen (Unix socket) and parse failures
// return false so callers default to "no extra restriction".
func isNonLoopbackTCP(listen string) bool {
	if listen == "" {
		return false
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	switch host {
	case "", "127.0.0.1", "localhost", "::1":
		return false
	default:
		return true
	}
}
