package main

import "sync/atomic"

// browserControl is the shell-owned capability switch for the built-in browser.
// The shell persists it; this process only mirrors the value the shell pushes so
// session construction reads one source.
type browserControl struct {
	disabled atomic.Bool
}

func (b *browserControl) setEnabled(enabled bool) { b.disabled.Store(!enabled) }

func (b *browserControl) off() bool { return b.disabled.Load() }

// setBrowserControlEnabled records whether new sessions may drive the built-in
// browser. The shell owns the value and pushes it over desktop/browserControl,
// so this stays off the renderer-facing contract.
func (a *App) setBrowserControlEnabled(enabled bool) {
	a.browserControl.setEnabled(enabled)
}
