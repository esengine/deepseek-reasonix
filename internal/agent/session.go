// Package agent wires a Provider, a tool Registry, and a Session into the
// harness loop that drives a coding task to completion.
package agent

import (
	"bytes"
	"slices"
	"strings"
	"sync"

	"reasonix/internal/provider"
)

// Session holds the conversation history for one task. The run loop (one turn at
// a time) is the only writer, but a frontend can read History/Save from another
// goroutine while a turn appends, so mu guards Messages. Direct Messages reads on
// the run-loop goroutine stay lock-free (serial with its own writes); cross-
// goroutine access goes through Snapshot.
type Session struct {
	cacheSessionID string // ephemeral transport identity; never model-visible or persisted
	mu             sync.RWMutex
	Messages       []provider.Message
	version        uint64
	rewriteVersion int // bumped each time the log is rewritten (compact/fold)
	// persistedRewriteVersion is the highest rewriteVersion whose transcript
	// has fully reached disk. It lives on the Session — not on the controller
	// — so swapping session objects can never orphan or misattribute the
	// baseline: NeedsRewriteSave always compares a session against its own
	// save history. Save paths advance it under s.mu with the rewriteVersion
	// captured alongside the message snapshot, never a re-read one, so a
	// compaction landing mid-save stays unpersisted.
	persistedRewriteVersion int
	persisted               sessionPersistState
	// normalizedDirty is set when LoadSession repaired the history on the way in
	// (empty tool-call names, dangling calls, truncated args, …). The repair
	// already lives in Messages, so the next Save persists it automatically as
	// part of the usual full rewrite; the flag exists for observability and to
	// let callers opt out of work that a dirty session would make redundant.
	normalizedDirty bool
	// eventLogDamaged is set when LoadSession found the on-disk event log torn
	// or corrupt and returned the replayable prefix (or the .jsonl checkpoint).
	// The next save heals the log with a rewrite-and-compact.
	eventLogDamaged bool
	// rawMessages preserves the pre-normalization transcript when the load-time
	// repairs changed it (normalizedDirty). It is only meaningful on a freshly
	// loaded Session: checkSnapshotWrite compares a pending snapshot against
	// what is actually on disk, and the repaired view no longer represents
	// those bytes — a session that kept running extends the raw transcript.
	rawMessages []provider.Message
	// pendingContentReasons accumulates a reason string each time Rewrite()
	// actually replaces provider-visible message bytes (compact, prune/snip,
	// summarize, rewind, guardian merge). ReplaceLocalMetadata bumps
	// rewriteVersion for the same save-path (NeedsRewriteSave) purpose without
	// appending here, because ModelMessages strips or never serializes the
	// local-only metadata it changes — so that path must never report a
	// cache-prefix change. DrainContentRewriteReasons (run_loop.go, once per
	// provider request) is the sole consumer.
	pendingContentReasons []string
	// persistObserver receives non-blocking post-commit projection hints. It is
	// deliberately session-local so multiple runtimes cannot steal each other's
	// observer registration.
	persistObserver SessionPersistObserver
	// writeAuth is the generation-bound write permit for this session's path.
	// Controllers bind it after acquiring a SessionLease; save/ownership paths
	// consult it instead of a process-level "I hold a lease" boolean.
	writeAuth *SessionWriteAuthority
	// authRequired becomes true once any authority has been bound. From then
	// on, saves fail closed without a live authority rather than forking
	// recovery under a stale controller.
	authRequired bool
	// persistedMessages is the last paired on-disk view for persistedViewPath.
	persistedMessages []provider.Message
	// persistedViewPath is empty when the persist baseline has no paired view.
	persistedViewPath string
	// recoveryLane is a session-instance identity, allocated lazily on the
	// first true conflict. It bounds repeated saves by this live controller to
	// one recovery file without letting a replacement controller overwrite it.
	recoveryLane string
	head         sessionHeadState
}

// NewSession initializes a session with an optional system prompt.
func NewSession(system string) *Session {
	s := &Session{}
	if system != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleSystem, Content: system, ID: NewMessageID()})
	}
	return s
}

