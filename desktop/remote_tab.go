package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
)

// The remote-tab bridge exchanges its pre-shared token for an HttpOnly
// session cookie over the loopback tunnel. Subsequent API and SSE requests
// use that cookie, keeping the token out of request lines and access logs.

const remoteTabStreamOpenStability = 50 * time.Millisecond

func remoteSessionTransitionBusy(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "while a turn is running") ||
		strings.Contains(message, "while another session change is in progress") ||
		strings.Contains(message, "session is finishing background teardown")
}

// attachRemoteTabServe starts the event pump before entering the session so
// /new or /resume frames are not missed. The caller's context owns the pump;
// handshake and session entry use a bounded child context.
func (a *App) attachRemoteTabServe(ctx context.Context, tabID, base, token, instanceID string, opts RemoteTabOpenOptions) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, err := newServeHTTPClient(base)
	if err != nil {
		return false, err
	}
	if err := serveHandshake(callCtx, client, base, token); err != nil {
		log.Printf("[remote] attachRemoteTabServe: handshake FAILED tab=%s base=%q err=%v", tabID, base, err)
		return false, err
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	a.remoteTabMu.Unlock()
	if tab == nil {
		return false, fmt.Errorf("remote tab %q closed during bootstrap", tabID)
	}
	tab.sessionMu.Lock()
	defer tab.sessionMu.Unlock()

	// Resolve every non-new target before opening the all-session pump. A
	// detached controller may replay pending prompts as soon as /resume starts;
	// publishing its route first keeps those frames on the foreground surface.
	focusOnly := !opts.NewSession && strings.TrimSpace(opts.SessionName) == "" && strings.TrimSpace(opts.SessionPath) == ""
	var target serveSessionEntry
	if !opts.NewSession {
		target, err = preflightRemoteSessionTarget(callCtx, client, base, opts)
		if err != nil {
			return false, err
		}
	}

	pumpCtx, gen, attachPathRevision, err := a.installRemoteTabAttachPump(ctx, tabID, tab, client, base, token, target.Path, !opts.NewSession)
	if err != nil {
		return false, err
	}

	opened := make(chan error, 1)
	a.goRemoteTabSafe("remoteTabPump", func() { a.remoteTabPump(pumpCtx, tabID, gen, opened) })
	select {
	case err = <-opened:
		if err != nil {
			a.retireRemoteTabGeneration(tabID, gen)
			return false, err
		}
	case <-callCtx.Done():
		a.retireRemoteTabGeneration(tabID, gen)
		return false, callCtx.Err()
	}
	entered := true
	if !focusOnly {
		enterOpts := opts
		if !opts.NewSession {
			enterOpts.SessionName, enterOpts.SessionPath, enterOpts.SessionTitle = target.Name, target.Path, target.Title
		}
		target, err = enterRemoteSessionTarget(callCtx, client, base, enterOpts)
		entered = err == nil
	}
	if err != nil {
		// A busy serve refuses session transitions with 409 but retains its
		// usable current session. Keep the attach so pending work remains visible.
		if remoteSessionTransitionBusy(err) {
			log.Printf("[remote] attachRemoteTabServe: enterRemoteSession BUSY (attached to current session) tab=%s err=%v", tabID, err)
			entered = false
			target, _ = serveCurrentSession(callCtx, client, base)
		} else if remoteSessionTakenOver(err) {
			// A local runtime on the serve host owns the session. Pin the tab to
			// the requested session as a read-only spectator: the mirror's frames
			// route here, /history and /status serve the file-backed view, and
			// the take-back banner drives /reclaim. No serve-frontend transition
			// ran, but the tab must stay attached to render the mirror.
			log.Printf("[remote] attachRemoteTabServe: enterRemoteSession TAKEN OVER (read-only spectator) tab=%s session=%q err=%v", tabID, target.Path, err)
			entered = false
			if strings.TrimSpace(target.Path) == "" {
				current, _ := serveCurrentSession(callCtx, client, base)
				target = current
			}
			target.TakenOver = true
		} else {
			log.Printf("[remote] attachRemoteTabServe: enterRemoteSession FAILED tab=%s err=%v", tabID, err)
			a.retireRemoteTabGeneration(tabID, gen)
			return false, err
		}
	}
	if !a.commitRemoteTabAttachResponse(tabID, tab, gen, attachPathRevision, target, opts.NewSession) {
		entered = false
	}
	if !a.waitRemoteTabStreamStable(callCtx, tabID, gen) {
		return false, fmt.Errorf("remote tab %q event stream closed during session attach", tabID)
	}
	a.remoteTabMu.Lock()
	if current := a.remoteTabs[tabID]; current == tab && current.gen == gen {
		current.session.instanceID = instanceID
	}
	a.remoteTabMu.Unlock()
	// A 200 response is only the stream-open barrier. The stream can still die
	// while /new or /resume is in flight; publish readiness only if its pump has
	// not already moved this same generation into reconnecting/error.
	if !a.markRemoteTabAttached(tabID, gen) {
		return false, fmt.Errorf("remote tab %q event stream closed during session attach", tabID)
	}
	return entered, nil
}

