package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/billing"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/extension/providerext"
	"reasonix/internal/fileutil"
	"reasonix/internal/notify"
	"reasonix/internal/provider"
	"reasonix/internal/store"
	"reasonix/internal/turnevent"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// WorkspaceTab

// tabDisplayState follows one live runtime across visible, detached, and
// reattached WorkspaceTab wrappers. Keeping one shared state pointer closes the
// handoff window where an event already routed to the old wrapper could append
// after a clone copied its buffers.
type tabDisplayState struct {
	mu             sync.Mutex
	planner        displayTurnBuffer
	executor       displayTurnBuffer
	pendingWrites  []*pendingDisplayWrite
	persistRunning bool
}

const displayPersistRetryLimit = 4

var errNoDesktopChatModel = errors.New("no desktop chat model is available; add a chat-capable provider in Settings > Model > Access")

type pendingDisplayWrite struct {
	dir         string
	sessionPath string
	userContent string
	messages    []HistoryMessage
	persist     func(string, string, string, []HistoryMessage) error
	onPersisted func()
	onRetry     func()
}

// WorkspaceTab is one open conversation tab in the desktop. Each tab owns an
// independent controller (its own agent, session, tool registry, plugin host,
// memory, permissions) scoped to a workspace root, so multiple projects and
// topics can be active concurrently without interfering.
type WorkspaceTab struct {
	ID                  string                   // stable random id
	Scope               string                   // "project" | "global"
	WorkspaceRoot       string                   // project root dir (empty for global)
	SharedHostKey       string                   // opaque key for the shared plugin host (set by buildTabController)
	TopicID             string                   // topic within the project
	TopicTitle          string                   // display title
	topicTitleSource    string                   // auto or manual; controls localization at API boundaries
	SessionPath         string                   // exact .jsonl file this tab continues
	SessionGeneration   uint64                   // bumps on session rotation (clear/new); frontend hydrate identity
	ReadOnly            bool                     // true for external channel transcripts opened for browsing
	Takeover            struct{ Spectator bool } // handoff state grouped by its cross-runtime lifetime
	Ctrl                control.SessionAPI       // nil while booting / on error
	Label               string                   // model label (for the tab badge)
	Ready               bool                     // true once boot.Build completes
	StartupErr          string                   // build error, surfaced to the frontend
	StartupErrLeaseHeld bool                     // true when StartupErr can be retried after a session lease releases
	modelApplication    tabModelApplicationState // guarded by App.mu; never persisted
	runtimeID           string                   // process-local SessionRuntime registry identity
	sessionLease        *agent.SessionLease
	sessionLeaseMu      sync.Mutex
	sessionLeaseKey     atomic.Pointer[string] // lock-free mirror; updated with sessionLease under sessionLeaseMu
	sink                *tabEventSink          // routes events with this tab's ID
	buildCancel         context.CancelFunc     // cancels in-flight boot for tabs removed before Ready
	buildGeneration     uint64                 // identifies the current in-flight build
	// buildDone is closed exactly once when the build that owns buildDoneGen
	// terminates (success, failure, or superseded abandon). Topic-activation
	// completions wait on it to learn that the controller build finished
	// without polling. Guarded by App.mu alongside buildGeneration; always
	// nil-ed after close so a replacement build can install a fresh channel.
	buildDone    chan struct{}
	buildDoneGen uint64
	removed      bool       // set when the visible tab is pruned/closed before build completes
	reconcileMu  sync.Mutex // serializes stale controller workspace repair for this tab
	turnStartMu  sync.Mutex // serializes foreground turn admission for this tab

	ActivityStatus string // transient project-tree status for the in-flight turn

	saveMu       sync.Mutex
	saving       bool
	saveAgain    bool
	saveFailures int
	// lastAutosaveWarnAt debounces the user-facing autosave-failure notice:
	// a persistently failing disk (AV hold, full volume) otherwise emits a
	// chat warning for every completed turn. Logs are never debounced.
	lastAutosaveWarnAt time.Time

	// closing is set under saveMu when the tab is being torn down. Once set,
	// tabSnapshotLoop stops taking new snapshot work and CloseTab waits on
	// saveCond until any in-flight snapshot finishes - so no background
	// snapshot can write a session file back to disk after CloseTab returns.
	// Without this, deleting a just-closed session races that write and the
	// session "resurrects" (#4384).
	closing  bool
	saveCond *sync.Cond

	// readTelemetry tracks files read during this tab's session.
	readTelemetry  []readFileRecord
	usageTelemetry sessionUsageStats
	// runtimeCostQuote is an automatic wallet-currency hint for the live tab.
	// It is deliberately outside usageTelemetry so it cannot be persisted into
	// telemetry/history or become configuration. Guarded by telemMu.
	runtimeCostDisplayCurrency string
	runtimeCostQuote           *billing.CostQuote
	runtimeCostGeneration      uint64 // invalidates stale wallet responses
	// telemetrySessionKey is the sessionRuntimeKey the telemetry above belongs
	// to. Controller-side session rotations (typed /new, bot /reset) bypass the
	// App bindings, so telemetry writers and readers re-key through
	// syncTelemetryToSession before trusting the in-memory totals — otherwise a
	// previous session's cost keeps accumulating under the new session and gets
	// persisted into its sidecar (#5850).
	telemetrySessionKey string
	telemMu             sync.Mutex

	// Display-only output belongs to the live runtime, not a particular visible
	// tab wrapper. detach/reattach paths share this state before rebinding the
	// event sink so output cannot fall into a discarded wrapper.
	displayStateMu sync.Mutex
	displayState   *tabDisplayState

	model            string // active model ref (for meta)
	effort           *string
	qualityFloor     string // standard|delivery; see desktop/quality_floor.go
	mode             string // "normal" | "plan" | "yolo" | "plan-yolo"; yolo/full access is runtime-only
	goal             string
	toolApprovalMode string
	disabledMCP      map[string]ServerView
	mcpOrder         []string
	lastBuildResult  *boot.BuildResult // incremental extension reload

	PinnedFiles              []string
	pendingLegacyPinnedFiles []string // round-tripped until the session sidecar publishes
	pinnedFilesMu            sync.RWMutex

	// metaExtras caches the expensive MetaForTab fields (git branch, image
	// input capability) computed off the request path by
	// refreshTabMetaExtras. Lock-free reads keep MetaForTab synchronous and
	// cheap; refresh dedup goes through metaExtrasRefreshing.
	metaExtras           atomic.Pointer[tabMetaExtras]
	metaExtrasRefreshing atomic.Bool
}

const (
	topicStatusThinking            = "thinking"
	topicStatusStreaming           = "streaming"
	topicStatusWaitingConfirmation = "waiting_confirmation"
	topicStatusBackgroundJob       = "background_job"
	topicStatusPaused              = "paused"
	topicStatusError               = "error"
	// topicStatusDivergedRecovery marks a topic holding two or more independent
	// recovery branches. It is informational: the user picks which to keep, so
	// it must not gate archiving the way live runtime states do.
	topicStatusDivergedRecovery = "diverged_recovery"
)

type readFileRecord struct {
	Path      string `json:"path"`
	Turn      int    `json:"turn"`
	Time      int64  `json:"time"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type sessionUsageStats struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
	CacheHitTokens   int `json:"cacheHitTokens"`
	CacheMissTokens  int `json:"cacheMissTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
	// CacheWriteBilledTokens preserves provider-specific cache-write pricing
	// across persisted telemetry repricing without changing hit-rate totals.
	CacheWriteBilledTokens float64 `json:"cacheWriteBilledTokens,omitempty"`
	Estimated              bool    `json:"estimated,omitempty"`
	// LastUsedTokens is the executor-reported context fill (prompt+completion)
	// from the most recent turn. It is persisted so the status bar / context
	// panel can show a meaningful fill percentage after a session rebind
	// rebuilds the controller (which resets the in-memory executor state).
	LastUsedTokens int `json:"lastUsedTokens,omitempty"`
	// Per-turn token breakdown from the most recent turn. Persisted separately
	// from the cumulative totals above so the context-panel donut chart and
	// type breakdown survive a session rebind (which resets executor.LastUsage).
	LastPromptTokens     int     `json:"lastPromptTokens,omitempty"`
	LastCompletionTokens int     `json:"lastCompletionTokens,omitempty"`
	LastReasoningTokens  int     `json:"lastReasoningTokens,omitempty"`
	LastCacheHitTokens   int     `json:"lastCacheHitTokens,omitempty"`
	LastCacheMissTokens  int     `json:"lastCacheMissTokens,omitempty"`
	LastEstimated        bool    `json:"lastEstimated,omitempty"`
	RequestCount         int     `json:"requestCount"`
	ElapsedMs            int64   `json:"elapsedMs"`
	SessionCost          float64 `json:"sessionCost,omitempty"`
	SessionCurrency      string  `json:"sessionCurrency,omitempty"`
	SessionCostUsd       float64 `json:"sessionCostUsd,omitempty"`
	// SessionCostComplete is false when any entry lacks a shared display valuation.
	SessionCostComplete bool `json:"sessionCostComplete,omitempty"`
	// CostLedger stores occurrence-time quotes keyed by model+source+fingerprint+rateDate.
	CostLedger *billing.Ledger `json:"costLedger,omitempty"`
	// SessionCostQuote is the aggregate quote for the current display currency.
	SessionCostQuote *billing.CostQuote          `json:"sessionCostQuote,omitempty"`
	Sources          map[string]usageSourceStats `json:"sources,omitempty"`

	activeTurnStartedAt int64
	sourceSessionCache  map[string]sourceSessionCacheCounters
}

type usageSourceStats struct {
	PromptTokens           int     `json:"promptTokens"`
	CompletionTokens       int     `json:"completionTokens"`
	TotalTokens            int     `json:"totalTokens"`
	ReasoningTokens        int     `json:"reasoningTokens"`
	CacheHitTokens         int     `json:"cacheHitTokens"`
	CacheMissTokens        int     `json:"cacheMissTokens"`
	CacheWriteTokens       int     `json:"cacheWriteTokens,omitempty"`
	CacheWriteBilledTokens float64 `json:"cacheWriteBilledTokens,omitempty"`
	Estimated              bool    `json:"estimated,omitempty"`
	RequestCount           int     `json:"requestCount"`
	SessionCost            float64 `json:"sessionCost,omitempty"`
	SessionCurrency        string  `json:"sessionCurrency,omitempty"`
	SessionCostUsd         float64 `json:"sessionCostUsd,omitempty"`
}

type sourceSessionCacheCounters struct {
	Hit  int
	Miss int
}

func cloneSessionUsageStats(in sessionUsageStats) sessionUsageStats {
	out := in
	if len(in.Sources) > 0 {
		out.Sources = make(map[string]usageSourceStats, len(in.Sources))
		maps.Copy(out.Sources, in.Sources)
	}
	if len(in.sourceSessionCache) > 0 {
		out.sourceSessionCache = make(map[string]sourceSessionCacheCounters, len(in.sourceSessionCache))
		maps.Copy(out.sourceSessionCache, in.sourceSessionCache)
	}
	return out
}

func (s *sessionUsageStats) cacheTokenDelta(source string, u *provider.Usage, sessionHit, sessionMiss int) (hit, miss int) {
	if u != nil {
		hit = u.CacheHitTokens
		miss = u.CacheMissTokens
	}
	if source != event.UsageSourceExecutor && source != event.UsageSourcePlanner {
		return hit, miss
	}
	if sessionHit+sessionMiss <= 0 {
		return hit, miss
	}
	if s.sourceSessionCache == nil {
		s.sourceSessionCache = map[string]sourceSessionCacheCounters{}
	}
	prev, ok := s.sourceSessionCache[source]
	s.sourceSessionCache[source] = sourceSessionCacheCounters{Hit: sessionHit, Miss: sessionMiss}
	if !ok {
		return sessionHit, sessionMiss
	}
	if sessionHit < prev.Hit || sessionMiss < prev.Miss {
		if hit+miss > 0 {
			return hit, miss
		}
		return sessionHit, sessionMiss
	}
	return sessionHit - prev.Hit, sessionMiss - prev.Miss
}

type tabTelemetrySnapshot struct {
	Version   int               `json:"version"`
	ReadFiles []readFileRecord  `json:"readFiles"`
	Usage     sessionUsageStats `json:"usage"`
}

func cloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func cloneServerViewMap(in map[string]ServerView) map[string]ServerView {
	out := make(map[string]ServerView, len(in))
	for name, view := range in {
		view.EnvKeys = append([]string(nil), view.EnvKeys...)
		view.HeaderKeys = append([]string(nil), view.HeaderKeys...)
		out[name] = view
	}
	return out
}

func (t *WorkspaceTab) currentSessionPath() string {
	if t == nil {
		return ""
	}
	tabPath := strings.TrimSpace(t.SessionPath)
	// Recovery handoff is two-phase: the desktop callback acquires the new
	// lease and updates SessionPath before Controller commits its own path. The
	// lease-backed tab path is authoritative during that window; otherwise a
	// concurrent, newer tab-layout save can overwrite the recovery anchor with
	// the controller's old path. Outside a handoff, keep the controller-first
	// behavior so an unleased/stale tab field cannot mask the live runtime.
	if tabPath != "" && sessionRuntimeKey(tabPath) == t.sessionLeaseRuntimeKey() {
		return tabPath
	}
	if t.Ctrl != nil {
		if path := strings.TrimSpace(t.Ctrl.SessionPath()); path != "" {
			return path
		}
	}
	return tabPath
}

func (t *WorkspaceTab) hasActiveRuntimeWork() bool {
	if t == nil || t.Ctrl == nil {
		return false
	}
	status := t.Ctrl.RuntimeStatus()
	return status.Running || status.PendingPrompt || status.BackgroundJobs > 0
}

// sessionRuntimeKey is the comparison/map key for "same session" checks. It
// layers agent.CanonicalSessionPath on top of the desktop path normalization
// so the key matches the form held by session leases (lowercased on Windows).
// Comparing a lease's Path() against a raw tab path without this fold made
// every rebuild on Windows look like a foreign holder (self-lock, #5999).
// Keys are identities only — never use them as display or file paths.
func sessionRuntimeKey(path string) string {
	return agent.CanonicalSessionPath(canonicalTabSessionPath(path))
}

var sessionLeaseAcquireHookForTest func()

func (t *WorkspaceTab) ensureSessionLease(path string) error {
	if t == nil || t.ReadOnly {
		return nil
	}
	key := sessionRuntimeKey(path)
	if key == "" {
		return nil
	}
	t.sessionLeaseMu.Lock()
	if t.sessionLease != nil && sessionRuntimeKey(t.sessionLease.Path()) == key {
		t.storeSessionLeaseRuntimeKey(key)
		t.sessionLeaseMu.Unlock()
		return nil
	}
	lease, err := agent.TryAcquireSessionLease(key)
	if err != nil {
		t.sessionLeaseMu.Unlock()
		return err
	}
	if hook := sessionLeaseAcquireHookForTest; hook != nil {
		hook()
	}
	old := t.sessionLease
	t.sessionLease = lease
	t.storeSessionLeaseRuntimeKey(key)
	t.sessionLeaseMu.Unlock()
	if old != nil {
		old.Release()
	}
	return nil
}

func (t *WorkspaceTab) releaseSessionLease() {
	if t == nil {
		return
	}
	t.sessionLeaseMu.Lock()
	lease := t.sessionLease
	t.sessionLease = nil
	t.storeSessionLeaseRuntimeKey("")
	t.sessionLeaseMu.Unlock()
	if lease != nil {
		lease.Release()
	}
}

// takeSessionLease removes and returns the tab's current lease WITHOUT
// releasing it, so ownership can transfer to another holder. All access to
// t.sessionLease must go through sessionLeaseMu; never read or assign the
// field directly outside these helpers.
func (t *WorkspaceTab) takeSessionLease() *agent.SessionLease {
	if t == nil {
		return nil
	}
	t.sessionLeaseMu.Lock()
	lease := t.sessionLease
	t.sessionLease = nil
	t.storeSessionLeaseRuntimeKey("")
	t.sessionLeaseMu.Unlock()
	return lease
}

// adoptSessionLease installs lease as the tab's session lease, releasing any
// previously held lease unless it is the very same lease. A nil tab releases
// the lease immediately so ownership is never dropped on the floor.
func (t *WorkspaceTab) adoptSessionLease(lease *agent.SessionLease) {
	if t == nil {
		if lease != nil {
			lease.Release()
		}
		return
	}
	t.sessionLeaseMu.Lock()
	old := t.sessionLease
	t.sessionLease = lease
	key := ""
	if lease != nil {
		key = sessionRuntimeKey(lease.Path())
	}
	t.storeSessionLeaseRuntimeKey(key)
	t.sessionLeaseMu.Unlock()
	if old != nil && old != lease {
		old.Release()
	}
}

func (t *WorkspaceTab) storeSessionLeaseRuntimeKey(key string) {
	if t == nil || key == "" {
		if t != nil {
			t.sessionLeaseKey.Store(nil)
		}
		return
	}
	stored := key
	t.sessionLeaseKey.Store(&stored)
}

// sessionLeaseRuntimeKey reports the runtime key of the currently held lease,
// or "" when no lease is held. The mirror is lock-free so callers holding
// App.mu never wait on a concurrent lease acquisition (whose test hook and
// platform file operations run under sessionLeaseMu).
func (t *WorkspaceTab) sessionLeaseRuntimeKey() string {
	if t == nil {
		return ""
	}
	key := t.sessionLeaseKey.Load()
	if key == nil {
		return ""
	}
	return *key
}

// releaseSessionLeaseForKey releases the tab's lease only when it is bound to
// key. Superseded builds clean up with this instead of releaseSessionLease:
// on a removed tab the keys match and the lease is released as before, but
// when a session rebind superseded the build, the rebind's replacement build
// holds a lease for a *different* session key (rebind early-returns on equal
// keys), and releasing that here would strip the live session's protection.
func (t *WorkspaceTab) releaseSessionLeaseForKey(key string) {
	if t == nil || key == "" {
		return
	}
	t.sessionLeaseMu.Lock()
	lease := t.sessionLease
	if lease == nil || sessionRuntimeKey(lease.Path()) != key {
		t.sessionLeaseMu.Unlock()
		return
	}
	t.sessionLease = nil
	t.storeSessionLeaseRuntimeKey("")
	t.sessionLeaseMu.Unlock()
	lease.Release()
}

func detachedRuntimeTabID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "detached_" + hex.EncodeToString(sum[:8])
}

func (a *App) ensureDetachedSessionsLocked() {
	if a.detachedSessions == nil {
		a.detachedSessions = map[string]*WorkspaceTab{}
	}
}

func (a *App) runtimeTabsLocked() []*WorkspaceTab {
	seen := map[*WorkspaceTab]bool{}
	out := make([]*WorkspaceTab, 0, len(a.tabs)+len(a.detachedSessions))
	for _, tab := range a.tabs {
		if tab != nil && !seen[tab] {
			seen[tab] = true
			out = append(out, tab)
		}
	}
	for _, tab := range a.detachedSessions {
		if tab != nil && !seen[tab] {
			seen[tab] = true
			out = append(out, tab)
		}
	}
	return out
}

func (a *App) tabByEventSinkIDLocked(tabID string) *WorkspaceTab {
	if tab := a.tabs[tabID]; tab != nil {
		return tab
	}
	for _, tab := range a.detachedSessions {
		if tab != nil && tab.ID == tabID {
			return tab
		}
	}
	return nil
}

func (a *App) detachSessionRuntime(tab *WorkspaceTab) bool {
	if tab == nil {
		return false
	}
	a.mu.RLock()
	ctrl := tab.Ctrl
	fallbackPath := strings.TrimSpace(tab.SessionPath)
	sink := tab.sink
	a.mu.RUnlock()
	path := fallbackPath
	if ctrl != nil {
		if p := strings.TrimSpace(ctrl.SessionPath()); p != "" {
			path = p
		}
	}
	key := sessionRuntimeKey(path)
	if key == "" {
		return false
	}
	if sink != nil {
		sink.clearContext()
	}
	a.mu.Lock()
	a.ensureDetachedSessionsLocked()
	tab.SessionPath = canonicalTabSessionPath(path)
	a.bindSessionRuntimeKeyLocked(tab, path)
	a.detachedSessions[key] = tab
	a.mu.Unlock()
	return true
}

// cloneDetachedRuntimeTab copies a running tab's runtime state into a fresh
// detached tab. Callers must hold a.mu: the copied fields (Ctrl, Ready,
// ActivityStatus, disabledMCP, ...) are written under a.mu by bound methods
// and the event sink, and the disabledMCP map read would otherwise race those
// writers. The session lease is transferred separately by the caller through
// the sessionLeaseMu helpers. key is the runtime identity (map key / tab id
// hash); path is the real session path — keys are case-folded on Windows and
// must not leak into SessionPath, which is displayed and persisted.
func cloneDetachedRuntimeTab(tab *WorkspaceTab, key, path string) *WorkspaceTab {
	if tab == nil {
		return nil
	}
	tab.telemMu.Lock()
	readTelemetry := append([]readFileRecord(nil), tab.readTelemetry...)
	usageTelemetry := cloneSessionUsageStats(tab.usageTelemetry)
	telemetrySessionKey := tab.telemetrySessionKey
	tab.telemMu.Unlock()
	pinnedFiles, pendingLegacyPinnedFiles := tab.pinnedFilesState()

	return &WorkspaceTab{
		ID:                       detachedRuntimeTabID(key),
		Scope:                    tab.Scope,
		WorkspaceRoot:            tab.WorkspaceRoot,
		SharedHostKey:            tab.SharedHostKey,
		TopicID:                  tab.TopicID,
		TopicTitle:               tab.TopicTitle,
		topicTitleSource:         tab.topicTitleSource,
		SessionPath:              canonicalTabSessionPath(path),
		Ctrl:                     tab.Ctrl,
		Label:                    tab.Label,
		Ready:                    tab.Ready,
		StartupErr:               tab.StartupErr,
		StartupErrLeaseHeld:      tab.StartupErrLeaseHeld,
		modelApplication:         tab.modelApplication,
		lastBuildResult:          tab.lastBuildResult,
		runtimeID:                tab.runtimeID,
		sink:                     tab.sink,
		ActivityStatus:           tab.ActivityStatus,
		readTelemetry:            readTelemetry,
		usageTelemetry:           usageTelemetry,
		telemetrySessionKey:      telemetrySessionKey,
		displayState:             tab.displayBufferState(),
		model:                    tab.model,
		effort:                   cloneStringPtr(tab.effort),
		qualityFloor:             tab.qualityFloor,
		mode:                     tab.mode,
		goal:                     tab.goal,
		toolApprovalMode:         tab.toolApprovalMode,
		disabledMCP:              cloneServerViewMap(tab.disabledMCP),
		mcpOrder:                 append([]string(nil), tab.mcpOrder...),
		PinnedFiles:              pinnedFiles,
		pendingLegacyPinnedFiles: pendingLegacyPinnedFiles,
	}
}

func (a *App) detachRuntimeForReplacement(tab *WorkspaceTab) bool {
	if tab == nil {
		return false
	}
	// One a.mu critical section covers the membership check, the field
	// snapshot, the lease/sink handover, and the re-publication:
	//   - the clone reads fields that bound methods and the event sink write
	//     under a.mu (ActivityStatus every event, disabledMCP is a map);
	//   - inserting the clone without re-checking a.tabs would resurrect a
	//     runtime that DeleteSession/TrashTopic/RemoveWorkspace already
	//     unlinked and closed (the "session resurrects" class, #4384);
	//   - publishing before the lease/sink handover would let a concurrent
	//     attachExistingSessionRuntime claim a half-initialized clone.
	// The lease transfer stays deadlock-safe here: neither side holds a lease
	// to release, so no lease I/O runs under a.mu.
	a.mu.Lock()
	detached := a.detachRuntimeForReplacementLocked(tab)
	a.mu.Unlock()
	return detached
}

// detachRuntimeForReplacementLocked transfers a visible tab's live runtime to
// the detached registry without closing its controller or releasing its lease.
// Callers must hold App.mu. The transfer itself performs no file or host I/O.
func (a *App) detachRuntimeForReplacementLocked(tab *WorkspaceTab) bool {
	if tab == nil {
		return false
	}
	if tab.removed || a.tabs[tab.ID] != tab {
		return false
	}
	sourcePath := tab.currentSessionPath()
	key := sessionRuntimeKey(sourcePath)
	if key == "" {
		return false
	}
	detached := cloneDetachedRuntimeTab(tab, key, sourcePath)
	if detached == nil {
		return false
	}
	// Transfer lease ownership through the locked helpers: a concurrent
	// ensureSessionLease (blank-session boot, recovery callback) must never
	// observe a torn pointer or have its freshly acquired lease clobbered.
	detached.adoptSessionLease(tab.takeSessionLease())
	if rt := a.runtimeForTabLocked(tab); rt != nil {
		rt.Owner = detached
		detached.runtimeID = rt.ID
		tab.runtimeID = ""
	}
	if detached.sink != nil {
		detached.sink.setBinding(detached.ID, nil)
		// clearContext (locked nil + drain the queued emitter), not a bare
		// ctx=nil: the latter both data-races s.ctx and leaves already-queued
		// events to flush onto the rebound tab after this session is backgrounded
		// (#5352 — stale "AI 不断输出" on the now-visible session).
		detached.sink.clearContext()
	}
	a.ensureDetachedSessionsLocked()
	a.detachedSessions[key] = detached
	return true
}

// applyRuntimeTab moves source's runtime (controller, sink, lease, telemetry)
// onto target. path is the real session path for display/persistence; the
// case-folded runtime key must never be written into SessionPath.
func applyRuntimeTab(target, source *WorkspaceTab, path string, wailsCtx context.Context, app *App) {
	if target == nil || source == nil {
		return
	}
	source.telemMu.Lock()
	readTelemetry := append([]readFileRecord(nil), source.readTelemetry...)
	usageTelemetry := cloneSessionUsageStats(source.usageTelemetry)
	telemetrySessionKey := source.telemetrySessionKey
	source.telemMu.Unlock()
	pinnedFiles, pendingLegacyPinnedFiles := source.pinnedFilesState()

	// Share the runtime-owned display state before rebinding the sink. An event
	// already routed to source and one arriving on target after setBinding then
	// append under the same state lock instead of straddling two buffers.
	target.adoptDisplayState(source.displayBufferState())
	if source.sink != nil {
		source.sink.setBinding(target.ID, app)
		source.sink.setSessionGeneration(target.SessionGeneration)
		source.sink.setContext(wailsCtx)
	}

	target.Ctrl = source.Ctrl
	target.modelApplication.failure = source.modelApplication.failure
	target.lastBuildResult = source.lastBuildResult
	target.sink = source.sink
	target.adoptSessionLease(source.takeSessionLease())
	target.SessionPath = canonicalTabSessionPath(path)
	target.SharedHostKey = source.SharedHostKey
	target.Label = source.Label
	target.Ready = source.Ready && source.Ctrl != nil
	clearTabStartupError(target)
	target.ActivityStatus = source.ActivityStatus
	target.model = source.model
	target.effort = cloneStringPtr(source.effort)
	target.qualityFloor = source.qualityFloor
	target.mode = source.mode
	target.goal = source.goal
	target.toolApprovalMode = source.toolApprovalMode
	target.disabledMCP = cloneServerViewMap(source.disabledMCP)
	target.mcpOrder = append([]string(nil), source.mcpOrder...)
	target.setPinnedFilesState(pinnedFiles, pendingLegacyPinnedFiles)
	target.replaceTelemetry(tabTelemetrySnapshot{ReadFiles: readTelemetry, Usage: usageTelemetry}, telemetrySessionKey)
	if app != nil {
		key := sessionRuntimeKey(path)
		rt := app.runtimeForTabLocked(source)
		targetRuntime := app.runtimeForTabLocked(target)
		if rt == nil {
			rt = targetRuntime
		}
		if rt == nil {
			rt = app.newSessionRuntimeLocked(source, key)
		} else if targetRuntime != nil && targetRuntime != rt {
			app.removeSessionRuntimeMappingsLocked(targetRuntime)
			target.runtimeID = ""
		}
		if source.Ctrl != nil && source.Ready {
			rt.Phase = sessionRuntimeReady
			rt.Issue = nil
			closeRuntimeReadyChannelLocked(rt)
		}
		rt.Owner = target
		if rt.Key != "" && rt.Key != key && app.runtimeBySessionKey[rt.Key] == rt {
			delete(app.runtimeBySessionKey, rt.Key)
		}
		rt.Key = key
		app.runtimeBySessionKey[key] = rt
		target.runtimeID = rt.ID
		source.runtimeID = ""
		if target.sink != nil {
			target.sink.setRuntimeEpoch(rt.Epoch)
		}
	}
}

func (a *App) attachExistingSessionRuntimeCore(tab *WorkspaceTab, path string, wailsCtx context.Context) bool {
	key := sessionRuntimeKey(path)
	if tab == nil || key == "" {
		return false
	}

	a.mu.Lock()
	if tab.removed || a.tabs[tab.ID] != tab {
		a.mu.Unlock()
		return false
	}
	if rt := a.runtimeBySessionKey[key]; rt != nil && !a.runtimeOwnerLiveLocked(rt) {
		a.removeSessionRuntimeMappingsLocked(rt)
	}
	registered := a.runtimeBySessionKey[key]
	if registered != nil && registered.Phase == sessionRuntimeStarting && registered.Owner != tab {
		// A starting runtime owns only an admission placeholder; its controller,
		// lease, and sink have not been published yet. Moving that tab would
		// supersede the owner build while the attaching build closes its own
		// candidate, leaving the session permanently starting with no controller.
		// claimSessionRuntime waits on readyCh and retries the attach after the
		// owner publishes a terminal phase. The owner itself may still adopt a
		// usable legacy runtime that predates the registry.
		a.mu.Unlock()
		return false
	}
	attachable := func(source *WorkspaceTab) bool {
		if source == nil || source.Ctrl == nil {
			return false
		}
		if rt := a.runtimeForTabLocked(source); rt != nil {
			return rt.Phase == sessionRuntimeReady
		}
		// Compatibility for visible/detached runtimes constructed before the
		// process-local registry existed.
		return source.Ready
	}
	detached := a.detachedSessions[key]
	if detached == nil {
		if rt := a.runtimeBySessionKey[key]; rt != nil && rt.Owner != nil && rt.Owner != tab {
			detached = rt.Owner
			if a.tabs[detached.ID] != detached {
				delete(a.detachedSessions, key)
			} else {
				detached = nil
			}
		}
	}
	if detached != nil {
		if !attachable(detached) {
			a.mu.Unlock()
			return false
		}
		delete(a.detachedSessions, key)
		applyRuntimeTab(tab, detached, path, wailsCtx, a)
		if current := a.tabs[tab.ID]; current == tab {
			a.saveTabsLocked()
		}
		attachedCtrl := tab.Ctrl
		attachedSink := tab.sink
		attachedEpoch := a.runtimeEpochForTabLocked(tab)
		a.mu.Unlock()
		a.replayPendingPromptsAfterRuntimeAttach(tab.ID, attachedSink, attachedCtrl, attachedEpoch)
		return true
	}

	var source *WorkspaceTab
	if rt := a.runtimeBySessionKey[key]; rt != nil && rt.Owner != nil && rt.Owner != tab {
		source = rt.Owner
	}
	for _, candidate := range a.tabs {
		if source != nil {
			break
		}
		if candidate == nil || candidate == tab {
			continue
		}
		if sessionRuntimeKey(candidate.currentSessionPath()) == key {
			source = candidate
			break
		}
	}
	if source == nil {
		a.mu.Unlock()
		return false
	}
	if !attachable(source) {
		a.mu.Unlock()
		return false
	}
	delete(a.tabs, source.ID)
	a.removeTabOrderLocked(source.ID)
	if a.activeTabID == source.ID {
		a.activeTabID = tab.ID
	}
	applyRuntimeTab(tab, source, path, wailsCtx, a)
	a.saveTabsLocked()
	attachedCtrl := tab.Ctrl
	attachedSink := tab.sink
	attachedEpoch := a.runtimeEpochForTabLocked(tab)
	a.mu.Unlock()
	if path != "" && !tab.ReadOnly {
		a.attachTakeoverMirror(tab.ID, path)
		go a.adoptSessionFromLocalServe(tab.ID, path)
	}

	a.replayPendingPromptsAfterRuntimeAttach(tab.ID, attachedSink, attachedCtrl, attachedEpoch)
	return true
}

func (t *WorkspaceTab) recordReadFile(rec readFileRecord) {
	t.telemMu.Lock()
	t.readTelemetry = append(t.readTelemetry, rec)
	t.telemMu.Unlock()
}

func (t *WorkspaceTab) recordTurnDone(now int64) {
	t.telemMu.Lock()
	if started := t.usageTelemetry.activeTurnStartedAt; started > 0 && now >= started {
		t.usageTelemetry.ElapsedMs += now - started
		t.usageTelemetry.activeTurnStartedAt = 0
	}
	t.telemMu.Unlock()
}

// contextTelemetryFromUsage returns the latest-attempt context shape for
// rebind-surviving Last* telemetry fields. Prefer Context* when set (multi-
// attempt sampling recovery); otherwise fall back to billable totals / the
// per-event cache delta already computed for this Usage event.
//
// When a Context shape is present, ContextCacheHit/Miss are kept even if both
// are zero — many providers omit cache splits, and falling back to the
// event's aggregated cache would re-inflate multi-attempt totals.
func contextTelemetryFromUsage(u *provider.Usage, eventCacheHit, eventCacheMiss int) (prompt, completion, reasoning, hit, miss int) {
	if u == nil {
		return 0, 0, 0, eventCacheHit, eventCacheMiss
	}
	if u.ContextPromptTokens > 0 || u.ContextCompletionTokens > 0 {
		return u.ContextPromptTokens, u.ContextCompletionTokens, u.ContextReasoningTokens,
			u.ContextCacheHitTokens, u.ContextCacheMissTokens
	}
	return u.PromptTokens, u.CompletionTokens, u.ReasoningTokens, eventCacheHit, eventCacheMiss
}