// Add appends a message.
func (s *Session) Add(m provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = NewMessageID()
	}
	s.expireProtocolRecoveryLocked([]provider.Message{m})
	s.Messages = append(s.Messages, m)
	s.version++
}

// AddBatch appends one logical transcript batch under a single lock. Turn
// admission uses it for an optional host context revision plus the real user
// message so autosave can never observe only half of the admitted boundary.
func (s *Session) AddBatch(messages ...provider.Message) {
	if s == nil || len(messages) == 0 {
		return
	}
	s.mu.Lock()
	mintMessageIDs(messages)
	s.expireProtocolRecoveryLocked(messages)
	s.Messages = append(s.Messages, messages...)
	s.version++
	s.mu.Unlock()
}

// SetLeadingSystemPrompt updates or sets the leading system prompt message.
func (s *Session) SetLeadingSystemPrompt(prompt string) {
	s.SetLeadingSystemPromptWithReason(prompt, "system_prompt_refresh")
}

// SetLeadingSystemPromptWithReason refreshes the authoritative system prompt
// and records the provider-visible rewrite boundary exactly once. It is used
// for low-frequency host prompt migrations, never ordinary pinned-context
// updates (which append user-role revisions instead).
func (s *Session) SetLeadingSystemPromptWithReason(prompt, reason string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Messages) > 0 && s.Messages[0].Role == provider.RoleSystem {
		if s.Messages[0].Content == prompt {
			return false
		}
		s.Messages[0].Content = prompt
	} else if prompt != "" {
		s.Messages = append([]provider.Message{{Role: provider.RoleSystem, Content: prompt, ID: NewMessageID()}}, s.Messages...)
	} else {
		return false
	}
	s.rewriteVersion++
	if reason = strings.TrimSpace(reason); reason != "" {
		s.pendingContentReasons = append(s.pendingContentReasons, reason)
	}
	s.version++
	return true
}

// ConsumeFinalReadinessRecovery marks the newest pending readiness checkpoint
// consumed before any next user turn (explicit recovery or ordinary follow-up).
// This is local metadata only, so the rewrite does not alter provider bytes or
// prompt-cache identity.
func (s *Session) ConsumeFinalReadinessRecovery() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range slices.Backward(s.Messages) {
		message := &s.Messages[i]
		if message.LocalOnly && message.FinalReadinessRecovery != nil && message.FinalReadinessRecovery.Pending {
			consumed := *message.FinalReadinessRecovery
			consumed.Pending = false
			consumed.Missing = append([]string(nil), consumed.Missing...)
			consumed.Checkpoint = append([]byte(nil), consumed.Checkpoint...)
			message.FinalReadinessRecovery = &consumed
			s.rewriteVersion++
			s.version++
			return true
		}
		if IsUserAuthoredTurnMessage(*message) {
			return false
		}
	}
	return false
}

// AddDecisionReceipt persists local decision metadata without inserting a
// standalone message into the current tool turn. Tool results must remain
// directly adjacent to the assistant message that requested them; otherwise
// session normalization fabricates interrupted placeholders and older readers
// can lose the real result. Attaching to the newest assistant message keeps the
// provider-visible transcript byte-for-byte equivalent after ModelMessages.
//
// The fallback sentinel covers host decisions made before any assistant message
// exists. Older readers already discard this unmatched tool record safely.
func (s *Session) AddDecisionReceipt(receipt *provider.DecisionReceipt) {
	if s == nil || receipt == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//nolint:modernize // slices.Backward yields element copies; this body writes through the index.
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if IsUserAuthoredTurnMessage(s.Messages[i]) {
			break
		}
		if s.Messages[i].Role != provider.RoleAssistant || s.Messages[i].LocalOnly {
			continue
		}
		receipts := append([]*provider.DecisionReceipt(nil), s.Messages[i].DecisionReceipts...)
		s.Messages[i].DecisionReceipts = append(receipts, receipt)
		// A mid-turn snapshot may already contain this assistant message. Force
		// the next save to replace it instead of treating the later tool result
		// as the only append-only change.
		s.rewriteVersion++
		s.version++
		return
	}
	s.Messages = append(s.Messages, provider.Message{
		Role:            provider.RoleTool,
		ToolCallID:      provider.LocalOnlyToolID,
		Name:            provider.LocalOnlyToolName,
		LocalOnly:       true,
		DecisionReceipt: receipt,
	})
	s.version++
}