// markRemoteTabSpectatorIfLocalOwned reconciles ownership when a mid-view
// status/notice needs an authoritative probe, or when an older Serve omitted
// the synchronous ownership header used by attach and resume responses.
func (a *App) markRemoteTabSpectatorIfLocalOwned(ctx context.Context, tabID string, client *http.Client, base string, gen uint64) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	path := ""
	selectionRevision := uint64(0)
	if tab != nil && tab.gen == gen {
		path = strings.TrimSpace(tab.routing.currentPath)
		selectionRevision = tab.selectionRevision
	}
	a.remoteTabMu.Unlock()
	if path == "" {
		return
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	view, probeErr := takeoverOwnership(probeCtx, client, base, path)
	probeCancel()
	// A transport failure says nothing about ownership. Preserve the current
	// spectator pin and let the next status/notice probe retry instead of
	// briefly reopening input against an ownership state we could not prove.
	if probeErr != nil {
		return
	}
	// "external" = a mirrored local writer; "other" = a local runtime holds
	// the lease without a registered mirror (adopter absent). Serve mounts
	// both as read-only spectator surfaces, so the banner and the take-back
	// button must appear for either — otherwise the tab unlocks its composer
	// against a transcript it cannot write.
	locallyOwned := takeoverViewLocallyOwned(view)
	a.remoteTabMu.Lock()
	current := a.remoteTabs[tabID]
	if current != tab || current == nil || current.gen != gen || current.client != client ||
		current.selectionRevision != selectionRevision ||
		agent.CanonicalSessionPath(current.routing.currentPath) != agent.CanonicalSessionPath(path) {
		a.remoteTabMu.Unlock()
		return
	}
	if !locallyOwned {
		// The probed session is not locally owned (e.g. a fresh /new or a
		// free session). Clear any stale spectator pin left over from the
		// previous session so the banner and read-only composer go away.
		current.session.takenOver = false
		a.remoteTabMu.Unlock()
		return
	}
	current.session.takenOver = true
	a.remoteTabMu.Unlock()
	// No remote-tab:updated emit: the async probe racing with hydration
	// causes the frontend to re-render mid-fetch, appearing as a retry
	// loop. The periodic /status poll (recordRemoteTabSessionStatus)
	// naturally picks up takenOver and emits a meta update.
	slog.Info("desktop: remote tab switched to spectator on local-owned session",
		"tab", tabID, "session", path, "holder", view.Holder)
}

// probeSpectatorAfterNotice re-probes spectator state when Serve broadcasts a
// takeover or reclaim notice for a session. The entry-time probe cannot see
// mid-view transitions, so without this the banner and the composer lock
// drift from the real ownership until the next session switch.
func (a *App) probeSpectatorAfterNotice(tabID string, gen uint64, client *http.Client, base, framePath string) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	viewing := tab != nil && tab.gen == gen && tab.routing.currentPath != "" &&
		agent.CanonicalSessionPath(tab.routing.currentPath) == agent.CanonicalSessionPath(framePath)
	a.remoteTabMu.Unlock()
	if !viewing {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a.markRemoteTabSpectatorIfLocalOwned(ctx, tabID, client, base, gen)
}

// clearRemoteTabSpectator drops the read-only spectator pin when the route it
// was pinned to lost the publication fence (or after reclaim lands the
// foreground on the session).
func (a *App) clearRemoteTabSpectator(tabID string, gen uint64) {
	a.remoteTabMu.Lock()
	current := a.remoteTabs[tabID]
	// gen 0 means "any generation" — used by failure cleanups where the
	// caller doesn't track the exact generation.
	if current == nil || (gen != 0 && current.gen != gen) {
		a.remoteTabMu.Unlock()
		return
	}
	if !current.session.takenOver {
		a.remoteTabMu.Unlock()
		return
	}
	current.session.takenOver = false
	a.remoteTabMu.Unlock()
	// No emit: let the /status poll propagate the change.
}

