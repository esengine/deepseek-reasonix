package main

import (
	"errors"
	"log/slog"
	"slices"

	"reasonix/internal/agent"
	"reasonix/internal/control"
)

type topicArchiveRemovalOwnership struct {
	guard      *agent.SessionRemovalGuard
	restoreTab *WorkspaceTab
}

type topicArchiveOwnershipBatch struct {
	entries []*topicArchiveRemovalOwnership
	byPath  map[string]*topicArchiveRemovalOwnership
}

func takeTopicArchiveSessionLease(tab *WorkspaceTab, sessionPath string) *agent.SessionLease {
	if tab == nil {
		return nil
	}
	key := sessionRuntimeKey(sessionPath)
	tab.sessionLeaseMu.Lock()
	defer tab.sessionLeaseMu.Unlock()
	lease := tab.sessionLease
	if lease == nil || sessionRuntimeKey(lease.Path()) != key {
		return nil
	}
	tab.sessionLease = nil
	tab.storeSessionLeaseRuntimeKey("")
	return lease
}

func topicArchiveLeaseOwners(removed []removedSessionRuntime) map[string]*WorkspaceTab {
	owners := make(map[string]*WorkspaceTab, len(removed))
	for _, item := range removed {
		if item.tab == nil || item.readOnly || item.sessionPath == "" {
			continue
		}
		key := sessionRuntimeKey(item.sessionPath)
		if key != "" && item.tab.sessionLeaseRuntimeKey() == key {
			owners[key] = item.tab
		}
	}
	return owners
}

func acquireTopicArchiveOwnership(targets []topicTrashTarget, removed []removedSessionRuntime) (*topicArchiveOwnershipBatch, error) {
	batch := &topicArchiveOwnershipBatch{byPath: make(map[string]*topicArchiveRemovalOwnership, len(targets))}
	localOwners := topicArchiveLeaseOwners(removed)
	// Acquire historical/non-runtime sessions first. If another process owns
	// any target, the archive aborts before a local runtime lease is touched.
	for _, target := range targets {
		key := sessionRuntimeKey(target.sessionPath)
		if localOwners[key] != nil {
			continue
		}
		guard, err := acquireSessionRemovalGuard(target.sessionPath)
		if err != nil {
			batch.release()
			return nil, err
		}
		batch.add(target, guard, nil)
	}
	// Convert local runtime leases without releasing the lease lock. Converted
	// guards can be restored if marker publication or generation validation
	// fails before detach commits.
	for _, target := range targets {
		key := sessionRuntimeKey(target.sessionPath)
		tab := localOwners[key]
		if tab == nil {
			continue
		}
		lease := takeTopicArchiveSessionLease(tab, target.sessionPath)
		if lease == nil {
			// No lease available (e.g. tab in sessionRuntimeFailed after
			// a failed restore). Fall back to non-runtime guard acquisition
			// so the archive can proceed without a live lease.
			guard, err := acquireSessionRemovalGuard(target.sessionPath)
			if err != nil {
				batch.rollback()
				return nil, err
			}
			batch.add(target, guard, nil)
			continue
		}
		guard, err := lease.TryConvertToRemovalGuard()
		if err != nil {
			// Lease conversion failed (stale authority, mismatched generation,
			// etc.). Release the lease and fall back to a non-runtime guard
			// instead of aborting the entire archive.
			tab.adoptSessionLease(lease)
			if errors.Is(err, agent.ErrSessionLeaseHeld) {
				batch.rollback()
				return nil, errSessionBusyElsewhere
			}
			slog.Warn("desktop: topic archive lease conversion failed, falling back to non-runtime guard",
				"session", target.sessionPath, "err", err)
			guard2, err2 := acquireSessionRemovalGuard(target.sessionPath)
			if err2 != nil {
				batch.rollback()
				return nil, err2
			}
			batch.add(target, guard2, nil)
			continue
		}
		batch.add(target, guard, tab)
	}
	return batch, nil
}

func (b *topicArchiveOwnershipBatch) add(target topicTrashTarget, guard *agent.SessionRemovalGuard, tab *WorkspaceTab) {
	entry := &topicArchiveRemovalOwnership{guard: guard, restoreTab: tab}
	b.entries = append(b.entries, entry)
	b.byPath[sessionRuntimeKey(target.sessionPath)] = entry
}

func (b *topicArchiveOwnershipBatch) take(sessionPath string) *agent.SessionRemovalGuard {
	if b == nil {
		return nil
	}
	entry := b.byPath[sessionRuntimeKey(sessionPath)]
	if entry == nil {
		return nil
	}
	guard := entry.guard
	entry.guard = nil
	return guard
}

func (b *topicArchiveOwnershipBatch) rollback() {
	if b == nil {
		return
	}
	for _, entry := range slices.Backward(b.entries) {
		if entry.guard == nil {
			continue
		}
		if entry.restoreTab != nil {
			lease, err := entry.guard.RestoreSessionLease()
			if err != nil {
				slog.Warn("desktop: restoring topic archive session ownership failed")
				entry.guard.Release()
			} else {
				entry.restoreTab.adoptSessionLease(lease)
			}
		} else {
			entry.guard.Release()
		}
		entry.guard = nil
	}
}

func (b *topicArchiveOwnershipBatch) release() {
	if b == nil {
		return
	}
	for _, entry := range b.entries {
		if entry.guard != nil {
			entry.guard.Release()
			entry.guard = nil
		}
	}
}

func delayedDesktopTopicTrash(dir, sessionPath, key string, guard *agent.SessionRemovalGuard, destroys []control.SessionDestroyHandle) {
	waitAllDestroyHandles(destroys)
	if err := trashSessionArtifactsWithGuard(dir, sessionPath, key, guard); err != nil {
		slog.Warn("desktop: delayed topic archive cleanup remains pending")
	}
	finishDestroyHandles(destroys)
}