// UpdateToolCallPreview replaces the preview fields of the newest matching
// assistant tool call. A dependent writer can only be previewed after an
// earlier writer in the same model batch succeeds; updating under the session
// lock keeps live History/Snapshot readers race-free and ensures the refreshed
// preview is what a resumed session archives.
func (s *Session) UpdateToolCallPreview(call provider.ToolCall) bool {
	if call.ID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//nolint:modernize // slices.Backward yields element copies; this body writes through the index.
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role != provider.RoleAssistant {
			continue
		}
		calls := s.Messages[i].ToolCalls
		for j := range calls {
			if calls[j].ID != call.ID {
				continue
			}
			cloned := append([]provider.ToolCall(nil), calls...)
			cloned[j].Diff = call.Diff
			cloned[j].Added = call.Added
			cloned[j].Removed = call.Removed
			s.Messages[i].ToolCalls = cloned
			// A snapshot may have persisted the original assistant message while
			// its tools were still running. Mark this as a rewrite so a later
			// autosave replaces that message instead of misclassifying the tool
			// results as an append-only suffix.
			s.rewriteVersion++
			s.version++
			return true
		}
	}
	return false
}

// UpdateToolCallResolution persists the host-resolved target metadata for the
// newest matching stable proxy call. The model-visible Name/Arguments remain
// unchanged; this metadata exists only so live and reloaded frontends classify
// MCP readers and writers accurately.
func (s *Session) UpdateToolCallResolution(call provider.ToolCall) bool {
	if call.ID == "" || call.ResolvedReadOnly == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//nolint:modernize // slices.Backward yields element copies; this body writes through the index.
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role != provider.RoleAssistant {
			continue
		}
		calls := s.Messages[i].ToolCalls
		for j := range calls {
			if calls[j].ID != call.ID {
				continue
			}
			cloned := append([]provider.ToolCall(nil), calls...)
			readOnly := *call.ResolvedReadOnly
			cloned[j].ResolvedName = call.ResolvedName
			cloned[j].CapabilityID = call.CapabilityID
			cloned[j].ResolvedReadOnly = &readOnly
			s.Messages[i].ToolCalls = cloned
			// A mid-turn snapshot may already contain the unresolved proxy call.
			// Force the next save to rewrite that assistant message with its
			// resolved local metadata.
			s.rewriteVersion++
			s.version++
			return true
		}
	}
	return false
}

// Replace swaps the whole message log without classifying the change as a
// persisted-history rewrite. Call Rewrite when a live session changes messages
// that a mid-turn snapshot may already have written.
func (s *Session) Replace(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mintMessageIDs(msgs)
	s.Messages = msgs
	s.version++
}

// Rewrite atomically replaces the message log and marks it as a rewrite. The
// atomic classification matters when a periodic snapshot races compaction,
// pruning, or local metadata edits: a later autosave must use owned-rewrite
// conflict checks instead of mistaking the modified prefix for another writer.
//
// reason names the provider-visible change (e.g. "compact_auto", "snip",
// "rewind_truncate") and is queued for the next DrainContentRewriteReasons
// call, which feeds cache-diagnostics attribution. Callers whose msgs only
// change local-only display metadata (never serialized to the provider) must
// use ReplaceLocalMetadata instead, so they don't misreport a cache-prefix
// change that never happened.
func (s *Session) Rewrite(msgs []provider.Message, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mintMessageIDs(msgs)
	s.Messages = msgs
	s.rewriteVersion++
	s.version++
	if reason != "" {
		s.pendingContentReasons = append(s.pendingContentReasons, reason)
	}
}