func (t *WorkspaceTab) recordUsage(e event.Event) {
	if e.Usage == nil {
		return
	}
	u := e.Usage
	source := strings.TrimSpace(e.UsageSource)
	if source == "" {
		source = event.UsageSourceExecutor
	}
	t.telemMu.Lock()
	t.usageTelemetry.PromptTokens += u.PromptTokens
	t.usageTelemetry.CompletionTokens += u.CompletionTokens
	t.usageTelemetry.TotalTokens += u.TotalTokens
	t.usageTelemetry.ReasoningTokens += u.ReasoningTokens
	cacheHitTokens, cacheMissTokens := t.usageTelemetry.cacheTokenDelta(source, u, e.SessionHit, e.SessionMiss)
	t.usageTelemetry.CacheHitTokens += cacheHitTokens
	t.usageTelemetry.CacheMissTokens += cacheMissTokens
	t.usageTelemetry.CacheWriteTokens += u.CacheWriteTokens
	t.usageTelemetry.CacheWriteBilledTokens += u.CacheWriteBilledTokens
	t.usageTelemetry.Estimated = t.usageTelemetry.Estimated || u.Estimated
	requestCount := u.RequestCount
	if requestCount <= 0 {
		requestCount = 1
	}
	t.usageTelemetry.RequestCount += requestCount
	if source == event.UsageSourceExecutor {
		// Persist the latest-attempt context shape for rebind fallback — never
		// the multi-attempt billable aggregate (PromptTokens/CompletionTokens
		// after stream recovery). ContextSnapshot semantics are latest
		// prompt+completion; Context* fields carry that shape.
		prompt, completion, reasoning, hit, miss := contextTelemetryFromUsage(u, cacheHitTokens, cacheMissTokens)
		t.usageTelemetry.LastUsedTokens = prompt + completion
		t.usageTelemetry.LastPromptTokens = prompt
		t.usageTelemetry.LastCompletionTokens = completion
		t.usageTelemetry.LastReasoningTokens = reasoning
		t.usageTelemetry.LastCacheHitTokens = hit
		t.usageTelemetry.LastCacheMissTokens = miss
		t.usageTelemetry.LastEstimated = u.Estimated
	}
	if t.usageTelemetry.Sources == nil {
		t.usageTelemetry.Sources = map[string]usageSourceStats{}
	}
	src := t.usageTelemetry.Sources[source]
	src.PromptTokens += u.PromptTokens
	src.CompletionTokens += u.CompletionTokens
	src.TotalTokens += u.TotalTokens
	src.ReasoningTokens += u.ReasoningTokens
	src.CacheHitTokens += cacheHitTokens
	src.CacheMissTokens += cacheMissTokens
	src.CacheWriteTokens += u.CacheWriteTokens
	src.CacheWriteBilledTokens += u.CacheWriteBilledTokens
	src.Estimated = src.Estimated || u.Estimated
	src.RequestCount += requestCount
	// Prefer the middleware CostQuote; fall back only when older emitters omit it.
	q := e.CostQuote
	if q == nil && e.Pricing != nil {
		q = event.EnsureCostQuote(e, nil)
	}
	if q != nil {
		if t.usageTelemetry.CostLedger == nil {
			t.usageTelemetry.CostLedger = billing.NewLedger()
		}
		tokens := billing.UsageTokens{
			PromptTokens:           u.PromptTokens,
			CompletionTokens:       u.CompletionTokens,
			CacheHitTokens:         cacheHitTokens,
			CacheMissTokens:        cacheMissTokens,
			CacheWriteTokens:       u.CacheWriteTokens,
			CacheWriteBilledTokens: u.CacheWriteBilledTokens,
			Estimated:              u.Estimated,
		}
		t.usageTelemetry.CostLedger.Add(*q, tokens, time.Now().UTC())
		display := billing.NormalizeCurrency(t.runtimeCostDisplayCurrency)
		if display == "" {
			display = billing.NormalizeCurrency(t.usageTelemetry.SessionCurrency)
		}
		if display == "" && q.Selected != nil {
			display = billing.NormalizeCurrency(q.Selected.Currency)
		}
		if display == "" {
			display = billing.NormalizeCurrency(q.Original.Currency)
		}
		total := t.usageTelemetry.CostLedger.Total(display)
		if t.runtimeCostDisplayCurrency != "" {
			t.runtimeCostQuote = &total
		} else {
			t.usageTelemetry.SessionCostQuote = &total
			t.usageTelemetry.SessionCostComplete = total.Complete
		}
		if total.Selected != nil {
			if t.runtimeCostDisplayCurrency == "" {
				t.usageTelemetry.SessionCost = total.Selected.Float64()
				t.usageTelemetry.SessionCurrency = total.LegacyCurrencyCode()
				t.usageTelemetry.SessionCostUsd = t.usageTelemetry.SessionCost
			}
			src.SessionCost += q.LegacyCostFloat()
			src.SessionCostUsd = src.SessionCost
			src.SessionCurrency = total.LegacyCurrencySymbol()
		} else {
			// Incomplete: never invent a zero total by wiping prior costs.
			if t.runtimeCostDisplayCurrency == "" {
				t.usageTelemetry.SessionCostComplete = false
				t.usageTelemetry.SessionCost = 0
				t.usageTelemetry.SessionCurrency = ""
				t.usageTelemetry.SessionCostUsd = 0
			}
			if q.Selected == nil {
				src.SessionCurrency = billing.CurrencySymbol(q.Original.Currency)
			}
		}
	}
	t.usageTelemetry.Sources[source] = src
	t.telemMu.Unlock()
}

func (a *App) repriceTabUsageForCurrentCurrency(tab *WorkspaceTab) {
	if a == nil || tab == nil {
		return
	}
	a.mu.RLock()
	root := tab.WorkspaceRoot
	a.mu.RUnlock()
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return
	}
	// Display preference only — automatic mode remains unresolved until a
	// wallet-aware surface supplies a session hint.
	display := cfg.ExplicitDisplayCurrency()
	if !tab.selectDisplayCurrency(display) {
		return
	}
	if path := tab.currentSessionPath(); path != "" {
		_ = saveTelemetry(path+".telemetry.json", tab.telemetrySnapshot())
	}
}

func (t *WorkspaceTab) telemetrySnapshot() tabTelemetrySnapshot {
	t.telemMu.Lock()
	defer t.telemMu.Unlock()
	records := make([]readFileRecord, len(t.readTelemetry))
	copy(records, t.readTelemetry)
	usage := t.usageTelemetry
	if started := usage.activeTurnStartedAt; started > 0 {
		now := time.Now().UnixMilli()
		if now >= started {
			usage.ElapsedMs += now - started
		}
	}
	if len(t.usageTelemetry.Sources) > 0 {
		usage.Sources = make(map[string]usageSourceStats, len(t.usageTelemetry.Sources))
		maps.Copy(usage.Sources, t.usageTelemetry.Sources)
	}
	usage.activeTurnStartedAt = 0
	usage.sourceSessionCache = nil
	return tabTelemetrySnapshot{Version: 3, ReadFiles: records, Usage: usage}
}

// displayTelemetrySnapshot overlays the live wallet hint onto a copy used by
// UI reads. The persisted snapshot remains the occurrence-time/original view.
func (t *WorkspaceTab) displayTelemetrySnapshot() tabTelemetrySnapshot {
	snapshot := t.telemetrySnapshot()
	t.telemMu.Lock()
	quote := t.runtimeCostQuote
	t.telemMu.Unlock()
	if quote == nil {
		return snapshot
	}
	snapshot.Usage.SessionCostQuote = quote
	snapshot.Usage.SessionCostComplete = quote.Complete
	if quote.Selected != nil {
		snapshot.Usage.SessionCost = quote.Selected.Float64()
		snapshot.Usage.SessionCurrency = quote.LegacyCurrencyCode()
		snapshot.Usage.SessionCostUsd = snapshot.Usage.SessionCost
	} else {
		snapshot.Usage.SessionCostComplete = false
		snapshot.Usage.SessionCost = 0
		snapshot.Usage.SessionCurrency = ""
		snapshot.Usage.SessionCostUsd = 0
	}
	return snapshot
}

func (t *WorkspaceTab) resetTelemetry(sessionPath string) {
	t.telemMu.Lock()
	t.readTelemetry = nil
	t.usageTelemetry = sessionUsageStats{}
	t.runtimeCostDisplayCurrency = ""
	t.runtimeCostQuote = nil
	t.runtimeCostGeneration++
	t.telemetrySessionKey = sessionRuntimeKey(sessionPath)
	t.telemMu.Unlock()
}

// syncTelemetryToSession keys the in-memory telemetry to the runtime's current
// session. When the runtime rotated to a different session underneath the tab
// (typed /new routes through Controller.Submit and never reaches App.NewSession),
// the previous session's totals must not bleed into the new one: swap in the
// new session's persisted sidecar, or start from zero when none exists. The
// sidecar is rewritten on every recorded event, so a reload never loses more
// than the sub-second in-memory delta of an in-flight record.
func (t *WorkspaceTab) syncTelemetryToSession(sessionPath string) {
	key := sessionRuntimeKey(sessionPath)
	if key == "" {
		return
	}
	t.telemMu.Lock()
	same := t.telemetrySessionKey == key
	t.telemMu.Unlock()
	if same {
		return
	}
	// File I/O stays outside telemMu; re-check the key after reacquiring in
	// case a concurrent sync or reset re-keyed the tab first.
	snapshot := loadTelemetry(sessionPath + ".telemetry.json")
	t.telemMu.Lock()
	if t.telemetrySessionKey != key {
		t.readTelemetry = snapshot.ReadFiles
		t.usageTelemetry = snapshot.Usage
		t.runtimeCostDisplayCurrency = ""
		t.runtimeCostQuote = nil
		t.runtimeCostGeneration++
		t.telemetrySessionKey = key
	}
	t.telemMu.Unlock()
}

func (t *WorkspaceTab) resetDisplayTurn() {
	state := t.displayBufferState()
	state.mu.Lock()
	if len(state.planner.messages) == 0 {
		state.planner.tools = nil
	}
	if len(state.executor.messages) == 0 {
		state.executor.tools = nil
	}
	state.mu.Unlock()
}

func (t *WorkspaceTab) recordDisplayEvent(e event.Event) {
	state := t.displayBufferState()
	state.mu.Lock()
	defer state.mu.Unlock()
	buffer := &state.executor
	if strings.TrimSpace(e.Source) == event.UsageSourcePlanner {
		buffer = &state.planner
	}
	recordHistoryDisplayEvent(buffer, e)
}

func (t *WorkspaceTab) displayBufferState() *tabDisplayState {
	t.displayStateMu.Lock()
	defer t.displayStateMu.Unlock()
	if t.displayState == nil {
		t.displayState = &tabDisplayState{}
	}
	return t.displayState
}

func (t *WorkspaceTab) adoptDisplayState(state *tabDisplayState) {
	if t == nil || state == nil {
		return
	}
	t.displayStateMu.Lock()
	t.displayState = state
	t.displayStateMu.Unlock()
}

func recoverPendingTurnProjections(tab *WorkspaceTab, ctrl control.SessionAPI) {
	if tab == nil || ctrl == nil {
		return
	}
	projectionCtrl, ok := ctrl.(interface {
		PendingTurnProjections() []turnevent.PendingProjection
		AcknowledgeTurnProjection(string) error
	})
	if !ok {
		return
	}
	pending := projectionCtrl.PendingTurnProjections()
	if len(pending) == 0 {
		return
	}
	users := make([]string, 0)
	for _, message := range ctrl.History() {
		if agent.IsUserAuthoredTurnMessage(message) {
			if text := strings.TrimSpace(agent.UserMessageText(message)); text != "" {
				users = append(users, text)
			}
		}
	}
	firstUser := len(users) - len(pending)
	for i, projection := range pending {
		messages := displayMessagesFromProjection(projection)
		if len(messages) == 0 {
			if err := projectionCtrl.AcknowledgeTurnProjection(projection.TurnID); err != nil {
				slog.Warn("desktop: acknowledge empty recovered projection", "err", err)
			}
			continue
		}
		userIndex := firstUser + i
		if userIndex < 0 || userIndex >= len(users) {
			slog.Warn("desktop: retain unacknowledged projection without matching user turn")
			continue
		}
		turnID := projection.TurnID
		persistOrEnqueueDisplayWrite(tab.displayBufferState(), &pendingDisplayWrite{
			dir: controllerSessionDir(ctrl), sessionPath: ctrl.SessionPath(), userContent: users[userIndex], messages: messages,
			persist: func(dir, sessionPath, userContent string, messages []HistoryMessage) error {
				return recordSessionPlannerDisplayForTurn(dir, sessionPath, turnID, userContent, messages)
			},
			onPersisted: func() {
				if err := projectionCtrl.AcknowledgeTurnProjection(turnID); err != nil {
					slog.Warn("desktop: acknowledge recovered turn projection", "err", err)
				}
			},
			onRetry: func() {
				if observer, ok := ctrl.(interface{ ObserveTurnProjectionRetry() }); ok {
					observer.ObserveTurnProjectionRetry()
				}
			},
		})
	}
}

func ensureDisplayAssistant(buffer *displayTurnBuffer) *bufferedHistoryMessage {
	if n := len(buffer.messages); n > 0 && buffer.messages[n-1].message.Role == "assistant" {
		return buffer.messages[n-1]
	}
	message := &bufferedHistoryMessage{message: HistoryMessage{Role: "assistant"}}
	buffer.messages = append(buffer.messages, message)
	return message
}

func ensureDisplayAssistantForTool(buffer *displayTurnBuffer) *bufferedHistoryMessage {
	if n := len(buffer.messages); n > 0 && buffer.messages[n-1].message.Role == "assistant" && !buffer.messages[n-1].content.hasNonWhitespace() {
		return buffer.messages[n-1]
	}
	message := &bufferedHistoryMessage{message: HistoryMessage{Role: "assistant"}}
	buffer.messages = append(buffer.messages, message)
	return message
}

func updateBufferedHistoryToolCallSummary(messages []*bufferedHistoryMessage, callID, output string) {
	if callID == "" {
		return
	}
	for _, v := range slices.Backward(messages) {
		for j := range v.message.ToolCalls {
			call := &v.message.ToolCalls[j]
			if call.ID != callID {
				continue
			}
			if call.Summary == "" {
				call.Summary = historyToolSummary(call.Name, call.Arguments, output)
			}
			return
		}
	}
}

func plannerToolResultDisplay(content string, failed bool) (display, errPreview string) {
	if strings.TrimSpace(content) == "" {
		return "", ""
	}
	if failed || historyToolResultFailed(content) {
		display = clipHistoryToolPreview(strings.TrimSpace(content))
		return display, display
	}
	return "", ""
}

func (t *WorkspaceTab) takeDisplayTurn(cancelled bool) []HistoryMessage {
	state := t.displayBufferState()
	state.mu.Lock()
	defer state.mu.Unlock()
	out := state.planner.materialize()
	if !cancelled {
		out = append(out, state.executor.resultMessages()...)
	}
	if cancelled {
		out = append(out, state.executor.materialize()...)
		if len(out) > 0 {
			out = append(out, HistoryMessage{
				Role:    "notice",
				Level:   "info",
				Code:    event.NoticeCodeCancelledTurn,
				Content: "This turn was interrupted. Partial output is kept for reference; only completed tool pairs and a bounded recovery summary enter the next model turn. Inspect the workspace before continuing or reverting changes.",
			})
		}
	}
	state.planner.reset()
	state.executor.reset()
	return out
}

func enqueuePendingDisplayWrite(state *tabDisplayState, write *pendingDisplayWrite) {
	if state == nil || write == nil || write.persist == nil {
		return
	}
	state.mu.Lock()
	state.pendingWrites = append(state.pendingWrites, write)
	if state.persistRunning {
		state.mu.Unlock()
		return
	}
	state.persistRunning = true
	state.mu.Unlock()
	go retryPendingDisplayWrites(state)
}

func persistOrEnqueueDisplayWrite(state *tabDisplayState, write *pendingDisplayWrite) bool {
	if state == nil || write == nil || write.persist == nil {
		return true
	}
	state.mu.Lock()
	hasPending := len(state.pendingWrites) > 0
	state.mu.Unlock()
	if hasPending {
		enqueuePendingDisplayWrite(state, write)
		return false
	}
	if err := write.persist(write.dir, write.sessionPath, write.userContent, write.messages); err != nil {
		slog.Warn("desktop: persist display-only turn history; queued for retry", "err", err)
		if write.onRetry != nil {
			write.onRetry()
		}
		enqueuePendingDisplayWrite(state, write)
		return false
	}
	if write.onPersisted != nil {
		write.onPersisted()
	}
	return true
}

func retryPendingDisplayWrites(state *tabDisplayState) {
	failures := 0
	for {
		state.mu.Lock()
		if len(state.pendingWrites) == 0 {
			state.persistRunning = false
			state.mu.Unlock()
			return
		}
		write := state.pendingWrites[0]
		state.mu.Unlock()

		if failures > 0 {
			time.Sleep(time.Duration(failures*failures) * 50 * time.Millisecond)
		}
		if err := write.persist(write.dir, write.sessionPath, write.userContent, write.messages); err != nil {
			if write.onRetry != nil {
				write.onRetry()
			}
			failures++
			if failures < displayPersistRetryLimit {
				continue
			}
			state.mu.Lock()
			state.persistRunning = false
			state.mu.Unlock()
			slog.Warn("desktop: display-only turn history remains pending after retries", "err", err)
			return
		}

		state.mu.Lock()
		if len(state.pendingWrites) > 0 && state.pendingWrites[0] == write {
			state.pendingWrites[0] = nil
			state.pendingWrites = state.pendingWrites[1:]
		}
		state.mu.Unlock()
		if write.onPersisted != nil {
			write.onPersisted()
		}
		failures = 0
	}
}

// tabEventSink wraps a parent event.Sink and prepends a tabId to every wire
// event so the frontend can route it to the correct tab's reducer.
//
// tabID and app are rebound while the controller keeps emitting when a running
// session is detached to the background or reattached to another tab, so they
// live under mu like ctx does (a bare field write would data-race Emit). Read
// them via binding(), write via setBinding().
type tabEventSink struct {
	tabID             string
	app               *App
	mu                sync.RWMutex
	ctx               context.Context
	runtimeEpoch      string
	sessionGeneration uint64 // source session binding generation
	runtimeEvents     asyncRuntimeEmitter
	botSink           event.Sink // optional: when set, events are also forwarded here
	botSinkGen        uint64
	turn              turnSubmissionState // stays reserved through the end of TurnDone fan-out
	// takeoverMirror, when set, forwards every event to the serve that used to
	// own this session so the remote tab keeps rendering after a local
	// takeover. Atomic so Emit reads it without the sink lock.
	takeoverMirror atomic.Pointer[takeoverMirror]
}

// setTakeoverMirror installs (or clears) the session-takeover frame mirror.
func (s *tabEventSink) setTakeoverMirror(m *takeoverMirror) {
	if s == nil {
		return
	}
	s.takeoverMirror.Store(m)
}

type closeableEventSink interface {
	Close()
}

// binding snapshots the sink's current tab routing under the sink lock.
func (s *tabEventSink) binding() (string, *App) {
	if s == nil {
		return "", nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tabID, s.app
}

func (s *tabEventSink) runtimeEpochSnapshot() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtimeEpoch
}

func (s *tabEventSink) Emit(e event.Event) {
	// Typed-nil sinks can appear as non-nil event.Sink interfaces when a tab
	// controller is built before the tab sink binding is installed.
	if s == nil {
		return
	}
	if e.Kind == event.TurnStarted {
		s.mu.Lock()
		s.turn.inFlight = true
		s.mu.Unlock()
	}
	tabID, app := s.binding()
	var turnStartedAt int64
	if app != nil {
		if e.Kind == event.TurnDone {
			// Keep the legacy completion as a cheap missed-event safety net. The
			// hub owns the actual resource invalidation and coalesces this probe.
			app.reconcileWorkspaceForTab(tabID)
		}
		switch e.Kind {
		case event.TurnStarted:
			s.resetDisplayTurn()
			turnStartedAt = s.recordTurnStarted()
		case event.Usage:
			s.recordUsageTelemetry(e)
		case event.TurnDone:
			s.recordTurnDone()
		}
		if e.Kind == event.TurnDone {
			s.recordDisplay(e)
			s.flushDisplay(e.TurnID, e.Cancelled)
		}
		if m := app.metrics.Load(); m != nil {
			m.observe(e)
			persistMetricsEvent(app, m, tabID, e)
		}
	}
	s.emitRuntimeEvent(eventChannel, toWireTabWithSubmission(e, tabID, s.runtimeEpochSnapshot(), s.submissionIDSnapshot(), turnStartedAt, s.sessionGenerationSnapshot()))
	if m := s.takeoverMirror.Load(); m != nil {
		m.forwardEvent(e)
	}
	if app != nil {
		if status, update := topicActivityStatusFromEvent(e); update {
			changed := app.setTabActivityStatus(tabID, status)
			if changed || isBackgroundJobLifecycleNotice(e) {
				// Runtime status is an in-memory projection, not catalog metadata.
				// Publish it directly so a turn never fans out into one catalog read
				// per expanded project folder.
				app.emitProjectTreeRuntimeChangedWithLegacy()
			}
		}
	}
	// Record read_file successes in the tab's telemetry.
	if e.Kind == event.ToolResult && e.Tool.Name == "read_file" && e.Tool.Err == "" {
		s.recordReadTelemetry(e)
	}
	if app != nil && e.Kind != event.TurnDone {
		s.recordDisplay(e)
	}
	// Persist after each turn so a force-kill loses at most the in-flight prompt.
	if e.Kind == event.TurnDone && app != nil {
		app.scheduleTabSnapshot(tabID)
	}
	// Forward event to bot channels when a bot forwarder is attached.
	// Read the sink under the read lock so SetBotSink can safely swap it
	// from another goroutine.
	bs, botSinkGen := s.botSinkSnapshot()
	if bs != nil {
		bs.Emit(e)
		// Detach the forwarder after TurnDone so subsequent turns on the
		// same tab do not keep pushing to bot channels.
		if e.Kind == event.TurnDone {
			s.clearBotSink(botSinkGen)
		}
	}
	// Unlike the transient botSink above, the bridge observes every tab for
	// its whole lifetime (god view: /desktop status, watch subscriptions,
	// remote approvals). observe only does in-memory bookkeeping and queueing.
	if app != nil && app.botBridge != nil {
		app.botBridge.observe(tabID, e)
	}
	if e.Kind == event.TurnDone {
		s.mu.Lock()
		s.turn = turnSubmissionState{}
		s.mu.Unlock()
	}
}

// SetBotSink atomically sets or clears the bot event forwarder on this sink.
// It is safe to call concurrently with Emit.
func (s *tabEventSink) SetBotSink(sink event.Sink) uint64 {
	s.mu.Lock()
	old := s.botSink
	s.botSink = sink
	s.botSinkGen++
	generation := s.botSinkGen
	s.mu.Unlock()
	if old != nil && old != sink {
		if closer, ok := old.(closeableEventSink); ok {
			closer.Close()
		}
	}
	return generation
}

func (s *tabEventSink) botSinkSnapshot() (event.Sink, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.botSink, s.botSinkGen
}

// clearBotSink clears only the forwarder generation observed by the finishing
// turn. A delayed TurnDone must not detach a replacement installed meanwhile.
func (s *tabEventSink) clearBotSink(generation uint64) {
	s.mu.Lock()
	if s.botSinkGen != generation {
		s.mu.Unlock()
		return
	}
	old := s.botSink
	s.botSink = nil
	s.botSinkGen++
	s.mu.Unlock()
	if closer, ok := old.(closeableEventSink); ok {
		closer.Close()
	}
}

// tryBeginTurn reserves the tab until its TurnDone has finished fan-out. The
// controller clears RuntimeStatus().Running before it emits TurnDone, so the
// controller status alone leaves a window where a new turn can inherit the old
// turn's forwarder or have its replacement cleared by the old completion.
func (s *tabEventSink) tryBeginTurn(submissionID ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn.inFlight {
		return false
	}
	s.turn = turnSubmissionState{inFlight: true, submissionID: firstSubmissionID(submissionID)}
	return true
}

func (s *tabEventSink) cancelTurnStart() {
	s.mu.Lock()
	s.turn = turnSubmissionState{}
	s.mu.Unlock()
}

func (s *tabEventSink) setContext(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
}

func (s *tabEventSink) context() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ctx
}

func (s *tabEventSink) emitRuntimeEvent(name string, payload ...any) {
	if s == nil {
		return
	}
	ctx := s.context()
	if ctx == nil {
		return
	}
	s.runtimeEvents.Emit(ctx, name, payload...)
}

type runtimeEventEmitFunc func(context.Context, string, ...any)

type runtimeEventEnvelope struct {
	ctx     context.Context
	name    string
	payload []any
}

// asyncRuntimeEmitter decouples Wails' runtime event bridge from agent emission.
// runtime.EventsEmit can block when the single webview event channel backs up;
// callers enqueue in-order work and return without holding the agent event lock.
// runtimeEventsEmitFallback is the emit used when no per-instance override is
// installed. Production keeps the real Wails bridge; the test binary swaps in
// a no-op via TestMain, because Wails EventsEmit log.Fatals outside a running
// Wails app and would kill the whole test process from any code path that
// emits a runtime event with a plain Background context.
var runtimeEventsEmitFallback runtimeEventEmitFunc = runtime.EventsEmit

type asyncRuntimeEmitter struct {
	mu                     sync.Mutex
	emit                   runtimeEventEmitFunc
	queue                  []runtimeEventEnvelope
	head                   int
	running                bool
	configWarningsRevision atomic.Uint64
}

func (e *asyncRuntimeEmitter) Emit(ctx context.Context, name string, payload ...any) {
	if ctx == nil {
		return
	}
	item := runtimeEventEnvelope{
		ctx:     ctx,
		name:    name,
		payload: append([]any(nil), payload...),
	}
	e.mu.Lock()
	e.queue = append(e.queue, item)
	if !e.running {
		e.running = true
		go e.run()
	}
	e.mu.Unlock()
}

func (e *asyncRuntimeEmitter) Clear() {
	e.mu.Lock()
	clear(e.queue)
	e.queue = nil
	e.head = 0
	e.mu.Unlock()
}

func (e *asyncRuntimeEmitter) run() {
	for {
		e.mu.Lock()
		if e.head >= len(e.queue) {
			clear(e.queue)
			e.queue = nil
			e.head = 0
			e.running = false
			e.mu.Unlock()
			return
		}
		item := e.queue[e.head]
		var zero runtimeEventEnvelope
		e.queue[e.head] = zero
		e.head++
		if e.head > 64 && e.head*2 >= len(e.queue) {
			e.queue = append([]runtimeEventEnvelope(nil), e.queue[e.head:]...)
			e.head = 0
		}
		emit := e.emit
		if emit == nil {
			emit = runtimeEventsEmitFallback
		}
		e.mu.Unlock()

		emit(item.ctx, item.name, item.payload...)
	}
}

func topicActivityStatusFromEvent(e event.Event) (string, bool) {
	switch e.Kind {
	case event.TurnStarted, event.Reasoning, event.ToolDispatch, event.ToolProgress, event.ToolResultPreview, event.ToolResult, event.CompactionStarted, event.CompactionDone, event.Retrying:
		return topicStatusThinking, true
	case event.Text, event.Message:
		return topicStatusStreaming, true
	case event.ApprovalRequest, event.AskRequest:
		return topicStatusWaitingConfirmation, true
	case event.TurnDone:
		if status, ok := topicStatusFromTurnDone(e.Outcome); ok {
			return status, true
		}
		if e.Err != nil {
			return topicStatusError, true
		}
		return "", true
	case event.Notice:
		if isBackgroundJobLifecycleNotice(e) {
			return "", true
		}
		return "", false
	default:
		return "", false
	}
}

func isBackgroundJobLifecycleNotice(e event.Event) bool {
	if e.Kind != event.Notice {
		return false
	}
	text := strings.TrimSpace(e.Text)
	return strings.HasPrefix(text, "background ") &&
		(strings.Contains(text, " started: ") ||
			strings.Contains(text, " finished: ") ||
			strings.Contains(text, " failed: ") ||
			strings.Contains(text, " killed: "))
}

// notifyTabRuntimeRebuilt tells the frontend a tab's controller was replaced
// in place (model/effort/token-mode switch, clear-while-running). A rebuilt
// controller restarts its approval/ask id counter at "1", so tab-scoped
// frontend state keyed by prompt id (the attention-chime dedupe) must reset —
// unlike agent:ready, this event carries no reload semantics, so emitting it
// on every swap adds no hydration churn.
//
// Ordering matters: the reset must reach the frontend BEFORE the rebuilt
// controller's first approval/ask event, or the stale key still mutes it. The
// tab's agent events ride the tab sink's own async queue, so the notice goes
// through THAT queue — same lane, FIFO, guaranteed to arrive first. The
// App-level queue is only the fallback when the sink cannot deliver (no sink,
// or its webview context is cleared); it cannot order against sink traffic,
// but an unordered notice still beats none.
func (a *App) notifyTabRuntimeRebuilt(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	a.mu.Lock()
	epoch := a.advanceSessionRuntimeEpochLocked(tab)
	a.mu.Unlock()
	a.notifyTabRuntimeRebuiltAtEpoch(tab, epoch)
}

// notifyTabRuntimeRebuiltAtEpoch emits the rebuild fence for a transaction
// that advanced its epoch inside the controller/path/lease commit. Keeping the
// chosen epoch avoids a second generation bump after publication.
func (a *App) notifyTabRuntimeRebuiltAtEpoch(tab *WorkspaceTab, epoch string) {
	if tab == nil {
		return
	}
	a.mu.RLock()
	sink := tab.sink
	tabID := tab.ID
	ctrl, _ := tab.Ctrl.(*control.Controller)
	a.mu.RUnlock()
	if ctrl != nil {
		go ctrl.NotifyInboxRuntimeReady()
	}
	if sink != nil && sink.context() != nil {
		sink.emitRuntimeEvent("runtime:rebuilt", tabID, epoch)
		return
	}
	a.emitRuntimeEvent("runtime:rebuilt", tabID, epoch)
}

// replayPendingPromptsAfterRuntimeAttach publishes the runtime generation on
// the tab sink before asking the same controller to replay. Both events use the
// sink's FIFO queue, so the frontend cannot reject a valid prompt as belonging
// to the runtime that was just replaced.
func (a *App) replayPendingPromptsAfterRuntimeAttach(tabID string, sink *tabEventSink, ctrl control.SessionAPI, epoch string) {
	if ctrl == nil {
		return
	}
	if sink != nil && sink.context() != nil {
		// Use the sink captured in the same App.mu commit as ctrl. Re-reading the
		// tab here would let a concurrent replacement put the fence on a newer
		// sink while this older controller replays on the transferred one.
		sink.emitRuntimeEvent("runtime:rebuilt", tabID, epoch)
	} else {
		a.emitRuntimeEvent("runtime:rebuilt", tabID, epoch)
	}
	ctrl.ReplayPendingPrompts()
}

func (a *App) emitReady(ctx context.Context, tabID ...string) {
	a.mu.RLock()
	hook := a.readyHook
	a.mu.RUnlock()
	if hook != nil {
		hook()
		return
	}
	if ctx != nil {
		if len(tabID) > 0 && strings.TrimSpace(tabID[0]) != "" {
			a.runtimeEvents.Emit(ctx, "agent:ready", strings.TrimSpace(tabID[0]))
			return
		}
		a.runtimeEvents.Emit(ctx, "agent:ready")
	}
}

func (s *tabEventSink) recordReadTelemetry(e event.Event) {
	tabID, app := s.binding()
	if app == nil {
		return
	}
	app.mu.RLock()
	tab := app.tabByEventSinkIDLocked(tabID)
	var ctrl control.SessionAPI
	if tab != nil {
		ctrl = tab.Ctrl
	}
	app.mu.RUnlock()
	if tab == nil {
		return
	}
	turn := 0
	if ctrl != nil {
		turn = ctrl.Turn()
	}

	// Parse read_file args: {"path": "...", "offset": N, "limit": N}
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	path := e.Tool.Args
	offset := 0
	limit := 0
	if err := json.Unmarshal([]byte(e.Tool.Args), &args); err == nil && args.Path != "" {
		path = args.Path
		offset = args.Offset
		limit = args.Limit
	}

	truncated := e.Tool.Truncated || strings.Contains(e.Tool.Output, "truncated") ||
		strings.Contains(e.Tool.Output, "File truncated")

	sp := ""
	if ctrl != nil {
		sp = ctrl.SessionPath()
	}
	if sp != "" {
		tab.syncTelemetryToSession(sp)
	}
	tab.recordReadFile(readFileRecord{
		Path:      path,
		Turn:      turn,
		Time:      time.Now().UnixMilli(),
		Offset:    offset,
		Limit:     limit,
		Truncated: truncated,
	})
	if sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
}

func (s *tabEventSink) recordTurnStarted() int64 {
	tab, sp := s.telemetryTab()
	if tab == nil {
		return 0
	}
	if sp != "" {
		tab.syncTelemetryToSession(sp)
	}
	startedAt := tab.recordTurnStarted(time.Now().UnixMilli())
	if sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
	return startedAt
}

func (s *tabEventSink) recordTurnDone() {
	tab, sp := s.telemetryTab()
	if tab == nil {
		return
	}
	if sp != "" {
		tab.syncTelemetryToSession(sp)
	}
	tab.recordTurnDone(time.Now().UnixMilli())
	if sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
}

func (s *tabEventSink) recordUsageTelemetry(e event.Event) {
	tab, sp := s.telemetryTab()
	if tab == nil {
		return
	}
	if sp != "" {
		tab.syncTelemetryToSession(sp)
	}
	tab.recordUsage(e)
	if sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
}

func (s *tabEventSink) resetDisplayTurn() {
	tab, _ := s.eventTabAndController()
	if tab != nil {
		tab.resetDisplayTurn()
	}
}

func (s *tabEventSink) recordDisplay(e event.Event) {
	tab, _ := s.eventTabAndController()
	if tab != nil {
		tab.recordDisplayEvent(e)
	}
}

