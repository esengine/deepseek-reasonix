// Tests for the periodic session snapshot engine.
//
// These tests cover env-var parsing, interval clamping, and the Start/Stop
// lifecycle. They do not assert on real snapshot timing — the engine reuses
// snapshotAllTabs(), whose behaviour is covered by app_autosave_test.go.

package main

import (
	"os"
	"testing"
	"time"
)

// ── Enable-flag parsing ────────────────────────────────────────────────────

func TestPeriodicSnapshotEnabledFromEnv(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"absent", "", false},
		{"zero", "0", false},
		{"false", "false", false},
		{"FALSE", "FALSE", false},
		{"off", "off", false},
		{"no", "no", false},
		{"one", "1", true},
		{"true", "true", true},
		{"TRUE", "TRUE", true},
		{"True", "True", true},
		{"yes", "yes", true},
		{"on", "on", true},
		{"ON", "ON", true},
		{"garbage", "maybe", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value == "" {
				os.Unsetenv(periodicSnapshotEnvEnable)
			} else {
				t.Setenv(periodicSnapshotEnvEnable, tc.value)
			}
			if got := periodicSnapshotEnabledFromEnv(); got != tc.want {
				t.Fatalf("enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── Interval resolution ─────────────────────────────────────────────────────

func TestResolvePeriodicSnapshotIntervalDisabled(t *testing.T) {
	os.Unsetenv(periodicSnapshotEnvEnable)
	os.Unsetenv(periodicSnapshotEnvInterval)
	if got := resolvePeriodicSnapshotInterval(); got != 0 {
		t.Fatalf("interval = %v when disabled, want 0", got)
	}
}

func TestResolvePeriodicSnapshotIntervalDefault(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	os.Unsetenv(periodicSnapshotEnvInterval)
	if got := resolvePeriodicSnapshotInterval(); got != periodicSnapshotDefaultInterval {
		t.Fatalf("interval = %v, want default %v", got, periodicSnapshotDefaultInterval)
	}
}

func TestResolvePeriodicSnapshotIntervalCustom(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	t.Setenv(periodicSnapshotEnvInterval, "120")
	want := 120 * time.Second
	if got := resolvePeriodicSnapshotInterval(); got != want {
		t.Fatalf("interval = %v, want %v", got, want)
	}
}

func TestResolvePeriodicSnapshotIntervalClampedToMinimum(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	t.Setenv(periodicSnapshotEnvInterval, "1")
	if got := resolvePeriodicSnapshotInterval(); got != periodicSnapshotMinInterval {
		t.Fatalf("interval = %v, want clamped minimum %v", got, periodicSnapshotMinInterval)
	}
}

func TestResolvePeriodicSnapshotIntervalInvalidFallsBackToDefault(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	t.Setenv(periodicSnapshotEnvInterval, "not-a-number")
	if got := resolvePeriodicSnapshotInterval(); got != periodicSnapshotDefaultInterval {
		t.Fatalf("interval = %v after invalid value, want default %v", got, periodicSnapshotDefaultInterval)
	}
}

func TestResolvePeriodicSnapshotIntervalZeroFallsBackToDefault(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	t.Setenv(periodicSnapshotEnvInterval, "0")
	if got := resolvePeriodicSnapshotInterval(); got != periodicSnapshotDefaultInterval {
		t.Fatalf("interval = %v after zero value, want default %v", got, periodicSnapshotDefaultInterval)
	}
}

// ── Lifecycle ───────────────────────────────────────────────────────────────

func TestPeriodicSnapshotterDisabledStartIsNoOp(t *testing.T) {
	os.Unsetenv(periodicSnapshotEnvEnable)
	os.Unsetenv(periodicSnapshotEnvInterval)

	a := &App{}
	p := newPeriodicSnapshotter(a)
	if p.enabled() {
		t.Fatal("snapshotter should be disabled by default")
	}

	// Start must not launch a goroutine or panic.
	p.Start()
	p.Start() // idempotent

	// Stop must be safe to call on a never-started engine.
	p.Stop()
	p.Stop() // idempotent
}

func TestPeriodicSnapshotterEnabledStartStop(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	// Use a short interval so the ticker fires at least once, but not so short
	// that it thrashes. The test only asserts lifecycle correctness, not timing.
	t.Setenv(periodicSnapshotEnvInterval, "5")

	a := &App{
		tabs: map[string]*WorkspaceTab{},
	}
	p := newPeriodicSnapshotter(a)
	if !p.enabled() {
		t.Fatal("snapshotter should be enabled")
	}

	p.Start()
	p.Start() // idempotent — must not spawn a second goroutine

	// Let the ticker fire at least once.
	time.Sleep(100 * time.Millisecond)

	// Stop blocks until the in-flight tick (if any) finishes.
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s — ticker goroutine leaked")
	}

	// Second Stop is a no-op and must not block.
	p.Stop()
}

func TestPeriodicSnapshotterStopBeforeStartIsSafe(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	t.Setenv(periodicSnapshotEnvInterval, "30")

	a := &App{}
	p := newPeriodicSnapshotter(a)

	// Stop before Start must not panic or block.
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Stop before Start blocked")
	}
}

// ── Construction ────────────────────────────────────────────────────────────

func TestNewPeriodicSnapshotterReadsEnvOnce(t *testing.T) {
	t.Setenv(periodicSnapshotEnvEnable, "1")
	t.Setenv(periodicSnapshotEnvInterval, "45")

	a := &App{}
	p := newPeriodicSnapshotter(a)

	want := 45 * time.Second
	if p.interval != want {
		t.Fatalf("interval = %v, want %v", p.interval, want)
	}

	// Changing the env after construction must not affect the running interval —
	// the snapshotter reads env once at construction time to avoid races with the
	// ticker.
	t.Setenv(periodicSnapshotEnvInterval, "999")
	if p.interval != want {
		t.Fatalf("interval changed after construction: %v, want %v", p.interval, want)
	}
}