// commitRemoteTabAttachResponse applies an attach response only while it still
// owns the foreground route. A session_changed frame for a newer adoption is
// authoritative even when the older /new or /resume response arrives later.
func (a *App) commitRemoteTabAttachResponse(tabID string, tab *remoteTab, gen, requestPathRevision uint64, target serveSessionEntry, reset bool) bool {
	tab.routeEventMu.Lock()
	defer tab.routeEventMu.Unlock()
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	current := a.remoteTabs[tabID]
	if current != tab || current.gen != gen {
		return false
	}
	target.Path = strings.TrimSpace(target.Path)
	if current.routing.pathRevision != requestPathRevision && current.routing.currentPath != target.Path {
		return false
	}
	alreadyAdopted := current.routing.pathRevision != requestPathRevision
	if !alreadyAdopted {
		commitRemoteTabAttachRoute(current, target.Path, reset)
	}
	current.session.takenOver = target.TakenOver
	if name := strings.TrimSpace(target.Name); name != "" {
		current.session.name = name
	}
	if title := strings.TrimSpace(target.Title); title != "" {
		current.topicTitle = title
	}
	if current.routing.running == nil {
		current.routing.running = map[string]bool{}
	}
	return true
}

func (a *App) waitRemoteTabStreamStable(ctx context.Context, tabID string, gen uint64) bool {
	timer := time.NewTimer(remoteTabStreamOpenStability)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return a.remoteTabGenerationCurrent(tabID, gen)
	}
}

func (a *App) markRemoteTabAttached(tabID string, gen uint64) bool {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	tab := a.remoteTabs[tabID]
	if tab == nil || tab.gen != gen || tab.state != "connecting" {
		return false
	}
	tab.attachedGen = gen
	return true
}

func (a *App) publishRemoteTabAttachedReady(tabID string, gen uint64) bool {
	tab := a.lockRemoteTabPublication(tabID)
	if tab == nil {
		return false
	}
	a.remoteTabMu.Lock()
	if a.remoteTabs[tabID] != tab || tab.gen != gen || tab.attachedGen != gen || tab.state != "connecting" {
		a.remoteTabMu.Unlock()
		tab.routeEventMu.Unlock()
		return false
	}
	tab.attachedGen = 0
	tab.state = "ready"
	tab.err = ""
	a.remoteTabMu.Unlock()
	a.emitRemoteEvent(fmt.Sprintf("remote-tab:%s:state", tabID), RemoteTabStateView{State: "ready"})
	tab.routeEventMu.Unlock()
	a.applyPendingRemoteTabOpenSelection(tabID)
	return true
}

func (a *App) remoteTabGenerationCurrent(tabID string, gen uint64) bool {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	tab := a.remoteTabs[tabID]
	return tab != nil && tab.gen == gen
}