func (s *tabEventSink) flushDisplay(turnID string, cancelRequested bool) bool {
	tab, ctrl := s.eventTabAndController()
	if tab == nil || ctrl == nil {
		return false
	}
	history := ctrl.History()
	keepExecutorDisplay := cancelRequested && (lastHistoryMessageIsUser(history) || hasPendingInterruptedRecovery(history))
	messages := tab.takeDisplayTurn(keepExecutorDisplay)
	if len(messages) == 0 {
		acknowledgeProjectionForController(ctrl, turnID)
		return true
	}
	sessionPath := ctrl.SessionPath()
	if sessionPath == "" {
		return false
	}
	userContent := lastUserMessageContent(history)
	if strings.TrimSpace(userContent) == "" {
		return false
	}
	return persistOrEnqueueDisplayWrite(tab.displayBufferState(), &pendingDisplayWrite{
		dir:         controllerSessionDir(ctrl),
		sessionPath: sessionPath,
		userContent: userContent,
		messages:    messages,
		persist: func(dir, sessionPath, userContent string, messages []HistoryMessage) error {
			return recordSessionPlannerDisplayForTurn(dir, sessionPath, turnID, userContent, messages)
		},
		onPersisted: func() { acknowledgeProjectionForController(ctrl, turnID) },
		onRetry:     func() { observeProjectionRetryForController(ctrl) },
	})
}

func observeProjectionRetryForController(ctrl control.SessionAPI) {
	if ctrl == nil {
		return
	}
	if observer, ok := ctrl.(interface{ ObserveTurnProjectionRetry() }); ok {
		observer.ObserveTurnProjectionRetry()
	}
}

func acknowledgeProjectionForController(ctrl control.SessionAPI, turnID string) {
	if ctrl == nil || strings.TrimSpace(turnID) == "" {
		return
	}
	if ack, ok := ctrl.(interface{ AcknowledgeTurnProjection(string) error }); ok {
		if err := ack.AcknowledgeTurnProjection(turnID); err != nil {
			slog.Warn("desktop: acknowledge turn display projection", "err", err)
		}
	}
}

func lastHistoryMessageIsUser(history []provider.Message) bool {
	return len(history) > 0 && agent.IsUserAuthoredTurnMessage(history[len(history)-1])
}

func hasPendingInterruptedRecovery(history []provider.Message) bool {
	for _, v := range slices.Backward(history) {
		m := v
		if m.LocalOnly && m.InterruptedTurn != nil {
			return m.InterruptedTurn.Pending
		}
		if agent.IsUserAuthoredTurnMessage(m) {
			return false
		}
	}
	return false
}

func (s *tabEventSink) eventTabAndController() (*WorkspaceTab, control.SessionAPI) {
	tabID, app := s.binding()
	if app == nil {
		return nil, nil
	}
	app.mu.RLock()
	defer app.mu.RUnlock()
	tab := app.tabByEventSinkIDLocked(tabID)
	if tab == nil {
		return nil, nil
	}
	return tab, tab.Ctrl
}

func lastUserMessageContent(msgs []provider.Message) string {
	for _, v := range slices.Backward(msgs) {
		if agent.IsUserAuthoredTurnMessage(v) {
			return agent.UserMessageText(v)
		}
	}
	return ""
}

func (s *tabEventSink) telemetryTab() (*WorkspaceTab, string) {
	tabID, app := s.binding()
	if app == nil {
		return nil, ""
	}
	app.mu.RLock()
	tab := app.tabByEventSinkIDLocked(tabID)
	var ctrl control.SessionAPI
	if tab != nil {
		ctrl = tab.Ctrl
	}
	app.mu.RUnlock()
	if tab == nil {
		return nil, ""
	}
	if ctrl == nil {
		return tab, ""
	}
	sp := ctrl.SessionPath()
	if sp == "" {
		return tab, ""
	}
	return tab, sp
}

// wire event with tab

func toWireTab(e event.Event, tabID string, runtimeEpoch ...string) wireEventTab {
	w := eventwire.ToWire(e)
	epoch := ""
	if len(runtimeEpoch) > 0 {
		epoch = runtimeEpoch[0]
	}
	return wireEventTab{
		Event:             w,
		TabID:             tabID,
		RuntimeEpoch:      epoch,
		SessionHitTokens:  e.SessionHit,
		SessionMissTokens: e.SessionMiss,
		SessionCost:       0, // filled by frontend accumulator per tab
		SessionCurrency:   "",
		SessionCostUsd:    0, // deprecated compatibility alias
	}
}

// wireEventTab extends the shared event wire with tab routing info. The frontend reducer
// uses tabId to dispatch to the correct per-tab state.
type wireEventTab struct {
	eventwire.Event
	TabID             string `json:"tabId"`
	RuntimeEpoch      string `json:"runtimeEpoch,omitempty"`
	SessionGeneration uint64 `json:"sessionGeneration,omitempty"`
	TurnStartedAt     int64  `json:"turnStartedAt,omitempty"`
	// Session-cumulative tokens per tab.
	SessionHitTokens  int `json:"sessionHitTokens,omitempty"`
	SessionMissTokens int `json:"sessionMissTokens,omitempty"`
	// SessionCost is filled by the frontend's per-tab accumulator.
	SessionCost     float64 `json:"sessionCost,omitempty"`
	SessionCurrency string  `json:"sessionCurrency,omitempty"`
	// SessionCostUsd is a deprecated compatibility alias. It mirrors
	// SessionCost and does not imply USD.
	SessionCostUsd float64 `json:"sessionCostUsd,omitempty"`
}

// Tab management on App

func enrichTabMeta(meta TabMeta) TabMeta {
	if meta.Active {
		meta.GitBranch = workspaceGitBranchForMeta(meta.WorkspaceRoot)
	}
	return meta
}

func enrichTabMetas(metas []TabMeta) []TabMeta {
	for i := range metas {
		if metas[i].Active {
			metas[i].GitBranch = workspaceGitBranchForMeta(metas[i].WorkspaceRoot)
		}
	}
	return metas
}

func (a *App) tabMeta(tab *WorkspaceTab, active bool) TabMeta {
	runtimeView := a.sessionRuntimeViewLocked(tab)
	sessionPath := tab.currentSessionPath()
	var sessionRevision int64
	var sessionDigest string
	if meta, ok, err := agent.LoadBranchMeta(sessionPath); err == nil && ok {
		sessionRevision = meta.Revision
		sessionDigest = meta.ContentDigest
	}
	floor := derivedQualityFloor(tab)
	m := TabMeta{
		ID:                tab.ID,
		Scope:             tab.Scope,
		WorkspaceRoot:     tab.WorkspaceRoot,
		WorkspaceName:     workspaceName(tab.WorkspaceRoot),
		WorkspacePath:     tab.WorkspaceRoot,
		TopicID:           tab.TopicID,
		TopicTitle:        a.localizedTopicTitle(tab.TopicTitle, tab.topicTitleSource),
		SessionPath:       sessionPath,
		SessionRevision:   sessionRevision,
		SessionDigest:     sessionDigest,
		SessionGeneration: tab.SessionGeneration,
		ReadOnly:          tab.ReadOnly,
		TakenOver:         tab.Takeover.Spectator,
		Label:             tab.Label,
		Ready:             runtimeView.Phase == sessionRuntimeReady && tab.Ctrl != nil,
		Runtime:           runtimeView,
		TurnStartedAt:     tab.turnStartedAt(),
		Mode:              currentTabMode(tab),
		CollaborationMode: currentTabCollaborationMode(tab),
		ToolApprovalMode:  currentTabToolApprovalMode(tab),
		QualityFloor:      floor.floor,
		FloorInferred:     floor.inferred,
		AgentPreset:       agentPresetForFloor(floor.floor),
		TokenMode:         tokenModeForFloor(floor.floor),
		Goal:              currentTabGoal(tab),
		GoalStatus:        currentTabGoalStatus(tab),
		StartupErr:        tab.StartupErr,
		Active:            active,
		Cwd:               tab.WorkspaceRoot,
		IsolatedWorktree:  floor.isolated,
	}
	switch tab.Scope {
	case "global":
		m.ProjectColor = globalProjectColor()
		m.WorkspaceName = globalProjectTitle()
	case "project":
		m.ProjectColor = projectColor(tab.WorkspaceRoot)
	}
	if tab.Ctrl != nil {
		status := tab.Ctrl.RuntimeStatus()
		m.Running = status.Running || status.PendingPrompt || status.BackgroundJobs > 0
		m.PendingPrompt = status.PendingPrompt
		m.BackgroundJobs = status.BackgroundJobs
		m.CancelRequested = status.CancelRequested
		m.Cancellable = status.Cancellable
		m.TurnID = status.TurnID
		m.TurnStatus = string(status.Status)
		m.TurnEventSeq = status.TurnEventSeq
		m.TurnReplayAfter = status.ReplayAfterSeq
	}
	if a.botBridge != nil {
		m.RemoteControlled = a.botBridge.remoteControlledTabs()[tab.ID]
	}
	if meta, ok, err := agent.LoadBranchMeta(tab.currentSessionPath()); err == nil && ok {
		m.VersionKind = string(meta.EffectiveVersionKind())
		m.VersionState = string(meta.EffectiveVersionState())
		m.ParentVersionID = meta.ParentVersionID
		if meta.Recovered {
			m.Recovered = true
			m.RecoveryReason = meta.RecoveryReason
			m.RecoveryDigest = meta.RecoveryDigest
			m.RecoveryParentID = string(meta.ParentID)
		}
	}
	return m
}

// ListTabs returns every open view container's metadata for the frontend chrome and sidebar.
func (a *App) ListTabs() []TabMeta {
	a.mu.RLock()
	out := make([]TabMeta, 0, len(a.tabs))
	ordered, needsRepair := a.orderedTabIDsSnapshotLocked()
	for _, id := range ordered {
		if tab := a.tabs[id]; tab != nil {
			out = append(out, a.tabMeta(tab, tab.ID == a.activeTabID))
		}
	}
	a.mu.RUnlock()
	if !needsRepair {
		return a.listTabsWithRemote(out)
	}

	a.mu.Lock()
	out = make([]TabMeta, 0, len(a.tabs))
	for _, id := range a.orderedTabIDsLocked() {
		if tab := a.tabs[id]; tab != nil {
			out = append(out, a.tabMeta(tab, tab.ID == a.activeTabID))
		}
	}
	a.mu.Unlock()
	return a.listTabsWithRemote(out)
}

// syncTabWorkspaceRootSpellings repoints visible and detached project runtimes
// at the registry spelling. Registry writes may adopt the caller's spelling,
// while the frontend compares roots exactly. Callers must not hold a.mu.
func (a *App) syncTabWorkspaceRootSpellings() {
	projects := loadProjectsFile().Projects
	a.mu.Lock()
	changed := false
	for _, tab := range a.tabs {
		changed = syncRuntimeWorkspaceRootSpelling(tab, projects) || changed
	}
	for _, tab := range a.detachedSessions {
		changed = syncRuntimeWorkspaceRootSpelling(tab, projects) || changed
	}
	if changed {
		a.saveTabsLocked()
	}
	a.mu.Unlock()
	if changed {
		a.emitProjectTreeMetadataChanged()
	}
}

// OpenProjectTab builds a controller scoped to workspaceRoot and opens the
// session selected by the given topic. Topic selection resolves to a concrete
// session path first; the visible tab is then attached to that session runtime.
func (a *App) OpenProjectTab(workspaceRoot, topicID string) (TabMeta, error) {
	return a.openProjectTab(workspaceRoot, topicID)
}

func (a *App) openProjectTab(workspaceRoot, topicID string) (TabMeta, error) {
	if workspaceRoot == "" {
		return TabMeta{}, fmt.Errorf("workspaceRoot is required")
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		workspaceRoot = abs
	}

	sessionPath, _ := a.findTopicSessionForTarget("project", workspaceRoot, topicID)
	return a.openTopicTabWithActivation("project", workspaceRoot, topicID, sessionPath, true)
}

func (a *App) openTopicTab(scope, workspaceRoot, topicID, sessionPath string) (TabMeta, error) {
	return a.openTopicTabPreferLiveActivation(scope, workspaceRoot, topicID, sessionPath, true)
}

func (a *App) openProjectTabInactive(workspaceRoot, topicID string) (TabMeta, error) {
	if workspaceRoot == "" {
		return TabMeta{}, fmt.Errorf("workspaceRoot is required")
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		workspaceRoot = abs
	}

	sessionPath, _ := a.findTopicSessionForTarget("project", workspaceRoot, topicID)
	return a.openTopicTabWithActivation("project", workspaceRoot, topicID, sessionPath, false)
}

func (a *App) openGlobalTabInactive(topicID string) (TabMeta, error) {
	globalRoot := globalWorkspaceRoot()
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		return TabMeta{}, fmt.Errorf("create global workspace: %w", err)
	}

	sessionPath, _ := a.findTopicSessionForTarget("global", "", topicID)
	return a.openTopicTabWithActivation("global", "", topicID, sessionPath, false)
}

func (a *App) openTopicTabWithActivation(scope, workspaceRoot, topicID, sessionPath string, activate bool) (TabMeta, error) {
	actualRoot, sessionPath := a.resolveOpenTopicSessionPath(scope, workspaceRoot, sessionPath)
	releaseAdmission, err := a.beginProjectRuntimeAdmission(scope, actualRoot)
	if err != nil {
		return TabMeta{}, err
	}
	defer releaseAdmission()
	if strings.TrimSpace(scope) == "project" {
		saveWorkspace(actualRoot)
		a.registerProjectRoot(actualRoot)
	}
	targetKey := sessionRuntimeKey(sessionPath)

	a.mu.Lock()
	if targetKey != "" {
		for _, tab := range a.tabs {
			if tab == nil {
				continue
			}
			if sessionRuntimeKey(tab.currentSessionPath()) == targetKey {
				if activate {
					a.activeTabID = tab.ID
				}
				meta := a.tabMeta(tab, tab.ID == a.activeTabID)
				a.saveTabsLocked()
				a.mu.Unlock()
				return enrichTabMeta(meta), nil
			}
		}
	}

	for _, tab := range a.tabs {
		if tabMatchesTopicTarget(tab, scope, workspaceRoot, topicID) {
			if activate {
				a.activeTabID = tab.ID
			}
			sameSession := targetKey == "" || sessionRuntimeKey(tab.currentSessionPath()) == targetKey
			meta := a.tabMeta(tab, tab.ID == a.activeTabID)
			a.saveTabsLocked()
			a.mu.Unlock()
			if sameSession || a.skipContinuationRebind(tab, sessionPath) {
				return enrichTabMeta(meta), nil
			}
			if err := a.rebindTabToSessionPath(tab, sessionPath); err != nil {
				return TabMeta{}, err
			}
			a.mu.RLock()
			meta = a.tabMeta(tab, tab.ID == a.activeTabID)
			a.mu.RUnlock()
			return enrichTabMeta(meta), nil
		}
	}

	tabID := a.newUniqueTabIDLocked()
	topicTitle := topicTitleForTab(scope, workspaceRoot, topicID)
	if t, source, ok := topicTitleFallbackForOpen(workspaceRoot, topicID, sessionPath); ok {
		topicTitle = t
		_ = setTopicTitleWithSource(workspaceRoot, topicID, t, source)
	}

	if sessionPath == "" {
		var err error
		sessionPath, err = createEmptySessionFile(desktopSessionDir(actualRoot), "")
		if err != nil {
			a.mu.Unlock()
			return TabMeta{}, err
		}
		if err := pinNewEmptySessionBranchMeta(sessionPath, scope, actualRoot, topicID, topicTitle); err != nil {
			a.mu.Unlock()
			return TabMeta{}, err
		}
	}
	profile := loadTabSessionProfile(sessionPath)
	tab := &WorkspaceTab{
		ID:               tabID,
		Scope:            scope,
		WorkspaceRoot:    actualRoot,
		TopicID:          topicID,
		TopicTitle:       topicTitle,
		topicTitleSource: loadTopicTitleSource(topicTitleRoot(scope, workspaceRoot), topicID),
		SessionPath:      sessionPath,
		disabledMCP:      map[string]ServerView{},
	}
	applyTabSessionProfile(tab, profile)
	tab.sink = &tabEventSink{tabID: tabID, app: a}

	a.tabs[tabID] = tab
	a.tabOrder = append(a.tabOrder, tabID)
	if activate {
		a.activeTabID = tabID
	}
	a.saveTabsLocked()
	meta := a.tabMeta(tab, tab.ID == a.activeTabID)
	a.mu.Unlock()

	a.startTabControllerBuild(tab)
	if scope == "project" {
		a.emitProjectTreeRuntimeChangedWithLegacy()
	}
	return enrichTabMeta(meta), nil
}

// OpenGlobalTab opens a new global-scope tab (no project root). The global
// workspace root is the reasonix user config directory.
func (a *App) OpenGlobalTab(topicID string) (TabMeta, error) {
	return a.openGlobalTab(topicID)
}

func (a *App) openGlobalTab(topicID string) (TabMeta, error) {
	globalRoot := globalWorkspaceRoot()
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		return TabMeta{}, fmt.Errorf("create global workspace: %w", err)
	}

	sessionPath, _ := a.findTopicSessionForTarget("global", "", topicID)
	return a.openTopicTabWithActivation("global", "", topicID, sessionPath, true)
}

// OpenTopicSession opens a concrete saved session from the sidebar. Unlike
// OpenProjectTab/OpenGlobalTab, it does not resolve the topic to the latest
// session first; sessionPath is the runtime identity being selected.
func (a *App) OpenTopicSession(scope, workspaceRoot, topicID, sessionPath string) (TabMeta, error) {
	return a.openTopicSession(scope, workspaceRoot, topicID, sessionPath)
}

func (a *App) openTopicSession(scope, workspaceRoot, topicID, sessionPath string) (TabMeta, error) {
	scope = strings.TrimSpace(scope)
	if scope != "project" {
		scope = "global"
		workspaceRoot = ""
	}
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
		if workspaceRoot == "" {
			return TabMeta{}, fmt.Errorf("workspaceRoot is required")
		}
	}
	_, validPath, err := a.sessionDirForPath(sessionPath)
	if err != nil {
		return TabMeta{}, err
	}
	return a.openTopicTab(scope, workspaceRoot, topicID, validPath)
}

// ActivateTopic opens a topic into the single visible conversation surface used
// by layouts without a tab strip. It delegates the actual open/reuse behavior to
// the classic tab path, then prunes every non-active visible tab so historical
// clicks do not accumulate hidden startup work.
//
// Interop with StartTopicActivation: a legacy ActivateTopic call supersedes any
// pending ticketed activation (its background completion becomes a no-op and a
// "cancelled" event is emitted for the old requestId), and ticketed
// activations supersede each other the same way. The synchronous return
// contract — TabMeta after the prune — is unchanged.
func (a *App) ActivateTopic(scope, workspaceRoot, topicID, sessionPath string) (TabMeta, error) {
	a.singleSurfaceMu.Lock()
	defer a.singleSurfaceMu.Unlock()

	var meta TabMeta
	var err error
	if strings.TrimSpace(sessionPath) != "" {
		meta, err = a.openTopicSession(scope, workspaceRoot, topicID, sessionPath)
	} else if strings.TrimSpace(scope) == "project" {
		meta, err = a.openProjectTab(workspaceRoot, topicID)
	} else {
		meta, err = a.openGlobalTab(topicID)
	}
	if err != nil {
		return TabMeta{}, err
	}
	// A legacy activation supersedes any pending ticketed activation: its
	// completion must not prune or publish after this call's own prune.
	if reqID, tabID := a.supersedePendingTopicActivation(meta.ID); reqID != "" {
		a.emitTopicActivation(TopicActivationEvent{RequestID: reqID, TabID: tabID, Phase: topicActivationPhaseCancelled})
	}
	return a.keepOnlyVisibleTab(meta.ID)
}

// EnsureBlankSurface mirrors EnsureBlankTab for no-tab-strip layouts: after
// creating or reusing a blank session, it removes other visible tabs while
// preserving running runtimes as detached background sessions.
func (a *App) EnsureBlankSurface(scope, workspaceRoot string) (TabMeta, error) {
	return a.ensureBlankSurface(scope, workspaceRoot)
}

func (a *App) ensureBlankSurface(scope, workspaceRoot string) (TabMeta, error) {
	a.singleSurfaceMu.Lock()
	defer a.singleSurfaceMu.Unlock()

	meta, err := a.ensureBlankTab(scope, workspaceRoot)
	if err != nil {
		return TabMeta{}, err
	}
	// Same interop rule as ActivateTopic: this synchronous surface switch
	// supersedes any pending ticketed activation.
	if reqID, tabID := a.supersedePendingTopicActivation(meta.ID); reqID != "" {
		a.emitTopicActivation(TopicActivationEvent{RequestID: reqID, TabID: tabID, Phase: topicActivationPhaseCancelled})
	}
	return a.keepOnlyVisibleTab(meta.ID)
}

func tabMatchesTopicTarget(tab *WorkspaceTab, scope, workspaceRoot, topicID string) bool {
	if tab == nil || tab.Scope != scope || tab.TopicID != topicID {
		return false
	}
	if scope == "global" {
		return true
	}
	return sameProjectRoot(tab.WorkspaceRoot, workspaceRoot)
}

func tabInWorkspace(tab *WorkspaceTab, workspaceRoot string) bool {
	return tab != nil &&
		tab.Scope == "project" &&
		sameProjectRoot(tab.WorkspaceRoot, workspaceRoot)
}

// EnsureBlankTab activates the existing blank tab for the target scope, or
// creates one if none exists. Reusing a blank tab keeps repeated "new session"
// clicks from piling up empty conversations.
func (a *App) EnsureBlankTab(scope, workspaceRoot string) (TabMeta, error) {
	return a.ensureBlankTab(scope, workspaceRoot)
}

func (a *App) ensureBlankTab(scope, workspaceRoot string) (TabMeta, error) {
	scope = strings.TrimSpace(scope)
	if scope != "project" {
		scope = "global"
	}

	globalRoot := ""
	if scope == "project" {
		workspaceRoot = strings.TrimSpace(workspaceRoot)
		if workspaceRoot == "" {
			return TabMeta{}, fmt.Errorf("workspaceRoot is required")
		}
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = abs
		}
	} else {
		workspaceRoot = ""
		globalRoot = globalWorkspaceRoot()
		if err := os.MkdirAll(globalRoot, 0o755); err != nil {
			return TabMeta{}, fmt.Errorf("create global workspace: %w", err)
		}
	}

	var created *WorkspaceTab
	// Compute actual root early — both the indexed-topic fallback and the
	// new-topic path need it when constructing the tab below.
	actualRoot := workspaceRoot
	if scope == "global" {
		actualRoot = globalRoot
	}
	releaseAdmission, err := a.beginProjectRuntimeAdmission(scope, actualRoot)
	if err != nil {
		return TabMeta{}, err
	}
	defer releaseAdmission()
	if scope == "project" {
		saveWorkspace(workspaceRoot)
		a.registerProjectRoot(workspaceRoot)
	}
	defaultModel, defaultToolApprovalMode := desktopNewSessionDefaults(scope, actualRoot)

	a.mu.Lock()
	var reusable *WorkspaceTab
	for _, id := range a.orderedTabIDsLocked() {
		tab := a.tabs[id]
		if a.blankTabMatchesTargetLocked(tab, scope, workspaceRoot) {
			if err := resetReusableBlankTabTitle(tab, scope, workspaceRoot); err != nil {
				a.mu.Unlock()
				return TabMeta{}, err
			}
			reusable = tab
			break
		}
	}
	if reusable != nil {
		a.mu.Unlock()
		if err := a.alignReusableBlankTabModel(reusable, defaultModel); err != nil {
			return TabMeta{}, err
		}
		a.mu.Lock()
		if reusable.removed || a.tabs[reusable.ID] != reusable {
			a.mu.Unlock()
			return TabMeta{}, fmt.Errorf("blank session changed while applying the default model; retry")
		}
		a.activeTabID = reusable.ID
		meta := a.tabMeta(reusable, true)
		a.saveTabsLocked()
		a.mu.Unlock()
		return enrichTabMeta(meta), nil
	}

	// New blank sessions start from global defaults for model and approval
	// posture, keeping execution-local settings (effort/floor/MCP) from the
	// active tab without letting it override global defaults (#4019).
	inheritedModel := defaultModel
	var inheritedEffort *string
	inheritedFloor := tabQualityFloor(workspaceRoot, a.activeTabLocked().qualityFloorSafe())
	inheritedMode := tabModeFromAxes(false, defaultToolApprovalMode == control.ToolApprovalYolo)
	inheritedToolApprovalMode := defaultToolApprovalMode
	inheritedDisabledMCP := map[string]ServerView{}
	var inheritedMCPOrder []string
	if active := a.activeTabLocked(); active != nil {
		inheritedEffort = cloneStringPtr(active.effort)
		inheritedDisabledMCP = cloneServerViewMap(active.disabledMCP)
		inheritedMCPOrder = append([]string(nil), active.mcpOrder...)
	}

	if topicID := a.indexedBlankTopicIDLocked(scope, workspaceRoot); topicID != "" {
		// Reuse a previously-indexed but unused blank topic instead of
		// creating a new one.  Build it inline (not via OpenProjectTab /
		// OpenGlobalTab) so it inherits settings from the active tab.
		if loadTopicCreatedAt(topicTitleRoot(scope, workspaceRoot), topicID) <= 0 {
			createdAt := topicIDCreatedAt(topicID)
			if createdAt <= 0 {
				createdAt = time.Now().UnixMilli()
			}
			_ = setTopicCreatedAt(topicTitleRoot(scope, workspaceRoot), topicID, createdAt)
		}
		tabID := a.newUniqueTabIDLocked()
		topicTitle := topicTitleForTab(scope, workspaceRoot, topicID)
		created = &WorkspaceTab{
			ID:               tabID,
			Scope:            scope,
			WorkspaceRoot:    actualRoot,
			TopicID:          topicID,
			TopicTitle:       topicTitle,
			topicTitleSource: loadTopicTitleSource(topicTitleRoot(scope, workspaceRoot), topicID),
			model:            inheritedModel,
			effort:           inheritedEffort,
			qualityFloor:     inheritedFloor,
			mode:             inheritedMode,
			toolApprovalMode: inheritedToolApprovalMode,
			disabledMCP:      inheritedDisabledMCP,
			mcpOrder:         inheritedMCPOrder,
		}
		created.sink = &tabEventSink{tabID: tabID, app: a}
		a.tabs[tabID] = created
		a.tabOrder = append(a.tabOrder, tabID)
		a.activeTabID = tabID
		prePath, err := createEmptySessionFile(desktopSessionDir(actualRoot), inheritedModel)
		if err != nil {
			delete(a.tabs, tabID)
			a.removeTabOrderLocked(tabID)
			a.mu.Unlock()
			return TabMeta{}, err
		}
		if err := pinNewEmptySessionBranchMeta(prePath, scope, actualRoot, topicID, topicTitle); err != nil {
			delete(a.tabs, tabID)
			a.removeTabOrderLocked(tabID)
			a.mu.Unlock()
			return TabMeta{}, err
		}
		created.SessionPath = prePath
		a.saveTabsLocked()
		meta := a.tabMeta(created, true)
		a.mu.Unlock()

		a.startTabControllerBuild(created)
		a.emitProjectTreeChangedForSessionDirs(sessionDirectoryForPath(prePath))
		return enrichTabMeta(meta), nil
	}

	topicID := newTopicID()
	topicTitle := defaultTopicTitle
	createdAt := time.Now().UnixMilli()
	if err := createTopicState(workspaceRoot, topicID, topicTitle, topicTitleSourceAuto, createdAt); err != nil {
		a.mu.Unlock()
		return TabMeta{}, err
	}
	_ = prependTopicInProjectsFile(workspaceRoot, topicID, false)

	tabID := a.newUniqueTabIDLocked()
	created = &WorkspaceTab{
		ID:               tabID,
		Scope:            scope,
		WorkspaceRoot:    actualRoot,
		TopicID:          topicID,
		TopicTitle:       topicTitleForTab(scope, workspaceRoot, topicID),
		topicTitleSource: topicTitleSourceAuto,
		model:            inheritedModel,
		effort:           inheritedEffort,
		qualityFloor:     inheritedFloor,
		mode:             inheritedMode,
		toolApprovalMode: inheritedToolApprovalMode,
		disabledMCP:      inheritedDisabledMCP,
		mcpOrder:         inheritedMCPOrder,
	}
	created.sink = &tabEventSink{tabID: tabID, app: a}
	a.tabs[tabID] = created
	a.tabOrder = append(a.tabOrder, tabID)
	a.activeTabID = tabID
	prePath, err := createEmptySessionFile(desktopSessionDir(actualRoot), inheritedModel)
	if err != nil {
		delete(a.tabs, tabID)
		a.removeTabOrderLocked(tabID)
		a.mu.Unlock()
		return TabMeta{}, err
	}
	if err := pinNewEmptySessionBranchMeta(prePath, scope, actualRoot, topicID, topicTitle); err != nil {
		delete(a.tabs, tabID)
		a.removeTabOrderLocked(tabID)
		a.mu.Unlock()
		return TabMeta{}, err
	}
	created.SessionPath = prePath
	a.saveTabsLocked()
	meta := a.tabMeta(created, true)
	a.mu.Unlock()

	a.startTabControllerBuild(created)
	a.emitProjectTreeChangedForSessionDirs(sessionDirectoryForPath(prePath))
	return enrichTabMeta(meta), nil
}

// alignReusableBlankTabModel makes a reused empty session obey the same
// provider/model default as a newly-created session. Ready runtimes use the
// normal failure-atomic model switch. A tab that is still starting has no
// controller to swap, so invalidate its startup generation, update the empty
// session's model metadata, and restart the build from the intended provider.
func (a *App) alignReusableBlankTabModel(tab *WorkspaceTab, model string) error {
	model = strings.TrimSpace(model)
	if tab == nil || model == "" {
		return nil
	}

	a.mu.RLock()
	if tab.removed || a.tabs[tab.ID] != tab {
		a.mu.RUnlock()
		return fmt.Errorf("blank session changed while applying the default model; retry")
	}
	currentModel := strings.TrimSpace(tab.model)
	ctrl := tab.Ctrl
	path := strings.TrimSpace(tab.SessionPath)
	a.mu.RUnlock()

	storedModel, hasStoredModel := agent.LoadSessionModel(path)
	storedModelChanged := path != "" && (!hasStoredModel || strings.TrimSpace(storedModel) != model)

	if ctrl != nil {
		if currentModel != model {
			if err := a.SetModelForTab(tab.ID, model); err != nil {
				return err
			}
		} else if storedModelChanged {
			return a.persistTabModelIfCurrent(tab, model)
		}
		return nil
	}

	if currentModel == model && !storedModelChanged {
		return nil
	}
	if storedModelChanged {
		// With no published controller there is nothing to swap atomically. Fix
		// the empty session metadata first so the replacement startup cannot
		// prefer the outgoing provider over the corrected tab model.
		if err := agent.SetBranchModelPreserveUpdated(path, model); err != nil {
			return fmt.Errorf("persist default model for blank session: %w", err)
		}
	}

	// A startup build may already have read the old sidecar model. Fence and
	// cancel that generation before publishing the corrected tab model, then
	// start a replacement build. The generation check prevents the cancelled
	// build from overwriting the replacement if it completes late.
	a.mu.Lock()
	if tab.removed || a.tabs[tab.ID] != tab {
		a.mu.Unlock()
		return fmt.Errorf("blank session changed while applying the default model; retry")
	}
	if tab.Ctrl != nil {
		a.mu.Unlock()
		return a.alignReusableBlankTabModel(tab, model)
	}
	a.supersedeTabBuildLocked(tab)
	tab.model = model
	tab.Label = model
	tab.Ready = false
	clearTabStartupError(tab)
	a.saveTabsLocked()
	a.mu.Unlock()
	a.startTabControllerBuild(tab)
	return nil
}

// blankTabMatchesTargetLocked returns true if tab is a reusable blank tab
// matching the given scope/project root — no running controller, no real history.
func (a *App) blankTabMatchesTargetLocked(tab *WorkspaceTab, scope, workspaceRoot string) bool {
	if tab == nil || tab.Scope != scope {
		return false
	}
	if scope == "project" && !sameProjectRoot(tab.WorkspaceRoot, workspaceRoot) {
		return false
	}
	if tab.Ctrl == nil {
		return blankTabSessionPathHasNoContent(tab)
	}
	if tab.hasActiveRuntimeWork() {
		return false
	}
	return !messagesHaveConversationContent(tab.Ctrl.History())
}

func createEmptySessionFile(dir, model string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("session dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for range 3 {
		path := agent.NewSessionPath(dir, model)
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if closeErr := f.Close(); closeErr != nil {
				return "", closeErr
			}
			// Ensure branch meta exists for topic ownership; Auto Guard no longer
			// stores a per-session toggle (it is built into Auto).
			_, _ = agent.EnsureBranchMeta(path)
			return path, nil
		}
		if os.IsExist(err) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("create empty session file: exhausted filename retries")
}

func pinNewEmptySessionBranchMeta(path, scope, workspaceRoot, topicID, topicTitle string) error {
	if err := pinSessionBranchMeta(path, scope, workspaceRoot, topicID, topicTitle); err != nil {
		pinErr := fmt.Errorf("pin empty session metadata: %w", err)
		if cleanupErr := removeDesktopSessionArtifacts(path); cleanupErr != nil {
			return errors.Join(pinErr, fmt.Errorf("clean up unbound empty session: %w", cleanupErr))
		}
		return pinErr
	}
	return nil
}