// ReplaceLocalMetadata atomically replaces the message log exactly like
// Rewrite (including the rewriteVersion bump that forces the next save to use
// owned-rewrite conflict checks), for callers that only changed local-only
// display metadata (e.g. marking a resubmitted message Edited) rather than any
// provider-visible byte. Unlike Rewrite, it never queues a cache-prefix-change
// reason, since ModelMessages strips or never serializes what changed.
func (s *Session) ReplaceLocalMetadata(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mintMessageIDs(msgs)
	s.Messages = msgs
	s.rewriteVersion++
	s.version++
}

// DrainContentRewriteReasons returns and clears the reasons queued by Rewrite
// since the last drain. Called once per provider request (run_loop.go) so
// CompareShape can attribute a cache-prefix change to the operation that
// actually caused it.
func (s *Session) DrainContentRewriteReasons() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	reasons := s.pendingContentReasons
	s.pendingContentReasons = nil
	return reasons
}

// NoteContentRewrite queues a provider-visible prefix-change reason without
// mutating Messages. Projection installs and resume-time system migrations use
// this so cache diagnostics attribute the next request's miss while the
// canonical transcript and its persistence baseline stay intact.
func (s *Session) NoteContentRewrite(reason string) {
	if s == nil || reason == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingContentReasons = append(s.pendingContentReasons, reason)
}

// Snapshot returns a copy of the messages, safe to read from another goroutine
// while a turn appends. Frontends (History, Save) use it instead of touching the
// live slice.
func (s *Session) Snapshot() []provider.Message {
	msgs, _, _ := s.snapshotWithVersion()
	return msgs
}

// DisplayBaseline captures messages and their rewrite/head identity together.
// The controller calls this at an idle/admission boundary, before any new
// streaming event can commit to its display projection.
func (s *Session) DisplayBaseline() (messages []provider.Message, headID string, rewriteEpoch uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]provider.Message(nil), s.Messages...), s.head.ref.HeadID, uint64(s.rewriteVersion)
}

// Len returns the number of messages, safe to call from any goroutine.
func (s *Session) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Messages)
}

// MessageRange returns a copy of the messages in [start, end), clamped to the
// current log bounds, safe to read from another goroutine while a turn
// appends. Paging frontends use it to fetch a display window without paying
// for a Snapshot of the whole history.
func (s *Session) MessageRange(start, end int) []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if start < 0 {
		start = 0
	}
	if end > len(s.Messages) {
		end = len(s.Messages)
	}
	if start >= end {
		return []provider.Message{}
	}
	return append([]provider.Message(nil), s.Messages[start:end]...)
}

// CloneWithMessages returns a fresh Session carrying msgs while preserving the
// persistence baseline of the source session. Resume paths use this when they
// need to adjust loaded history before a rewrite; dropping persisted would make
// CAS treat the first legitimate rewrite as a stale-runtime conflict.
//
// Callers that are handed history from outside this Session should prefer
// CloneWithMessagesIfCompatible, so stale carried history cannot borrow a newer
// on-disk baseline.
func (s *Session) CloneWithMessages(msgs []provider.Message) *Session {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	version := s.version
	if !messagesEqualForStorageList(s.Messages, msgs) {
		version++
	}
	return &Session{
		Messages:                append([]provider.Message(nil), msgs...),
		version:                 version,
		rewriteVersion:          s.rewriteVersion,
		persistedRewriteVersion: s.persistedRewriteVersion,
		persisted:               s.persisted,
		normalizedDirty:         s.normalizedDirty,
		eventLogDamaged:         s.eventLogDamaged,
		rawMessages:             append([]provider.Message(nil), s.rawMessages...),
		pendingContentReasons:   append([]string(nil), s.pendingContentReasons...),
	}
}

