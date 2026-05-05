package daemon

import (
	"testing"

	"github.com/wesm/kata/internal/hooks"
)

// TestServerConfig_NilHooks_FillsNoop verifies NewServer substitutes a
// hooks.NewNoop() Sink when ServerConfig.Hooks is nil so handler tests
// that don't wire a dispatcher can still trigger mutations safely.
func TestServerConfig_NilHooks_FillsNoop(t *testing.T) {
	cfg := ServerConfig{Hooks: nil}
	srv := NewServer(cfg)
	t.Cleanup(func() { _ = srv.Close() })
	if srv.cfg.Hooks == nil {
		t.Fatal("Hooks should default to NewNoop, not stay nil")
	}
	if _, ok := srv.cfg.Hooks.(*hooks.Dispatcher); ok {
		t.Fatal("default Hooks should be Noop, not Dispatcher")
	}
}