// pinSessionBranchMeta stores the workspace scope, root, and topic on a newly
// created session before a controller can reconcile the tab against it.
func pinSessionBranchMeta(sessionPath, scope, workspaceRoot, topicID, topicTitle string) error {
	unlock, err := agent.LockSessionMetaPath(sessionPath)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := agent.EnsureBranchMetaLocked(sessionPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(scope) == "project" {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
		if workspaceRoot == "" {
			return fmt.Errorf("project workspace root is required")
		}
		scope = "project"
	} else {
		scope = "global"
		workspaceRoot = ""
	}
	m.Scope = scope
	m.WorkspaceRoot = workspaceRoot
	m.TopicID = topicID
	m.TopicTitle = topicTitle
	return agent.SaveBranchMetaPreserveUpdatedLocked(sessionPath, m)
}

func blankTabSessionPathHasNoContent(tab *WorkspaceTab) bool {
	if tab == nil {
		return false
	}
	if strings.TrimSpace(tab.SessionPath) == "" {
		return true
	}
	return sessionPathHasNoContent(tabSessionDir(tab), tab.SessionPath)
}

func sessionPathHasNoContent(sessionDir, sessionPath string) bool {
	if strings.TrimSpace(sessionPath) == "" {
		return true
	}
	path, ok := pinnedTabSessionPath(sessionDir, sessionPath)
	if !ok {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if info.Size() == 0 {
		return true
	}
	session, err := agent.LoadSession(path)
	if err != nil {
		return false
	}
	return !session.HasContent()
}

func resetReusableBlankTabTitle(tab *WorkspaceTab, scope, workspaceRoot string) error {
	if tab == nil {
		return nil
	}
	topicID := strings.TrimSpace(tab.TopicID)
	if topicID == "" {
		return nil
	}
	titleRoot := topicTitleRoot(scope, workspaceRoot)
	if source := loadTopicTitleSource(titleRoot, topicID); source != topicTitleSourceAuto {
		return nil
	}
	if err := setTopicTitleWithSource(titleRoot, topicID, defaultTopicTitle, topicTitleSourceAuto); err != nil {
		return err
	}
	_ = deleteTopicAutoTitleMeta(titleRoot, topicID)
	tab.TopicTitle = defaultTopicTitle
	tab.topicTitleSource = topicTitleSourceAuto
	return nil
}

// indexedBlankTopicIDLocked finds a blank topic ID that is indexed on disk
// but not open in any tab — for reusing without creating a new topic.
func (a *App) indexedBlankTopicIDLocked(scope, workspaceRoot string) string {
	titleRoot := topicTitleRoot(scope, workspaceRoot)
	titles := loadTopicTitles(titleRoot)
	f := loadProjectsFile()

	var topicIDs []string
	if scope == "global" {
		topicIDs = orderedTopicIDs(f.GlobalTopics, titles)
	} else if i := projectIndexByRoot(f.Projects, workspaceRoot); i >= 0 {
		topicIDs = orderedTopicIDs(f.Projects[i].Topics, titles)
	}
	if len(topicIDs) == 0 {
		return ""
	}
	// Blank-tab reuse is an automatic write path: the reused ID flows into
	// ensureTopicIndexed, whose intentional single-topic prepend clears delete
	// tombstones. Picking a tombstoned topic here (its default title can
	// linger title-only after a delete raced a scan save) would therefore
	// fully resurrect a topic the user removed — skip them.
	deletedTopics := make(map[string]bool, len(f.DeletedTopics))
	for _, id := range f.DeletedTopics {
		deletedTopics[id] = true
	}

	openTopics := map[string]bool{}
	for _, tab := range a.tabs {
		if tab == nil || tab.Scope != scope || strings.TrimSpace(tab.TopicID) == "" {
			continue
		}
		if scope == "project" && !sameProjectRoot(tab.WorkspaceRoot, workspaceRoot) {
			continue
		}
		openTopics[tab.TopicID] = true
	}
	seenSessionDirs := map[string]bool{}
	sessionIndexes := []topicSessionDirIndex{}
	addSessionIndex := func(dir string) {
		dir = cleanDesktopPath(dir)
		if dir == "" {
			return
		}
		if seenSessionDirs[dir] {
			return
		}
		seenSessionDirs[dir] = true
		if index, err := topicSessionIndexForDir(dir); err == nil {
			sessionIndexes = append(sessionIndexes, index)
		}
	}
	if scope == "project" {
		addSessionIndex(desktopSessionDir(workspaceRoot))
	} else {
		addSessionIndex(config.SessionDir())
		addSessionIndex(desktopSessionDir(globalWorkspaceRoot()))
	}
	for _, topicID := range topicIDs {
		if deletedTopics[topicID] || openTopics[topicID] {
			continue
		}
		if topicTitleForTab(scope, workspaceRoot, topicID) != defaultTopicTitle {
			continue
		}
		hasSession := false
		leaseHeld := false
		for _, index := range sessionIndexes {
			if topicSessionIndexHasContentTopic(index, topicID) {
				hasSession = true
				break
			}
			if topicSessionIndexHasForeignLeaseTopic(index, topicID) {
				leaseHeld = true
			}
		}
		if hasSession || leaseHeld {
			continue
		}
		return topicID
	}
	return ""
}

// ReorderTabs persists the full local+remote strip while keeping each
// registry's internal order independent.
func (a *App) ReorderTabs(tabIDs []string) error {
	a.remoteTabMu.Lock()
	remoteCount := len(a.remoteTabs)
	a.remoteTabMu.Unlock()
	a.mu.Lock()
	if len(tabIDs) != len(a.tabs)+remoteCount {
		a.mu.Unlock()
		return fmt.Errorf("tab order length mismatch")
	}
	seen := make(map[string]bool, len(tabIDs))
	next := make([]string, 0, len(a.tabs))
	nextRemote := make([]string, 0, remoteCount)
	for _, id := range tabIDs {
		if seen[id] {
			a.mu.Unlock()
			return fmt.Errorf("duplicate tab %q", id)
		}
		seen[id] = true
		if _, ok := a.tabs[id]; ok {
			next = append(next, id)
		} else {
			nextRemote = append(nextRemote, id)
		}
	}
	if len(next) != len(a.tabs) {
		a.mu.Unlock()
		return fmt.Errorf("tab order is missing local tabs")
	}
	a.remoteTabMu.Lock()
	remoteOK := len(nextRemote) == len(a.remoteTabs)
	if remoteOK {
		for _, id := range nextRemote {
			if a.remoteTabs[id] == nil {
				remoteOK = false
				break
			}
		}
	}
	if !remoteOK {
		a.remoteTabMu.Unlock()
		a.mu.Unlock()
		return fmt.Errorf("tab order is missing remote tabs")
	}
	a.remoteTabLayout.order = append([]string(nil), nextRemote...)
	a.remoteTabLayout.stripOrder = append([]string(nil), tabIDs...)
	a.remoteTabMu.Unlock()
	a.tabOrder = next
	dir, entries, activeID, version := a.saveTabsCollectLocked()
	a.mu.Unlock()
	a.saveTabsWrite(dir, entries, activeID, version)
	return nil
}

// CloseTab removes a visible tab. If the tab's session still has foreground or
// background work, the controller is detached so closing a view does not destroy
// the session runtime.
func (a *App) CloseTab(tabID string) error {
	return a.closeTab(tabID, true)
}

func (a *App) closeTabRuntime(tabID string, allowDetach bool) error {
	defer a.lockRuntimeMutation("close-tab")()
	a.sessionRemovalMu.Lock()
	defer a.sessionRemovalMu.Unlock()
	// The runtime mutation barrier is acquired before sessionRemovalMu. This waits
	// for a turn whose admission is already in progress, blocks later turns/builds,
	// and leaves the tab visible until an earlier MCP Host-wide gate completes.

	a.mu.Lock()
	tab, ok := a.tabs[tabID]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("tab %q not found", tabID)
	}
	if len(a.tabs) <= 1 && !a.hasRemoteTabSurface() {
		a.mu.Unlock()
		return fmt.Errorf("cannot close the last tab")
	}
	a.mu.Unlock()

	// Snapshot while the tab binding is still present, but outside a.mu because
	// snapshot recovery can re-enter App and acquire a.mu. sessionRemovalMu keeps
	// DeleteSession/topic/workspace removal from trashing the same files while
	// this save is in flight.
	if err := a.snapshotTab(tab); err != nil {
		slog.Warn("desktop: snapshot before closing tab failed", "tab", tabID, "err", err)
		return fmt.Errorf("save current session before closing tab: %w", err)
	}
	if err := a.saveTabSessionMetaForCurrentSession(tab); err != nil {
		slog.Warn("desktop: session metadata before closing tab failed", "tab", tabID, "err", err)
		return fmt.Errorf("save current session metadata before closing tab: %w", err)
	}
	// A terminal belongs to the visible chat tab, even when another tab points
	// at the same project. Reap its PTY before removing the tab binding.
	if a.terminals != nil {
		a.terminals.closeForTab(tabID)
	}

	a.mu.Lock()
	if current := a.tabs[tabID]; current != tab {
		a.mu.Unlock()
		if current == nil {
			return fmt.Errorf("tab %q not found", tabID)
		}
		return fmt.Errorf("tab %q changed while closing", tabID)
	}
	if len(a.tabs) <= 1 && !a.hasRemoteTabSurface() {
		a.mu.Unlock()
		return fmt.Errorf("cannot close the last tab")
	}
	if !allowDetach && tab.hasActiveRuntimeWork() {
		a.mu.Unlock()
		return fmt.Errorf("task still has active work")
	}
	if tab.Ctrl == nil || !tab.hasActiveRuntimeWork() {
		a.markTabRemovedLocked(tab)
	}

	ordered := a.orderedTabIDsLocked()
	closedIndex := -1
	for i, id := range ordered {
		if id == tabID {
			closedIndex = i
			break
		}
	}
	delete(a.tabs, tabID)
	a.removeTabOrderLocked(tabID)
	wasActive := a.activeTabID == tabID
	if wasActive {
		a.activeTabID = ""
		if len(a.tabOrder) > 0 {
			nextIndex := max(closedIndex, 0)
			if nextIndex >= len(a.tabOrder) {
				nextIndex = len(a.tabOrder) - 1
			}
			a.activeTabID = a.tabOrder[nextIndex]
		}
	}
	a.saveTabsLocked()
	// Snapshot the teardown targets while still holding the lock: the tab is
	// no longer reachable from a.tabs after this section, but locked writers
	// holding stale pointers (rememberTabSessionPath, applySessionBindingToTab)
	// can still write its fields under a.mu.
	closeCtrl := tab.Ctrl
	closeSink := tab.sink
	a.mu.Unlock()
	if a.workspaceHub != nil {
		a.workspaceHub.reconcileRoots()
	}

	// Tear down outside App.mu while retaining the lifecycle barrier acquired
	// before the tab binding was removed.
	discardPath, discardTransientBlank := a.transientBlankSessionArtifactPath(tab)
	if closeCtrl != nil {
		if allowDetach && controllerHasActiveRuntimeWork(closeCtrl) && a.detachSessionRuntime(tab) {
			// Detached runtimes keep running and must keep saving: do not
			// clear the path or drain for them.
			return nil
		}
		closeCtrl.SetSessionPath("") // future snapshots become no-ops
		a.quiesceTabAutosave(tab)    // wait for any in-flight snapshot to finish
		closeCtrl.Cancel()
		closeCtrl.Close()
		// Release the shared plugin host reference. The host stays alive as
		// long as any other tab for the same workspace root holds a reference;
		// on the last release the host is closed and its subprocesses exit.
		a.releaseTabSharedHost(tab)
		tab.releaseSessionLease()
	}
	if closeSink != nil {
		closeSink.clearContext() // stop further emissions (nil ctx -> Emit becomes no-op)
	}
	if discardTransientBlank {
		if discardTransientBlankSessionArtifacts(discardPath) {
			a.removeSessionCatalogPath(discardPath, "transient_blank_discarded")
		}
	}
	return nil
}

func (a *App) keepOnlyVisibleTab(tabID string) (TabMeta, error) {
	type pruneCandidate struct {
		id  string
		tab *WorkspaceTab
	}

	// sessionRemovalMu covers snapshotting, pruning the hidden bindings, and
	// closing the removed runtimes (a detached runtime must finish its
	// in-flight autosave before DeleteSession can see the files). The
	// project-tree event stays outside so a listener can never re-enter a
	// removal path while the lock is held.
	meta, err := func() (TabMeta, error) {
		defer a.lockRuntimeMutation("prune-visible-tabs")()
		a.sessionRemovalMu.Lock()
		defer a.sessionRemovalMu.Unlock()

		a.mu.Lock()
		active := a.tabs[tabID]
		if active == nil {
			a.mu.Unlock()
			return TabMeta{}, fmt.Errorf("tab %q not found", tabID)
		}
		candidates := make([]pruneCandidate, 0, len(a.tabs)-1)
		for id, tab := range a.tabs {
			if id == tabID {
				continue
			}
			candidates = append(candidates, pruneCandidate{id: id, tab: tab})
		}
		a.mu.Unlock()

		// Keep tab bindings in a.tabs while saving so DeleteSession still sees
		// them, but do not hold a.mu: Snapshot can run recovery callbacks that
		// re-enter App and need the same lock.
		snapshotted := make(map[string]*WorkspaceTab, len(candidates))
		for _, candidate := range candidates {
			id, tab := candidate.id, candidate.tab
			snapshotted[id] = tab
			if err := a.snapshotTab(tab); err != nil {
				slog.Warn("desktop: snapshot before pruning hidden tab failed", "tab", id, "err", err)
				return TabMeta{}, fmt.Errorf("save current session before switching tabs: %w", err)
			}
			if err := a.saveTabSessionMetaForCurrentSession(tab); err != nil {
				slog.Warn("desktop: session metadata before pruning hidden tab failed", "tab", id, "err", err)
				return TabMeta{}, fmt.Errorf("save current session metadata before switching tabs: %w", err)
			}
		}

		a.mu.Lock()
		active = a.tabs[tabID]
		if active == nil {
			a.mu.Unlock()
			return TabMeta{}, fmt.Errorf("tab %q not found", tabID)
		}
		for id, tab := range a.tabs {
			if id != tabID && snapshotted[id] != tab {
				a.mu.Unlock()
				return TabMeta{}, fmt.Errorf("visible tabs changed while switching; retry")
			}
		}
		a.activeTabID = tabID
		removed := make([]*WorkspaceTab, 0, len(candidates))
		for _, candidate := range candidates {
			id, tab := candidate.id, candidate.tab
			if tab == nil || a.tabs[id] != tab {
				continue
			}
			if tab.Ctrl == nil || !tab.hasActiveRuntimeWork() {
				a.markTabRemovedLocked(tab)
			}
			removed = append(removed, tab)
			delete(a.tabs, id)
			a.removeTabOrderLocked(id)
		}
		a.tabOrder = []string{tabID}
		a.saveTabsLocked()
		meta := a.tabMeta(active, true)
		a.mu.Unlock()

		for _, tab := range removed {
			a.removeVisibleTabRuntimeAdmissionHeld(tab)
		}
		return meta, nil
	}()
	if err != nil {
		return TabMeta{}, err
	}
	// Visibility and detached/open ownership are runtime state. Snapshot saves
	// above already enqueue exact-path catalog updates; a topic switch must not
	// rescan every session directory and expose partial catalog generations.
	a.emitProjectTreeRuntimeChangedWithLegacy()
	return enrichTabMeta(meta), nil
}

func (a *App) applySingleSurfaceTabPolicy() error {
	a.singleSurfaceMu.Lock()
	defer a.singleSurfaceMu.Unlock()

	a.mu.RLock()
	tabID := a.activeTabID
	if tabID == "" || a.tabs[tabID] == nil {
		for _, id := range a.tabOrder {
			if a.tabs[id] != nil {
				tabID = id
				break
			}
		}
		if tabID == "" {
			for id := range a.tabs {
				tabID = id
				break
			}
		}
	}
	a.mu.RUnlock()
	if tabID == "" {
		return nil
	}
	_, err := a.keepOnlyVisibleTab(tabID)
	return err
}

func (a *App) removeVisibleTabRuntimeAdmissionHeld(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	if err := a.snapshotTab(tab); err != nil {
		slog.Warn("desktop: snapshot before removing visible tab runtime failed", "tab", tab.ID, "err", err)
	}
	discardPath, discardTransientBlank := a.transientBlankSessionArtifactPath(tab)
	a.mu.RLock()
	ctrl := tab.Ctrl
	a.mu.RUnlock()
	if ctrl != nil && controllerHasActiveRuntimeWork(ctrl) && a.detachSessionRuntime(tab) {
		return
	}
	a.markTabRemoved(tab)
	a.closeTabRuntimeAdmissionHeld(tab)
	if discardTransientBlank {
		if discardTransientBlankSessionArtifacts(discardPath) {
			a.removeSessionCatalogPath(discardPath, "transient_blank_discarded")
		}
	}
}

// transientBlankSessionArtifactPath reports the artifact path to discard when
// closing a still-blank tab. It snapshots the racy tab fields under a.mu and
// keeps the file probe (sessionPathHasNoContent) outside the lock. Callers
// must not hold a.mu.
func (a *App) transientBlankSessionArtifactPath(tab *WorkspaceTab) (string, bool) {
	if tab == nil {
		return "", false
	}
	snap := a.tabRuntimeSnapshot(tab)
	if snap.readOnly || strings.TrimSpace(snap.topicID) != "" || controllerHasActiveRuntimeWork(snap.ctrl) {
		return "", false
	}
	if strings.TrimSpace(snap.sessionPath) == "" {
		return "", false
	}
	dir := sessionDirForSnapshot(snap)
	if !sessionPathHasNoContent(dir, snap.sessionPath) {
		return "", false
	}
	path, ok := pinnedTabSessionPath(dir, snap.sessionPath)
	if !ok {
		return "", false
	}
	return path, true
}

func (a *App) markTabRemoved(tab *WorkspaceTab) {
	a.mu.Lock()
	a.markTabRemovedLocked(tab)
	a.mu.Unlock()
}

func (a *App) markTabRemovedLocked(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	tab.removed = true
	if tab.buildCancel != nil {
		tab.buildCancel()
		tab.buildCancel = nil
	}
}

// tabBuildSupersededLocked reports whether an in-flight build lost ownership
// of its tab: the tab was removed/replaced, or a session rebind bumped
// buildGeneration to invalidate it. Generation 0 marks the synchronous
// rebuild paths, which serialize through runtimeRebuildMu instead and are
// never superseded by generation bumps. Callers must hold a.mu.
func (a *App) tabBuildSupersededLocked(tab *WorkspaceTab, generation uint64) bool {
	if tab == nil || tab.removed || a.shuttingDown.Load() || a.tabs[tab.ID] != tab {
		return true
	}
	return generation != 0 && tab.buildGeneration != generation
}

func (a *App) tabBuildSuperseded(tab *WorkspaceTab, generation uint64) bool {
	if tab == nil {
		return true
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tabBuildSupersededLocked(tab, generation)
}

// supersedeTabBuildLocked invalidates any in-flight startup build and cancels
// its context. A synchronous rebuild (model/effort/token switch) that has
// already installed its controller calls this so a slower blank-session build
// cannot finish afterward, overwrite tab.Ctrl, and release or steal the
// session lease the switch just bound. Callers must hold a.mu.
func (a *App) supersedeTabBuildLocked(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	tab.buildGeneration++
	if tab.buildCancel != nil {
		tab.buildCancel()
		tab.buildCancel = nil
	}
}

// abandonSupersededBuild cleans up after a build that lost tab ownership
// mid-flight (removed tab, or a session rebind bumped the generation). It
// releases only what THIS build acquired — its controller, its own
// shared-host reference (rootKey), and the session lease bound to its own
// path (leaseKey) — and never reads or clears the tab's SharedHostKey or
// lease outright: on a live rebound tab the replacement build may already
// have published its own key and lease there, and taking those would leak
// the new runtime's host reference (or close a host still in use) and strip
// the new session's lease. Callers must not hold a.mu.
func (a *App) abandonSupersededBuild(tab *WorkspaceTab, ctrl control.SessionAPI, rootKey, leaseKey string) {
	if ctrl != nil {
		ctrl.Close()
	}
	if rootKey != "" {
		a.releaseSharedHost(rootKey)
	}
	tab.releaseSessionLeaseForKey(leaseKey)
}

func (a *App) clearTabBuildCancel(tab *WorkspaceTab, generation uint64, cancel context.CancelFunc, keepContext bool) {
	if cancel == nil {
		return
	}
	if !keepContext {
		defer cancel()
	}
	if tab == nil {
		return
	}
	a.mu.Lock()
	if tab.buildGeneration == generation {
		tab.buildCancel = nil
	}
	a.mu.Unlock()
}

func (a *App) closeTabRuntimeAdmissionHeld(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	a.mu.RLock()
	ctrl := tab.Ctrl
	sink := tab.sink
	a.mu.RUnlock()
	if ctrl != nil {
		ctrl.SetSessionPath("") // future snapshots become no-ops
		a.quiesceTabAutosave(tab)
		ctrl.Cancel()
		ctrl.Close()
		a.releaseTabSharedHost(tab)
	}
	if sink != nil {
		sink.clearContext()
	}
	tab.releaseSessionLease()
	a.mu.Lock()
	a.releaseSessionRuntimeLocked(tab)
	a.mu.Unlock()
}

// buildTabController assembles a controller for a tab in the background, the
// same way buildController works for the single-controller App. On success it
// wires the controller and flips Ready; on failure it stores StartupErr.
func (a *App) startTabControllerBuild(tab *WorkspaceTab) {
	buildCtx, cancel := context.WithCancel(a.bootContext())
	a.mu.Lock()
	if tab == nil || tab.removed {
		a.mu.Unlock()
		cancel()
		return
	}
	tab.buildGeneration++
	generation := tab.buildGeneration
	tab.buildCancel = cancel
	if tab.buildDone != nil {
		// Defensive: the owning build's terminal defer nils buildDone after
		// closing it, so a non-nil channel here means that build never ran its
		// defer. Close it anyway so activation completions never wait forever.
		close(tab.buildDone)
	}
	tab.buildDone = make(chan struct{})
	tab.buildDoneGen = generation
	a.mu.Unlock()
	if a.ctx == nil {
		a.buildTabControllerWithContext(tab, loadedTabSession{}, buildCtx, generation, cancel)
		return
	}
	go a.buildTabControllerWithContext(tab, loadedTabSession{}, buildCtx, generation, cancel)
}

func (a *App) buildTabController(tab *WorkspaceTab) {
	a.buildTabControllerWithLoadedSession(tab, loadedTabSession{})
}

type loadedTabSession struct {
	Path    string
	Session *agent.Session
}

func (s loadedTabSession) matches(path string) bool {
	return s.Session != nil && sessionRuntimeKey(s.Path) != "" && sessionRuntimeKey(s.Path) == sessionRuntimeKey(path)
}

func (a *App) buildTabControllerWithLoadedSession(tab *WorkspaceTab, loadedSession loadedTabSession) {
	a.buildTabControllerWithContext(tab, loadedSession, a.bootContext(), 0, nil)
}

func (a *App) desktopNotificationSender() notify.Sender {
	if a == nil {
		return notify.NewPlatformSender()
	}
	a.notificationSenderOnce.Do(func() {
		if a.notificationSender == nil {
			a.notificationSender = notify.NewPlatformSender()
		}
	})
	return a.notificationSender
}

func (a *App) desktopControllerSink(inner event.Sink, cfg config.NotificationsConfig) event.Sink {
	if !cfg.Enabled {
		return inner
	}
	sender := a.desktopNotificationSender()
	if sender == nil {
		return inner
	}
	return notify.NewSink(inner, sender, cfg)
}

// closeTabBuildDone signals waiters (topic-activation completions) that the
// build owning buildGeneration has terminated. Every build funnels through
// buildTabControllerWithContextCore, whose deferred call guarantees
// the channel startTabControllerBuild created is closed exactly once, on every
// terminal path — success, failure, and superseded abandon alike. Synchronous
// rebuild paths pass generation 0 and never created a channel.
func (a *App) closeTabBuildDone(tab *WorkspaceTab, buildGeneration uint64) {
	if tab == nil || buildGeneration == 0 {
		return
	}
	a.mu.Lock()
	if tab.buildDoneGen == buildGeneration && tab.buildDone != nil {
		close(tab.buildDone)
		tab.buildDone = nil
	}
	a.mu.Unlock()
}

func (a *App) buildTabControllerWithContext(tab *WorkspaceTab, loadedSession loadedTabSession, buildCtx context.Context, buildGeneration uint64, buildCancel context.CancelFunc) {
	a.buildTabControllerWithContextCore(tab, loadedSession, buildCtx, buildGeneration, buildCancel)
}

// buildTabControllerWithContextCore performs configuration, session routing,
// and extension boot outside runtimeAdmissionMu. Only publication enters the
// lifecycle barrier.
func (a *App) buildTabControllerWithContextCore(tab *WorkspaceTab, loadedSession loadedTabSession, buildCtx context.Context, buildGeneration uint64, buildCancel context.CancelFunc) {
	defer a.recoverToPending("buildTabController")
	keepBuildContext := false
	defer func() {
		a.clearTabBuildCancel(tab, buildGeneration, buildCancel, keepBuildContext)
	}()
	defer a.closeTabBuildDone(tab, buildGeneration)
	if hook := a.tabBuildStartHook; hook != nil && tab != nil {
		// Test-only gate: lets activation-ordering tests hold builds in flight
		// and release them out of order. Runs even for already-superseded
		// builds so the test can observe every build it started.
		hook(tab.ID)
	}
	wailsCtx := a.ctx
	if a.tabBuildSuperseded(tab, buildGeneration) {
		return
	}
	a.mu.Lock()
	if !tab.removed && tab.Ctrl == nil {
		// A lease-blocked tab keeps its blocked banner steady while the
		// deferred-rebuild loop retries every 2s: resetting to "starting" on
		// each attempt makes the frontend's takeover banner (and the dialog
		// built from it) mount/unmount in a 2s cycle. The state flips to ready
		// only when a retry actually wins the lease. Deliberate user rebuilds
		// never carry StartupErrLeaseHeld, so they reset as before.
		if !tab.StartupErrLeaseHeld {
			tab.Ready = false
			clearTabStartupError(tab)
			a.setSessionRuntimePhaseLocked(tab, sessionRuntimeStarting, nil)
		}
	}
	a.mu.Unlock()
	a.reconcileTabWithPinnedSessionMeta(tab)

	// Snapshot the identity/profile fields under a.mu before the off-lock
	// stretch: session rebinding, recovery, and topic assignment write them
	// under the lock while this goroutine builds.
	a.mu.RLock()
	tabWorkspaceRoot := tab.WorkspaceRoot
	tabScope := tab.Scope
	tabTopicID := tab.TopicID
	tabSessionPath := tab.SessionPath
	tabModel := tab.model
	tabSink := tab.sink
	a.mu.RUnlock()

	root := tabWorkspaceRoot
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}

	// Load config for this tab's workspace root.
	_ = config.MigrateLegacyCredentialsForRoot(root)
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		a.recordTabStartupFailure(tab, buildGeneration, wailsCtx, err)
		return
	}

	if a.tabBuildSuperseded(tab, buildGeneration) {
		return
	}
	if tabSink != nil {
		tabSink.setContext(wailsCtx)
	}

	sessionDir := desktopSessionDir(root)
	if tabScope == "global" {
		sessionDir = desktopSessionDir(globalWorkspaceRoot())
	}
	topicID := strings.TrimSpace(tabTopicID)
	pinnedPath, hasPinnedPath := pinnedTabSessionPathForBuild(tabScope, tabWorkspaceRoot, sessionDir, tabSessionPath)
	if hasPinnedPath && agent.IsCleanupPending(pinnedPath) {
		// Boot reconciliation may finish the pending deletion before the later
		// resume step. Clear the local candidate now so the disappeared path is
		// not mistaken for a deliberate empty placeholder afterward.
		hasPinnedPath = false
		pinnedPath = ""
	}
	catalogTopicPath := ""
	if hasPinnedPath {
		// A restored tab's exact path is already known state, not history
		// discovery. Keep legacy-directory and empty placeholder paths usable
		// while the catalog is still opening or rebuilding.
		sessionDir = filepath.Dir(pinnedPath)
	} else {
		catalogTopicPath = a.catalogSessionPathForTopic(tabScope, tabWorkspaceRoot, topicID)
	}
	if !hasPinnedPath && catalogTopicPath != "" {
		sessionDir = filepath.Dir(catalogTopicPath)
	}
	startupSessionPath := ""
	if hasPinnedPath {
		if !agent.IsCleanupPending(pinnedPath) {
			startupSessionPath = pinnedPath
		}
	} else if catalogTopicPath != "" {
		startupSessionPath = catalogTopicPath
	}
	prepareStartupPinnedContext(tab, startupSessionPath, tabSessionPath)
	model := strings.TrimSpace(tabModel)
	if sessionModel, ok := agent.LoadSessionModel(startupSessionPath); ok {
		config.NormalizeLegacyMimoCustomProvidersForRefs(cfg, sessionModel)
		if _, ok := cfg.ResolveModel(sessionModel); ok {
			model = sessionModel
		}
	}
	if model == "" {
		if def := strings.TrimSpace(cfg.DefaultModel); providerext.PluginRefOwner(def) != "" {
			// A plugin-namespaced default_model belongs to an extension
			// sidecar: the config catalog can never resolve it, but boot's
			// merged resolver can. Pass it through untouched.
			model = def
		} else {
			resolved, _, ok := cfg.ResolveDesktopNewSessionModel()
			if !ok {
				a.recordTabStartupFailure(tab, buildGeneration, wailsCtx, errNoDesktopChatModel)
				return
			}
			model = resolved
		}
	}
	config.NormalizeLegacyMimoCustomProvidersForRefs(cfg, model)
	requestedModel := model
	if providerext.PluginRefOwner(model) == "" {
		// Plugin refs skip the config fallback: rerouting an unavailable
		// extension model onto a config provider would silently change the
		// session; boot's unknown-model error is the honest failure.
		if resolved, fallback, ok := cfg.ResolveModelWithFallback(model); ok {
			if fallback && strings.TrimSpace(tabModel) != "" {
				a.noticeForTab(tab.ID, fmt.Sprintf("model %q is no longer available; switched to %s", requestedModel, resolved))
			}
			model = resolved
		}
	}

	// Acquire a shared plugin host for this workspace root so MCP processes
	// are launched once per root, not once per tab. SharedHostKey is an a.mu-
	// guarded field (takeTabSharedHostKey reads it under the lock during
	// teardown), so publish it under the lock alongside the model. Capture the
	// tab-local runtime profile here too: bound methods (SetModeForTab,
	// SetGoalForTab, SetEffortForTab, ...) write these under a.mu, so the
	// off-lock boot.Build below must read a locked snapshot, not the live tab.
	rootKey := tabWorkspaceRoot
	if rootKey == "" {
		rootKey = "__global__" // stable key for global workspace tabs
	}
	a.mu.Lock()
	if a.tabBuildSupersededLocked(tab, buildGeneration) {
		a.mu.Unlock()
		return
	}
	tab.model = model
	tab.Label = model
	tab.SharedHostKey = rootKey
	buildEffort := cloneStringPtr(tab.effort)
	buildTokenMode := currentTabTokenMode(tab)
	buildMode := tab.mode
	buildToolApprovalMode := tab.toolApprovalMode
	buildGoal := tab.goal
	buildSink := tab.sink
	a.saveTabsLocked()
	a.mu.Unlock()
	buildRuntime := (tabRuntimeSnapshot{
		tokenMode:        buildTokenMode,
		mode:             buildMode,
		goal:             buildGoal,
		toolApprovalMode: buildToolApprovalMode,
	}).normalizedRuntime()
	// Capture the extension generation before the shared host is mutated by
	// boot.Build. A concurrent plugin delete/update/reauth bumps the counter;
	// if it moves before publication we abandon this controller rather than
	// resurrecting removed tools on the shared host.
	extensionGen := a.currentExtensionGeneration()
	sharedHost := a.acquireSharedHost(rootKey)
	sink := a.desktopControllerSink(buildSink, cfg.Notifications)
	buildCtx, registration := beginSharedHostMCPRegistration(buildCtx, sharedHost)
	defer registration.rollback()
	ctrl, err := a.buildTabControllerBootFenced(buildCtx, extensionGen, boot.Options{
		Model:                    model,
		RequireKey:               false,
		StatsSource:              "desktop",
		TaskStore:                a.taskStore(),
		OnConfigLoadWarnings:     a.configLoadWarningsHandler(),
		Sink:                     sink,
		WorkspaceRoot:            root,
		SessionDir:               sessionDir,
		EffortOverride:           cloneStringPtr(buildEffort),
		SharedHost:               sharedHost,
		CleanupPendingReconciler: reconcileDesktopCleanupPending,
		SubagentParentLive:       a.subagentParentProbeForBuild(tab),
		SessionRecoveryMeta:      a.tabSessionRecoveryMeta(tab),
		PinnedContextLoader:      pinnedContextLoader(root),
		OnSessionRecovered:       a.handleTabSessionRecovered(tab),
		OnSessionTransition:      a.handleTabSessionTransition(tab),
		BeforeInboxDispatch:      a.beforeInboxDispatch,
		OnSessionTitleChanged:    a.onSessionTitleChanged,
	})
	if a.handleTabControllerBootError(tab, registration, rootKey, buildGeneration, wailsCtx, err) {
		return
	}
	if a.tabBuildSuperseded(tab, buildGeneration) {
		registration.rollback()
		a.abandonSupersededBuild(tab, ctrl, rootKey, "")
		return
	}
	if a.currentExtensionGeneration() != extensionGen {
		registration.rollback()
		a.abandonSupersededBuild(tab, ctrl, rootKey, "")
		a.scheduleDeferredStartupBuild(tab.ID)
		return
	}
	a.bindControllerDisplayRecorder(ctrl)
	configureControllerRuntime(ctrl, nil, buildRuntime)

	acquiredLeaseKey := ""
	restoredRuntime := buildRuntime
	if dir := ctrl.SessionDir(); dir != "" {
		// Refresh the topic/session locals under the lock: a rebind or the
		// recovery callback may have rewritten them since the early snapshot.
		a.mu.RLock()
		tabTopicID = strings.TrimSpace(tab.TopicID)
		tabSessionPath = tab.SessionPath
		a.mu.RUnlock()
		var path string
		var resumeSession *agent.Session
		var resumeLoadErr error
		// Prefer the exact session file persisted for this tab. Topic lookup is a
		// compatibility fallback for older desktop-tabs.json files that only stored
		// topicId and could pick the wrong session when one topic had multiple files.
		if loaded, pinnedPath, ok, loadErr := loadPinnedTabSessionWithPreload(dir, tabSessionPath, loadedSession); loadErr != nil {
			resumeLoadErr = loadErr
		} else if ok {
			path = pinnedPath
			resumeSession = loaded
		}
		if resumeLoadErr == nil && path == "" && tabTopicID != "" {
			existingPath := a.catalogSessionPathForTopic(tabScope, tabWorkspaceRoot, tabTopicID)
			if existingPath != "" {
				if loaded, err := loadResumableSession(existingPath); err == nil {
					path = existingPath
					resumeSession = loaded
				} else {
					resumeLoadErr = err
				}
			}
		}
		if resumeLoadErr != nil {
			resumeLoadErr = friendlySessionLoadError(resumeLoadErr)
			a.mu.Lock()
			if a.tabBuildSupersededLocked(tab, buildGeneration) {
				a.mu.Unlock()
				a.abandonSupersededBuild(tab, ctrl, rootKey, "")
				return
			}
			leaseHeld, save := a.markTabStartupFailureLocked(tab, resumeLoadErr, suppressStartupRestore)
			hostKey := takeTabSharedHostKey(tab)
			tab.releaseSessionLease()
			a.mu.Unlock()
			a.writeTabsSaveRequest(save)
			ctrl.Close()
			if hostKey != "" {
				a.releaseSharedHost(hostKey)
			}
			if leaseHeld {
				a.scheduleDeferredStartupBuild(tab.ID)
			}
			a.emitReady(wailsCtx, tab.ID)
			return
		}
		if path == "" {
			path = agent.NewSessionPath(dir, ctrl.Label())
		}
		// Write/update scope/session meta.
		if path != "" {
			if a.claimSessionRuntime(tab, path, buildCtx) {
				ctrl.Close()
				a.releaseSharedHost(rootKey)
				a.emitReady(wailsCtx, tab.ID)
				return
			}
			preLeaseKey := tab.sessionLeaseRuntimeKey()
			if err := a.ensureTabSessionLeaseForRebuild(tab, path, ""); err != nil {
				a.mu.Lock()
				if a.tabBuildSupersededLocked(tab, buildGeneration) {
					a.mu.Unlock()
					a.abandonSupersededBuild(tab, ctrl, rootKey, "")
					return
				}
				leaseHeld, save := a.markTabStartupFailureLocked(tab, err, suppressStartupRestore)
				hostKey := takeTabSharedHostKey(tab)
				// Release only a lease bound to THIS build's session: a failed
				// ensure leaves any prior lease untouched, and that lease may
				// belong to a runtime a concurrent switch just installed.
				tab.releaseSessionLeaseForKey(sessionRuntimeKey(path))
				a.mu.Unlock()
				a.writeTabsSaveRequest(save)
				ctrl.Close()
				if hostKey != "" {
					a.releaseSharedHost(hostKey)
				}
				if leaseHeld {
					a.scheduleDeferredStartupBuild(tab.ID)
				}
				a.emitReady(wailsCtx, tab.ID)
				return
			}
			// Remember which lease THIS build bound: if the build is later
			// superseded, only a lease still carrying this key may be
			// released (see abandonSupersededBuild). A fast-path reuse means
			// the lease existed before this build (bound by a concurrent
			// switch or recovery) — it is not ours to release.
			if key := sessionRuntimeKey(path); key != preLeaseKey {
				acquiredLeaseKey = key
			}
			// Re-check ownership right after the (potentially slow) lease
			// bind: a rebind that superseded this build while ensure was in
			// flight has already retargeted the tab, and continuing into
			// Resume/persistTabSessionPath would write the stale session
			// path back onto the rebound tab.
			if a.tabBuildSuperseded(tab, buildGeneration) {
				a.abandonSupersededBuild(tab, ctrl, rootKey, acquiredLeaseKey)
				return
			}
			var restoreErr error
			restoredRuntime, restoreErr = resumeControllerRuntimeWithSession(ctrl, resumeSession, path, buildRuntime)
			if restoreErr != nil {
				a.mu.Lock()
				if a.tabBuildSupersededLocked(tab, buildGeneration) {
					a.mu.Unlock()
					a.abandonSupersededBuild(tab, ctrl, rootKey, acquiredLeaseKey)
					return
				}
				leaseHeld, save := a.markTabStartupFailureLocked(tab, restoreErr, suppressStartupRestore)
				hostKey := takeTabSharedHostKey(tab)
				tab.releaseSessionLeaseForKey(sessionRuntimeKey(path))
				a.mu.Unlock()
				a.writeTabsSaveRequest(save)
				ctrl.Close()
				if hostKey != "" {
					a.releaseSharedHost(hostKey)
				}
				if leaseHeld {
					a.scheduleDeferredStartupBuild(tab.ID)
				}
				a.emitReady(wailsCtx, tab.ID)
				return
			}
			a.persistTabSessionPath(tab, path)
			a.mu.RLock()
			indexScope := tab.Scope
			indexRoot := tab.WorkspaceRoot
			indexTopicID := strings.TrimSpace(tab.TopicID)
			indexTopicTitle := tab.TopicTitle
			a.mu.RUnlock()
			if indexTopicID != "" {
				if err := ensureTopicIndexed(indexScope, indexRoot, indexTopicID, indexTopicTitle, loadTopicTitleSource(topicTitleRoot(indexScope, indexRoot), indexTopicID)); err == nil {
					a.emitProjectTreeChangedForSessionDirs(ctrl.SessionDir())
				}
			}
			// Key telemetry to the session this build binds: restore its
			// persisted sidecar, or start from zero when none exists (fresh
			// session, CLI-created session, pre-telemetry session). Keeping
			// the previous session's totals here made 会话费用 accumulate
			// across sessions and persisted the stale totals into the new
			// session's sidecar on the next event (#5850).
			snapshot := loadTelemetry(path + ".telemetry.json")
			tab.replaceTelemetry(snapshot, sessionRuntimeKey(path))
		}
	}

	// Lifecycle admission protects only the compare-and-publish boundary. Slow
	// config, history, lease, and extension work above remains cancellable and
	// cannot prevent shutdown from acquiring the write side.
	releasePublication, extensionsCurrent := a.lockTabControllerPublication(extensionGen, tabScope, tabWorkspaceRoot)
	if !extensionsCurrent {
		registration.rollback()
		a.abandonSupersededBuild(tab, ctrl, rootKey, acquiredLeaseKey)
		a.scheduleDeferredStartupBuild(tab.ID)
		return
	}
	defer releasePublication()
	if a.rejectStaleStartupModelSettings(tab, ctrl, buildGeneration, wailsCtx, func() {
		registration.rollback()
		a.abandonSupersededBuild(tab, ctrl, rootKey, acquiredLeaseKey)
	}) {
		return
	}
	a.mu.Lock()
	if a.tabBuildSupersededLocked(tab, buildGeneration) {
		a.mu.Unlock()
		a.abandonSupersededBuild(tab, ctrl, rootKey, acquiredLeaseKey)
		return
	}
	// Commit the scope while the final tab-generation check is still guarded.
	// It only takes the plugin Host leaf lock and cannot call back into App.
	if !a.commitStartupWriteAuthorityLocked(tab, ctrl, registration, rootKey, acquiredLeaseKey, wailsCtx) {
		return
	}
	tab.Ctrl = ctrl
	tab.Label = ctrl.Label()
	applyNormalizedRuntimeToTabLocked(tab, restoredRuntime)
	tab.Ready = true
	clearTabStartupError(tab)
	a.bindSessionRuntimeKeyLocked(tab, tab.currentSessionPath())
	a.advanceSessionRuntimeEpochLocked(tab)
	keepBuildContext = true
	a.mu.Unlock()
	a.finishStartupPublication(tab, ctrl, wailsCtx)
}

