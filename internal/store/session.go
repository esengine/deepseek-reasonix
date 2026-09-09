// Package store is the single authority for reasonix's on-disk persistence
// layout. Nothing else should construct a persistence path by hand.
//
// This first slice owns the session-artifact sidecars — the files and
// directories that live beside a session's .jsonl (branch metadata, goal state,
// checkpoints, background-job artifacts, the cleanup-pending marker). They were
// previously derived independently in internal/agent, internal/jobs,
// internal/control and internal/acp, each re-spelling the suffix convention; a
// layout change meant hunting across packages. Centralizing them here makes
// store the one place that knows where a session's artifacts go.
//
// store is a leaf: it imports only the standard library, so any package may
// depend on it without risking an import cycle. Root/directory resolution (the
// ~/.reasonix tree) and the desktop root unification land in later slices.
package store

import "strings"

// IsSessionTranscriptName reports whether name is a primary session transcript
// file. Append-only event logs and guardian sidecars also end in .jsonl, so
// callers that discover sessions by directory scan must use this helper instead
// of filepath.Ext.
func IsSessionTranscriptName(name string) bool {
	name = strings.TrimSpace(name)
	return strings.HasSuffix(name, ".jsonl") &&
		!strings.HasSuffix(name, ".events.jsonl") &&
		!strings.HasSuffix(name, ".turns.jsonl") &&
		!strings.HasSuffix(name, ".conflicts.jsonl") &&
		!strings.HasSuffix(name, ".guardian.jsonl")
}

// SessionRecoveryState is the persisted Auto-mode recovery checkpoint state
// (<id>.recovery.json). It is a regular session-owned sidecar, not a transcript.
func SessionRecoveryState(sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".recovery.json"
}

// SessionTranscriptProjection stores display-only records and their exact
// committed event coverage; it never supplies provider-visible messages.
func SessionTranscriptProjection(sessionPath string) string {
	if strings.TrimSpace(sessionPath) == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".transcript-projection.json"
}

// SessionContext is the context-projection / compaction-state sidecar
// (<id>.context.json). It holds the model-visible projection and cache
// telemetry; transcript authority remains with the native event log once one
// exists, with the primary .jsonl retained as its compatibility checkpoint.
func SessionContext(sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".context.json"
}

// SessionPinnedContext is the optional desktop pinned-workspace-context
// sidecar (<id>.pinned-context.json). Older versions ignore it while keeping
// the primary transcript fully readable.
func SessionPinnedContext(sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".pinned-context.json"
}

// sessionStem strips the .jsonl suffix so a sidecar sits beside the session as
// <id>.<kind> rather than <id>.jsonl.<kind>.
func sessionStem(sessionPath string) string {
	return strings.TrimSuffix(sessionPath, ".jsonl")
}

// SessionMeta is the branch-metadata sidecar. Unlike the other sidecars it
// appends to the full session path (historical layout), so session.jsonl yields
// session.jsonl.meta.
func SessionMeta(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionPath + ".meta"
}

// SessionGoalState is the persisted active-goal sidecar (<id>.goal-state.json).
func SessionGoalState(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".goal-state.json"
}

// SessionEventLog is the append-only transcript event log (<id>.events.jsonl).
func SessionEventLog(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".events.jsonl"
}

// SessionEventLogDamaged is the salvage sidecar for event-log bytes that tail
// repair would otherwise discard (<id>.events.jsonl.damaged). It must NOT end
// in .jsonl: older binaries scanning a shared session directory classify any
// non-excluded .jsonl file as a primary transcript and would resurrect the
// damaged bytes as a phantom session.
func SessionEventLogDamaged(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return SessionEventLog(sessionPath) + ".damaged"
}

// SessionEventLogRotating marks a schema-2 log whose rotation is between
// reading the old file and publishing the new one; unlocked appenders wait
// for it to clear before trusting that their bytes reached the current log.
func SessionEventLogRotating(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return SessionEventLog(sessionPath) + ".rotating"
}

// SessionTurnEventLog is the append-only local runtime lifecycle ledger
// (<id>.turns.jsonl). It is independent from the provider transcript so old
// readers can continue to consume the primary session unchanged.
func SessionTurnEventLog(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".turns.jsonl"
}

// SessionTurnEventLogDamaged preserves a corrupt/torn ledger tail before the
// valid prefix is truncated back into service.
func SessionTurnEventLogDamaged(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return SessionTurnEventLog(sessionPath) + ".damaged"
}

// SessionEventIndex is the listing/checkpoint index for the event log
// (<id>.event-index.json). It contains derived offsets and digests, not the
// transcript body.
func SessionEventIndex(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".event-index.json"
}

// SessionDisplayIndex is the paging sidecar for the transcript
// (<id>.display-index.json). It contains per-message byte offsets, roles, and
// turn boundaries derived from the transcript, never message bodies, so a
// reader can page a huge history without parsing whole session files.
func SessionDisplayIndex(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".display-index.json"
}

// SessionConflictLog is the append-only diagnostic log for snapshot conflict
// recoveries (<id>.conflicts.jsonl). It contains revision counters and branch
// ids, not transcript content.
func SessionConflictLog(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".conflicts.jsonl"
}

// SessionLockFile is the advisory save lock (<id>.jsonl.lock).
func SessionLockFile(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionPath + ".lock"
}

// SessionLeaseLock is the runtime ownership lock (<id>.jsonl.lease.lock).
func SessionLeaseLock(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionPath + ".lease.lock"
}

// SessionLeaseInfo is the runtime ownership metadata
// (<id>.jsonl.lease.json).
func SessionLeaseInfo(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionPath + ".lease.json"
}

// SessionCheckpointDir is the snapshot-checkpoint directory (<id>.ckpt).
func SessionCheckpointDir(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".ckpt"
}

// SessionJobsDir is the background-job artifact directory (<id>.jobs).
func SessionJobsDir(sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".jobs"
}

// SessionInboxDir is the durable session-level instruction inbox
// (<id>.inbox/). Manifest metadata and frozen prompt blobs live here.
func SessionInboxDir(sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".inbox"
}

// SessionCleanupPending is the delayed-cleanup marker (<id>.cleanup-pending.json).
func SessionCleanupPending(sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	return sessionStem(sessionPath) + ".cleanup-pending.json"
}

// SessionSidecarFiles returns every regular-file sidecar owned by a session
// transcript: branch meta, goal state, event/index logs, pinned context, and
// diagnostic logs.
// Every surface that deletes a session (desktop trash, /clear, serve, ACP)
// must remove all of these — the event log is the authoritative transcript, so
// leaving it behind both leaks the "deleted" conversation and lets LoadSession
// resurrect it. Directory artifacts (checkpoints, jobs) and ephemeral
// lock/lease files have their own lifecycles and are intentionally not listed.
func SessionSidecarFiles(sessionPath string) []string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return nil
	}
	return []string{
		SessionMeta(sessionPath),
		SessionGoalState(sessionPath),
		SessionEventLog(sessionPath),
		SessionEventLogDamaged(sessionPath),
		SessionEventLogRotating(sessionPath),
		SessionTurnEventLog(sessionPath),
		SessionTurnEventLogDamaged(sessionPath),
		SessionEventIndex(sessionPath),
		SessionDisplayIndex(sessionPath),
		SessionTranscriptProjection(sessionPath),
		SessionConflictLog(sessionPath),
		SessionRecoveryState(sessionPath),
		SessionContext(sessionPath),
		SessionPinnedContext(sessionPath),
	}
}