// remoteTabPump forwards Serve events for one tab generation. Cancellation,
// stream death, or a generation mismatch retires the pump.
func (a *App) remoteTabPump(ctx context.Context, tabID string, gen uint64, opened chan<- error) {
	signalOpened := func(err error) {
		if opened == nil {
			return
		}
		select {
		case opened <- err:
		default:
		}
		opened = nil
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	var client *http.Client
	var base string
	if tab != nil && tab.gen == gen {
		client, base = tab.client, tab.base
	}
	a.remoteTabMu.Unlock()
	if client == nil || base == "" {
		if opened != nil {
			opened <- fmt.Errorf("remote tab %q event stream was retired before opening", tabID)
		}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serveURL(base, "/events?all=1"), nil)
	if err != nil {
		signalOpened(err)
		a.emitRemoteTabStateForGeneration(tabID, gen, "error", err.Error())
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		signalOpened(err)
		if ctx.Err() == nil {
			log.Printf("[remote] remoteTabPump: /events DO-FAILED tab=%s err=%v", tabID, err)
			a.emitRemoteTabStateForGeneration(tabID, gen, "error", err.Error())
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("serve /events: status %d", resp.StatusCode)
		signalOpened(err)
		log.Printf("[remote] remoteTabPump: /events BAD-STATUS tab=%s status=%d", tabID, resp.StatusCode)
		a.emitRemoteTabStateForGeneration(tabID, gen, "error", err.Error())
		return
	}
	signalOpened(nil)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), serveEventMaxBytes)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			continue // ": ping" keepalives and other SSE fields
		}
		frame := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if frame == "" {
			continue
		}
		if !a.remoteTabGenerationCurrent(tabID, gen) {
			return
		}
		kind, framePath, current, reset := probeRemoteTabFrame(frame)
		if kind == "runtime_state" {
			a.acceptRemoteRuntimeFrame(tabID, gen, framePath, json.RawMessage(frame))
			continue
		}
		// A takeover notice for the session this tab is viewing flips the
		// spectator pin live: the entry-time probe only runs when the tab
		// enters a session, so a mid-view takeover (or its reversal) would
		// otherwise leave the banner and the composer locked to stale state.
		if kind == "notice" && framePath != "" &&
			(strings.Contains(frame, event.NoticeCodeSessionTakenOver) ||
				strings.Contains(frame, event.NoticeCodeSessionReclaimed)) {
			a.goRemoteTabSafe("remoteTabTakeoverNoticeProbe", func() {
				a.probeSpectatorAfterNotice(tabID, gen, client, base, framePath)
			})
		}
		if !a.routeRemoteTabWireFrame(tabID, gen, framePath, kind, current, reset) {
			continue
		}
		if a.bufferRemoteTabResumeFrame(tabID, gen, framePath, kind, json.RawMessage(frame)) {
			continue
		}
		a.publishRemoteTabFrame(tabID, gen, framePath, kind, json.RawMessage(frame))
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[remote] remoteTabPump: READ-EXIT tab=%s gen=%d err=%v ctxErr=%v", tabID, gen, err, ctx.Err())
	}
	// Only the current generation reacts to an unexpected stream death.
	// Reattach now; the host status hook also retries on connection recovery.
	if ctx.Err() == nil {
		if startRetry := a.reconnectRemoteTabGeneration(tabID, gen); startRetry {
			log.Printf("[remote] remoteTabPump: DIED tab=%s gen=%d — reattaching", tabID, gen)
			a.goRemoteTabSafe("remoteTabReattach", func() { a.reattachRemoteTab(tabID) })
		}
	}
}

func (a *App) completeRemoteTabTurn(tabID string, gen uint64) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	if tab == nil || tab.gen != gen {
		a.remoteTabMu.Unlock()
		return
	}
	tab.runtime.revision++
	tab.pendingEvents = nil
	tab.runtime.running = false
	tab.runtime.turnStartedAt = 0
	tab.runtime.pendingPrompt = false
	tab.runtime.cancelRequested = false
	tab.runtime.cancellable = tab.runtime.backgroundJobs > 0
	// A completed turn makes the fresh session non-blank even when the
	// best-effort /sessions title lookup fails. New Topic must never reuse
	// a conversation that already has a completed turn.
	tab.session.reset = false
	meta := remoteTabMetaLocked(tab)
	a.remoteTabMu.Unlock()
	a.emitRemoteEvent("remote-tab:updated", meta)
}