type sessionBinding struct {
	path          string
	scope         string
	workspaceRoot string
	topicID       string
	topicTitle    string
	hasMeta       bool
	meta          agent.BranchMeta
}

func (a *App) reconcileTabWithPinnedSessionMeta(tab *WorkspaceTab) (string, bool) {
	if tab == nil {
		return "", false
	}
	a.mu.RLock()
	current := a.tabs[tab.ID]
	path := strings.TrimSpace(tab.SessionPath)
	ctrl := tab.Ctrl
	scope := tab.Scope
	workspaceRoot := tab.WorkspaceRoot
	a.mu.RUnlock()
	if current != tab {
		return "", false
	}
	if path != "" {
		if resolved, ok := a.reconcileTabWithSessionPath(tab, path); ok {
			return resolved, true
		}
	}
	if ctrl == nil {
		return "", false
	}
	path = strings.TrimSpace(ctrl.SessionPath())
	if path == "" {
		return "", false
	}
	binding, ok := a.resolveSessionBinding(path)
	if !ok {
		return "", false
	}
	if scope == "project" && binding.scope != "project" && normalizeProjectRoot(workspaceRoot) != "" {
		if root, ok := safeControllerWorkspaceRoot(ctrl); ok && sameProjectRoot(root, workspaceRoot) {
			return "", false
		}
	}
	a.applySessionBindingToTab(tab, binding)
	return binding.path, true
}

func (a *App) reconcileTabWithSessionPath(tab *WorkspaceTab, sessionPath string) (string, bool) {
	if tab == nil || strings.TrimSpace(sessionPath) == "" {
		return "", false
	}
	binding, ok := a.resolveSessionBinding(sessionPath)
	if !ok {
		return "", false
	}
	a.applySessionBindingToTab(tab, binding)
	return binding.path, true
}

func (a *App) applySessionBindingToTab(tab *WorkspaceTab, binding sessionBinding) {
	if tab == nil || binding.path == "" {
		return
	}
	var terminalSessions []*terminalSession
	reopenTerminalGate := false
	scope := binding.scope
	workspaceRoot := binding.workspaceRoot
	if scope == "" {
		scope = "global"
	}
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
		if workspaceRoot == "" {
			return
		}
		releaseAdmission, err := a.beginChangedProjectRuntimeAdmission(tab, scope, workspaceRoot)
		if err != nil {
			return
		}
		defer releaseAdmission()
		a.registerProjectRoot(workspaceRoot)
	} else {
		scope = "global"
		workspaceRoot = globalTabWorkspaceRoot()
	}
	topicID := strings.TrimSpace(binding.topicID)
	topicTitle := strings.TrimSpace(binding.topicTitle)
	if topicTitle == "" && topicID != "" {
		topicTitle = topicTitleForTab(scope, workspaceRoot, topicID)
	}
	topicSource := ""
	if topicID != "" {
		topicSource = loadTopicTitleSource(topicTitleRoot(scope, workspaceRoot), topicID)
	}
	pinnedState, preservePendingLegacy := pinnedContextStateForSessionBinding(tab, binding.path)

	a.mu.Lock()
	current := a.tabs[tab.ID]
	if current != nil && current != tab {
		a.mu.Unlock()
		return
	}
	oldScope := tab.Scope
	oldWorkspaceRoot := tab.WorkspaceRoot
	changed := tab.Scope != scope ||
		tab.WorkspaceRoot != workspaceRoot ||
		canonicalTabSessionPath(tab.SessionPath) != canonicalTabSessionPath(binding.path)
	// Spelling-only root updates still persist above, but an equivalent root is
	// the same workspace — do not warn the user about a switch.
	workspaceChanged := tab.Scope != scope || !sameProjectRoot(tab.WorkspaceRoot, workspaceRoot)
	if workspaceChanged && current == tab && a.terminals != nil {
		// A session binding can move a visible tab to another project. Invalidate
		// the old terminal scope before publishing the new root so an in-flight
		// shell start cannot register against the old workspace after this
		// transition. Reopen only after the new binding is visible.
		terminalSessions = a.terminals.detachForTab(tab.ID)
		reopenTerminalGate = !tab.ReadOnly && !tab.removed
	}
	applyPinnedContextSessionBinding(tab, pinnedState, preservePendingLegacy)
	tab.Scope = scope
	tab.WorkspaceRoot = workspaceRoot
	tab.SessionPath = canonicalTabSessionPath(binding.path)
	if topicID != "" {
		changed = changed || tab.TopicID != topicID
		tab.TopicID = topicID
		tab.topicTitleSource = topicSource
	}
	if topicTitle != "" {
		changed = changed || tab.TopicTitle != topicTitle
		tab.TopicTitle = topicTitle
	}
	if changed && current == tab {
		a.saveTabsLocked()
	}
	sink := tab.sink
	a.mu.Unlock()
	if workspaceChanged && a.workspaceHub != nil {
		a.workspaceHub.reconcileRoots()
	}
	if reopenTerminalGate {
		a.terminals.reopenForTab(tab.ID)
	}
	if len(terminalSessions) > 0 {
		a.terminals.closeSessions(terminalSessions)
	}
	if workspaceChanged && sink != nil {
		sink.Emit(event.Event{
			Kind:  event.Notice,
			Level: event.LevelWarn,
			Text:  sessionBindingWorkspaceNotice(oldScope, oldWorkspaceRoot, scope, workspaceRoot),
		})
	}
}

func sessionBindingWorkspaceNotice(oldScope, oldWorkspaceRoot, scope, workspaceRoot string) string {
	return "Session belongs to " + describeSessionBindingWorkspace(scope, workspaceRoot) +
		"; switched tab from " + describeSessionBindingWorkspace(oldScope, oldWorkspaceRoot) +
		" to match the saved session."
}

func describeSessionBindingWorkspace(scope, workspaceRoot string) string {
	if strings.TrimSpace(scope) == "project" && strings.TrimSpace(workspaceRoot) != "" {
		// %q escapes Windows separators, which turns a user-facing path into
		// C:\\Users\\... in the notice. Preserve native separators while escaping
		// only the delimiters that can appear in a Unix path.
		root := strings.ReplaceAll(strings.TrimSpace(workspaceRoot), `"`, `\"`)
		return `project workspace "` + root + `"`
	}
	return "global workspace"
}

func (a *App) resolveSessionBinding(sessionPath string) (sessionBinding, bool) {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return sessionBinding{}, false
	}
	for _, dir := range a.knownSessionDirs() {
		if binding, ok := sessionBindingInDir(dir, sessionPath); ok {
			return binding, true
		}
	}
	if !filepath.IsAbs(sessionPath) {
		return sessionBinding{}, false
	}
	path, err := filepath.Abs(sessionPath)
	if err != nil {
		return sessionBinding{}, false
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		return sessionBinding{}, false
	}
	for _, dir := range sessionBindingCandidateDirs(meta) {
		if binding, ok := sessionBindingInDir(dir, path); ok {
			return binding, true
		}
	}
	return sessionBindingFromMeta(path, meta)
}

func sessionBindingCandidateDirs(meta agent.BranchMeta) []string {
	if meta.DefaultScope() == "project" {
		if root := normalizeProjectRoot(meta.WorkspaceRoot); root != "" {
			return []string{desktopSessionDir(root)}
		}
		return nil
	}
	return []string{desktopSessionDir(globalWorkspaceRoot()), config.SessionDir()}
}

func sessionBindingInDir(dir, sessionPath string) (sessionBinding, bool) {
	path, ok := pinnedTabSessionPath(dir, sessionPath)
	if !ok {
		return sessionBinding{}, false
	}
	meta, hasMeta, err := agent.LoadBranchMeta(path)
	if err != nil {
		return sessionBinding{}, false
	}
	scope, workspaceRoot, _, ownerOK := legacyMigrationTargetForDir(dir)
	if !ownerOK {
		if !hasMeta {
			return sessionBinding{}, false
		}
		return sessionBindingFromMeta(path, meta)
	}
	if scope == "global" {
		if !hasMeta {
			return sessionBinding{}, false
		}
		return sessionBindingFromMeta(path, meta)
	}
	binding := sessionBinding{
		path:          path,
		scope:         scope,
		workspaceRoot: workspaceRoot,
		hasMeta:       hasMeta,
		meta:          meta,
	}
	if hasMeta {
		binding.topicID = strings.TrimSpace(meta.TopicID)
		binding.topicTitle = strings.TrimSpace(meta.TopicTitle)
	}
	if binding.scope == "project" {
		binding.workspaceRoot = normalizeProjectRoot(binding.workspaceRoot)
	}
	return binding, true
}

func sessionBindingFromMeta(path string, meta agent.BranchMeta) (sessionBinding, bool) {
	scope := meta.DefaultScope()
	workspaceRoot := ""
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(meta.WorkspaceRoot)
		if workspaceRoot == "" {
			return sessionBinding{}, false
		}
	} else {
		scope = "global"
		workspaceRoot = globalTabWorkspaceRoot()
	}
	return sessionBinding{
		path:          path,
		scope:         scope,
		workspaceRoot: workspaceRoot,
		topicID:       strings.TrimSpace(meta.TopicID),
		topicTitle:    strings.TrimSpace(meta.TopicTitle),
		hasMeta:       true,
		meta:          meta,
	}, true
}

// active tab helpers

// activeTab returns the currently active tab (nil when there are no tabs).
// Self-locking; safe to call from any goroutine without external lock.
func (a *App) activeTab() *WorkspaceTab {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeTabID == "" {
		return nil
	}
	return a.tabs[a.activeTabID]
}

// activeTabLocked is like activeTab but assumes the caller already holds a.mu
// (either RLock or Lock). Use this inside critical sections that already own
// the lock to avoid double-locking a write-lock holder.
func (a *App) activeTabLocked() *WorkspaceTab {
	if a.activeTabID == "" {
		return nil
	}
	return a.tabs[a.activeTabID]
}

// activeCtrl returns the controller of the active tab, or nil.
// Self-locking; safe to call from any goroutine without external lock.
func (a *App) activeCtrl() control.SessionAPI {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeCtrlLocked()
}

// activeCtrlLocked is like activeCtrl but assumes the caller already holds a.mu.
func (a *App) activeCtrlLocked() control.SessionAPI {
	t := a.activeTabLocked()
	if t == nil {
		return nil
	}
	return t.Ctrl
}

func (a *App) tabByID(tabID string) *WorkspaceTab {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tabByIDLocked(tabID)
}

func (a *App) tabByIDLocked(tabID string) *WorkspaceTab {
	if strings.TrimSpace(tabID) == "" {
		return a.activeTabLocked()
	}
	return a.tabs[tabID]
}

func (a *App) ctrlByTabID(tabID string) control.SessionAPI {
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		return nil
	}
	return tab.Ctrl
}

// autosave per tab

const maxTabSnapshotFailureRetries = 2

// autosaveWarnInterval rate-limits the user-facing autosave-failure notice
// per tab; slog keeps recording every failure regardless.
const autosaveWarnInterval = 5 * time.Minute

func tabSnapshotRetryDelay(failures int) time.Duration {
	switch {
	case failures <= 1:
		return 100 * time.Millisecond
	case failures == 2:
		return 250 * time.Millisecond
	default:
		return 500 * time.Millisecond
	}
}

func (a *App) scheduleTabSnapshot(tabID string) {
	a.mu.RLock()
	tab := a.tabByEventSinkIDLocked(tabID)
	a.mu.RUnlock()
	if tab == nil {
		return
	}
	tab.saveMu.Lock()
	defer tab.saveMu.Unlock()
	if tab.closing {
		// Tab is being torn down: don't start new snapshot work that could
		// race DeleteSession and resurrect a trashed session file (#4384).
		return
	}
	if tab.saving {
		tab.saveAgain = true
		return
	}
	tab.saving = true
	tab.saveFailures = 0
	go a.tabSnapshotLoop(tab)
}

// quiesceTabAutosave marks the tab as closing and blocks until any in-flight
// tabSnapshotLoop has finished its current (and final) write. After it returns,
// no background goroutine can call Snapshot on this tab's controller again, so
// a subsequent DeleteSession cannot race a late write. Safe to call after the
// controller's session path has been cleared: the loop's Snapshot becomes a
// no-op and it exits on its next iteration.
func (a *App) quiesceTabAutosave(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	tab.saveMu.Lock()
	if tab.saveCond == nil {
		// saveCond is lazily initialized on first snapshot; if it was never
		// set there is no loop to wait for.
		tab.closing = true
		tab.saveMu.Unlock()
		return
	}
	tab.closing = true
	for tab.saving {
		tab.saveCond.Wait()
	}
	tab.saveMu.Unlock()
}

func (a *App) tabSnapshotLoop(tab *WorkspaceTab) {
	defer a.recoverToPending("tabSnapshotLoop")
	for {
		var snapshotErr error
		a.mu.RLock()
		ctrl := tab.Ctrl
		a.mu.RUnlock()
		if ctrl != nil {
			if err := a.snapshotTab(tab); err == nil {
				a.mu.RLock()
				scope, workspaceRoot := tab.Scope, tab.WorkspaceRoot
				a.mu.RUnlock()
				a.requestSessionCatalogPath(scope, workspaceRoot, ctrl.SessionPath())
				if !a.maybeAutoTitleTopic(tab) {
					a.emitProjectTreeChangedForSessionDirs(ctrl.SessionDir())
				}
			} else {
				snapshotErr = err
			}
		}
		tab.saveMu.Lock()
		if tab.saveCond == nil {
			tab.saveCond = sync.NewCond(&tab.saveMu)
		}
		if snapshotErr == nil {
			tab.saveFailures = 0
		} else {
			tab.saveFailures++
		}
		if tab.closing {
			// Tab is being torn down: stop without picking up saveAgain work.
			tab.saving = false
			tab.saveCond.Broadcast()
			tab.saveMu.Unlock()
			if snapshotErr != nil {
				slog.Warn("desktop: session autosave failed during teardown", "tab", tab.ID, "err", snapshotErr)
			}
			return
		}
		if tab.saveAgain {
			tab.saveAgain = false
			tab.saveMu.Unlock()
			if snapshotErr != nil {
				slog.Warn("desktop: session autosave failed; newer snapshot queued", "tab", tab.ID, "err", snapshotErr)
			}
			continue
		}
		if snapshotErr != nil && tab.saveFailures <= maxTabSnapshotFailureRetries {
			delay := tabSnapshotRetryDelay(tab.saveFailures)
			attempt := tab.saveFailures
			tab.saveMu.Unlock()
			// Retries are routine (transient AV/indexer holds); tell the user
			// only when the whole burst gives up, not once per attempt.
			slog.Warn("desktop: session autosave failed; retrying", "tab", tab.ID, "attempt", attempt, "err", snapshotErr)
			time.Sleep(delay)
			continue
		}
		exhausted := snapshotErr
		tab.saving = false
		tab.saveCond.Broadcast()
		tab.saveMu.Unlock()
		if exhausted != nil {
			a.reportTabSnapshotError(tab, "autosave", exhausted)
		}
		return
	}
}

func (a *App) maybeAutoTitleTopic(tab *WorkspaceTab) bool {
	if tab == nil {
		return false
	}
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	// Runs on the autosave goroutine; TopicID/Scope/WorkspaceRoot/Ctrl are
	// written under a.mu by session switches and recovery.
	a.mu.RLock()
	topicID := strings.TrimSpace(tab.TopicID)
	titleRoot := tab.WorkspaceRoot
	if tab.Scope == "global" {
		titleRoot = ""
	}
	ctrl := tab.Ctrl
	a.mu.RUnlock()
	if topicID == "" || ctrl == nil {
		return false
	}
	if source := loadTopicTitleSource(titleRoot, topicID); source != topicTitleSourceAuto {
		return false
	}
	sessionPath := ctrl.SessionPath()
	if sessionPath == "" {
		return false
	}
	if sessionHasManualDisplayTitle(sessionPath) {
		return false
	}
	nextTitle, updated := autoTitleTopicFromSession(titleRoot, topicID, sessionPath)
	if !updated {
		return false
	}
	if topicAutoTitleCommittedHookForTest != nil {
		topicAutoTitleCommittedHookForTest()
	}
	a.updateOpenTopicTitle(topicID, nextTitle, topicTitleSourceAuto)
	changedDirs := a.updateTopicSessionTitles(topicID, nextTitle)
	if len(changedDirs) > 0 {
		a.emitProjectTreeChangedForSessionDirs(changedDirs...)
	} else {
		a.emitProjectTreeMetadataChanged()
	}
	return true
}

func autoTitleTopicFromSession(workspaceRoot, topicID, sessionPath string) (string, bool) {
	if source := loadTopicTitleSource(workspaceRoot, topicID); source != topicTitleSourceAuto {
		return "", false
	}
	if sessionHasManualDisplayTitle(sessionPath) {
		return "", false
	}
	proposal := autoTopicTitleProposalFromSession(sessionPath)
	if proposal.Title == "" {
		return "", false
	}
	if !shouldApplyAutoTopicTitle(workspaceRoot, topicID, proposal) {
		return "", false
	}
	nextTitle := proposal.Title
	sameTitle := nextTitle == strings.TrimSpace(loadTopicTitle(workspaceRoot, topicID))
	applied, err := applyAutoTopicTitle(workspaceRoot, topicID, nextTitle, proposal)
	if err != nil || !applied {
		return "", false
	}
	if sameTitle {
		return "", false
	}
	return nextTitle, true
}

type autoTopicTitleProposal struct {
	Title     string
	Stage     int
	UserTurns int
	BasisHash string
}

func autoTopicTitleProposalFromSession(path string) autoTopicTitleProposal {
	users := topicTitleUserTurnsFromSession(path)
	if len(users) == 0 {
		return autoTopicTitleProposal{}
	}
	stage := 1
	if len(users) >= 3 {
		stage = 3
	}
	basis := users
	if len(basis) > stage {
		basis = basis[:stage]
	}
	title := topicTitleFromUserTurns(basis)
	if title == "" {
		return autoTopicTitleProposal{}
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s", stage, strings.Join(basis, "\x00")))
	return autoTopicTitleProposal{
		Title:     title,
		Stage:     stage,
		UserTurns: len(users),
		BasisHash: hex.EncodeToString(sum[:8]),
	}
}

func shouldApplyAutoTopicTitle(workspaceRoot, topicID string, proposal autoTopicTitleProposal) bool {
	if proposal.Stage <= 0 || proposal.BasisHash == "" {
		return false
	}
	meta := loadTopicAutoTitleMeta(workspaceRoot)[topicID]
	if meta.Stage > proposal.Stage {
		return false
	}
	if meta.Stage == proposal.Stage && meta.BasisHash == proposal.BasisHash {
		return false
	}
	return true
}

func sessionHasManualDisplayTitle(sessionPath string) bool {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return false
	}
	if meta, ok, err := agent.LoadBranchMeta(sessionPath); err == nil && ok {
		if strings.TrimSpace(meta.CustomTitle) != "" {
			return true
		}
	}
	dir := filepath.Dir(sessionPath)
	if dir == "." || dir == string(filepath.Separator) {
		return false
	}
	return strings.TrimSpace(loadSessionTitles(dir)[filepath.Base(sessionPath)]) != ""
}

func topicTitleFallbackForOpen(workspaceRoot, topicID, sessionPath string) (string, string, bool) {
	topicID = strings.TrimSpace(topicID)
	sessionPath = strings.TrimSpace(sessionPath)
	if topicID == "" || sessionPath == "" {
		return "", "", false
	}
	storedTitle := strings.TrimSpace(loadTopicTitle(workspaceRoot, topicID))
	storedSource := strings.TrimSpace(loadTopicTitleSource(workspaceRoot, topicID))
	if storedTitle != "" {
		if storedSource == topicTitleSourceManual || !isDefaultTopicTitle(storedTitle) {
			return "", "", false
		}
	}

	if storedTitle == "" {
		dir := filepath.Dir(sessionPath)
		if meta, ok, err := agent.LoadBranchMeta(sessionPath); err == nil && ok {
			if title := storedSessionTopicTitle(dir, sessionPath, meta); title != "" {
				return title, topicTitleSourceManual, true
			}
		} else if title := topicTitleFromText(loadSessionTitles(dir)[filepath.Base(sessionPath)]); title != "" {
			return title, topicTitleSourceManual, true
		}
	}

	if storedSource == topicTitleSourceManual {
		return "", "", false
	}
	if storedSource == "" || storedSource == topicTitleSourceAuto {
		if title := topicTitleFromSession(sessionPath); title != "" {
			return title, topicTitleSourceAuto, true
		}
	}
	return "", "", false
}

func topicTitleFromSession(path string) string {
	users := topicTitleUserTurnsFromSession(path)
	if len(users) == 0 {
		return ""
	}
	return topicTitleFromText(users[0])
}

func topicTitleUserTurnsFromSession(path string) []string {
	// Event-log aware: decoding the .jsonl checkpoint directly would stop
	// seeing user turns after the first save, silently disabling the ≥3-turn
	// title upgrade.
	msgs, err := agent.LoadSessionUserMessages(path)
	if err != nil {
		return nil
	}
	var users []string
	for _, msg := range msgs {
		// Host-injected synthetic turns (readiness nudges, recovery retries) and
		// mid-turn steers are persisted as role "user" but are not user-authored:
		// counting them inflated userTurns past the stage-3 threshold and let
		// "Host final-answer readiness check failed…" become a topic title.
		if !agent.IsUserAuthoredTurnMessage(msg.Message) {
			continue
		}
		// UserPreviewText is the canonical user-authored view: it unwraps
		// memory-compiler execution contracts and strips transient blocks
		// (and runs HandoffTask), so internal wrappers can never become a
		// title basis (#5666).
		content := control.StripComposePrefixes(agent.UserPreviewText(agent.UserMessageText(msg.Message)))
		content = control.StripReferencedContextPrefix(content)
		if strings.TrimSpace(content) != "" {
			users = append(users, content)
		}
	}
	return users
}

func topicTitleFromUserTurns(users []string) string {
	type candidate struct {
		title string
		score int
	}
	best := candidate{score: -1}
	for i, text := range users {
		title := topicTitleFromText(text)
		if title == "" || lowSignalTopicTitle(title) {
			continue
		}
		runes := len([]rune(title))
		score := min(runes, 24)
		if i == 0 {
			score += 3
		}
		if runes < 5 {
			score -= 6
		}
		if score > best.score {
			best = candidate{title: title, score: score}
		}
	}
	if best.title != "" {
		return best.title
	}
	if len(users) > 0 {
		return topicTitleFromText(users[0])
	}
	return ""
}

func lowSignalTopicTitle(title string) bool {
	normalized := strings.ToLower(strings.TrimSpace(title))
	normalized = strings.Trim(normalized, " \t\r\n，。！？；：、,.!?;:\"'`“”‘’()（）[]【】")
	switch normalized {
	case "", "好", "好的", "好啊", "可以", "嗯", "对", "是的", "继续", "继续吧", "采纳建议", "采用建议", "收到", "明白", "ok", "okay", "yes", "yep", "go on", "continue", "thanks", "thank you":
		return true
	default:
		return false
	}
}

func topicTitleFromText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Trim(text, " \t\r\n，。！？；：、,.!?;:\"'`“”‘’()（）[]【】")
	if text == "" {
		return ""
	}
	const maxRunes = 18
	runes := []rune(text)
	if len(runes) > maxRunes {
		text = strings.TrimRightFunc(string(runes[:maxRunes]), unicode.IsPunct) + "…"
	}
	if isDefaultTopicTitle(text) {
		return ""
	}
	return text
}

// persistence: desktop-projects.json

const desktopProjectsFile = "desktop-projects.json"
const tabsFileName = "desktop-tabs.json"
const desktopGlobalOrderToken = "__global__"
const legacyProjectSidebarRecoveryMarker = "desktop-projects-legacy-recovered"

var desktopProjectsFileMu sync.Mutex

func desktopConfigDir() string {
	return config.ReasonixHomeDir()
}

func (a *App) saveTabsLocked() {
	dir, entries, activeID, version := a.saveTabsCollectLocked()
	a.saveTabsWrite(dir, entries, activeID, version)
}

// saveTabsCollectLocked gathers the tab-snapshot data under the caller's lock
// (it calls orderedTabIDsLocked which requires a.mu). Returns the config dir,
// the serializable entries, the active tab ID, and a monotonic snapshot version.
// The write can happen outside the lock to avoid blocking the UI with disk I/O.
func (a *App) saveTabsCollectLocked() (string, []desktopTabEntry, string, uint64) {
	dir := desktopConfigDir()
	var entries []desktopTabEntry
	for _, id := range a.orderedTabIDsLocked() {
		if tab := a.tabs[id]; tab != nil {
			if a.suppressTabStartupRestoreLocked(tab) {
				continue
			}
			entries = append(entries, persistedDesktopTabEntry(tab))
		}
	}
	a.tabsSaveVersion++
	return dir, entries, persistedActiveTabID(entries, a.activeTabID), a.tabsSaveVersion
}

// saveTabsWrite writes the tab-snapshot to disk. It does not require a.mu, but
// writes must be serialized because every save uses the same destination and
// fixed .tmp path.
func (a *App) saveTabsWrite(dir string, entries []desktopTabEntry, activeID string, version uint64) {
	a.tabsSaveMu.Lock()
	defer a.tabsSaveMu.Unlock()
	if version < a.tabsLastWrittenVersion {
		return
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	localIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		localIDs = append(localIDs, entry.ID)
	}
	remoteEntries, remoteOrder, tabOrder, remoteActive := a.remoteTabsFileEntries(localIDs)
	if remoteActive != "" {
		activeID = remoteActive
	}
	f := desktopTabsFile{Tabs: entries, ActiveTab: activeID, RemoteTabs: remoteEntries, RemoteTabOrder: remoteOrder, TabOrder: tabOrder}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(dir, tabsFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	if err := fileutil.ReplaceFile(tmp, path); err != nil {
		return
	}
	a.tabsLastWrittenVersion = version
}

func (a *App) orderedTabIDsLocked() []string {
	ordered, needsRepair := a.orderedTabIDsSnapshotLocked()
	if needsRepair {
		a.tabOrder = append([]string(nil), ordered...)
	}
	return ordered
}

func (a *App) orderedTabIDsSnapshotLocked() ([]string, bool) {
	seen := make(map[string]bool, len(a.tabs))
	ordered := make([]string, 0, len(a.tabs))
	for _, id := range a.tabOrder {
		if _, ok := a.tabs[id]; ok && !seen[id] {
			ordered = append(ordered, id)
			seen[id] = true
		}
	}
	var missing []string
	for id := range a.tabs {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	ordered = append(ordered, missing...)
	return ordered, len(ordered) != len(a.tabOrder) || len(missing) > 0
}

func (a *App) removeTabOrderLocked(tabID string) {
	next := a.tabOrder[:0]
	for _, id := range a.tabOrder {
		if id != tabID {
			next = append(next, id)
		}
	}
	a.tabOrder = next
}

func loadTabsFile() desktopTabsFile {
	path := filepath.Join(desktopConfigDir(), tabsFileName)
	b, err := readFileUTF8(path)
	if err != nil {
		return desktopTabsFile{}
	}
	var f desktopTabsFile
	_ = json.Unmarshal(b, &f)
	return f
}

func desktopMCPMigrationRoots(tabs desktopTabsFile) []string {
	seen := map[string]bool{}
	var roots []string
	add := func(root string) {
		root = normalizeProjectRoot(root)
		key := projectRootKey(root)
		if root == "" || seen[key] {
			return
		}
		seen[key] = true
		roots = append(roots, root)
	}
	if cur := loadWorkspace(); cur != "" {
		add(cur)
	}
	for _, root := range loadWorkspaces() {
		add(root)
	}
	for _, entry := range tabs.Tabs {
		if entry.Scope == "project" {
			add(entry.WorkspaceRoot)
		}
	}
	for _, project := range loadProjectsFile().Projects {
		add(project.Root)
	}
	return roots
}

func recoverLegacyProjectSidebarRoots(tabs desktopTabsFile) (bool, error) {
	markerPath := filepath.Join(desktopConfigDir(), legacyProjectSidebarRecoveryMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return false, nil
	}

	changed := false
	err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		seen := map[string]bool{}
		for _, project := range f.Projects {
			root := normalizeProjectRoot(project.Root)
			if root != "" {
				seen[projectRootKey(root)] = true
			}
		}

		add := func(root string) {
			root = normalizeProjectRoot(root)
			key := projectRootKey(root)
			if root == "" || seen[key] || !existingDirectory(root) {
				return
			}
			seen[key] = true
			f.Projects = append(f.Projects, desktopProject{Root: root})
			changed = true
		}
		if cur := loadWorkspace(); cur != "" {
			add(cur)
		}
		for _, root := range loadWorkspaces() {
			add(root)
		}
		for _, entry := range tabs.Tabs {
			if entry.Scope == "project" {
				add(entry.WorkspaceRoot)
			}
		}
		return changed, nil
	})
	if err != nil {
		return false, err
	}
	return changed, writeLegacyProjectSidebarRecoveryMarker(markerPath)
}

func existingDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func writeLegacyProjectSidebarRecoveryMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("ok\n"), 0o644)
}

func loadProjectsFile() desktopProjectFile {
	path := filepath.Join(desktopConfigDir(), desktopProjectsFile)
	b, err := readFileUTF8(path)
	if err != nil {
		return desktopProjectFile{}
	}
	var f desktopProjectFile
	_ = json.Unmarshal(b, &f)
	f = normalizeProjectsFile(f)
	if organization, ok := loadProjectOrganizationFile(); ok {
		return applyProjectOrganization(f, organization)
	}
	// Upgrade existing inline organization state immediately. The sidecar is
	// what makes a later old-version save non-destructive.
	if projectsFileHasOrganization(f) {
		_ = saveProjectOrganizationFile(f)
	}
	return f
}

func saveProjectsFile(f desktopProjectFile) error {
	dir := desktopConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f = normalizeProjectsFile(f)
	if err := saveProjectOrganizationFile(f); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, desktopProjectsFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return fileutil.ReplaceFile(tmp, path)
}

func updateProjectsFile(mutator func(*desktopProjectFile) (bool, error)) error {
	desktopProjectsFileMu.Lock()
	defer desktopProjectsFileMu.Unlock()

	f := loadProjectsFile()
	changed, err := mutator(&f)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return saveProjectsFile(f)
}

func prependTopicInProjectsFile(workspaceRoot, topicID string, ensureProject bool) error {
	// Single-topic prepends are intentional writes (topic creation, a live tab
	// indexing its session, restore from trash): they clear any delete
	// tombstone so the topic fully returns instead of landing in a half-state
	// where only its title resurfaces.
	return prependTopicsInProjectsFileOpts(workspaceRoot, []string{topicID}, ensureProject, false)
}

func prependTopicsInProjectsFile(workspaceRoot string, topicIDs []string, ensureProject bool) error {
	// Batch prepends come from the legacy migration and index-repair scans:
	// they must respect delete tombstones so a scan never resurrects a topic
	// the user removed.
	return prependTopicsInProjectsFileOpts(workspaceRoot, topicIDs, ensureProject, true)
}

func prependTopicsInProjectsFileOpts(workspaceRoot string, topicIDs []string, ensureProject, respectTombstones bool) error {
	workspaceRoot = normalizeProjectRoot(workspaceRoot)
	topicIDs = uniqueStrings(topicIDs)
	if len(topicIDs) == 0 {
		return nil
	}
	return updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		// Tombstones are checked under the projects-file lock: a DeleteTopic
		// that lands between a scan reading DeletedTopics and this write must
		// not be resurrected by the stale batch.
		live := topicIDs
		changed := false
		if respectTombstones {
			live = make([]string, 0, len(topicIDs))
			for _, id := range topicIDs {
				if !containsDesktopString(f.DeletedTopics, id) {
					live = append(live, id)
				}
			}
			if len(live) == 0 {
				return false, nil
			}
		} else {
			for _, id := range topicIDs {
				if next := removeString(f.DeletedTopics, id); !sameStringList(next, f.DeletedTopics) {
					f.DeletedTopics = next
					changed = true
				}
			}
		}
		if workspaceRoot == "" {
			next := uniqueStrings(append(append([]string(nil), live...), f.GlobalTopics...))
			if sameStringList(next, f.GlobalTopics) {
				return changed, nil
			}
			f.GlobalTopics = next
			return true, nil
		}
		for i, p := range f.Projects {
			if !sameProjectRoot(p.Root, workspaceRoot) {
				continue
			}
			next := uniqueStrings(append(append([]string(nil), live...), p.Topics...))
			if sameStringList(next, p.Topics) {
				return changed, nil
			}
			f.Projects[i].Topics = next
			return true, nil
		}
		if !ensureProject {
			return changed, nil
		}
		f.Projects = append(f.Projects, desktopProject{Root: workspaceRoot, Topics: live})
		return true, nil
	})
}

func removeTopicFromProjectsFile(topicID string) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil
	}
	return updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		changed := false
		if next := removeString(f.GlobalTopics, topicID); !sameStringList(next, f.GlobalTopics) {
			f.GlobalTopics = next
			changed = true
		}
		if next := removeString(f.GlobalPinnedTopics, topicID); !sameStringList(next, f.GlobalPinnedTopics) {
			f.GlobalPinnedTopics = next
			changed = true
		}
		if next, removed := groupsWithoutTopic(f.GlobalGroups, topicID); removed {
			f.GlobalGroups = next
			f.GlobalGroupsRevision++
			changed = true
		}
		if next := prependUniqueString(f.DeletedTopics, topicID); !sameStringList(next, f.DeletedTopics) {
			f.DeletedTopics = next
			changed = true
		}
		for i, p := range f.Projects {
			if next := removeString(p.Topics, topicID); !sameStringList(next, p.Topics) {
				f.Projects[i].Topics = next
				changed = true
			}
			if next := removeString(p.PinnedTopics, topicID); !sameStringList(next, p.PinnedTopics) {
				f.Projects[i].PinnedTopics = next
				changed = true
			}
			if next, removed := groupsWithoutTopic(p.Groups, topicID); removed {
				f.Projects[i].Groups = next
				f.Projects[i].GroupsRevision++
				changed = true
			}
		}
		return changed, nil
	})
}

func normalizeProjectRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

func sameProjectRoot(a, b string) bool {
	return sameDesktopPath(normalizeProjectRoot(a), normalizeProjectRoot(b))
}

func projectIndexByRoot(projects []desktopProject, root string) int {
	root = normalizeProjectRoot(root)
	if root == "" {
		return -1
	}
	for i, project := range projects {
		if sameProjectRoot(project.Root, root) {
			return i
		}
	}
	return -1
}

func projectRootInList(roots []string, root string) bool {
	root = normalizeProjectRoot(root)
	if root == "" {
		return false
	}
	for _, candidate := range roots {
		if sameProjectRoot(candidate, root) {
			return true
		}
	}
	return false
}

func normalizeProjectsFile(f desktopProjectFile) desktopProjectFile {
	out := desktopProjectFile{
		GlobalTitle:            strings.TrimSpace(f.GlobalTitle),
		GlobalColor:            normalizeProjectColor(f.GlobalColor),
		GlobalTopics:           uniqueStrings(f.GlobalTopics),
		GlobalPinnedTopics:     uniqueStrings(f.GlobalPinnedTopics),
		GlobalManualTopicOrder: f.GlobalManualTopicOrder,
		GlobalGroups:           normalizeGroups(f.GlobalGroups),
		GlobalGroupsRevision:   f.GlobalGroupsRevision,
		DeletedTopics:          uniqueStrings(f.DeletedTopics),
	}
	for _, p := range f.Projects {
		root := normalizeProjectRoot(p.Root)
		if root == "" {
			continue
		}
		p.Root = root
		p.Title = strings.TrimSpace(p.Title)
		p.Color = normalizeProjectColor(p.Color)
		p.Topics = uniqueStrings(p.Topics)
		p.PinnedTopics = uniqueStrings(p.PinnedTopics)
		p.Groups = normalizeGroups(p.Groups)
		if i := projectIndexByRoot(out.Projects, root); i >= 0 {
			if out.Projects[i].Title == "" && p.Title != "" {
				out.Projects[i].Title = p.Title
			}
			if out.Projects[i].Color == "" && p.Color != "" {
				out.Projects[i].Color = p.Color
			}
			out.Projects[i].Topics = uniqueStrings(append(out.Projects[i].Topics, p.Topics...))
			out.Projects[i].PinnedTopics = uniqueStrings(append(out.Projects[i].PinnedTopics, p.PinnedTopics...))
			out.Projects[i].ManualTopicOrder = out.Projects[i].ManualTopicOrder || p.ManualTopicOrder
			out.Projects[i].Groups = mergeDesktopGroups(out.Projects[i].Groups, p.Groups)
			out.Projects[i].GroupsRevision = max(out.Projects[i].GroupsRevision, p.GroupsRevision)
			continue
		}
		out.Projects = append(out.Projects, p)
	}
	for _, root := range uniqueStrings(f.PinnedProjects) {
		root = normalizeProjectRoot(root)
		if i := projectIndexByRoot(out.Projects, root); i >= 0 && !projectRootInList(out.PinnedProjects, out.Projects[i].Root) {
			out.PinnedProjects = append(out.PinnedProjects, out.Projects[i].Root)
		}
	}
	out.SidebarOrder = normalizeSidebarOrder(f.SidebarOrder, out.Projects)
	return out
}

func normalizeSidebarOrder(order []string, projects []desktopProject) []string {
	seenGlobal := false
	// Dedupe roots against a roots-only list: out also holds the global order
	// token, which must never be path-compared against project roots.
	var seenRoots []string
	out := make([]string, 0, len(order))
	for _, value := range order {
		value = strings.TrimSpace(value)
		if value == desktopGlobalOrderToken {
			if !seenGlobal {
				seenGlobal = true
				out = append(out, value)
			}
			continue
		}
		root := normalizeProjectRoot(value)
		i := projectIndexByRoot(projects, root)
		if i < 0 {
			continue
		}
		root = projects[i].Root
		if projectRootInList(seenRoots, root) {
			continue
		}
		seenRoots = append(seenRoots, root)
		out = append(out, root)
	}
	return out
}

func sameProjectOrder(a, b []desktopProject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Root != b[i].Root {
			return false
		}
	}
	return true
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func prependUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return uniqueStrings(values)
	}
	return uniqueStrings(append([]string{value}, values...))
}

func removeString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return uniqueStrings(values)
	}
	out := make([]string, 0, len(values))
	for _, item := range uniqueStrings(values) {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

func containsDesktopString(values []string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return slices.Contains(uniqueStrings(values), value)
}

func pinnedTopicIDs(topicIDs []string, pinned []string) []string {
	if len(topicIDs) == 0 || len(pinned) == 0 {
		return topicIDs
	}
	available := make(map[string]bool, len(topicIDs))
	for _, tid := range topicIDs {
		available[tid] = true
	}
	out := make([]string, 0, len(topicIDs))
	seen := make(map[string]bool, len(topicIDs))
	for _, tid := range uniqueStrings(pinned) {
		if available[tid] && !seen[tid] {
			out = append(out, tid)
			seen[tid] = true
		}
	}
	for _, tid := range topicIDs {
		if !seen[tid] {
			out = append(out, tid)
		}
	}
	return out
}

func orderedTopicIDs(explicit []string, titleMap map[string]string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(explicit)+len(titleMap))
	for _, tid := range explicit {
		tid = strings.TrimSpace(tid)
		if tid == "" || seen[tid] {
			continue
		}
		seen[tid] = true
		out = append(out, tid)
	}
	var remaining []string
	for tid := range titleMap {
		if !seen[tid] {
			remaining = append(remaining, tid)
		}
	}
	sort.Strings(remaining)
	return append(out, remaining...)
}

func projectTreeOrderKey(node ProjectNode) string {
	switch node.Kind {
	case "global_folder":
		return desktopGlobalOrderToken
	case "project":
		return normalizeProjectRoot(node.Root)
	default:
		return ""
	}
}

func applyProjectTreeOrder(nodes []ProjectNode, order []string) []ProjectNode {
	if len(order) == 0 {
		return nodes
	}
	byKey := make(map[string]ProjectNode, len(nodes))
	for _, node := range nodes {
		key := projectTreeOrderKey(node)
		if key != "" {
			byKey[key] = node
		}
	}
	seen := make(map[string]bool, len(nodes))
	out := make([]ProjectNode, 0, len(nodes))
	for _, value := range order {
		key := strings.TrimSpace(value)
		if key != desktopGlobalOrderToken {
			key = normalizeProjectRoot(key)
		}
		if key == "" || seen[key] {
			continue
		}
		node, ok := byKey[key]
		if !ok {
			continue
		}
		seen[key] = true
		out = append(out, node)
	}
	for _, node := range nodes {
		key := projectTreeOrderKey(node)
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		out = append(out, node)
	}
	return out
}

func applyPinnedProjectOrder(nodes []ProjectNode, pinnedRoots []string) []ProjectNode {
	pinnedRoots = uniqueStrings(pinnedRoots)
	if len(pinnedRoots) == 0 {
		return nodes
	}
	byRoot := make(map[string]ProjectNode, len(nodes))
	for _, node := range nodes {
		if node.Kind == "project" && node.Root != "" {
			byRoot[normalizeProjectRoot(node.Root)] = node
		}
	}
	seen := make(map[string]bool, len(pinnedRoots))
	out := make([]ProjectNode, 0, len(nodes))
	for _, root := range pinnedRoots {
		root = normalizeProjectRoot(root)
		node, ok := byRoot[root]
		if !ok || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, node)
	}
	for _, node := range nodes {
		if node.Kind == "project" && node.Root != "" && seen[normalizeProjectRoot(node.Root)] {
			continue
		}
		out = append(out, node)
	}
	return out
}

func projectDisplayName(p desktopProject) string {
	if title := strings.TrimSpace(p.Title); title != "" {
		return title
	}
	return workspaceName(p.Root)
}

func normalizeProjectColor(color string) string {
	switch strings.TrimSpace(strings.ToLower(color)) {
	case "red", "orange", "amber", "green", "teal", "blue", "purple", "pink":
		return strings.TrimSpace(strings.ToLower(color))
	default:
		return ""
	}
}

func projectColor(root string) string {
	root = normalizeProjectRoot(root)
	if root == "" {
		return globalProjectColor()
	}
	for _, p := range loadProjectsFile().Projects {
		if sameProjectRoot(p.Root, root) {
			return normalizeProjectColor(p.Color)
		}
	}
	return ""
}

func globalProjectColor() string {
	return normalizeProjectColor(loadProjectsFile().GlobalColor)
}

func globalProjectTitle() string {
	if title := strings.TrimSpace(loadProjectsFile().GlobalTitle); title != "" {
		return title
	}
	return "Global"
}

func addProject(root, title string) error {
	root = normalizeProjectRoot(root)
	if root == "" {
		return fmt.Errorf("project root is required")
	}
	title = strings.TrimSpace(title)
	return updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		for i, p := range f.Projects {
			if sameProjectRoot(p.Root, root) {
				changed := false
				if f.Projects[i].Root != root {
					f.Projects[i].Root = root
					changed = true
				}
				if title != "" && f.Projects[i].Title != title {
					f.Projects[i].Title = title
					changed = true
				}
				if !changed {
					return false, nil
				}
				return true, nil
			}
		}
		f.Projects = append(f.Projects, desktopProject{Root: root, Title: title})
		return true, nil
	})
}

func renameProject(root, title string) error {
	title = strings.TrimSpace(title)
	root = normalizeProjectRoot(root)
	return updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		if root == "" {
			if f.GlobalTitle == title {
				return false, nil
			}
			f.GlobalTitle = title
			return true, nil
		}
		for i, p := range f.Projects {
			if sameProjectRoot(p.Root, root) {
				if f.Projects[i].Root == root && f.Projects[i].Title == title {
					return false, nil
				}
				f.Projects[i].Root = root
				f.Projects[i].Title = title
				return true, nil
			}
		}
		f.Projects = append(f.Projects, desktopProject{Root: root, Title: title})
		return true, nil
	})
}

func setProjectColor(root, color string) error {
	root = normalizeProjectRoot(root)
	color = normalizeProjectColor(color)
	return updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		if root == "" {
			if f.GlobalColor == color {
				return false, nil
			}
			f.GlobalColor = color
			return true, nil
		}
		for i, p := range f.Projects {
			if sameProjectRoot(p.Root, root) {
				if f.Projects[i].Root == root && f.Projects[i].Color == color {
					return false, nil
				}
				f.Projects[i].Root = root
				f.Projects[i].Color = color
				return true, nil
			}
		}
		f.Projects = append(f.Projects, desktopProject{Root: root, Color: color})
		return true, nil
	})
}

func removeProject(root string) error {
	root = normalizeProjectRoot(root)
	return updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		projects := make([]desktopProject, 0, len(f.Projects))
		for _, p := range f.Projects {
			if !sameProjectRoot(p.Root, root) {
				projects = append(projects, p)
			}
		}
		if len(projects) == len(f.Projects) {
			return false, nil
		}
		f.Projects = projects
		return true, nil
	})
}

// topic helpers

const (
	topicTitlesFile        = "desktop-topic-titles.json"
	topicTitleSourcesFile  = "desktop-topic-title-sources.json"
	topicCreatedAtsFile    = "desktop-topic-created-at.json"
	topicAutoTitlesFile    = "desktop-topic-auto-title-meta.json"
	defaultTopicTitle      = "新的会话"
	defaultTopicTitleEn    = "New session"
	defaultTopicTitleZhTW  = "新的會話"
	topicTitleSourceAuto   = "auto"
	topicTitleSourceManual = "manual"
)

const (
	desktopLocaleUnknown int32 = iota
	desktopLocaleEn
	desktopLocaleZh
	desktopLocaleZhTW
)

func (a *App) setDesktopLocale(locale string) {
	normalized := strings.ToLower(strings.TrimSpace(locale))
	switch {
	case strings.HasPrefix(normalized, "zh-tw"), strings.HasPrefix(normalized, "zh-hant"):
		a.desktopLocale.Store(desktopLocaleZhTW)
	case strings.HasPrefix(normalized, "zh"):
		a.desktopLocale.Store(desktopLocaleZh)
	default:
		a.desktopLocale.Store(desktopLocaleEn)
	}
}

func (a *App) localizedDefaultTopicTitle() string {
	switch a.desktopLocale.Load() {
	case desktopLocaleZh:
		return defaultTopicTitle
	case desktopLocaleZhTW:
		return defaultTopicTitleZhTW
	case desktopLocaleEn:
		return defaultTopicTitleEn
	default:
		return defaultTopicTitle
	}
}

func isDefaultTopicTitle(title string) bool {
	switch strings.TrimSpace(title) {
	case defaultTopicTitle, defaultTopicTitleEn, defaultTopicTitleZhTW:
		return true
	default:
		return false
	}
}

func (a *App) localizedTopicTitle(title, source string) string {
	if strings.TrimSpace(source) == topicTitleSourceAuto && isDefaultTopicTitle(title) {
		return a.localizedDefaultTopicTitle()
	}
	return title
}

const topicFileReadTimeout = 200 * time.Millisecond

var readFileWithTimeoutSlots = make(chan struct{}, 16)

func readFileWithTimeout(path string, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		return readFileUTF8(path)
	}
	select {
	case readFileWithTimeoutSlots <- struct{}{}:
	default:
		return nil, fmt.Errorf("too many pending file reads")
	}
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := readFileUTF8(path)
		<-readFileWithTimeoutSlots
		ch <- result{data: data, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.data, r.err
	case <-timer.C:
		return nil, fmt.Errorf("timed out after %v reading %s", timeout, filepath.Base(path))
	}
}

