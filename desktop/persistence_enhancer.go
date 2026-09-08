// Periodic snapshot engine — scheduled full-session persistence that complements
// the existing TurnDone-driven autosave.
//
// Reasonix currently persists sessions only when a turn completes (event.TurnDone
// → scheduleTabSnapshot). A long in-flight turn, a crash before TurnDone, or a
// version-update migration can therefore lose data that never reached disk. This
// engine adds a safety-net ticker that calls the existing snapshotAllTabs() on a
// configurable interval, so a force-kill loses at most interval seconds of work
// instead of an entire in-flight turn.
//
// Design goals:
//   - Zero behaviour change by default. The engine is opt-in via
//     REASONIX_ENABLE_PERIODIC_SNAPSHOT=1 so existing users, tests, and
//     cache-sensitive paths are unaffected.
//   - No new persistence mechanism. It reuses snapshotAllTabs() / snapshotTab(),
//     the same code path shutdown and tab-switch already rely on.
//   - No config-system changes. Interval is read from an env var so the feature
//     can ship without touching the TOML schema, migration, or UI.
//   - Deterministic shutdown. Stop() blocks until the in-flight tick (if any)
//     finishes, so close-time ordering with controller teardown stays well-defined.

package main

import (
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	// periodicSnapshotEnvEnable gates the engine. Empty or "0" / "false" / "off"
	// keeps it disabled, matching the historical behaviour.
	periodicSnapshotEnvEnable = "REASONIX_ENABLE_PERIODIC_SNAPSHOT"
	// periodicSnapshotEnvInterval overrides the default tick interval in seconds.
	periodicSnapshotEnvInterval = "REASONIX_PERIODIC_SNAPSHOT_INTERVAL"
	// periodicSnapshotDefaultInterval is the tick used when the env var is absent
	// or unparseable. 30s is a conservative balance: long enough to avoid visible
	// disk thrash on large transcripts, short enough that a forced kill loses at
	// most half a minute of in-flight work.
	periodicSnapshotDefaultInterval = 30 * time.Second
	// periodicSnapshotMinInterval guards against pathological configs that would
	// snapshot on every few hundred milliseconds and starve the UI goroutine.
	periodicSnapshotMinInterval = 5 * time.Second
)

// periodicSnapshotter drives scheduled full-session snapshots.
//
// The zero value is not usable; construct with newPeriodicSnapshotter. Start()
// and Stop() are idempotent and safe to call from any goroutine.
type periodicSnapshotter struct {
	app      *App
	interval time.Duration

	mu      sync.Mutex
	stop    chan struct{}
	stopped chan struct{}
	running bool
}

// newPeriodicSnapshotter builds a snapshotter for the given app. It does not start
// the background goroutine; call Start().
//
// If the feature is disabled by environment, interval is zero and Start() is a
// no-op so callers can unconditionally wire it into app lifecycle without a
// feature flag at every call site.
func newPeriodicSnapshotter(app *App) *periodicSnapshotter {
	return &periodicSnapshotter{
		app:      app,
		interval: resolvePeriodicSnapshotInterval(),
	}
}

// enabled reports whether the engine should run. It is derived once at construction
// time so toggling the env var after startup does not race with the ticker.
func (p *periodicSnapshotter) enabled() bool {
	return p.interval > 0
}

// Start launches the background ticker. It is a no-op when the feature is disabled
// or already running. Safe to call multiple times.
func (p *periodicSnapshotter) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.enabled() || p.running {
		return
	}

	p.running = true
	p.stop = make(chan struct{})
	p.stopped = make(chan struct{})

	go p.loop()

	slog.Info("desktop: periodic session snapshot enabled",
		"interval", p.interval.String())
}

// Stop shuts down the ticker and waits for any in-flight snapshot to finish. It is
// a no-op when the feature is disabled or not running. Safe to call multiple times.
//
// Callers should invoke Stop() before controller teardown in shutdownBody so the
// final snapshot completes while controllers are still alive.
func (p *periodicSnapshotter) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	close(p.stop)
	stopped := p.stopped
	p.mu.Unlock()

	<-stopped
}

// loop is the background goroutine. It ticks at the configured interval and calls
// snapshotAllTabs. A long snapshot does not pile up: the ticker fires while a
// snapshot is in flight, the in-flight call is allowed to finish, and the next
// tick starts a fresh one — the single-flight nature of snapshotTab (via
// tab.saveMu) prevents concurrent writes to the same session file.
func (p *periodicSnapshotter) loop() {
	defer close(p.stopped)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.snapshotOnce()
		}
	}
}

// snapshotOnce runs one full snapshot cycle. Recovered to a warn log so a panic in
// one tick cannot kill the background goroutine (the next tick would then never
// fire).
func (p *periodicSnapshotter) snapshotOnce() {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("desktop: periodic snapshot panicked", "panic", r)
		}
	}()

	p.app.snapshotAllTabs()
}

// resolvePeriodicSnapshotInterval reads the interval from the environment. Returns
// 0 when the feature is disabled, otherwise a duration clamped to
// [periodicSnapshotMinInterval, +∞).
func resolvePeriodicSnapshotInterval() time.Duration {
	if !periodicSnapshotEnabledFromEnv() {
		return 0
	}

	interval := periodicSnapshotDefaultInterval
	if raw := os.Getenv(periodicSnapshotEnvInterval); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			interval = time.Duration(seconds) * time.Second
		} else {
			slog.Warn("desktop: invalid periodic snapshot interval, using default",
				"raw", raw, "default", periodicSnapshotDefaultInterval.String())
		}
	}

	if interval < periodicSnapshotMinInterval {
		slog.Warn("desktop: periodic snapshot interval below minimum, clamped",
			"requested", interval.String(), "min", periodicSnapshotMinInterval.String())
		interval = periodicSnapshotMinInterval
	}

	return interval
}

// periodicSnapshotEnabledFromEnv parses the enable flag. Accepts "1", "true",
// "yes", "on" (case-insensitive) as enabled; everything else (including absent)
// is disabled.
func periodicSnapshotEnabledFromEnv() bool {
	raw := os.Getenv(periodicSnapshotEnvEnable)
	switch raw {
	case "1", "true", "TRUE", "True", "yes", "YES", "Yes", "on", "ON", "On":
		return true
	default:
		return false
	}
}