func (a *App) recordRemoteTabTurnStarted(tabID string, gen uint64, frame json.RawMessage) {
	var payload struct {
		TurnStartedAt int64 `json:"turnStartedAt"`
	}
	_ = json.Unmarshal(frame, &payload)
	if payload.TurnStartedAt <= 0 {
		payload.TurnStartedAt = time.Now().UnixMilli()
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	if tab == nil || tab.gen != gen {
		a.remoteTabMu.Unlock()
		return
	}
	tab.runtime.revision++
	tab.runtime.running = true
	tab.runtime.turnStartedAt = payload.TurnStartedAt
	tab.runtime.pendingPrompt = false
	tab.runtime.cancelRequested = false
	tab.runtime.cancellable = true
	meta := remoteTabMetaLocked(tab)
	a.remoteTabMu.Unlock()
	a.emitRemoteEvent("remote-tab:updated", meta)
}

func (a *App) cacheRemotePendingEvent(tabID string, gen uint64, kind string, frame json.RawMessage) {
	key := remotePendingEventKey(kind, frame)
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	if tab == nil || tab.gen != gen {
		a.remoteTabMu.Unlock()
		return
	}
	tab.runtime.revision++
	if tab.pendingEvents == nil {
		tab.pendingEvents = make(map[string]json.RawMessage)
	}
	tab.pendingEvents[key] = append(json.RawMessage(nil), frame...)
	tab.runtime.pendingPrompt = true
	tab.runtime.cancellable = true
	meta := remoteTabMetaLocked(tab)
	a.remoteTabMu.Unlock()
	a.emitRemoteEvent("remote-tab:updated", meta)
}

func (a *App) clearRemotePendingEvent(tabID, kind, callID string) {
	a.remoteTabMu.Lock()
	var meta TabMeta
	changed := false
	if tab := a.remoteTabs[tabID]; tab != nil {
		tab.runtime.revision++
		delete(tab.pendingEvents, kind+":"+strings.TrimSpace(callID))
		pending := len(tab.pendingEvents) > 0
		changed = tab.runtime.pendingPrompt != pending
		tab.runtime.pendingPrompt = pending
		meta = remoteTabMetaLocked(tab)
	}
	a.remoteTabMu.Unlock()
	if changed {
		a.emitRemoteEvent("remote-tab:updated", meta)
	}
}

// serveGet fetches a JSON member of the tab snapshot, returning the raw
// payload for verbatim passthrough.
func serveGet(ctx context.Context, client *http.Client, url string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, serveSnapshotMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > serveSnapshotMaxBytes {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", url, serveSnapshotMaxBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	return json.RawMessage(data), nil
}

// commandContext bounds one proxied command. Boot context when available;
// the timeout keeps a wedged tunnel from hanging the binding call.
func commandContext(a *App) (context.Context, context.CancelFunc) {
	ctx := a.bootContext()
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, 15*time.Second)
}

// remoteTabCommandClient resolves a tabID to its live serve client. A tab
// that has not finished bootstrap, is reconnecting, or has failed is an
// error, not a silent no-op.
func (a *App) remoteTabCommandClient(tabID string) (*http.Client, string, error) {
	client, base, _, err := a.remoteTabCommandTarget(tabID)
	return client, base, err
}

func (a *App) remoteTabCommandTarget(tabID string) (*http.Client, string, string, error) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	var client *http.Client
	var base, expectedPath string
	usable := tab != nil && tab.client != nil && tab.state == "ready"
	if usable {
		client, base = tab.client, tab.base
		expectedPath = tab.routing.currentPath
	}
	a.remoteTabMu.Unlock()
	if !usable {
		return nil, "", "", fmt.Errorf("remote tab %q is not connected", tabID)
	}
	return client, base, expectedPath, nil
}

func (a *App) isRemoteTab(tabID string) bool {
	if strings.TrimSpace(tabID) == "" {
		return false
	}
	a.remoteTabMu.Lock()
	_, ok := a.remoteTabs[tabID]
	a.remoteTabMu.Unlock()
	return ok
}

// remoteTabRefFor returns the host+workspace ref when tabID belongs to a
// remote tab; view builders use it to mark remote-shaped metas.
func (a *App) remoteTabRefFor(tabID string) (RemoteTabRef, bool) {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	if tab := a.remoteTabs[tabID]; tab != nil {
		return tab.ref, true
	}
	return RemoteTabRef{}, false
}

