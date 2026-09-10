package hostrpc

import (
	"context"
	"testing"

	"reasonix/internal/extension/rpcwire"
)

func TestBrowserControlHookReceivesEnabledFlag(t *testing.T) {
	var seen []bool
	h := newHarness(t, &fixtureTarget{}, Hooks{
		BrowserControl: func(_ context.Context, enabled bool) error {
			seen = append(seen, enabled)
			return nil
		},
	})
	h.mustHello()
	for _, enabled := range []bool{false, true} {
		var empty map[string]any
		if err := h.call("desktop/browserControl", map[string]bool{"enabled": enabled}, &empty); err != nil || len(empty) != 0 {
			t.Fatalf("browserControl enabled=%v = %v, %v", enabled, empty, err)
		}
	}
	if len(seen) != 2 || seen[0] || !seen[1] {
		t.Fatalf("hook saw %v, want [false true]", seen)
	}
}

func TestBrowserControlRequiresEnabledFlag(t *testing.T) {
	h := newHarness(t, &fixtureTarget{}, Hooks{BrowserControl: func(context.Context, bool) error {
		t.Error("hook must not run without the flag")
		return nil
	}})
	h.mustHello()
	assertCode(t, h.call("desktop/browserControl", map[string]any{}, nil), rpcwire.ErrInvalidParams, "")
}
