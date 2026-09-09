package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"reasonix/internal/agent"
)

type SessionRuntimePhase string

const (
	sessionRuntimeStarting     SessionRuntimePhase = "starting"
	sessionRuntimeReady        SessionRuntimePhase = "ready"
	sessionRuntimeLeaseBlocked SessionRuntimePhase = "lease_blocked"
	sessionRuntimeFailed       SessionRuntimePhase = "failed"
	sessionRuntimeClosing      SessionRuntimePhase = "closing"
)

type SessionRuntimeIssue struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	HolderPID  int    `json:"holderPid,omitempty"`
	HolderHost string `json:"holderHost,omitempty"`
	AcquiredAt string `json:"acquiredAt,omitempty"`
}

type SessionRuntimeView struct {
	Phase SessionRuntimePhase  `json:"phase"`
	Epoch string               `json:"epoch"`
	Issue *SessionRuntimeIssue `json:"issue,omitempty"`
}

// desktopSessionRuntime is the process-local ownership record for one writable
// session. WorkspaceTab still carries compatibility projections of controller
// and lease fields while the desktop code migrates, but this registry is the
// authority that prevents a second local build from competing for the same
// session path.
//
// All fields are guarded by App.mu.
type desktopSessionRuntime struct {
	ID, Key, Epoch         string
	Phase                  SessionRuntimePhase
	Issue                  *SessionRuntimeIssue
	Owner                  *WorkspaceTab
	suppressStartupRestore bool
	readyCh                chan struct{}
}

func newSessionRuntimeID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(b[:])
	}
	return prefix + "_" + time.Now().UTC().Format("20060102150405.000000000")
}

func cloneSessionRuntimeIssue(issue *SessionRuntimeIssue) *SessionRuntimeIssue {
	if issue == nil {
		return nil
	}
	copy := *issue
	return &copy
}

func sessionRuntimeIssueForError(err error) *SessionRuntimeIssue {
	if err == nil {
		return nil
	}
	issue := &SessionRuntimeIssue{
		Code:      "startup_failed",
		Message:   userFacingSessionLeaseError("", err).Error(),
		Retryable: false,
	}
	if !errors.Is(err, agent.ErrSessionLeaseHeld) {
		return issue
	}
	issue.Code = "session_lease_held"
	issue.Retryable = true
	var leaseErr *agent.SessionLeaseError
	if errors.As(err, &leaseErr) && leaseErr != nil && leaseErr.Info != nil {
		issue.HolderPID = leaseErr.Info.PID
		issue.HolderHost = strings.TrimSpace(leaseErr.Info.Hostname)
		if !leaseErr.Info.AcquiredAt.IsZero() {
			issue.AcquiredAt = leaseErr.Info.AcquiredAt.UTC().Format(time.RFC3339)
		}
	}
	return issue
}

func (a *App) newSessionRuntimeLocked(tab *WorkspaceTab, key string) *desktopSessionRuntime {
	if existing := a.runtimeBySessionKey[key]; key != "" && existing != nil && existing.Owner != tab {
		// A second tab may begin restoring the same persisted path before it
		// reaches claimSessionRuntime. Do not overwrite the first starting
		// placeholder; the later build will wait for and attach to it.
		key = ""
	}
	rt := &desktopSessionRuntime{
		ID:      newSessionRuntimeID("runtime"),
		Key:     key,
		Epoch:   newSessionRuntimeID("epoch"),
		Phase:   sessionRuntimeStarting,
		Owner:   tab,
		readyCh: make(chan struct{}),
	}
	if tab != nil && tab.Ctrl != nil && tab.Ready {
		rt.Phase = sessionRuntimeReady
		closeRuntimeReadyChannelLocked(rt)
	}
	if a.runtimeByID == nil {
		a.runtimeByID = map[string]*desktopSessionRuntime{}
	}
	if a.runtimeBySessionKey == nil {
		a.runtimeBySessionKey = map[string]*desktopSessionRuntime{}
	}
	a.runtimeByID[rt.ID] = rt
	if key != "" {
		a.runtimeBySessionKey[key] = rt
	}
	if tab != nil {
		tab.runtimeID = rt.ID
		if tab.sink != nil {
			tab.sink.setRuntimeEpoch(rt.Epoch)
		}
	}
	return rt
}

func (a *App) runtimeForTabLocked(tab *WorkspaceTab) *desktopSessionRuntime {
	if tab == nil || strings.TrimSpace(tab.runtimeID) == "" {
		return nil
	}
	rt := a.runtimeByID[tab.runtimeID]
	if rt == nil || rt.Owner != tab {
		return nil
	}
	return rt
}

