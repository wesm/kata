package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestAuthStartupGuard_NonLoopbackTCPWithoutToken_Refuses(t *testing.T) {
	err := checkAuthStartup("100.64.0.5:7777",
		authPolicy{Token: "", InsecureReadonly: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-loopback TCP")
}

func TestAuthStartupGuard_NonLoopbackTCP_InsecureReadonly_Permitted(t *testing.T) {
	err := checkAuthStartup("100.64.0.5:7777",
		authPolicy{Token: "", InsecureReadonly: true})
	require.NoError(t, err)
}

func TestAuthStartupGuard_LoopbackTCP_NoTokenOK(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		require.NoError(t, checkAuthStartup(addr,
			authPolicy{Token: "", InsecureReadonly: false}))
	}
}

func TestAuthStartupGuard_UnixSocket_NoTokenOK(t *testing.T) {
	require.NoError(t, checkAuthStartup("",
		authPolicy{Token: "", InsecureReadonly: false}))
}

func TestAuthStartupGuard_TokenConfigured_PermittedEverywhere(t *testing.T) {
	p := authPolicy{Token: "tok"}
	require.NoError(t, checkAuthStartup("", p))
	require.NoError(t, checkAuthStartup("127.0.0.1:0", p))
	require.NoError(t, checkAuthStartup("100.64.0.5:7777", p))
}

func TestServerConfig_AuthPolicyThreaded(t *testing.T) {
	cfg := ServerConfig{
		Auth:             config.AuthConfig{Token: "tok-123"},
		InsecureReadonly: false,
	}
	got := cfg.authPolicy()
	assert.Equal(t, "tok-123", got.Token)
	assert.False(t, got.InsecureReadonly)
}
