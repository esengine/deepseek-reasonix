package agent

import (
	"strings"
	"sync/atomic"
)

// The session-scoped effort override lets a frontend switch the reasoning
// depth without rebuilding the runtime: the override rides the per-request
// channel (provider.Request.EffortOverride), and adapters apply it only when
// the endpoint's effort vocabulary accepts it — see requestEffort in the
// openai adapter and the PerRequestEfforts probe it exposes.

// effortVarying is implemented by providers whose effort vocabulary is
// request-scoped. Optional on purpose: providers that cannot vary depth per
// call keep the boot-time rebuild path in the frontends.
type effortVarying interface {
	PerRequestEfforts() []string
}

// sessionEffortOverride holds a session-scoped reasoning-depth override —
// empty when the configured depth stands. atomic.Value follows the
// responseLanguage/reasoningLanguage precedent: written rarely (an explicit
// switch), read on every request.
type sessionEffortOverride struct{ atomic.Value }

// SetSessionEffortOverride stores a session-scoped effort override and
// reports whether the running provider honors per-request depth. An empty
// level clears the override and always succeeds. A non-empty level is
// accepted only when the provider lists it in its per-request vocabulary;
// returning false tells the caller to fall back to the rebuild path instead
// of writing an override that would silently degrade to the configured depth
// on the wire.
func (a *Agent) SetSessionEffortOverride(level string) bool {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		a.sessionEffort.Store("")
		return true
	}
	// A recovery-forked session keeps its reanchor semantics on the rebuild
	// path (forked file sealed, in-memory history anchored to a fresh branch):
	// an override that skips the rebuild would leave the forked file in active
	// use. Defer such sessions to the caller's fallback.
	if path := strings.TrimSpace(a.sess.path); path != "" {
		if meta, ok, err := LoadBranchMeta(path); err == nil && ok && meta.Recovered {
			return false
		}
	}
	varying, ok := a.svc.prov.(effortVarying)
	if !ok {
		return false
	}
	for _, l := range varying.PerRequestEfforts() {
		if strings.EqualFold(strings.TrimSpace(l), level) {
			a.sessionEffort.Store(level)
			return true
		}
	}
	return false
}

// sessionEffortOverrideValue returns the stored override; empty when unset,
// which lets the configured depth stand.
func (a *Agent) sessionEffortOverrideValue() string {
	if v, ok := a.sessionEffort.Load().(string); ok {
		return v
	}
	return ""
}

// effortOverrideForRequest picks the request-scoped depth: the governor's
// engaged override wins — a running guard must not be outbid by a
// session-level depth bump — then the session override, then the configured
// depth (empty).
func (a *Agent) effortOverrideForRequest() string {
	if gov := a.governorOverride(); gov != "" {
		return gov
	}
	return a.sessionEffortOverrideValue()
}