// runtimeEpochForTabLocked returns the generation already transferred to tab.
// Callers use it when reattaching an existing runtime: the generation must be
// announced before that runtime replays any pending prompt.
func (a *App) runtimeEpochForTabLocked(tab *WorkspaceTab) string {
	if rt := a.runtimeForTabLocked(tab); rt != nil {
		return rt.Epoch
	}
	if tab != nil && tab.sink != nil {
		return tab.sink.runtimeEpochSnapshot()
	}
	return ""
}

func (a *App) runtimeOwnerLiveLocked(rt *desktopSessionRuntime) bool {
	if rt == nil || rt.Owner == nil {
		return false
	}
	if a.tabs[rt.Owner.ID] == rt.Owner {
		return true
	}
	for _, detached := range a.detachedSessions {
		if detached == rt.Owner {
			return true
		}
	}
	return false
}

func (a *App) removeSessionRuntimeMappingsLocked(rt *desktopSessionRuntime) {
	if rt == nil {
		return
	}
	for key, candidate := range a.runtimeBySessionKey {
		if candidate == rt {
			delete(a.runtimeBySessionKey, key)
		}
	}
	delete(a.runtimeByID, rt.ID)
}

func closeRuntimeReadyChannelLocked(rt *desktopSessionRuntime) {
	if rt == nil || rt.readyCh == nil {
		return
	}
	close(rt.readyCh)
	rt.readyCh = nil
}

func (a *App) setSessionRuntimePhaseLocked(tab *WorkspaceTab, phase SessionRuntimePhase, err error) {
	if tab == nil {
		return
	}
	rt := a.runtimeForTabLocked(tab)
	if rt == nil {
		key := sessionRuntimeKey(tab.SessionPath)
		if existing := a.runtimeBySessionKey[key]; key != "" && existing != nil && existing.Owner != tab {
			return
		}
		rt = a.newSessionRuntimeLocked(tab, key)
	}
	rt.Phase = phase
	rt.Issue = sessionRuntimeIssueForError(err)
	rt.suppressStartupRestore = false
	if phase == sessionRuntimeStarting {
		rt.Issue = nil
		if rt.readyCh == nil {
			rt.readyCh = make(chan struct{})
		}
	} else {
		closeRuntimeReadyChannelLocked(rt)
	}
	if tab.sink != nil {
		tab.sink.setRuntimeEpoch(rt.Epoch)
	}
}

func (a *App) advanceSessionRuntimeEpochLocked(tab *WorkspaceTab) string {
	if tab == nil {
		return ""
	}
	rt := a.runtimeForTabLocked(tab)
	if rt == nil {
		rt = a.newSessionRuntimeLocked(tab, sessionRuntimeKey(tab.SessionPath))
	}
	rt.Epoch = newSessionRuntimeID("epoch")
	rt.Phase = sessionRuntimeReady
	rt.Issue = nil
	rt.suppressStartupRestore = false
	closeRuntimeReadyChannelLocked(rt)
	if tab.sink != nil {
		tab.sink.setRuntimeEpoch(rt.Epoch)
	}
	if binder, ok := tab.Ctrl.(interface{ BindTranscriptRuntimeEpoch(string) }); ok {
		binder.BindTranscriptRuntimeEpoch(rt.Epoch)
	}
	return rt.Epoch
}

func (a *App) sessionRuntimeViewLocked(tab *WorkspaceTab) SessionRuntimeView {
	if tab == nil {
		return SessionRuntimeView{Phase: sessionRuntimeStarting}
	}
	if rt := a.runtimeForTabLocked(tab); rt != nil {
		return SessionRuntimeView{
			Phase: rt.Phase,
			Epoch: rt.Epoch,
			Issue: cloneSessionRuntimeIssue(rt.Issue),
		}
	}
	view := SessionRuntimeView{Phase: sessionRuntimeStarting}
	switch {
	case tab.Ctrl != nil && tab.Ready:
		view.Phase = sessionRuntimeReady
	case tab.StartupErrLeaseHeld:
		view.Phase = sessionRuntimeLeaseBlocked
		view.Issue = sessionRuntimeIssueForError(&agent.SessionLeaseError{})
	case strings.TrimSpace(tab.StartupErr) != "":
		view.Phase = sessionRuntimeFailed
		view.Issue = &SessionRuntimeIssue{Code: "startup_failed", Message: tab.StartupErr}
	}
	return view
}

