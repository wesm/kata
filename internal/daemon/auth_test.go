package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAuthMiddleware_NoTokenConfigured_AllRequestsPass(t *testing.T) {
	mw := requireBearer(authPolicy{Token: "", InsecureReadonly: false})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_TokenConfigured_MissingHeader_401(t *testing.T) {
	mw := requireBearer(authPolicy{Token: "expected-token"})
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), `"auth_required"`)
}

func TestAuthMiddleware_TokenConfigured_WrongToken_403(t *testing.T) {
	mw := requireBearer(authPolicy{Token: "expected-token"})
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAuthMiddleware_TokenConfigured_CorrectToken_OK(t *testing.T) {
	mw := requireBearer(authPolicy{Token: "expected-token"})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_InsecureReadonly_GETPasses_POSTAndSSERejected(t *testing.T) {
	mw := requireBearer(authPolicy{Token: "", InsecureReadonly: true})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	assert.Equal(t, http.StatusOK, rr.Code)

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil))
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	req.Header.Set("Accept", "text/event-stream")
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthMiddleware_UnauthenticatedPathsAlwaysPass(t *testing.T) {
	mw := requireBearer(authPolicy{Token: "expected-token"})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, p := range []string{"/api/v1/ping", "/api/v1/health"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		assert.Equal(t, http.StatusOK, rr.Code, "unauthenticated path %s should pass", p)
	}
}