// CloneWithMessagesIfCompatible preserves the persistence baseline only when
// msgs is the same persisted history, optionally with a refreshed leading system
// prompt. Other history changes must happen after Resume so SaveRewrite can
// still detect genuine stale-controller conflicts.
func (s *Session) CloneWithMessagesIfCompatible(msgs []provider.Message) (*Session, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !messagesCompatibleForStorageBaseline(s.Messages, msgs) {
		return nil, false
	}
	version := s.version
	if !messagesEqualForStorageList(s.Messages, msgs) {
		version++
	}
	return &Session{
		Messages:                append([]provider.Message(nil), msgs...),
		version:                 version,
		rewriteVersion:          s.rewriteVersion,
		persistedRewriteVersion: s.persistedRewriteVersion,
		persisted:               s.persisted,
		normalizedDirty:         s.normalizedDirty,
		eventLogDamaged:         s.eventLogDamaged,
		rawMessages:             append([]provider.Message(nil), s.rawMessages...),
		pendingContentReasons:   append([]string(nil), s.pendingContentReasons...),
	}, true
}

// projectionValidationMessages returns the current canonical transcript and,
// when LoadSession repaired it, the exact pre-repair disk view. Resume wrappers
// preserve both so projection sidecars can be migrated without weakening the
// covered-prefix check.
func (s *Session) projectionValidationMessages() (current, preRepair []provider.Message) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	current = append([]provider.Message(nil), s.Messages...)
	if s.normalizedDirty && len(s.rawMessages) > 0 {
		preRepair = append([]provider.Message(nil), s.rawMessages...)
	}
	return current, preRepair
}

// snapshotWithVersion returns the messages together with the version and
// rewriteVersion they were captured under, in one lock window: save paths
// persist exactly this rewriteVersion as the new baseline, so a rewrite that
// lands after the capture cannot be misrecorded as saved.
func (s *Session) snapshotWithVersion() ([]provider.Message, uint64, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]provider.Message(nil), s.Messages...), s.version, s.rewriteVersion
}

// snapshotMessagesVersion returns a copy of the messages with the transcript
// version, for projection validity checks that do not need rewriteVersion.
func (s *Session) snapshotMessagesVersion() ([]provider.Message, uint64) {
	msgs, version, _ := s.snapshotWithVersion()
	return msgs, version
}

// TranscriptVersion returns the current append/rewrite counter used by
// context-projection validity checks.
func (s *Session) TranscriptVersion() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// RewriteVersion returns the current rewrite version.
func (s *Session) RewriteVersion() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rewriteVersion
}

// NeedsRewriteSave reports whether the history has been rewritten in memory
// (compaction, prune) since the last successful full save of this session.
// Snapshot paths use it to decide that the next write must be an owned
// rewrite instead of an append.
func (s *Session) NeedsRewriteSave() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rewriteVersion > s.persistedRewriteVersion
}

// HasUnsavedChanges reports whether the in-memory transcript contains storage
// changes that have not been durably recorded at path. It is intentionally
// conservative when no verified baseline exists: an idle controller must not
// replace an in-memory conversation with a possibly older disk copy after a
// bounded lock failure or an interrupted save.
func (s *Session) HasUnsavedChanges(path string) bool {
	if s == nil || strings.TrimSpace(path) == "" {
		return false
	}
	msgs, _, rewriteVersion := s.snapshotWithVersion()
	digest, err := digestSessionMessages(msgs)
	if err != nil {
		return true
	}
	key := canonicalSessionSavePath(path)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.persisted.ok || s.persisted.path != key {
		return true
	}
	if s.normalizedDirty || s.eventLogDamaged || rewriteVersion > s.persistedRewriteVersion {
		return true
	}
	return !bytes.Equal(digest[:], s.persisted.digest[:])
}

// IncrementRewrite bumps the rewrite version by 1.
func (s *Session) IncrementRewrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rewriteVersion++
	s.version++
}

// HasContent returns true when the session carries at least one user,
// assistant, or tool message — i.e. more than just a system prompt. An
// "empty" conversation that has never been used should not be persisted.
func (s *Session) HasContent() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.Messages {
		if m.Role != provider.RoleSystem {
			return true
		}
	}
	return false
}

// HasSystemMessage reports whether the session starts with a system message,
// which carries the agent's stable identity and behavioural contract. Sessions
// without one are not safe to persist: when reloaded the model has no identity
// context and falls back to its training-data defaults.
func (s *Session) HasSystemMessage() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Messages) > 0 && s.Messages[0].Role == provider.RoleSystem
}