func (a *App) bindSessionRuntimeKeyLocked(tab *WorkspaceTab, path string) bool {
	if tab == nil {
		return false
	}
	key := sessionRuntimeKey(path)
	if key == "" {
		return true
	}
	if existing := a.runtimeBySessionKey[key]; existing != nil && existing.Owner != tab {
		return false
	}
	rt := a.runtimeForTabLocked(tab)
	if rt == nil {
		a.newSessionRuntimeLocked(tab, key)
		return true
	}
	if rt.Key != "" && rt.Key != key && a.runtimeBySessionKey[rt.Key] == rt {
		delete(a.runtimeBySessionKey, rt.Key)
	}
	rt.Key = key
	a.runtimeBySessionKey[key] = rt
	return true
}

type sessionRuntimePathTransition struct {
	runtime       *desktopSessionRuntime
	owner         *WorkspaceTab
	oldKey        string
	targetKey     string
	expectedEpoch string
}

func (a *App) reserveSessionRuntimePath(tab *WorkspaceTab, path string) (sessionRuntimePathTransition, error) {
	targetKey := sessionRuntimeKey(path)
	if tab == nil || targetKey == "" {
		return sessionRuntimePathTransition{}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing := a.runtimeBySessionKey[targetKey]; existing != nil && existing.Owner != tab {
		return sessionRuntimePathTransition{}, fmt.Errorf("%w: local runtime already owns session", agent.ErrSessionLeaseHeld)
	}
	rt := a.runtimeForTabLocked(tab)
	if rt == nil {
		// A path transition must retain the source identity until commit. Using
		// targetKey here would make a failed first rebind forget the still-live
		// source controller and its lease.
		rt = a.newSessionRuntimeLocked(tab, sessionRuntimeKey(tab.currentSessionPath()))
	}
	transition := sessionRuntimePathTransition{
		runtime:       rt,
		owner:         tab,
		oldKey:        rt.Key,
		targetKey:     targetKey,
		expectedEpoch: rt.Epoch,
	}
	// Keep the old key mapped until the lease rebind succeeds. The target alias
	// prevents another local startup from claiming it during the off-lock file
	// operation.
	a.runtimeBySessionKey[targetKey] = rt
	return transition, nil
}

func (a *App) commitSessionRuntimePath(transition sessionRuntimePathTransition) {
	if transition.runtime == nil || transition.targetKey == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rt := transition.runtime
	if transition.oldKey != "" && transition.oldKey != transition.targetKey && a.runtimeBySessionKey[transition.oldKey] == rt {
		delete(a.runtimeBySessionKey, transition.oldKey)
	}
	rt.Key = transition.targetKey
	a.runtimeBySessionKey[transition.targetKey] = rt
}

// commitSessionRuntimePathLocked commits a previously reserved path only when
// the same runtime generation still owns both aliases. It lets controller
// swaps make the registry update part of their single App.mu commit.
func (a *App) commitSessionRuntimePathLocked(transition sessionRuntimePathTransition) bool {
	if transition.runtime == nil || transition.targetKey == "" {
		return false
	}
	if !a.sessionRuntimePathTransitionValidLocked(transition) {
		return false
	}
	rt := transition.runtime
	if transition.oldKey != "" && transition.oldKey != transition.targetKey && a.runtimeBySessionKey[transition.oldKey] == rt {
		delete(a.runtimeBySessionKey, transition.oldKey)
	}
	rt.Key = transition.targetKey
	a.runtimeBySessionKey[transition.targetKey] = rt
	return true
}

func (a *App) sessionRuntimePathTransitionValidLocked(transition sessionRuntimePathTransition) bool {
	if transition.runtime == nil || transition.targetKey == "" {
		return false
	}
	rt := transition.runtime
	if a.runtimeByID[rt.ID] != rt ||
		rt.Owner != transition.owner ||
		rt.Epoch != transition.expectedEpoch ||
		(rt.Key != transition.oldKey && rt.Key != transition.targetKey) ||
		a.runtimeBySessionKey[transition.targetKey] != rt {
		return false
	}
	return true
}

func (a *App) rollbackSessionRuntimePath(transition sessionRuntimePathTransition) {
	if transition.runtime == nil || transition.targetKey == "" || transition.targetKey == transition.oldKey {
		return
	}
	a.mu.Lock()
	if a.runtimeBySessionKey[transition.targetKey] == transition.runtime {
		delete(a.runtimeBySessionKey, transition.targetKey)
	}
	a.mu.Unlock()
}

// claimSessionRuntime reserves path for tab, or waits for/attaches the existing
// local runtime. The caller owns its not-yet-published candidate controller; a
// true return means that candidate must be closed because tab now uses the
// already registered runtime.
func (a *App) claimSessionRuntime(tab *WorkspaceTab, path string, ctx context.Context) bool {
	key := sessionRuntimeKey(path)
	if tab == nil || key == "" {
		return false
	}
	for {
		a.mu.Lock()
		if tab.removed || a.tabs[tab.ID] != tab {
			a.mu.Unlock()
			return false
		}
		rt := a.runtimeBySessionKey[key]
		if rt != nil && !a.runtimeOwnerLiveLocked(rt) {
			a.removeSessionRuntimeMappingsLocked(rt)
			rt = nil
		}
		switch {
		case rt == nil:
			// Detached runtimes created before the admission registry was
			// populated are a compatibility edge. Attach them before claiming
			// the key so applyRuntimeTab can publish one authoritative runtime
			// instead of leaving behind an unused placeholder.
			if detached := a.detachedSessions[key]; detached != nil && detached.Ctrl != nil {
				a.mu.Unlock()
				return a.attachExistingSessionRuntime(tab, path, a.ctx)
			}
			a.bindSessionRuntimeKeyLocked(tab, path)
			a.mu.Unlock()
			return false
		case rt.Owner == tab:
			a.mu.Unlock()
			// The target may own the starting placeholder while a legacy
			// visible/detached runtime for the same session predates the
			// registry. Let attachExistingSessionRuntime adopt that usable
			// controller; otherwise this remains the owner build.
			return a.attachExistingSessionRuntime(tab, path, a.ctx)
		case rt.Phase == sessionRuntimeStarting && rt.readyCh != nil:
			wait := rt.readyCh
			a.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return false
			case <-time.After(250 * time.Millisecond):
				// Re-check owner liveness even when a superseded build exited
				// before publishing a terminal phase.
				continue
			}
		default:
			a.mu.Unlock()
			return a.attachExistingSessionRuntime(tab, path, a.ctx)
		}
	}
}

