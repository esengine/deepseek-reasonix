package main

import (
	"context"
	"log/slog"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/repair"
	"reasonix/internal/stats"
)

// completeDesktopShutdown removes the lifecycle record only after every
// shutdown defer has returned normally. A panic in teardown deliberately leaves
// the shutting_down record behind for the next launch to diagnose.
func completeDesktopShutdown(tracker *desktopLifecycleTracker, body func()) {
	tracker.stopWriter()
	tracker.mark("shutting_down")
	body()
	tracker.clean()
}

func (a *App) shutdownBody() {
	// Stop the periodic snapshotter before controller teardown so its final
	// in-flight snapshot completes while controllers are still alive.
	if a.periodicSnapshot != nil {
		a.periodicSnapshot.Stop()
	}
	if a.topicState != nil {
		defer a.topicState.close()
	}
	if a.desktopShell.linuxRecovery != nil {
		a.desktopShell.linuxRecovery.stop()
	}
	if a.desktopShell.coordinator != nil {
		a.desktopShell.coordinator.stop()
	}
	if a.workspaceHub != nil {
		a.workspaceHub.close()
	}
	// A real quit terminates web windows whose tunnels die with this process.
	// Remote Serve stays resident; background tray close never reaches shutdown
	// and therefore keeps those windows alive.
	a.closeAllRemoteWindows()
	// Run after controller teardown (and after its deferred lifecycle unlocks)
	// so every accepted usage record reaches disk before a normal app exit.
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		_ = stats.Flush(flushCtx, config.StatsDir())
		_ = flushDesktopDerivedCatalogs(flushCtx)
	}()
	a.stopDeferredRebuildRetry()
	// End every takeover mirror before controller teardown releases the leases:
	// Serve's mirror-end then hands those sessions straight back to their
	// remote tabs instead of waiting out the stale-mirror timeout.
	a.stopTakeoverMirrors()
	a.stopHistoryIndexMigration()
	a.stopMainThreadWatchdog()
	if a.heartbeat != nil {
		a.heartbeat.Stop()
	}
	a.stopBotRuntime()
	a.stopRemoteRuntime()
	a.stopTray()
	// Terminal process shutdown is independent from controller teardown. Do it
	// before acquiring runtime lifecycle locks so a slow PTY cannot delay while
	// holding locks used by Wails-bound chat calls.
	if a.terminals != nil {
		a.terminals.closeAll()
	}
	// Save window geometry synchronously from Go so it's persisted even if the
	// frontend's beforeunload promise hasn't resolved yet.
	a.saveWindowStateSync()
	// Serialize shutdown with controller rebuilds and live MCP mutations. This
	// uses the same lifecycle lock order as lockMCPMutation so launch authorization
	// or reconnect cannot have its captured Host closed underneath it.
	a.runtimeRebuildMu.Lock()
	defer a.runtimeRebuildMu.Unlock()
	a.runtimeAdmissionMu.Lock()
	defer a.runtimeAdmissionMu.Unlock()
	// Close every shared plugin host before releasing the lifecycle barrier,
	// even if a tab cleanup panics.
	defer a.closeAllSharedHosts()

	a.mu.RLock()
	tabs := a.runtimeTabsLocked()
	type shutdownItem struct {
		tab      *WorkspaceTab
		ctrl     control.SessionAPI
		readOnly bool
	}
	items := make([]shutdownItem, 0, len(tabs))
	for _, t := range tabs {
		if t.Ctrl != nil {
			items = append(items, shutdownItem{tab: t, ctrl: t.Ctrl, readOnly: t.ReadOnly})
		}
	}
	a.mu.RUnlock()
	for _, it := range items {
		if !it.readOnly {
			if err := it.ctrl.SnapshotForShutdown(); err != nil {
				slog.Warn("desktop: shutdown snapshot failed", "tab", it.tab.ID, "err", err)
			}
		}
		it.ctrl.Close()
		if !a.returnTakeoverLeaseForShutdown(it.tab) {
			it.tab.releaseSessionLease()
		}
		a.mu.Lock()
		a.releaseSessionRuntimeLocked(it.tab)
		a.mu.Unlock()
	}
	// Every lease is released; taken-over sessions can go straight back to
	// their remote tabs.
	a.endTakeoverMirrors()
	if a.startupReady.Load() {
		// A stable React + Wails heartbeat is sufficient health evidence even if
		// the user closes before the delayed commit task runs.
		if err := a.commitPendingUpdateHealth(); err != nil {
			slog.Warn("desktop: commit healthy update during shutdown", "err", err)
		}
		if archived, err := archiveSupersededPendingUpdateAfterReady(); err != nil {
			slog.Warn("desktop: retire superseded update during shutdown", "err", err)
		} else if archived {
			slog.Info("desktop: archived superseded update transaction during shutdown")
		}
		// Independent last-known-good config snapshot after a successful UI session.
		_ = repair.RecordHealthyConfig(version)
	}
}