func (a *App) remoteTabCurrentModel(tabID string) (string, bool) {
	if !a.isRemoteTab(tabID) {
		return "", false
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	cur := ""
	if tab != nil {
		cur = tab.model
	}
	a.remoteTabMu.Unlock()
	return cur, true
}

// ReclaimRemoteTabSession takes a mirrored session back from the local
// runtime that took it over. Serve long-polls until the local writer yields,
// so this call can outlast a normal command timeout.
func (a *App) ReclaimRemoteTabSession(tabID string) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expectedPath) == "" {
		return fmt.Errorf("remote tab %q has no active session", tabID)
	}
	a.remoteTabMu.Lock()
	observedTab := a.remoteTabs[tabID]
	observedGen, observedRuntimeRevision, observedSelectionRevision := uint64(0), uint64(0), uint64(0)
	if observedTab != nil {
		observedGen = observedTab.gen
		observedRuntimeRevision = observedTab.runtime.revision
		observedSelectionRevision = observedTab.selectionRevision
	}
	a.remoteTabMu.Unlock()
	stillCurrent := func(tab *remoteTab) bool {
		return tab != nil && tab == observedTab && tab.client == client && tab.gen == observedGen &&
			tab.runtime.revision == observedRuntimeRevision && tab.selectionRevision == observedSelectionRevision &&
			agent.CanonicalSessionPath(tab.routing.currentPath) == agent.CanonicalSessionPath(expectedPath)
	}
	reconcileOwnership := func() { a.reconcileRemoteTabReclaimOwnership(tabID, client, base, expectedPath, stillCurrent) }
	// Short timeout: the serve caps un-mirrored reclaims at 10s and mirrored
	// ones use the writer's cooperative heartbeat (seconds, not minutes). A
	// long client-side timeout only hangs the UI button.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{
		"sessionPath": expectedPath,
		"mode":        "wait",
		"timeoutMs":   15000,
	})
	resp, err := serveDo(ctx, client, http.MethodPost, serveURL(base, "/reclaim"), body)
	if err != nil {
		reconcileOwnership()
		return fmt.Errorf("reclaim session: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusNoContent {
		errMsg := strings.TrimSpace(string(respBody))
		// A failed reclaim is not proof that ownership changed. Keep the
		// spectator pin until a fenced ownership probe proves this exact tab,
		// route, runtime, and selection generation is no longer locally owned.
		// This covers generation conflicts and transient 5xx responses without
		// reopening input against an ambiguous writer.
		reconcileOwnership()
		return fmt.Errorf("reclaim session: %s", errMsg)
	}
	// Reclaim succeeded: Serve now owns the session again. Clear the spectator
	// pin immediately so the composer un-locks without waiting for the next
	// status poll to observe takenOver=false.
	a.remoteTabMu.Lock()
	if tab := a.remoteTabs[tabID]; stillCurrent(tab) {
		tab.session.takenOver = false
		meta := remoteTabMetaLocked(tab)
		a.remoteTabMu.Unlock()
		a.emitRemoteEvent("remote-tab:updated", meta)
	} else {
		a.remoteTabMu.Unlock()
	}
	a.goRemoteTabSafe("reclaimStatusRefresh", func() { _, _ = a.RemoteTabStatus(tabID) })
	return nil
}

// remoteSessionTakenOver reports whether a session-entry refusal means the
// session is owned by a local runtime on the serve host. The tab then
// attaches as a read-only spectator instead of dying with the 409. Both
// refusal shapes match: the explicit takeover wording (mirrored session) and
// the plain lease wording ("in use by another Reasonix process" — the holder
// is a local window/CLI whose transcript the file-backed /history serves
// anyway, and whose lease /reclaim can take back).
func remoteSessionTakenOver(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "taken over by a local Reasonix") {
		return true
	}
	return strings.Contains(msg, "in use by another Reasonix process")
}

func (a *App) SubmitRemoteTab(tabID, text string) error {
	return a.SubmitRemoteTabWithSubmission(tabID, text, "")
}

func (a *App) SubmitRemoteTabWithSubmission(tabID, text, submissionID string) error {
	revision, err := a.ensureRemoteModelSettings(tabID)
	if err != nil {
		return err
	}
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	input := map[string]string{"input": text}
	if submissionID != "" {
		input["submissionId"] = submissionID
	}
	body, _ := json.Marshal(input)
	return servePostForSession(ctx, client, serveURL(base, "/submit"), body, expectedPath, revision)
}

func (a *App) CancelRemoteTab(tabID string) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	return servePostForSession(ctx, client, serveURL(base, "/cancel"), nil, expectedPath)
}

// ApproveRemoteTab answers a tool-approval request. Serve takes
// {id, allow, session, persist}; preserve the same once/session/persistent
// scopes exposed by the local approval surface.
func (a *App) ApproveRemoteTab(tabID, callID, decision string) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	decision = strings.ToLower(strings.TrimSpace(decision))
	allow, session, persist := false, false, false
	switch decision {
	case "allow", "once":
		allow = true
	case "session":
		allow, session = true, true
	case "persist", "persistent", "project":
		allow, session, persist = true, true, true
	case "deny":
	default:
		return fmt.Errorf("invalid remote approval decision %q", decision)
	}
	body, _ := json.Marshal(map[string]any{"id": callID, "allow": allow, "session": session, "persist": persist})
	if err := servePostForSession(ctx, client, serveURL(base, "/approve"), body, expectedPath); err != nil {
		return err
	}
	a.clearRemotePendingEvent(tabID, "approval_request", callID)
	return nil
}