func (a *App) releaseSessionRuntimeLocked(tab *WorkspaceTab) {
	rt := a.runtimeForTabLocked(tab)
	if rt == nil {
		if tab != nil {
			tab.runtimeID = ""
		}
		return
	}
	rt.Phase = sessionRuntimeClosing
	closeRuntimeReadyChannelLocked(rt)
	a.removeSessionRuntimeMappingsLocked(rt)
	if tab != nil {
		tab.runtimeID = ""
	}
}

func sameCurrentProcessLease(err error) bool {
	var leaseErr *agent.SessionLeaseError
	if !errors.As(err, &leaseErr) || leaseErr == nil || leaseErr.Info == nil {
		return false
	}
	if leaseErr.Info.PID != os.Getpid() || leaseErr.Info.WriterID != agent.SessionWriterID() {
		return false
	}
	host, _ := os.Hostname()
	return strings.TrimSpace(leaseErr.Info.Hostname) == strings.TrimSpace(host)
}

// sessionParentLive reports whether a desktop tab or detached runtime in this
// process currently owns, or is still building, the requested session. It is
// intentionally checked before stale-subagent cleanup probes the durable lease:
// a starting tab has published SessionPath but may not have bound that lease yet.
func (a *App) sessionParentLive(sessionPath string) bool {
	return a.sessionParentLiveForBuild(sessionPath, nil)
}

// subagentParentProbeForBuild excludes an initial build's own unbound tab: that
// build can safely repair its crash leftovers before it binds the session lease.
// Other live tabs remain protected from the sweep.
func (a *App) subagentParentProbeForBuild(building *WorkspaceTab) func(string) bool {
	return func(sessionPath string) bool {
		return a.sessionParentLiveForBuild(sessionPath, building)
	}
}

func (a *App) sessionParentLiveForBuild(sessionPath string, building *WorkspaceTab) bool {
	key := sessionRuntimeKey(sessionPath)
	if a == nil || key == "" {
		return false
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if rt := a.runtimeBySessionKey[key]; rt != nil && a.runtimeOwnerLiveLocked(rt) &&
		!(rt.Owner == building && building.Ctrl == nil) {
		return true
	}
	liveTab := func(tab *WorkspaceTab) bool {
		if tab == nil {
			return false
		}
		if tab == building && tab.Ctrl == nil {
			return false
		}
		if sessionRuntimeKey(tab.SessionPath) == key {
			return true
		}
		return tab.Ctrl != nil && sessionRuntimeKey(tab.Ctrl.SessionPath()) == key
	}
	for _, tab := range a.tabs {
		if liveTab(tab) {
			return true
		}
	}
	for _, tab := range a.detachedSessions {
		if liveTab(tab) {
			return true
		}
	}
	return false
}