type topicAutoTitleMeta struct {
	Stage     int    `json:"stage,omitempty"`
	UserTurns int    `json:"userTurns,omitempty"`
	BasisHash string `json:"basisHash,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

func loadStringMapForUpdate(path string) (map[string]string, error) {
	m := map[string]string{}
	b, err := readFileUTF8(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]string{}, nil
	}
	return m, nil
}

func loadTopicTitlesForUpdate(workspaceRoot string) (map[string]string, error) {
	snapshot, err := desktopTopicState.snapshot(workspaceRoot)
	if err != nil {
		if !legacyTopicFilesExist(workspaceRoot) {
			return nil, err
		}
		legacy, legacyErr := loadLegacyStringMap(topicTitlesPath(workspaceRoot))
		if legacyErr != nil {
			return nil, errors.Join(err, legacyErr)
		}
		return legacy, nil
	}
	values := make(map[string]string, len(snapshot.Records))
	for id, record := range snapshot.Records {
		if record.Title != "" {
			values[id] = agent.UserPreviewText(record.Title)
		}
	}
	return values, nil
}

func loadTopicTitleSourcesForUpdate(workspaceRoot string) (map[string]string, error) {
	snapshot, err := desktopTopicState.snapshot(workspaceRoot)
	if err != nil {
		if !legacyTopicFilesExist(workspaceRoot) {
			return nil, err
		}
		legacy, legacyErr := loadLegacyStringMap(topicTitleSourcesPath(workspaceRoot))
		if legacyErr != nil {
			return nil, errors.Join(err, legacyErr)
		}
		return legacy, nil
	}
	values := make(map[string]string, len(snapshot.Records))
	for id, record := range snapshot.Records {
		if record.TitleSource != "" {
			values[id] = record.TitleSource
		}
	}
	return values, nil
}

func saveTopicTitles(workspaceRoot string, m map[string]string) error {
	return desktopTopicState.replaceTitles(workspaceRoot, m)
}

func saveTopicTitleSources(workspaceRoot string, m map[string]string) error {
	return desktopTopicState.replaceSources(workspaceRoot, m)
}

func saveTopicCreatedAts(workspaceRoot string, m map[string]int64) error {
	return desktopTopicState.replaceCreatedAts(workspaceRoot, m)
}

func loadTopicTitle(workspaceRoot, topicID string) string {
	return loadTopicTitles(workspaceRoot)[topicID]
}

func loadTopicTitleSource(workspaceRoot, topicID string) string {
	return loadTopicTitleSources(workspaceRoot)[topicID]
}

func loadTopicCreatedAt(workspaceRoot, topicID string) int64 {
	return loadTopicCreatedAts(workspaceRoot)[topicID]
}

func topicIDCreatedAt(topicID string) int64 {
	topicID = strings.TrimSpace(topicID)
	for _, prefix := range []string{"topic_", "legacy_"} {
		if !strings.HasPrefix(topicID, prefix) {
			continue
		}
		stamp := strings.TrimPrefix(topicID, prefix)
		if len(stamp) < len("20060102-150405") {
			continue
		}
		stamp = stamp[:len("20060102-150405")]
		t, err := time.ParseInLocation("20060102-150405", stamp, time.UTC)
		if err != nil {
			continue
		}
		return t.UnixMilli()
	}
	return 0
}

func topicCreatedAtForTree(createdAts map[string]int64, topicID string) int64 {
	if createdAt := createdAts[topicID]; createdAt > 0 {
		return createdAt
	}
	return topicIDCreatedAt(topicID)
}

func topicTitleForTab(scope, workspaceRoot, topicID string) string {
	titleRoot := topicTitleRoot(scope, workspaceRoot)
	if title := strings.TrimSpace(loadTopicTitle(titleRoot, topicID)); title != "" {
		return title
	}
	if scope == "global" {
		return "Global"
	}
	return defaultTopicTitle
}

func topicTitleRoot(scope, workspaceRoot string) string {
	if scope == "global" {
		return ""
	}
	return workspaceRoot
}

func (a *App) forkTopicTitle(title string) string {
	base := strings.TrimSpace(title)
	if base == "" || isDefaultTopicTitle(base) || base == "Global" {
		switch a.desktopLocale.Load() {
		case desktopLocaleEn:
			return "Forked session"
		case desktopLocaleZhTW:
			return "分叉會話"
		default:
			return "分叉会话"
		}
	}
	if strings.HasSuffix(base, " · 分叉") || strings.HasSuffix(base, " · fork") {
		return base
	}
	if a.desktopLocale.Load() == desktopLocaleEn {
		return base + " · fork"
	}
	return base + " · 分叉"
}

type sessionRecoveryEvent struct {
	ConversationID    string `json:"conversationId,omitempty"`
	ActiveVersionID   string `json:"activeVersionId,omitempty"`
	RecoveryVersionID string `json:"recoveryVersionId,omitempty"`
	OriginalPath      string `json:"originalPath,omitempty"`
	RecoveryPath      string `json:"recoveryPath"`
	Scope             string `json:"scope,omitempty"`
	WorkspaceRoot     string `json:"workspaceRoot,omitempty"`
	TopicID           string `json:"topicId,omitempty"`
	TopicTitle        string `json:"topicTitle,omitempty"`
	RecoveryReason    string `json:"recoveryReason,omitempty"`
	RecoveryDigest    string `json:"recoveryDigest,omitempty"`
	RecoveryParentID  string `json:"recoveryParentId,omitempty"`
	Existing          bool   `json:"existing,omitempty"`
	BaseRevision      int64  `json:"baseRevision,omitempty"`
	DiskRevision      int64  `json:"diskRevision,omitempty"`
	CanContinue       bool   `json:"canContinue"`
	RequiresChoice    bool   `json:"requiresChoice"`
}

type sessionRecoveryFailedEvent struct {
	Reason          string `json:"reason,omitempty"`
	ConversationID  string `json:"conversationId,omitempty"`
	TopicID         string `json:"topicId,omitempty"`
	RecoveryPath    string `json:"recoveryPath,omitempty"`
	WorkspaceRoot   string `json:"workspaceRoot,omitempty"`
	CanContinue     bool   `json:"canContinue"`
	RecoveryPending bool   `json:"recoveryPending"`
}

func (a *App) tabSessionRecoveryMeta(tab *WorkspaceTab) func(control.SessionRecoveryRequest) agent.BranchMeta {
	return func(req control.SessionRecoveryRequest) agent.BranchMeta {
		if tab == nil {
			return agent.BranchMeta{Name: agent.RecoveryBranchDefaultName}
		}
		// This runs on the snapshot-recovery path, which can fire from the
		// controller's autosave goroutine; snapshot the tab fields under a.mu so
		// we don't read them mid-mutation. Recovery callbacks never hold a.mu, so
		// taking it here can't deadlock. Controller reads happen off-lock.
		a.mu.RLock()
		ctrl := tab.Ctrl
		scope := strings.TrimSpace(tab.Scope)
		workspaceRoot := strings.TrimSpace(tab.WorkspaceRoot)
		topicID := tab.TopicID
		topicTitle := tab.TopicTitle
		model := strings.TrimSpace(tab.model)
		tokenMode := boot.TokenModeFull // deprecated dual-write compat value
		qualityFloor := strings.TrimSpace(tab.qualityFloor)
		mode := normalizeTabMode(tab.mode)
		toolApprovalMode := normalizeToolApprovalMode(tab.toolApprovalMode)
		goal := strings.TrimSpace(tab.goal)
		a.mu.RUnlock()
		if ctrl != nil {
			mode = tabModeFromAxes(ctrl.PlanMode(), ctrl.AutoApproveTools())
			toolApprovalMode = normalizeToolApprovalMode(ctrl.ToolApprovalMode())
			if g := strings.TrimSpace(ctrl.Goal()); g != "" && ctrl.GoalStatus() == control.GoalStatusRunning {
				goal = g
			} else {
				goal = ""
			}
		}
		if scope != "project" {
			scope = "global"
		}
		if scope == "global" {
			workspaceRoot = ""
		}
		return agent.BranchMeta{
			Name:             agent.RecoveryBranchDefaultName,
			Scope:            scope,
			WorkspaceRoot:    workspaceRoot,
			TopicID:          topicID,
			TopicTitle:       topicTitle,
			Model:            model,
			AgentPreset:      currentTabAgentPreset(&WorkspaceTab{qualityFloor: qualityFloor}),
			QualityFloor:     qualityFloor,
			TokenMode:        tokenMode,
			Mode:             persistedTabMode(mode),
			ToolApprovalMode: persistedToolApprovalMode(toolApprovalMode),
			Goal:             goal,
		}
	}
}

// emitSessionRecoveredAndRefresh registers the frontend pending item before a
// catalog reconcile can publish the revision that classifies it.
func (a *App) emitSessionRecoveredAndRefresh(dir string, recovered sessionRecoveryEvent) {
	a.emitRuntimeEvent("session:recovered", recovered)
	a.emitProjectTreeChangedForSessionDirs(dir)
}

func setTopicTitle(workspaceRoot, topicID, title string) error {
	return setTopicTitleWithSource(workspaceRoot, topicID, title, topicTitleSourceManual)
}

func setTopicTitleWithSource(workspaceRoot, topicID, title, source string) error {
	return desktopTopicState.setTitle(workspaceRoot, topicID, title, source)
}

func createTopicState(workspaceRoot, topicID, title, source string, createdAt int64) error {
	return desktopTopicState.createTopic(workspaceRoot, topicID, title, source, createdAt)
}

func recordTopicAutoTitleMeta(workspaceRoot, topicID string, proposal autoTopicTitleProposal) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" || proposal.Stage <= 0 || proposal.BasisHash == "" {
		return nil
	}
	value := topicAutoTitleMeta{
		Stage:     proposal.Stage,
		UserTurns: proposal.UserTurns,
		BasisHash: proposal.BasisHash,
		UpdatedAt: time.Now().UnixMilli(),
	}
	return desktopTopicState.setAutoMeta(workspaceRoot, topicID, &value)
}

func applyAutoTopicTitle(workspaceRoot, topicID, title string, proposal autoTopicTitleProposal) (bool, error) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" || proposal.Stage <= 0 || proposal.BasisHash == "" {
		return false, nil
	}
	return desktopTopicState.applyAutoTitle(workspaceRoot, topicID, title, topicAutoTitleMeta{
		Stage: proposal.Stage, UserTurns: proposal.UserTurns,
		BasisHash: proposal.BasisHash, UpdatedAt: time.Now().UnixMilli(),
	})
}

func deleteTopicAutoTitleMeta(workspaceRoot, topicID string) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil
	}
	return desktopTopicState.setAutoMeta(workspaceRoot, topicID, nil)
}

func setTopicCreatedAt(workspaceRoot, topicID string, createdAt int64) error {
	return desktopTopicState.setCreatedAt(workspaceRoot, topicID, createdAt)
}

func deleteTopicState(workspaceRoot, topicID string) error {
	return desktopTopicState.delete(workspaceRoot, topicID)
}

// topicIndexMu serializes recovery writes to desktop-projects.json and topic
// title indexes. Startup builds restored tabs concurrently, and each tab may
// repair its missing index.
var topicIndexMu sync.Mutex

// topicAutoTitleCommittedHookForTest pauses between the authoritative auto
// title commit and its in-memory/session publication. Production leaves it nil.
var topicAutoTitleCommittedHookForTest func()

func ensureTopicIndexed(scope, workspaceRoot, topicID, title, source string) error {
	return ensureTopicIndexedState(scope, workspaceRoot, topicID, title, source, 0)
}

func ensureTopicIndexedWithCreatedAt(scope, workspaceRoot, topicID, title, source string, createdAt int64) error {
	return ensureTopicIndexedState(scope, workspaceRoot, topicID, title, source, createdAt)
}

func ensureTopicIndexedState(scope, workspaceRoot, topicID, title, source string, createdAt int64) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return fmt.Errorf("topicID is required")
	}
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	if strings.TrimSpace(scope) == "global" {
		workspaceRoot = ""
	} else {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = defaultTopicTitle
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = topicTitleSourceManual
	}
	wasDeleted := containsDesktopString(loadProjectsFile().DeletedTopics, topicID)
	if wasDeleted {
		// A migrated scope prunes tombstoned SQLite rows before mirroring. Clear
		// the tombstone first for an explicit restore; if the authoritative state
		// write then fails, restore the tombstone so the topic cannot be half shown.
		if err := prependTopicInProjectsFile(workspaceRoot, topicID, true); err != nil {
			return err
		}
	}
	var err error
	if createdAt > 0 {
		err = createTopicState(workspaceRoot, topicID, title, source, createdAt)
	} else {
		err = setTopicTitleWithSource(workspaceRoot, topicID, title, source)
	}
	if err != nil {
		if wasDeleted {
			if rollbackErr := removeTopicFromProjectsFile(topicID); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("restore topic tombstone: %w", rollbackErr))
			}
		}
		return err
	}
	if wasDeleted {
		return nil
	}
	return prependTopicInProjectsFile(workspaceRoot, topicID, true)
}

// telemetry

func saveTelemetry(path string, snapshot tabTelemetrySnapshot) error {
	if snapshot.Version == 0 {
		snapshot.Version = 3
	}
	if snapshot.ReadFiles == nil {
		snapshot.ReadFiles = []readFileRecord{}
	}
	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return fileutil.ReplaceFile(tmp, path)
}

func loadTelemetry(path string) tabTelemetrySnapshot {
	b, err := readFileUTF8(path)
	if err != nil {
		return tabTelemetrySnapshot{Version: 3, ReadFiles: []readFileRecord{}}
	}
	var snapshot tabTelemetrySnapshot
	if err := json.Unmarshal(b, &snapshot); err == nil && (snapshot.Version > 0 || snapshot.ReadFiles != nil) {
		if snapshot.ReadFiles == nil {
			snapshot.ReadFiles = []readFileRecord{}
		}
		if snapshot.Usage.SessionCost == 0 && snapshot.Usage.SessionCostUsd > 0 {
			snapshot.Usage.SessionCost = snapshot.Usage.SessionCostUsd
		}
		// Lazy-migrate pre-CostQuote telemetry: keep original amount, mark legacy.
		// Never reconstruct wiped mixed-currency zeros from current price tables.
		if snapshot.Version < 3 && snapshot.Usage.CostLedger == nil && snapshot.Usage.SessionCost > 0 {
			q := billing.MigrateLegacyUsage(billing.LegacyUsageRecord{
				SessionCost:     snapshot.Usage.SessionCost,
				SessionCurrency: snapshot.Usage.SessionCurrency,
				EndedAt:         time.Now().UTC(),
			})
			ledger := billing.NewLedger()
			ledger.Add(q, billing.UsageTokens{
				PromptTokens:     snapshot.Usage.PromptTokens,
				CompletionTokens: snapshot.Usage.CompletionTokens,
			}, time.Now().UTC())
			snapshot.Usage.CostLedger = ledger
			total := ledger.Total(billing.NormalizeCurrency(snapshot.Usage.SessionCurrency))
			snapshot.Usage.SessionCostQuote = &total
			snapshot.Usage.SessionCostComplete = total.Complete
			snapshot.Version = 3
		} else if snapshot.Version < 3 && snapshot.Usage.SessionCost <= 0 && strings.TrimSpace(snapshot.Usage.SessionCurrency) != "" {
			// Explicit zero with currency: prior mixed-currency wipe — mark incomplete.
			q := billing.MigrateLegacyUsage(billing.LegacyUsageRecord{
				SessionCost:     0,
				SessionCurrency: snapshot.Usage.SessionCurrency,
			})
			snapshot.Usage.SessionCostQuote = &q
			snapshot.Usage.SessionCostComplete = false
			snapshot.Version = 3
		} else if snapshot.Version < 3 {
			snapshot.Version = 3
		}
		return snapshot
	}
	var records []readFileRecord
	if err := json.Unmarshal(b, &records); err != nil || records == nil {
		records = []readFileRecord{}
	}
	return tabTelemetrySnapshot{Version: 1, ReadFiles: records}
}

// project tree

// ProjectNode is one node in the sidebar project tree (a project folder or a
// topic leaf).
type ProjectNode struct {
	Key                          string `json:"key"`  // stable key for React
	Kind                         string `json:"kind"` // "project" | "topic" | "session" | "global_folder" | "global_topic" | "global_session"
	Label                        string `json:"label"`
	Root                         string `json:"root,omitempty"` // project workspace root
	TopicID                      string `json:"topicId,omitempty"`
	SessionPath                  string `json:"sessionPath,omitempty"`
	Preview                      string `json:"preview,omitempty"`
	ProjectColor                 string `json:"projectColor,omitempty"`
	Turns                        int    `json:"turns,omitempty"`
	TurnsState                   string `json:"turnsState,omitempty"`
	Health                       string `json:"health,omitempty"`
	CreatedAt                    int64  `json:"createdAt,omitempty"`
	LastActivityAt               int64  `json:"lastActivityAt,omitempty"`
	Open                         bool   `json:"open,omitempty"`
	Running                      bool   `json:"running,omitempty"`
	Status                       string `json:"status,omitempty"`
	Pinned                       bool   `json:"pinned,omitempty"`
	SortOrder                    int    `json:"sortOrder"` // manual topic order index (0-based); -1 when unknown
	Recovered                    bool   `json:"recovered,omitempty"`
	RecoveryReason               string `json:"recoveryReason,omitempty"`
	RecoveryDigest               string `json:"recoveryDigest,omitempty"`
	RecoveryParentID             string `json:"recoveryParentId,omitempty"`
	RecoveryState                string `json:"recoveryState,omitempty"`
	RecoveryBranchCount          int    `json:"recoveryBranchCount,omitempty"`
	RecoveryUnresolvedCount      int    `json:"recoveryUnresolvedCount,omitempty"`
	RecoveryCleanupEligibleCount int    `json:"recoveryCleanupEligibleCount,omitempty"`
	// RecoveryCopyCount is retained for Wails compatibility with older desktop
	// frontends. Ordinary project-tree payloads intentionally leave it at zero:
	// physical recovery copies are an internal persistence detail.
	RecoveryCopyCount int           `json:"recoveryCopyCount,omitempty"`
	IsolatedWorktree  bool          `json:"isolatedWorktree,omitempty"`
	Remote            *RemoteTabRef `json:"remote,omitempty"`
	RuntimeOnly       bool          `json:"runtimeOnly,omitempty"`
	Children          []ProjectNode `json:"children,omitempty"`
}

func normalizeTopicStatus(status string) string {
	switch status {
	case topicStatusThinking, topicStatusStreaming, topicStatusWaitingConfirmation, topicStatusBackgroundJob, topicStatusPaused, topicStatusAwaitingDelivery, topicStatusError, topicStatusDivergedRecovery:
		return status
	default:
		return ""
	}
}

func legacySessionMetaMatchesMigrationTarget(meta agent.BranchMeta, scope, workspaceRoot string) bool {
	if strings.TrimSpace(meta.TopicID) != "" {
		return false
	}
	return legacySessionScopeMatchesMigrationTarget(meta, scope, workspaceRoot)
}

func legacySessionScopeMatchesMigrationTarget(meta agent.BranchMeta, scope, workspaceRoot string) bool {
	metaScope := strings.TrimSpace(meta.Scope)
	if metaScope != "" && metaScope != scope {
		return false
	}
	metaRoot := normalizeProjectRoot(meta.WorkspaceRoot)
	if scope == "project" {
		return metaRoot == "" || sameProjectRoot(workspaceRoot, metaRoot)
	}
	return metaRoot == "" || sameProjectRoot(globalWorkspaceRoot(), metaRoot)
}

func cleanDesktopPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func sameDesktopPath(a, b string) bool {
	a = cleanDesktopPath(a)
	b = cleanDesktopPath(b)
	if a == "" || b == "" {
		return false
	}
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// projectRootKey is the map-key form of a project root: cleaned, absolute,
// and case-folded on Windows — the same key form agent.CanonicalSessionPath
// uses for session paths, so equivalent spellings never split lookups.
func projectRootKey(root string) string {
	root = cleanDesktopPath(root)
	if os.PathSeparator == '\\' {
		return strings.ToLower(root)
	}
	return root
}

func restoreSessionTopicIndex(dir, sessionPath string) error {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return nil
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(meta.TopicID) == "" {
		// The migration pass takes per-session meta locks itself, so it must
		// run outside the lock taken below.
		migrateLegacySessionsIntoGlobalTopics(dir)
		return nil
	}

	// Read-modify-write on the branch-meta sidecar: re-read and save under the
	// per-path meta lock so a concurrent save's revision bump can't land in
	// between and get rolled back by the write at the end.
	unlock, err := agent.LockSessionMetaPath(sessionPath)
	if err != nil {
		return err
	}
	defer unlock()
	meta, ok, err = agent.LoadBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(meta.TopicID) == "" {
		return nil
	}

	topicID := strings.TrimSpace(meta.TopicID)
	scope := strings.TrimSpace(meta.Scope)
	workspaceRoot := strings.TrimSpace(meta.WorkspaceRoot)
	if scope != "global" && scope != "project" {
		if workspaceRoot == "" {
			scope = "global"
		} else {
			scope = "project"
		}
	}
	if scope == "global" {
		workspaceRoot = ""
	} else {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
		if workspaceRoot == "" {
			scope = "global"
		}
	}

	title := restoredSessionTopicTitle(dir, sessionPath, meta)
	if title == "" {
		title = defaultTopicTitle
	}
	if err := ensureTopicIndexed(scope, workspaceRoot, topicID, title, topicTitleSourceManual); err != nil {
		return err
	}

	if scope == "global" {
		meta.Scope = "global"
		meta.WorkspaceRoot = ""
	} else {
		meta.Scope = "project"
		meta.WorkspaceRoot = workspaceRoot
	}
	meta.TopicID = topicID
	meta.TopicTitle = title
	if err := agent.SaveBranchMetaPreserveUpdatedLocked(sessionPath, meta); err != nil {
		return err
	}
	invalidateTopicSessionIndexForPath(sessionPath)
	return nil
}

func restoredSessionTopicTitle(dir, sessionPath string, meta agent.BranchMeta) string {
	if title := storedSessionTopicTitle(dir, sessionPath, meta); title != "" {
		return title
	}
	if s, err := agent.LoadSession(sessionPath); err == nil {
		for _, msg := range s.Messages {
			if agent.IsUserAuthoredTurnMessage(msg) {
				if title := topicTitleFromText(agent.UserMessageText(msg)); title != "" {
					return title
				}
			}
		}
	}
	return ""
}

func storedSessionTopicTitle(dir, sessionPath string, meta agent.BranchMeta) string {
	if title := topicTitleFromText(meta.TopicTitle); title != "" {
		return title
	}
	return topicTitleFromText(loadSessionTitles(dir)[filepath.Base(sessionPath)])
}

func legacySessionTopicID(path string) string {
	id := agent.BranchID(path)
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	var b strings.Builder
	b.WriteString("legacy_")
	for _, r := range id {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	prefix := strings.TrimRight(b.String(), "_")
	if prefix == "legacy" {
		prefix = "legacy_session"
	}
	return prefix + "_" + hex.EncodeToString(sum[:])[:12]
}

// TopicMeta describes a topic for the project tree.
type TopicMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
}

// CreateTopic creates a new topic under a project workspace and returns its metadata.
func (a *App) CreateTopic(scope, workspaceRoot, title string) (TopicMeta, error) {
	trimmedTitle := strings.TrimSpace(title)
	titleSource := topicTitleSourceManual
	if trimmedTitle == "" {
		trimmedTitle = defaultTopicTitle
		titleSource = topicTitleSourceAuto
	}
	topicID := newTopicID()
	createdAt := time.Now().UnixMilli()
	if scope == "global" {
		workspaceRoot = ""
	}
	if workspaceRoot != "" {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = abs
		}
	}
	releaseAdmission, err := a.beginProjectRuntimeAdmission(scope, workspaceRoot)
	if err != nil {
		return TopicMeta{}, err
	}
	defer releaseAdmission()
	if err := createTopicState(workspaceRoot, topicID, trimmedTitle, titleSource, createdAt); err != nil {
		return TopicMeta{}, err
	}
	// New topics should appear first in their project/global group so the item
	// just created is immediately visible and selected in the sidebar.
	_ = prependTopicInProjectsFile(workspaceRoot, topicID, workspaceRoot != "")
	a.emitProjectTreeMetadataChanged()
	return TopicMeta{ID: topicID, Title: a.localizedTopicTitle(trimmedTitle, titleSource), CreatedAt: createdAt}, nil
}

// RenameProject updates the sidebar-only display title for a project folder.
// Empty title clears the override and falls back to the folder name.
func (a *App) RenameProject(workspaceRoot, title string) error {
	if err := renameProject(workspaceRoot, title); err != nil {
		return err
	}
	a.syncTabWorkspaceRootSpellings()
	a.emitProjectTreeMetadataChanged()
	return nil
}

// SetProjectColor updates the project-level accent color used by project topics
// in the sidebar and tabs. Empty color restores the default accent.
func (a *App) SetProjectColor(workspaceRoot, color string) error {
	if err := setProjectColor(workspaceRoot, color); err != nil {
		return err
	}
	a.syncTabWorkspaceRootSpellings()
	a.emitProjectTreeMetadataChanged()
	return nil
}

// SetProjectPinned controls whether a project folder is pinned above the rest of
// the desktop project tree.
func (a *App) SetProjectPinned(workspaceRoot string, pinned bool) error {
	root := normalizeProjectRoot(workspaceRoot)
	if root == "" {
		return fmt.Errorf("workspaceRoot is required")
	}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		i := projectIndexByRoot(f.Projects, root)
		if i < 0 {
			return false, fmt.Errorf("project %q not found", root)
		}
		root = f.Projects[i].Root
		next := make([]string, 0, len(f.PinnedProjects))
		for _, pinnedRoot := range f.PinnedProjects {
			if !sameProjectRoot(pinnedRoot, root) {
				next = append(next, pinnedRoot)
			}
		}
		if pinned {
			next = prependUniqueString(next, root)
		}
		if sameStringList(next, f.PinnedProjects) {
			return false, nil
		}
		f.PinnedProjects = next
		return true, nil
	}); err != nil {
		return err
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}

// ReorderProjects persists the user-defined order of project folders and,
// when present, the virtual Global sidebar section.
func (a *App) ReorderProjects(workspaceRoots []string) error {
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		var seenProjects []string
		next := make([]desktopProject, 0, len(workspaceRoots))
		sidebarOrder := make([]string, 0, len(workspaceRoots))
		hasGlobalOrder := false
		for _, root := range workspaceRoots {
			root = strings.TrimSpace(root)
			if root == desktopGlobalOrderToken {
				if hasGlobalOrder {
					return false, fmt.Errorf("duplicate global section")
				}
				hasGlobalOrder = true
				sidebarOrder = append(sidebarOrder, root)
				continue
			}
			root = normalizeProjectRoot(root)
			i := projectIndexByRoot(f.Projects, root)
			if i < 0 {
				return false, fmt.Errorf("project %q not found", root)
			}
			project := f.Projects[i]
			if projectRootInList(seenProjects, project.Root) {
				return false, fmt.Errorf("duplicate project %q", root)
			}
			seenProjects = append(seenProjects, project.Root)
			next = append(next, project)
			sidebarOrder = append(sidebarOrder, project.Root)
		}
		if len(next) != len(f.Projects) {
			return false, fmt.Errorf("project order length mismatch")
		}
		changed := !sameProjectOrder(next, f.Projects)
		f.Projects = next
		if hasGlobalOrder {
			if !sameStringList(sidebarOrder, f.SidebarOrder) {
				changed = true
			}
			f.SidebarOrder = sidebarOrder
		} else {
			if len(f.SidebarOrder) > 0 {
				changed = true
			}
			f.SidebarOrder = nil
		}
		return changed, nil
	}); err != nil {
		return err
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}

// RenameTopic updates a topic's display title.
func (a *App) RenameTopic(topicID, title string) error {
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		trimmed = defaultTopicTitle
	}
	// Find which workspace this topic belongs to by scanning all project topic titles.
	f := loadProjectsFile()
	for _, p := range f.Projects {
		m := loadTopicTitles(p.Root)
		if _, ok := m[topicID]; ok {
			if err := setTopicTitle(p.Root, topicID, trimmed); err != nil {
				return err
			}
			a.updateOpenTopicTitle(topicID, trimmed, topicTitleSourceManual)
			changedDirs := a.updateTopicSessionTitles(topicID, trimmed)
			if len(changedDirs) > 0 {
				a.emitProjectTreeChangedForSessionDirs(changedDirs...)
			} else {
				a.emitProjectTreeMetadataChanged()
			}
			return nil
		}
	}
	// Check global.
	m := loadTopicTitles("")
	if _, ok := m[topicID]; ok {
		if err := setTopicTitle("", topicID, trimmed); err != nil {
			return err
		}
		a.updateOpenTopicTitle(topicID, trimmed, topicTitleSourceManual)
		changedDirs := a.updateTopicSessionTitles(topicID, trimmed)
		if len(changedDirs) > 0 {
			a.emitProjectTreeChangedForSessionDirs(changedDirs...)
		} else {
			a.emitProjectTreeMetadataChanged()
		}
		return nil
	}
	if scope, workspaceRoot, ok := a.findTopicLocation(topicID); ok {
		if err := ensureTopicIndexed(scope, workspaceRoot, topicID, trimmed, topicTitleSourceManual); err != nil {
			return err
		}
		a.updateOpenTopicTitle(topicID, trimmed, topicTitleSourceManual)
		changedDirs := a.updateTopicSessionTitles(topicID, trimmed)
		if len(changedDirs) > 0 {
			a.emitProjectTreeChangedForSessionDirs(changedDirs...)
		} else {
			a.emitProjectTreeMetadataChanged()
		}
		return nil
	}
	// Catalog-only topics (no title map entry, no open tab) persist through
	// renameCatalogOnlyTopic instead of failing (#9090).
	return a.renameCatalogOnlyTopic(topicID, trimmed)
}

func (a *App) findTopicLocation(topicID string) (string, string, bool) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return "", "", false
	}
	a.mu.RLock()
	for _, tab := range a.tabs {
		if tab == nil || tab.TopicID != topicID {
			continue
		}
		scope := tab.Scope
		workspaceRoot := tab.WorkspaceRoot
		a.mu.RUnlock()
		if scope == "global" {
			return "global", "", true
		}
		return "project", normalizeProjectRoot(workspaceRoot), true
	}
	a.mu.RUnlock()

	infos, err := agent.ListSessions(config.SessionDir())
	if err != nil {
		return "", "", false
	}
	for _, info := range infos {
		if strings.TrimSpace(info.TopicID) != topicID {
			continue
		}
		scope := strings.TrimSpace(info.Scope)
		if scope == "" {
			scope = "global"
		}
		if scope == "global" {
			return "global", "", true
		}
		return "project", normalizeProjectRoot(info.WorkspaceRoot), true
	}
	return "", "", false
}

func (a *App) updateOpenTopicTitle(topicID, title, source string) {
	if strings.TrimSpace(topicID) == "" || strings.TrimSpace(title) == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.TopicID == topicID {
			tab.TopicTitle = title
			tab.topicTitleSource = source
		}
	}
}

func (a *App) updateTopicSessionTitles(topicID, title string) []string {
	if strings.TrimSpace(topicID) == "" || strings.TrimSpace(title) == "" {
		return nil
	}
	var changedDirs []string
	for _, dir := range a.knownSessionDirs() {
		changed := false
		for _, match := range topicSessionMatches(dir, topicID) {
			// Read-modify-write on the branch-meta sidecar: hold the per-path
			// meta lock so a concurrent save's revision bump can't land between
			// the load and save below and get rolled back by this write.
			unlock, lockErr := agent.LockSessionMetaPath(match.path)
			if lockErr != nil {
				continue
			}
			meta, ok, err := agent.LoadBranchMeta(match.path)
			if err != nil || !ok {
				unlock()
				continue
			}
			meta.TopicTitle = title
			err = agent.SaveBranchMetaPreserveUpdatedLocked(match.path, meta)
			unlock()
			if err == nil {
				invalidateTopicSessionIndex(dir)
				changed = true
			}
		}
		if changed {
			changedDirs = append(changedDirs, dir)
		}
	}
	return changedDirs
}

func (a *App) emitProjectTreeChanged() {
	a.requestProjectTreeCatalogRefresh()
	a.emitProjectTreeChangedEvent()
}

func (a *App) requestProjectTreeCatalogRefresh() {
	if a.projectTreeCatalogRefreshHook != nil {
		a.projectTreeCatalogRefreshHook()
	}
	a.requestSessionCatalogMetadataSync()
	for _, target := range a.sessionCatalogTargets() {
		a.requestSessionCatalogReconcile(target.Path)
	}
}

// emitProjectTreeChangedForSessionDirs schedules only the affected catalog
// directories. It never scans synchronously on the mutation or UI goroutine.
func (a *App) emitProjectTreeChangedForSessionDirs(dirs ...string) {
	for _, dir := range dirs {
		a.requestSessionCatalogReconcile(dir)
	}
	a.emitProjectTreeChangedEvent()
}

// emitProjectTreeMetadataChanged refreshes ordering, titles, pins, and runtime
// status without walking session storage.
func (a *App) emitProjectTreeMetadataChanged() {
	a.requestSessionCatalogMetadataSync()
	a.emitProjectTreeChangedEvent()
}

// DeleteTopic removes a topic and its title metadata.
func (a *App) DeleteTopic(topicID string) error {
	return friendlySessionFileError(a.deleteTopic(topicID))
}

func (a *App) deleteTopic(topicID string) error {
	// Deletion converges on the fully-deleted state instead of keying the
	// whole cleanup on the title entry: a retry after a partial failure (or a
	// concurrent duplicate delete) may find the title already gone while the
	// sources map, created-at entry, sidebar index, or tombstone still need
	// cleanup, so every step checks its own leftovers.
	//
	// Detailed cleanup is limited to roots that can actually hold the topic:
	// roots whose sidebar index lists it, plus any root whose title map
	// contains it. The title probe tolerates read errors on unindexed roots
	// so unreadable metadata in an unrelated project cannot abort the
	// deletion, while roots known to hold the topic still fail hard instead
	// of being half-cleaned silently.
	f := loadProjectsFile()
	indexed := map[string]bool{
		"": containsDesktopString(f.GlobalTopics, topicID) ||
			containsDesktopString(f.GlobalPinnedTopics, topicID),
	}
	roots := make([]string, 0, len(f.Projects)+1)
	for _, p := range f.Projects {
		roots = append(roots, p.Root)
		indexed[p.Root] = containsDesktopString(p.Topics, topicID) ||
			containsDesktopString(p.PinnedTopics, topicID)
	}
	// Publish the tombstone before clearing any per-scope state. A concurrent
	// session repair may already hold a stale title snapshot, but its commit-time
	// merge will now see the tombstone and cannot resurrect this topic.
	if err := removeTopicFromProjectsFile(topicID); err != nil {
		return err
	}
	roots = append(roots, "")
	for _, root := range roots {
		titles, err := loadTopicTitlesForUpdate(root)
		if err != nil {
			if indexed[root] {
				return err
			}
			continue
		}
		_, hasTitle := titles[topicID]
		if !hasTitle && !indexed[root] {
			continue
		}
		if err := deleteTopicState(root, topicID); err != nil {
			return err
		}
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}

// SetTopicPinned controls whether a topic is pinned to the top of its project
// or Global section in the desktop project tree.
func (a *App) SetTopicPinned(topicID string, pinned bool) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return fmt.Errorf("topicID is required")
	}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		for i, p := range f.Projects {
			m := loadTopicTitles(p.Root)
			if _, ok := m[topicID]; !ok && !containsDesktopString(p.Topics, topicID) {
				continue
			}
			next := removeString(f.Projects[i].PinnedTopics, topicID)
			if pinned {
				next = prependUniqueString(f.Projects[i].PinnedTopics, topicID)
			}
			if sameStringList(next, f.Projects[i].PinnedTopics) {
				return false, nil
			}
			f.Projects[i].PinnedTopics = next
			return true, nil
		}
		globalTitles := loadTopicTitles("")
		if _, ok := globalTitles[topicID]; !ok && !containsDesktopString(f.GlobalTopics, topicID) {
			return false, fmt.Errorf("topic %q not found", topicID)
		}
		next := removeString(f.GlobalPinnedTopics, topicID)
		if pinned {
			next = prependUniqueString(f.GlobalPinnedTopics, topicID)
		}
		if sameStringList(next, f.GlobalPinnedTopics) {
			return false, nil
		}
		f.GlobalPinnedTopics = next
		return true, nil
	}); err != nil {
		return err
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}

// ListProjectTree builds the sidebar tree: project folders each containing
// their topics, plus a Global section.
// topicSummary is used by ListProjectTree and mergeSessionInfos to track
// per-topic turn count and last activity.
type topicSummary struct {
	turns                int
	adoptedRecoveryTurns int
	lastActivityAt       int64
	hasNormalSession     bool
	hasRecoveryOnly      bool
	hasAdoptedRecovery   bool
}

func (s topicSummary) displayTurns() int {
	if s.adoptedRecoveryTurns > s.turns {
		return s.adoptedRecoveryTurns
	}
	return s.turns
}

// runtimeSessionStatus is one open or detached runtime session, as shown in
// the sidebar tree.
type runtimeSessionStatus struct {
	open    bool
	running bool
}

// topicHiddenAsRecoveryOnly keeps the legacy runtime fallback from creating a
// duplicate row for an idle recovery-only topic. The catalog-backed tree now
// supplies one logical row for recovery-only topics, and physical branches
// remain available from History.
func topicHiddenAsRecoveryOnly(summary topicSummary, pinned bool, runtimeSessions []runtimeSessionStatus) bool {
	if !summary.hasRecoveryOnly || summary.hasNormalSession || summary.hasAdoptedRecovery || pinned {
		return false
	}
	for _, session := range runtimeSessions {
		if session.open || session.running {
			return false
		}
	}
	return true
}

func topicSummaryKey(scope, workspaceRoot, topicID string) string {
	if scope == "global" {
		return "global::" + topicID
	}
	// Producers key by the live tab's root spelling while the sidebar keys by
	// the registry's canonical spelling; fold both so runtime status never
	// splits across equivalent roots.
	return "project:" + projectRootKey(workspaceRoot) + ":" + topicID
}

func projectSessionNodeKey(scope, sessionPath string) string {
	sum := sha256.Sum256([]byte(sessionRuntimeKey(sessionPath)))
	return scope + "_session_" + hex.EncodeToString(sum[:8])
}

// ContextPanelInfo is the right-side panel's data for one tab.
type ContextPanelInfo struct {
	UsedTokens       int  `json:"usedTokens"`
	WindowTokens     int  `json:"windowTokens"`
	PromptTokens     int  `json:"promptTokens"`
	CompletionTokens int  `json:"completionTokens"`
	TotalTokens      int  `json:"totalTokens"`
	ReasoningTokens  int  `json:"reasoningTokens"`
	CacheHitTokens   int  `json:"cacheHitTokens"`
	CacheMissTokens  int  `json:"cacheMissTokens"`
	Estimated        bool `json:"estimated,omitempty"`
	// Session-cumulative token counts (from telemetry, atomic snapshot).
	// Separate from the per-turn fields above so existing consumers (status bar
	// turn tokens, donut chart) are unaffected.
	SessionCacheHitTokens   int                         `json:"sessionCacheHitTokens"`
	SessionCacheMissTokens  int                         `json:"sessionCacheMissTokens"`
	SessionCompletionTokens int                         `json:"sessionCompletionTokens"`
	SessionEstimated        bool                        `json:"sessionEstimated,omitempty"`
	RequestCount            int                         `json:"requestCount"`
	ElapsedMs               int64                       `json:"elapsedMs"`
	SessionCost             float64                     `json:"sessionCost"`
	SessionCurrency         string                      `json:"sessionCurrency,omitempty"`
	SessionCostUsd          float64                     `json:"sessionCostUsd,omitempty"`
	SessionCostComplete     bool                        `json:"sessionCostComplete,omitempty"`
	SessionCostEstimated    bool                        `json:"sessionCostEstimated,omitempty"`
	SessionBillingMode      string                      `json:"sessionBillingMode,omitempty"`
	SessionCostQuote        *billing.CostQuote          `json:"sessionCostQuote,omitempty"`
	Sources                 map[string]usageSourceStats `json:"sources,omitempty"`
	Mock                    bool                        `json:"mock,omitempty"`
	ReadFiles               []readFileRecord            `json:"readFiles"`
	ChangedFiles            []ChangedFileInfo           `json:"changedFiles"`
	ContextBudget           *ContextBudgetInfo          `json:"contextBudget,omitempty"`
}

type ChangedFileInfo struct {
	Path         string   `json:"path"`
	OldPath      string   `json:"oldPath,omitempty"`
	Sources      []string `json:"sources"`
	GitStatus    string   `json:"gitStatus,omitempty"`
	Turns        []int    `json:"turns"`
	LatestPrompt string   `json:"latestPrompt,omitempty"`
	LatestTime   int64    `json:"latestTime,omitempty"`
}

// ContextPanel returns the context usage, read files, and changed files for a
// specific tab.
func (a *App) ContextPanel(tabID string) ContextPanelInfo {
	a.mu.RLock()
	tab, ok := a.tabs[tabID]
	var ctrl control.SessionAPI
	if ok && tab != nil {
		ctrl = tab.Ctrl
	}
	a.mu.RUnlock()
	if !ok {
		return ContextPanelInfo{ReadFiles: []readFileRecord{}, ChangedFiles: []ChangedFileInfo{}}
	}

	info := ContextPanelInfo{ReadFiles: []readFileRecord{}, ChangedFiles: []ChangedFileInfo{}}
	if ctrl != nil {
		if sp := ctrl.SessionPath(); sp != "" {
			tab.syncTelemetryToSession(sp)
		}
		_, window := ctrl.ContextSnapshot()
		info.WindowTokens = window
		// This panel breaks the last turn down into segments, so its total must
		// be that turn's usage and not the live-view measurement the status-bar
		// gauge reports — otherwise the segments stop summing to the total.
		if u := ctrl.LastUsage(); u != nil {
			info.UsedTokens = u.PromptTokens + u.CompletionTokens
		}
		if info.UsedTokens == 0 {
			if snap := tab.displayTelemetrySnapshot(); snap.Usage.LastUsedTokens > 0 {
				info.UsedTokens = snap.Usage.LastUsedTokens
			}
		}
		if u := ctrl.LastUsage(); u != nil {
			info.PromptTokens = u.PromptTokens
			info.CompletionTokens = u.CompletionTokens
			info.ReasoningTokens = u.ReasoningTokens
			info.CacheHitTokens = u.CacheHitTokens
			info.CacheMissTokens = u.CacheMissTokens
			info.Estimated = u.Estimated
		} else {
			// Executor rebuilt (session rebind): fall back to the telemetry-
			// persisted per-turn breakdown so the donut chart and type
			// breakdown show the last turn's composition instead of "other".
			snap := tab.displayTelemetrySnapshot()
			info.PromptTokens = snap.Usage.LastPromptTokens
			info.CompletionTokens = snap.Usage.LastCompletionTokens
			info.ReasoningTokens = snap.Usage.LastReasoningTokens
			info.CacheHitTokens = snap.Usage.LastCacheHitTokens
			info.CacheMissTokens = snap.Usage.LastCacheMissTokens
			info.Estimated = snap.Usage.LastEstimated
		}
	}

	telemetry := tab.displayTelemetrySnapshot()
	if records := telemetry.ReadFiles; records != nil {
		info.ReadFiles = records
	}
	usage := telemetry.Usage
	info.TotalTokens = usage.TotalTokens
	info.RequestCount = usage.RequestCount
	info.ElapsedMs = usage.ElapsedMs
	info.SessionCost = usage.SessionCost
	info.SessionCurrency = usage.SessionCurrency
	info.SessionCostUsd = usage.SessionCostUsd
	info.SessionCostComplete = usage.SessionCostComplete
	info.SessionCostEstimated = true
	info.SessionCostQuote = usage.SessionCostQuote
	if usage.SessionCostQuote != nil {
		info.SessionBillingMode = usage.SessionCostQuote.BillingMode
		info.SessionCostEstimated = usage.SessionCostQuote.Estimated
		if !usage.SessionCostQuote.Complete {
			info.SessionCostComplete = false
		}
	}
	info.Sources = usage.Sources
	info.SessionCacheHitTokens = usage.CacheHitTokens
	info.SessionCacheMissTokens = usage.CacheMissTokens
	info.SessionCompletionTokens = usage.CompletionTokens
	info.SessionEstimated = usage.Estimated
	if ctrl != nil {
		if snap := ctrl.ContextMaintenanceSnapshot(); snap.ContextBudget != nil {
			info.ContextBudget = contextBudgetInfo(snap.ContextBudget)
		}
	}

	// Gather workspace changes for this tab's root.
	if ctrl != nil && tab.WorkspaceRoot != "" {
		for _, meta := range ctrl.Checkpoints() {
			for _, path := range meta.Paths {
				info.ChangedFiles = append(info.ChangedFiles, ChangedFileInfo{
					Path:         path,
					Sources:      []string{"session"},
					Turns:        []int{meta.Turn},
					LatestPrompt: meta.Prompt,
					LatestTime:   meta.Time.UnixMilli(),
				})
			}
		}
	}

	return info
}

// utility

func (a *App) newUniqueTabIDLocked() string {
	for {
		id := newTabID()
		if _, exists := a.tabs[id]; !exists {
			return id
		}
	}
}

func (a *App) restoredTabIDLocked(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return a.newUniqueTabIDLocked()
	}
	if _, exists := a.tabs[id]; exists {
		return a.newUniqueTabIDLocked()
	}
	return id
}

func normalizeTabMode(mode string) string {
	switch mode {
	case "plan", "yolo", "plan-yolo", "yolo-plan":
		if mode == "yolo-plan" {
			return "plan-yolo"
		}
		return mode
	default:
		return "normal"
	}
}

func tabModeFromAxes(plan, autoApproveTools bool) string {
	switch {
	case plan && autoApproveTools:
		return "plan-yolo"
	case plan:
		return "plan"
	case autoApproveTools:
		return "yolo"
	default:
		return "normal"
	}
}

func tabModeHasPlan(mode string) bool {
	switch normalizeTabMode(mode) {
	case "plan", "plan-yolo":
		return true
	default:
		return false
	}
}

func tabModeHasAutoApproveTools(mode string) bool {
	switch normalizeTabMode(mode) {
	case "yolo", "plan-yolo":
		return true
	default:
		return false
	}
}

func currentTabMode(tab *WorkspaceTab) string {
	if tab == nil {
		return "normal"
	}
	if tab.Ctrl != nil {
		return tabModeFromAxes(tab.Ctrl.PlanMode(), tab.Ctrl.AutoApproveTools())
	}
	return normalizeTabMode(tab.mode)
}

func currentTabGoal(tab *WorkspaceTab) string {
	if tab == nil {
		return ""
	}
	if tab.Ctrl != nil {
		return tab.Ctrl.Goal()
	}
	return strings.TrimSpace(tab.goal)
}

func currentTabGoalStatus(tab *WorkspaceTab) string {
	if tab == nil {
		return control.GoalStatusStopped
	}
	if tab.Ctrl != nil {
		return tab.Ctrl.GoalStatus()
	}
	if strings.TrimSpace(tab.goal) != "" {
		return control.GoalStatusRunning
	}
	return control.GoalStatusStopped
}

func currentTabCollaborationMode(tab *WorkspaceTab) string {
	if tab == nil {
		return "normal"
	}
	if tabModeHasPlan(currentTabMode(tab)) {
		return "plan"
	}
	if strings.TrimSpace(currentTabGoal(tab)) != "" && currentTabGoalStatus(tab) == control.GoalStatusRunning {
		return "goal"
	}
	return "normal"
}

func currentTabToolApprovalMode(tab *WorkspaceTab) string {
	if tab == nil {
		return control.ToolApprovalAsk
	}
	if tab.Ctrl != nil {
		return tab.Ctrl.ToolApprovalMode()
	}
	return normalizeToolApprovalMode(tab.toolApprovalMode)
}

// tabRuntimeSnapshot is a consistent under-a.mu copy of the per-tab fields
// that bound methods and rebuild paths need after releasing the lock. The
// build/rebuild goroutines write these fields under a.mu, so lock-free reads
// from other goroutines are data races (same class as the sessionLease race
// fixed for #5955). Controller methods are invoked on the snapshot's ctrl
// AFTER unlocking, never while holding a.mu.
type tabRuntimeSnapshot struct {
	ctrl                          control.SessionAPI
	sink                          *tabEventSink
	label                         string
	ready                         bool
	readOnly                      bool
	startupErr                    string
	scope                         string
	workspaceRoot                 string
	sessionPath                   string
	topicID                       string
	topicTitle                    string
	sharedHostKey                 string
	model                         string
	effort                        *string
	tokenMode, qualityFloor, mode string
	goal, toolApprovalMode        string
}

// normalizedTabRuntime is the internal, orthogonal runtime profile restored
// across controller rebuilds. Goal sidecars remain authoritative; legacyGoal is
// only a fallback for a running legacy Goal with no sidecar.
type normalizedTabRuntime struct {
	collaborationMode, toolApprovalMode, tokenMode string
	qualityFloor, legacyGoal                       string
}

// snapshotTabRuntimeLocked copies the racy per-tab fields. Callers must hold
// a.mu (read or write side).
func snapshotTabRuntimeLocked(tab *WorkspaceTab) tabRuntimeSnapshot {
	if tab == nil {
		return tabRuntimeSnapshot{}
	}
	return tabRuntimeSnapshot{
		ctrl:             tab.Ctrl,
		sink:             tab.sink,
		label:            tab.Label,
		ready:            tab.Ready,
		readOnly:         tab.ReadOnly,
		startupErr:       tab.StartupErr,
		scope:            tab.Scope,
		workspaceRoot:    tab.WorkspaceRoot,
		sessionPath:      tab.SessionPath,
		topicID:          tab.TopicID,
		topicTitle:       tab.TopicTitle,
		sharedHostKey:    tab.SharedHostKey,
		model:            tab.model,
		effort:           cloneStringPtr(tab.effort),
		tokenMode:        currentTabTokenMode(tab),
		qualityFloor:     tab.qualityFloor,
		mode:             tab.mode,
		goal:             tab.goal,
		toolApprovalMode: tab.toolApprovalMode,
	}
}

func (a *App) tabRuntimeSnapshot(tab *WorkspaceTab) tabRuntimeSnapshot {
	if tab == nil {
		return tabRuntimeSnapshot{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return snapshotTabRuntimeLocked(tab)
}

// Snapshot-based forms of the currentTabX helpers, for callers that already
// hold a consistent tabRuntimeSnapshot.

func (s tabRuntimeSnapshot) currentMode() string {
	if s.ctrl != nil {
		return tabModeFromAxes(s.ctrl.PlanMode(), s.ctrl.AutoApproveTools())
	}
	return normalizeTabMode(s.mode)
}

func (s tabRuntimeSnapshot) currentGoal() string {
	if s.ctrl != nil {
		return s.ctrl.Goal()
	}
	return strings.TrimSpace(s.goal)
}

func (s tabRuntimeSnapshot) currentGoalStatus() string {
	if s.ctrl != nil {
		return s.ctrl.GoalStatus()
	}
	if strings.TrimSpace(s.goal) != "" {
		return control.GoalStatusRunning
	}
	return control.GoalStatusStopped
}

func (s tabRuntimeSnapshot) collaborationMode() string {
	if tabModeHasPlan(s.currentMode()) {
		return "plan"
	}
	if strings.TrimSpace(s.currentGoal()) != "" && s.currentGoalStatus() == control.GoalStatusRunning {
		return "goal"
	}
	return "normal"
}

func (s tabRuntimeSnapshot) currentToolApprovalMode() string {
	if s.ctrl != nil {
		return s.ctrl.ToolApprovalMode()
	}
	return normalizeToolApprovalMode(s.toolApprovalMode)
}

// normalizedRuntime reads live Controller state only after the App snapshot has
// released a.mu. Rebuild callers hold turnStartMu while invoking it, so all
// three axes and the legacy Goal fallback describe one admitted runtime state.
func (s tabRuntimeSnapshot) normalizedRuntime() normalizedTabRuntime {
	plan := tabModeHasPlan(normalizeTabMode(s.mode))
	approvalMode := normalizeToolApprovalMode(s.toolApprovalMode)
	goal := strings.TrimSpace(s.goal)
	goalStatus := control.GoalStatusStopped
	if goal != "" {
		goalStatus = control.GoalStatusRunning
	}
	if s.ctrl != nil {
		plan = s.ctrl.PlanMode()
		approvalMode = normalizeToolApprovalMode(s.ctrl.ToolApprovalMode())
		goal = strings.TrimSpace(s.ctrl.Goal())
		goalStatus = s.ctrl.GoalStatus()
	}

	qualityFloor := s.qualityFloor
	if qualityFloor == "" {
		qualityFloor = tabQualityFloor(s.workspaceRoot, "")
	}
	runtime := normalizedTabRuntime{
		collaborationMode: "normal",
		toolApprovalMode:  approvalMode,
		tokenMode:         boot.NormalizeTokenMode(s.tokenMode),
		qualityFloor:      qualityFloor,
	}
	switch {
	case plan:
		runtime.collaborationMode = "plan"
	case goal != "" && goalStatus == control.GoalStatusRunning:
		runtime.collaborationMode = "goal"
		runtime.legacyGoal = goal
	}
	return runtime
}

func (r normalizedTabRuntime) tabMode() string {
	return tabModeFromAxes(r.collaborationMode == "plan", r.toolApprovalMode == control.ToolApprovalYolo)
}

func applyNormalizedRuntimeToTabLocked(tab *WorkspaceTab, runtime normalizedTabRuntime) {
	if tab == nil {
		return
	}
	tab.mode = runtime.tabMode()
	tab.toolApprovalMode = normalizeToolApprovalMode(runtime.toolApprovalMode)
	tab.qualityFloor = runtime.qualityFloor
	if runtime.collaborationMode == "goal" {
		tab.goal = strings.TrimSpace(runtime.legacyGoal)
	} else {
		tab.goal = ""
	}
}

func normalizeToolApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case control.ToolApprovalAuto:
		return control.ToolApprovalAuto
	case control.ToolApprovalYolo, "full", "full-access", "bypass":
		return control.ToolApprovalYolo
	default:
		return control.ToolApprovalAsk
	}
}

func persistedToolApprovalMode(mode string) string {
	switch normalizeToolApprovalMode(mode) {
	case control.ToolApprovalAuto, control.ToolApprovalYolo:
		return normalizeToolApprovalMode(mode)
	default:
		return ""
	}
}

// persistedTabMode is the composer mode saved with a tab so it survives reload
// and app relaunch. plan, yolo, and plan-yolo are remembered (a restored yolo
// tab keeps its status-bar indicator); "normal" is the default and isn't
// persisted. (#3517)
func persistedTabMode(mode string) string {
	switch normalizeTabMode(mode) {
	case "plan", "yolo", "plan-yolo":
		return normalizeTabMode(mode)
	}
	return ""
}

func newTabID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := time.Now().UTC()
		return "tab_" + now.Format("20060102150405") + "_" + fmt.Sprintf("%09d", now.Nanosecond())
	}
	return "tab_" + hex.EncodeToString(b[:])
}

func newTopicID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := time.Now().UTC()
		return "topic_" + now.Format("20060102-150405") + "_" + fmt.Sprintf("%09d", now.Nanosecond())
	}
	return "topic_" + time.Now().UTC().Format("20060102-150405") + "_" + hex.EncodeToString(b[:])
}

func globalWorkspaceRoot() string {
	return filepath.Join(desktopConfigDir(), "global-workspace")
}

func ensureGlobalWorkspaceRoot() (string, error) {
	root := globalWorkspaceRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func globalTabWorkspaceRoot() string {
	root, err := ensureGlobalWorkspaceRoot()
	if err != nil {
		return globalWorkspaceRoot()
	}
	return root
}

func loadPinnedTabSession(dir, sessionPath string) (*agent.Session, string, bool, error) {
	return loadPinnedTabSessionWithPreloadAndMigrationFallback(dir, sessionPath, loadedTabSession{}, true)
}

func loadPinnedTabSessionWithPreload(dir, sessionPath string, preloaded loadedTabSession) (*agent.Session, string, bool, error) {
	return loadPinnedTabSessionWithPreloadAndMigrationFallback(dir, sessionPath, preloaded, false)
}

func loadPinnedTabSessionWithPreloadAndMigrationFallback(dir, sessionPath string, preloaded loadedTabSession, allowMigrationFallback bool) (*agent.Session, string, bool, error) {
	path, ok := pinnedTabSessionPath(dir, sessionPath)
	if !ok && allowMigrationFallback {
		path, ok = migratedPinnedTabSessionPath(dir, sessionPath)
	}
	if !ok {
		return nil, "", false, nil
	}
	if agent.IsCleanupPending(path) {
		return nil, "", false, nil
	}
	if preloaded.matches(path) {
		if preloaded.Session != nil && len(preloaded.Session.Snapshot()) == 0 {
			return nil, path, true, nil
		}
		return preloaded.Session, path, true, nil
	}
	loaded, err := agent.LoadSession(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, true, nil
		}
		return nil, path, true, err
	}
	// An empty file (0 messages) is a pre-created placeholder, not a real
	// session to resume.  Treating it as valid would make ctrl.Resume replace
	// the executor's live session (with system prompt) with the empty one,
	// causing the saved transcript to lack the agent identity contract.
	if len(loaded.Snapshot()) == 0 {
		return nil, path, true, nil
	}
	return loaded, path, true, nil
}

func migratedPinnedTabSessionPath(dir, sessionPath string) (string, bool) {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" || dir == "" || !filepath.IsAbs(sessionPath) {
		return "", false
	}
	if _, err := os.Stat(sessionPath); err == nil || !os.IsNotExist(err) {
		return "", false
	}
	base := filepath.Base(sessionPath)
	if base == "." || base == string(filepath.Separator) || !strings.HasSuffix(base, ".jsonl") {
		return "", false
	}
	path, _, err := validateSessionPath(dir, filepath.Join(dir, base))
	if err != nil {
		return "", false
	}
	return path, true
}

func pinnedTabSessionPath(dir, sessionPath string) (string, bool) {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" || dir == "" {
		return "", false
	}
	path, _, err := validateSessionPath(dir, sessionPath)
	if err != nil {
		if filepath.IsAbs(sessionPath) {
			return "", false
		}
		base := filepath.Base(sessionPath)
		if base == "." || base == string(filepath.Separator) || !strings.HasSuffix(base, ".jsonl") {
			return "", false
		}
		path, _, err = validateSessionPath(dir, filepath.Join(dir, base))
		if err != nil {
			return "", false
		}
	}
	return path, true
}

func pinnedTabSessionPathForBuild(scope, workspaceRoot, targetDir, sessionPath string) (string, bool) {
	if path, ok := pinnedTabSessionPath(targetDir, sessionPath); ok {
		return path, true
	}
	// Before per-project storage, desktop tabs persisted exact paths under the
	// global session directory. Accept only that owned legacy root, and require
	// project ownership metadata before routing a project tab through it.
	legacyDir := config.SessionDir()
	path, ok := pinnedTabSessionPath(legacyDir, sessionPath)
	if !ok {
		return "", false
	}
	meta, hasMeta, err := agent.LoadBranchMeta(path)
	if strings.TrimSpace(scope) == "project" {
		if err != nil || !hasMeta || meta.Scope != "project" || !sameProjectRoot(meta.WorkspaceRoot, workspaceRoot) {
			return "", false
		}
	} else if err == nil && hasMeta && meta.Scope == "project" {
		return "", false
	}
	return path, true
}

// saveTabSessionMeta persists the tab's scope/topic/mode fields into the
// session's branch-meta sidecar at path. Tab fields are snapshotted under a.mu
// (controller reads happen off-lock) so a concurrent tab mutation can't tear
// the persisted record.
func (a *App) saveTabSessionMeta(tab *WorkspaceTab, path string) error {
	if tab == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	a.mu.RLock()
	ctrl := tab.Ctrl
	snap := tabSessionMetaSnapshot{
		path:             path,
		scope:            tab.Scope,
		workspaceRoot:    tab.WorkspaceRoot,
		topicID:          tab.TopicID,
		topicTitle:       tab.TopicTitle,
		tokenMode:        currentTabTokenMode(tab),
		mode:             normalizeTabMode(tab.mode),
		toolApprovalMode: normalizeToolApprovalMode(tab.toolApprovalMode),
		goal:             strings.TrimSpace(tab.goal),
	}
	a.mu.RUnlock()
	if ctrl != nil {
		snap.mode = tabModeFromAxes(ctrl.PlanMode(), ctrl.AutoApproveTools())
		snap.toolApprovalMode = normalizeToolApprovalMode(ctrl.ToolApprovalMode())
		if goal := strings.TrimSpace(ctrl.Goal()); goal != "" && ctrl.GoalStatus() == control.GoalStatusRunning {
			snap.goal = goal
		} else {
			snap.goal = ""
		}
	}
	return saveTabSessionMetaSnapshot(snap)
}

type tabSessionMetaSnapshot struct {
	path, scope, workspaceRoot string
	topicID, topicTitle        string
	tokenMode, qualityFloor    string
	mode, toolApprovalMode     string
	goal                       string
}

func (a *App) saveTabSessionMetaForCurrentSession(tab *WorkspaceTab) error {
	snap, ok := a.tabSessionMetaSnapshotForCurrentSession(tab)
	if !ok {
		return nil
	}
	return a.saveTabSessionMetaSnapshotAndIndex(snap)
}

func (a *App) tabSessionMetaSnapshotForCurrentSession(tab *WorkspaceTab) (tabSessionMetaSnapshot, bool) {
	if tab == nil {
		return tabSessionMetaSnapshot{}, false
	}
	a.mu.RLock()
	if tab.ID != "" && a.tabs[tab.ID] != tab {
		a.mu.RUnlock()
		return tabSessionMetaSnapshot{}, false
	}
	readOnly := tab.ReadOnly
	ctrl := tab.Ctrl
	storedPath := strings.TrimSpace(tab.SessionPath)
	scope := tab.Scope
	workspaceRoot := tab.WorkspaceRoot
	topicID := tab.TopicID
	topicTitle := tab.TopicTitle
	tokenMode := currentTabTokenMode(tab)
	qualityFloor := strings.TrimSpace(tab.qualityFloor)
	mode := normalizeTabMode(tab.mode)
	toolApprovalMode := normalizeToolApprovalMode(tab.toolApprovalMode)
	goal := strings.TrimSpace(tab.goal)
	a.mu.RUnlock()
	if readOnly {
		return tabSessionMetaSnapshot{}, false
	}

	ctrlPath := ""
	ctrlDir := ""
	activeWork := false
	if ctrl != nil {
		ctrlPath = strings.TrimSpace(ctrl.SessionPath())
		if dir, ok := safeControllerSessionDir(ctrl); ok {
			ctrlDir = strings.TrimSpace(dir)
		}
		status := ctrl.RuntimeStatus()
		activeWork = status.Running || status.PendingPrompt || status.BackgroundJobs > 0
		mode = tabModeFromAxes(ctrl.PlanMode(), ctrl.AutoApproveTools())
		toolApprovalMode = normalizeToolApprovalMode(ctrl.ToolApprovalMode())
		qualityFloor = firstCtrlFloor(ctrl, qualityFloor)
		if ctrl.GoalStatus() == control.GoalStatusRunning {
			goal = strings.TrimSpace(ctrl.Goal())
		} else {
			goal = ""
		}
	}

	currentPath := ctrlPath
	if currentPath == "" {
		currentPath = storedPath
	}
	if currentPath == "" {
		return tabSessionMetaSnapshot{}, false
	}

	sessionDir := desktopSessionDir("")
	if workspaceRoot != "" {
		sessionDir = desktopSessionDir(workspaceRoot)
	} else if ctrlDir != "" {
		sessionDir = ctrlDir
	}
	runtimeDir := sessionDir
	if ctrlDir != "" {
		if _, _, err := validateSessionPath(ctrlDir, currentPath); err == nil {
			runtimeDir = ctrlDir
		}
	}
	if topicID == "" && !activeWork && storedPath != "" && sessionPathHasNoContent(sessionDir, storedPath) {
		return tabSessionMetaSnapshot{}, false
	}
	path := tabSessionMetaPathForSession(runtimeDir, sessionDir, currentPath)
	if path == "" {
		return tabSessionMetaSnapshot{}, false
	}
	return tabSessionMetaSnapshot{
		path:             path,
		scope:            scope,
		workspaceRoot:    workspaceRoot,
		topicID:          topicID,
		topicTitle:       topicTitle,
		tokenMode:        tokenMode,
		qualityFloor:     qualityFloor,
		mode:             mode,
		toolApprovalMode: toolApprovalMode,
		goal:             goal,
	}, true
}

func saveTabSessionMetaSnapshot(snap tabSessionMetaSnapshot) error {
	if strings.TrimSpace(snap.path) == "" {
		return nil
	}
	// Read-modify-write on the branch-meta sidecar: hold the per-path meta lock
	// so agent-side writers (autosave UpdateSessionMeta, in-flight markers)
	// can't interleave and drop fields.
	unlock, err := agent.LockSessionMetaPath(snap.path)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := agent.EnsureBranchMetaLocked(snap.path)
	if err != nil {
		return err
	}
	scope := snap.scope
	workspaceRoot := snap.workspaceRoot
	if ownerScope, ownerRoot, _, ok := legacyMigrationTargetForDir(filepath.Dir(snap.path)); ok {
		if ownerScope == "project" {
			scope = ownerScope
			workspaceRoot = ownerRoot
		}
	}
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
	} else {
		scope = "global"
		workspaceRoot = ""
	}
	m.Scope = scope
	m.WorkspaceRoot = workspaceRoot
	m.TopicID = snap.topicID
	m.TopicTitle = snap.topicTitle
	delivery := strings.TrimSpace(snap.qualityFloor) == control.QualityFloorDelivery
	m.QualityFloor, m.TokenMode, m.AgentPreset = "", boot.TokenModeFull, ""
	if delivery {
		m.QualityFloor, m.TokenMode, m.AgentPreset = control.QualityFloorDelivery, boot.TokenModeDelivery, boot.AgentPresetDelivery
	}
	m.Mode = persistedTabMode(snap.mode)
	m.ToolApprovalMode = persistedToolApprovalMode(snap.toolApprovalMode)
	m.Goal = strings.TrimSpace(snap.goal)
	if err := agent.SaveBranchMetaPreserveUpdatedLocked(snap.path, m); err != nil {
		return err
	}
	invalidateTopicSessionIndexForPath(snap.path)
	return nil
}

func tabSessionMetaPathForSession(runtimeDir, sessionDir, sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	for _, dir := range []string{runtimeDir, sessionDir} {
		if resolved, ok := pinnedTabSessionPath(dir, sessionPath); ok {
			return resolved
		}
	}
	path := canonicalTabSessionPath(sessionPath)
	if filepath.IsAbs(path) {
		return path
	}
	return ""
}

type tabSessionProfile struct {
	tokenMode, qualityFloor, mode string
	toolApprovalMode, goal        string
}

func defaultTabSessionProfile() tabSessionProfile {
	return tabSessionProfile{
		tokenMode:        boot.TokenModeFull,
		mode:             "normal",
		toolApprovalMode: control.ToolApprovalAsk,
	}
}

func tabSessionProfileFromMeta(sessionPath string, meta agent.BranchMeta) tabSessionProfile {
	profile := defaultTabSessionProfile()
	// Prefer agent_preset/quality_floor; fall back to legacy token_mode.
	profile.tokenMode = boot.NormalizeTokenMode(meta.TokenMode)
	if meta.AgentPreset != "" {
		profile.tokenMode = boot.TokenModeFromAgentPreset(meta.AgentPreset)
	}
	switch {
	case strings.TrimSpace(meta.QualityFloor) != "":
		profile.qualityFloor = meta.QualityFloor
	case boot.NormalizeTokenMode(meta.TokenMode) == boot.TokenModeDelivery || meta.AgentPreset == boot.AgentPresetDelivery:
		profile.qualityFloor = control.QualityFloorDelivery
	default:
		profile.qualityFloor = control.QualityFloorStandard
	}
	profile.mode = normalizeTabMode(meta.Mode)
	profile.toolApprovalMode = normalizeToolApprovalMode(meta.ToolApprovalMode)
	if profile.toolApprovalMode == control.ToolApprovalAsk && tabModeHasAutoApproveTools(meta.Mode) {
		profile.toolApprovalMode = control.ToolApprovalYolo
	}
	profile.goal = runningTabSessionGoal(sessionPath, meta.Goal)
	return profile
}

func loadTabSessionProfile(sessionPath string) tabSessionProfile {
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		return defaultTabSessionProfile()
	}
	return tabSessionProfileFromMeta(sessionPath, meta)
}

func applyTabSessionProfile(tab *WorkspaceTab, profile tabSessionProfile) {
	if tab == nil {
		return
	}
	tab.qualityFloor = profile.qualityFloor
	tab.mode = normalizeTabMode(profile.mode)
	tab.toolApprovalMode = normalizeToolApprovalMode(profile.toolApprovalMode)
	if tab.toolApprovalMode == control.ToolApprovalAsk && tabModeHasAutoApproveTools(tab.mode) {
		tab.toolApprovalMode = control.ToolApprovalYolo
	}
	tab.mode = tabModeFromAxes(tabModeHasPlan(tab.mode), tab.toolApprovalMode == control.ToolApprovalYolo)
	tab.goal = strings.TrimSpace(profile.goal)
}

func persistedTabGoal(tab *WorkspaceTab) string {
	goal := strings.TrimSpace(currentTabGoal(tab))
	if goal == "" || currentTabGoalStatus(tab) != control.GoalStatusRunning {
		return ""
	}
	return goal
}

type tabSessionGoalState struct {
	Goal   string `json:"goal,omitempty"`
	Status string `json:"status,omitempty"`
}

func runningTabSessionGoal(sessionPath, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return ""
	}
	data, err := readFileUTF8(store.SessionGoalState(sessionPath))
	if err != nil {
		return fallback
	}
	var state tabSessionGoalState
	if err := json.Unmarshal(data, &state); err != nil {
		return fallback
	}
	switch state.Status {
	case control.GoalStatusRunning:
		if goal := strings.TrimSpace(state.Goal); goal != "" {
			return goal
		}
		return fallback
	case "", control.GoalStatusStopped:
		return ""
	default:
		return ""
	}
}

func canonicalTabSessionPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if validPath, _, err := validateSessionPath(config.SessionDir(), path); err == nil {
		return validPath
	}
	// Project-scope sessions live outside config.SessionDir(), so validation
	// against it always fails. Still normalize the shape: without clean+abs,
	// the same file spelled with a different separator or a relative prefix
	// splits into distinct runtime keys.
	cleaned := filepath.Clean(path)
	if abs, err := filepath.Abs(cleaned); err == nil {
		return abs
	}
	return cleaned
}

func (a *App) rememberTabSessionPath(tab *WorkspaceTab, path string) {
	path = canonicalTabSessionPath(path)
	if tab == nil || path == "" {
		return
	}
	a.mu.Lock()
	if current := a.tabs[tab.ID]; current == tab {
		tab.SessionPath = path
		a.saveTabsLocked()
	} else {
		tab.SessionPath = path
	}
	a.mu.Unlock()
}

func (a *App) persistTabSessionPath(tab *WorkspaceTab, path string) {
	path = canonicalTabSessionPath(path)
	if tab == nil || path == "" {
		return
	}
	// A tab restored from the short-lived tab-scoped implementation may not
	// have had a session path when startup loaded its legacy pins. Publish that
	// one-time migration before reconcile loads the new session-owned sidecar.
	migratePendingLegacyPinnedFiles(tab, path)
	if reconciled, ok := a.reconcileTabWithSessionPath(tab, path); ok {
		path = canonicalTabSessionPath(reconciled)
	}
	_ = a.saveTabSessionMeta(tab, path)
	a.rememberTabSessionPath(tab, path)
}

func (a *App) knownSessionDirs() []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	add(config.SessionDir()) // legacy/global sessions from earlier desktop builds
	add(desktopSessionDir(globalWorkspaceRoot()))
	for _, project := range loadProjectsFile().Projects {
		dir := desktopSessionDir(project.Root)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue // project dir removed or external volume unmounted
		}
		add(dir)
	}
	a.mu.RLock()
	for _, tab := range a.tabs {
		add(tabSessionDir(tab))
	}
	for _, tab := range a.detachedSessions {
		add(tabSessionDir(tab))
	}
	a.mu.RUnlock()
	return out
}

func topicSessionMatchMatchesTarget(match topicSessionMatch, scope, workspaceRoot string) bool {
	if scope == "project" {
		return match.scope == "project" && sameProjectRoot(match.workspaceRoot, workspaceRoot)
	}
	return match.scope == "" || match.scope == "global"
}

func (a *App) findTopicSessionForTarget(scope, workspaceRoot, topicID string) (string, string) {
	return a.findTopicSessionForTargetByContent(scope, workspaceRoot, topicID, false)
}

func (a *App) findTopicContentSessionForTarget(scope, workspaceRoot, topicID string) (string, string) {
	return a.findTopicSessionForTargetByContent(scope, workspaceRoot, topicID, true)
}

func (a *App) findTopicSessionForTargetByContent(scope, workspaceRoot, topicID string, requireContent bool) (string, string) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return "", ""
	}
	type candidate struct {
		match topicSessionMatch
		dir   string
	}
	var candidates []candidate
	for _, dir := range a.knownSessionDirs() {
		for _, match := range topicSessionMatches(dir, topicID) {
			if !topicSessionMatchMatchesTarget(match, scope, workspaceRoot) {
				continue
			}
			candidates = append(candidates, candidate{match: match, dir: dir})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i].match, candidates[j].match
		if !a.updatedAt.Equal(b.updatedAt) {
			return a.updatedAt.After(b.updatedAt)
		}
		return a.path < b.path
	})
	// Content-bearing sessions outrank content-free ones regardless of
	// updatedAt: a freshly created empty session must not hijack the topic
	// from the conversation the user actually had (#7305). The content probe
	// reads session files, so it walks newest-first and stops at the first
	// hit — the common case checks one file.
	for _, c := range candidates {
		if sessionFileHasConversationContent(c.match.path) {
			return c.match.path, c.dir
		}
	}
	if requireContent || len(candidates) == 0 {
		return "", ""
	}
	return candidates[0].match.path, candidates[0].dir
}

type topicSessionFileSignature struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
}

type topicSessionMatch struct {
	path          string
	updatedAt     time.Time
	scope         string
	workspaceRoot string
}

type topicSessionDirIndex struct {
	signature []topicSessionFileSignature
	byTopic   map[string][]topicSessionMatch
}

// mergeSessionInfos merges one directory's session listing into the maps used by
// ListProjectTree. The result collection loop calls it serially.
func mergeSessionInfos(dir string, infos []agent.SessionInfo, titles map[string]string, sessionInfos map[string]agent.SessionInfo, sessionTitles map[string]string, topicSummaries map[string]topicSummary) {
	for _, info := range infos {
		sessionKey := sessionRuntimeKey(info.Path)
		if sessionKey != "" {
			sessionInfos[sessionKey] = info
			title := strings.TrimSpace(info.CustomTitle)
			if title == "" {
				title = titles[filepath.Base(info.Path)]
			}
			sessionTitles[sessionKey] = title
		}
		if strings.TrimSpace(info.TopicID) == "" {
			continue
		}
		key := topicSummaryKey(info.Scope, info.WorkspaceRoot, info.TopicID)
		summary := topicSummaries[key]
		lastActivityAt := info.LastActivityAt.UnixMilli()
		if sessionInfoIsAutomaticRecovery(info) {
			// A covered conflict copy duplicates its parent, so its turns must not
			// be added. Any branch with unique content keeps the topic visible.
			if sessionInfoIsUnmodifiedRecoveryCopy(info, dir) {
				summary.hasRecoveryOnly = true
			} else {
				summary.hasAdoptedRecovery = true
				if info.Turns > summary.adoptedRecoveryTurns {
					summary.adoptedRecoveryTurns = info.Turns
				}
			}
			if lastActivityAt > summary.lastActivityAt {
				summary.lastActivityAt = lastActivityAt
			}
			topicSummaries[key] = summary
			continue
		}
		summary.hasNormalSession = true
		summary.turns += info.Turns
		if lastActivityAt > summary.lastActivityAt {
			summary.lastActivityAt = lastActivityAt
		}
		topicSummaries[key] = summary
	}
}

var topicSessionIndexCache = struct {
	sync.Mutex
	byDir map[string]topicSessionDirIndex
}{byDir: map[string]topicSessionDirIndex{}}

func topicSessionDirKey(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func topicSessionDirSnapshot(dir string) ([]topicSessionFileSignature, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	signature := []topicSessionFileSignature{}
	sessionNames := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		isSession := store.IsSessionTranscriptName(name)
		isMeta := strings.HasSuffix(name, ".jsonl.meta")
		if !isSession && !isMeta {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		signature = append(signature, topicSessionFileSignature{
			Name:    name,
			Size:    info.Size(),
			ModTime: info.ModTime().UnixNano(),
		})
		if isSession {
			sessionNames = append(sessionNames, name)
		}
	}
	sort.Slice(signature, func(i, j int) bool {
		return signature[i].Name < signature[j].Name
	})
	sort.Strings(sessionNames)
	return signature, sessionNames, nil
}

func topicSessionSignaturesEqual(a, b []topicSessionFileSignature) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func topicSessionIndexForDir(dir string) (topicSessionDirIndex, error) {
	key := topicSessionDirKey(dir)
	if key == "" {
		return topicSessionDirIndex{}, nil
	}
	signature, sessionNames, err := topicSessionDirSnapshot(key)
	if err != nil {
		if os.IsNotExist(err) {
			return topicSessionDirIndex{}, nil
		}
		return topicSessionDirIndex{}, err
	}
	topicSessionIndexCache.Lock()
	cached, ok := topicSessionIndexCache.byDir[key]
	if ok && topicSessionSignaturesEqual(cached.signature, signature) {
		topicSessionIndexCache.Unlock()
		return cached, nil
	}
	topicSessionIndexCache.Unlock()

	index := topicSessionDirIndex{
		signature: signature,
		byTopic:   map[string][]topicSessionMatch{},
	}
	for _, name := range sessionNames {
		path := filepath.Join(key, name)
		meta, ok, err := agent.LoadBranchMeta(path)
		if err != nil || !ok {
			continue
		}
		topicID := strings.TrimSpace(meta.TopicID)
		if topicID == "" {
			continue
		}
		index.byTopic[topicID] = append(index.byTopic[topicID], topicSessionMatch{
			path:          path,
			updatedAt:     meta.UpdatedAt,
			scope:         meta.DefaultScope(),
			workspaceRoot: meta.WorkspaceRoot,
		})
	}

	topicSessionIndexCache.Lock()
	topicSessionIndexCache.byDir[key] = index
	topicSessionIndexCache.Unlock()
	return index, nil
}

func topicSessionIndexHasContentTopic(index topicSessionDirIndex, topicID string) bool {
	matches := index.byTopic[strings.TrimSpace(topicID)]
	for _, match := range matches {
		if sessionFileHasConversationContent(match.path) {
			return true
		}
	}
	return false
}

// topicSessionIndexHasForeignLeaseTopic reports whether any session file
// indexed under topicID is currently lease-held by a runtime other than this
// process. A blank topic can still be lease-held — its session lease keeper
// keeps a leftover blank tab's lease alive across a hide-to-tray close, and a
// stale-but-live holder blocks a genuinely new session from ever settling on
// this path. Reusing it anyway would make the "new" tab collide with that
// holder: every lease-gated switch (effort/model/token mode) would fail as if
// a foreign window owned it, and creating another "new" conversation would
// keep re-picking the same stuck topic (#6028, #6109).
func topicSessionIndexHasForeignLeaseTopic(index topicSessionDirIndex, topicID string) bool {
	matches := index.byTopic[strings.TrimSpace(topicID)]
	for _, match := range matches {
		if agent.SessionLeaseHeldByOtherRuntime(match.path) {
			return true
		}
	}
	return false
}

// topicIDForSessionPath reverse-maps a session file to its project-tree topic
// (id + workspace root) using the topic-session index. ok=false when the
// session has no indexed topic (catalog-only sessions, cleanup-pending
// entries, or an index that failed to build).
func topicIDForSessionPath(dir, sessionPath string) (topicID, workspaceRoot string, ok bool) {
	index, err := topicSessionIndexForDir(dir)
	if err != nil {
		return "", "", false
	}
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return "", "", false
	}
	for id, matches := range index.byTopic {
		for _, match := range matches {
			if match.path == sessionPath {
				return id, match.workspaceRoot, true
			}
		}
	}
	return "", "", false
}

func topicSessionMatches(dir, topicID string) []topicSessionMatch {
	index, err := topicSessionIndexForDir(dir)
	if err != nil {
		return nil
	}
	matches := index.byTopic[strings.TrimSpace(topicID)]
	if len(matches) == 0 {
		return nil
	}
	out := make([]topicSessionMatch, 0, len(matches))
	for _, match := range matches {
		if agent.IsCleanupPending(match.path) {
			continue
		}
		out = append(out, match)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func invalidateTopicSessionIndex(dir string) {
	key := topicSessionDirKey(dir)
	if key == "" {
		return
	}
	topicSessionIndexCache.Lock()
	delete(topicSessionIndexCache.byDir, key)
	topicSessionIndexCache.Unlock()
}

func invalidateTopicSessionIndexForPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	invalidateTopicSessionIndex(filepath.Dir(path))
}

// findTopicSession returns the most recently updated .jsonl file whose .meta
// carries the given topicID, using a directory-level sidecar index cache.
func findTopicSession(dir, topicID string) string {
	if topicID == "" || dir == "" {
		return ""
	}
	var bestPath string
	var bestTime time.Time
	for _, match := range topicSessionMatches(dir, topicID) {
		if match.updatedAt.After(bestTime) {
			bestTime = match.updatedAt
			bestPath = match.path
		}
	}
	return bestPath
}