// ResolveRemoteTabPlanDecision preserves the three distinct exit_plan_mode
// outcomes that the generic approval boolean cannot represent. Revision text
// travels in the same Serve request so the controller can durably stage it
// before resolving the approval; a tunnel failure can no longer split the
// decision from the requested revision.
func (a *App) ResolveRemoteTabPlanDecision(tabID, callID, action, feedback string) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "start_execution", "revise_plan", "exit_plan":
	default:
		return fmt.Errorf("invalid remote plan decision %q", action)
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"id": callID, "action": action, "feedback": strings.TrimSpace(feedback)})
	if err := servePostForSession(ctx, client, serveURL(base, "/plan-decision"), body, expectedPath); err != nil {
		return err
	}
	a.clearRemotePendingEvent(tabID, "approval_request", callID)
	return nil
}

type RemoteAskAnswer struct {
	QuestionID string   `json:"QuestionID"`
	Selected   []string `json:"Selected"`
}

// AnswerRemoteTab preserves the batch ask id at the top level and sends every
// question's own id/selections in the Serve AskAnswer wire shape.
func (a *App) AnswerRemoteTab(tabID, callID string, answers []RemoteAskAnswer) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	body, _ := json.Marshal(map[string]any{
		"id":      callID,
		"answers": answers,
	})
	if err := servePostForSession(ctx, client, serveURL(base, "/answer"), body, expectedPath); err != nil {
		return err
	}
	a.clearRemotePendingEvent(tabID, "ask_request", callID)
	return nil
}

func (a *App) SubmitRemoteTabExtensionForm(tabID, pluginID, surfaceID string, values map[string]any) error {
	if err := a.remoteTabPost(tabID, "/extension-form", map[string]any{
		"pluginId": pluginID, "surfaceId": surfaceID, "values": values,
	}); err != nil {
		return err
	}
	a.clearRemotePendingExtensionForm(tabID, pluginID, surfaceID)
	return nil
}

// RewindRemoteTab rewinds to a checkpoint. Serve identifies checkpoints by
// TURN index and takes {turn, scope}; the checkpointID string is that turn.
func (a *App) RewindRemoteTab(tabID, checkpointID, scope string) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	turn, convErr := strconv.Atoi(strings.TrimSpace(checkpointID))
	if convErr != nil {
		return fmt.Errorf("invalid checkpoint id %q: want the turn index", checkpointID)
	}
	scope = strings.TrimSpace(scope)
	switch scope {
	case "code", "conversation", "both":
	default:
		return fmt.Errorf("invalid rewind scope %q", scope)
	}
	body, _ := json.Marshal(map[string]any{"turn": turn, "scope": scope})
	return servePostForSession(ctx, client, serveURL(base, "/rewind"), body, expectedPath)
}

func (a *App) SetRemoteTabToolApprovalMode(tabID, mode string) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"mode": mode})
	return servePostForSession(ctx, client, serveURL(base, "/tool-approval-mode"), body, expectedPath)
}

func (a *App) SetRemoteTabComposerProfile(tabID, collaborationMode, toolApprovalMode, goal string) ([]string, error) {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"collaborationMode": collaborationMode,
		"toolApprovalMode":  toolApprovalMode,
		"goal":              goal,
	})
	ctx, cancel := commandContext(a)
	defer cancel()
	resp, err := serveDoForSession(ctx, client, http.MethodPost, serveURL(base, "/composer-profile"), body, expectedPath)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if message := strings.TrimSpace(string(data)); message != "" {
			return nil, fmt.Errorf("%s: status %d: %s", serveURL(base, "/composer-profile"), resp.StatusCode, message)
		}
		return nil, fmt.Errorf("%s: status %d", serveURL(base, "/composer-profile"), resp.StatusCode)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return []string{}, nil
	}
	var result struct {
		DrainedApprovalIDs []string `json:"drainedApprovalIDs"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode remote composer profile response: %w", err)
	}
	return result.DrainedApprovalIDs, nil
}

func (a *App) SetRemoteTabGoal(tabID, goal string) error {
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"goal": goal})
	return servePostForSession(ctx, client, serveURL(base, "/goal"), body, expectedPath)
}

func (a *App) SetRemoteTabQualityFloor(tabID, floor string) error {
	return a.remoteTabPost(tabID, "/quality-floor", map[string]any{"floor": floor})
}

// RemoteTabSnapshot mirrors the frontend shape: raw serve payloads passed
// through verbatim so the surface decides how to consume them.
