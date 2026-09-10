package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/desktop/internal/browserops"
	"reasonix/internal/browser"
	"reasonix/internal/config"
	"reasonix/internal/extension/rpcwire"
)

// Shell error codes for host/browser.* replies; anything else is transport
// failure and therefore an unknown outcome for a reserved write.
const (
	hostBrowserErrStaleReference = -32010
	hostBrowserErrTakenOver      = -32011
	hostBrowserErrNoGrant        = -32012
)

const hostBrowserReadTimeout = 60 * time.Second

type hostRequester interface {
	Request(ctx context.Context, method string, params any, result any) error
}

// hostBrowserExecutor implements browser.Executor for one desktop tab. Every
// call carries the tab's grant; the shell binds tabs, epochs and document
// tokens to that grant so a revoked or restarted service can never act.
type hostBrowserExecutor struct {
	app     *App
	host    hostRequester
	tabID   string
	grantID string
	// sessionKey overrides the grant's session binding when set; remote
	// broker executors use it because their tabs are not workspace tabs.
	sessionKey string
	granted    atomic.Bool
	revoked    atomic.Bool
	grantMu    sync.Mutex
}

type hostBrowserTab struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Loading   bool   `json:"loading"`
	Temporary bool   `json:"temporary"`
}

func (t hostBrowserTab) tab() browser.Tab {
	return browser.Tab{ID: t.ID, URL: t.URL, Title: t.Title, Loading: t.Loading, Temporary: t.Temporary}
}

func (a *App) browserExecutorForTab(tab *WorkspaceTab) browser.Executor {
	if tab == nil || !a.hostMode() || a.browserControl.off() {
		return nil
	}
	a.browserExecMu.Lock()
	defer a.browserExecMu.Unlock()
	if a.browserExecutors == nil {
		a.browserExecutors = map[string]*hostBrowserExecutor{}
	}
	if exec, ok := a.browserExecutors[tab.ID]; ok {
		return exec
	}
	exec := &hostBrowserExecutor{app: a, host: a.hostShell.server, tabID: tab.ID, grantID: newBrowserGrantID()}
	a.browserExecutors[tab.ID] = exec
	return exec
}

func newBrowserGrantID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "grant-" + hex.EncodeToString(buf)
}

func (a *App) revokeRemoteBrowserHost(hostID string) {
	a.remoteTabMu.Lock()
	var ids []string
	for _, tab := range a.remoteTabs {
		if tab != nil && tab.ref.HostID == hostID {
			ids = append(ids, tab.id)
		}
	}
	a.remoteTabMu.Unlock()
	for _, id := range ids {
		a.forgetRemoteBrowserExecutor(id)
	}
}

// forgetBrowserExecutorLocked drops the tab's executor and revokes its grant
// off the caller's lock; a revoked executor fails closed forever.
func (a *App) forgetBrowserExecutorLocked(tabID string) {
	a.browserExecMu.Lock()
	exec, ok := a.browserExecutors[tabID]
	delete(a.browserExecutors, tabID)
	a.browserExecMu.Unlock()
	if !ok {
		return
	}
	a.revokeBrowserExecutor(exec)
}

func (a *App) revokeBrowserExecutor(exec *hostBrowserExecutor) {
	exec.revoked.Store(true)
	a.goSafe("revokeBrowserGrant", func() {
		exec.grantMu.Lock()
		defer exec.grantMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), rpcHostWindowTimeout)
		defer cancel()
		_ = exec.host.Request(ctx, "host/browser.revoke", map[string]string{"grantId": exec.grantID}, nil)
	})
}

// forgetRemoteBrowserExecutor drops the broker executor of a closed remote
// tab; its grant ID is namespaced with the "remote/" prefix used at creation.
func (a *App) forgetRemoteBrowserExecutor(remoteTabID string) {
	a.forgetBrowserExecutorLocked("remote/" + remoteTabID)
}

func (a *App) browserLedger() (*browserops.Ledger, error) {
	a.browserExecMu.Lock()
	defer a.browserExecMu.Unlock()
	if a.browserOps != nil {
		return a.browserOps, nil
	}
	ledger, err := browserops.Open(filepath.Join(config.MemoryUserDir(), "browser", "operations-v1.json"))
	if err != nil {
		return nil, err
	}
	a.browserOps = ledger
	return ledger, nil
}

func (e *hostBrowserExecutor) Available(context.Context) bool {
	return !e.revoked.Load() && e.app.hostMode()
}

// browserSessionKey is the session identity the grant binds to: the explicit
// override for broker-created executors, else the workspace tab's session.
func (e *hostBrowserExecutor) browserSessionKey() string {
	if e.sessionKey != "" {
		return e.sessionKey
	}
	return e.app.tabSessionKeyForBrowser(e.tabID)
}

func (e *hostBrowserExecutor) ensureGrant(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.revoked.Load() {
		return browser.ErrNoGrant
	}
	if e.granted.Load() {
		return nil
	}
	e.grantMu.Lock()
	defer e.grantMu.Unlock()
	if e.revoked.Load() {
		return browser.ErrNoGrant
	}
	if e.granted.Load() {
		return nil
	}
	params := map[string]string{"grantId": e.grantID, "tabId": e.tabID, "sessionId": e.browserSessionKey()}
	if err := e.host.Request(ctx, "host/browser.grant", params, nil); err != nil {
		return mapHostBrowserError(err)
	}
	if e.revoked.Load() {
		return browser.ErrNoGrant
	}
	e.granted.Store(true)
	return nil
}

func (a *App) tabSessionKeyForBrowser(tabID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if tab, ok := a.tabs[tabID]; ok {
		return tab.SessionPath
	}
	return ""
}

func (e *hostBrowserExecutor) call(ctx context.Context, method string, params map[string]any, result any) error {
	if err := e.ensureGrant(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if params == nil {
		params = map[string]any{}
	}
	params["grantId"] = e.grantID
	ctx, cancel := context.WithTimeout(ctx, hostBrowserReadTimeout)
	defer cancel()
	if err := e.host.Request(ctx, method, params, result); err != nil {
		return mapHostBrowserError(err)
	}
	return nil
}

func mapHostBrowserError(err error) error {
	var resp *rpcwire.ResponseError
	if errors.As(err, &resp) {
		switch resp.Code {
		case hostBrowserErrStaleReference:
			return browser.ErrStaleReference
		case hostBrowserErrTakenOver:
			return browser.ErrTakenOver
		case hostBrowserErrNoGrant:
			return browser.ErrNoGrant
		}
		return fmt.Errorf("browser host: %s", resp.Message)
	}
	return err
}

func (e *hostBrowserExecutor) Tabs(ctx context.Context) ([]browser.Tab, error) {
	var out struct {
		Tabs []hostBrowserTab `json:"tabs"`
	}
	if err := e.call(ctx, "host/browser.tabs.list", nil, &out); err != nil {
		return nil, err
	}
	tabs := make([]browser.Tab, 0, len(out.Tabs))
	for _, t := range out.Tabs {
		tabs = append(tabs, t.tab())
	}
	return tabs, nil
}

func (e *hostBrowserExecutor) Open(ctx context.Context, req browser.OpenRequest) (browser.Tab, error) {
	var out hostBrowserTab
	err := e.write(ctx, req.OperationID, "open", "", req, "host/browser.tabs.open", map[string]any{"url": req.URL, "temporary": req.Temporary}, &out)
	return out.tab(), err
}

func (e *hostBrowserExecutor) Navigate(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	var out hostBrowserTab
	err := e.write(ctx, req.OperationID, "navigate", req.TabID, req, "host/browser.tabs.navigate", map[string]any{"tabId": req.TabID, "url": req.URL, "action": req.Action}, &out)
	return out.tab(), err
}

func (e *hostBrowserExecutor) Close(ctx context.Context, req browser.CloseRequest) error {
	return e.write(ctx, req.OperationID, "close", req.TabID, req, "host/browser.tabs.close", map[string]any{"tabId": req.TabID}, nil)
}

// Every browser write uses the same durable reservation, including history
// operations whose reply may disappear after the browser already navigated.
func (e *hostBrowserExecutor) write(ctx context.Context, id, action, tabID string, request any, method string, params map[string]any, out any) error {
	if err := e.ensureGrant(ctx); err != nil {
		return err
	}
	ledger, err := e.app.browserLedger()
	if err != nil {
		return err
	}
	digest, err := actDigest(request)
	if err != nil {
		return err
	}
	if err := ledger.Reserve(browserops.Operation{ID: id, SessionID: e.browserSessionKey(), Generation: e.grantID, TabID: tabID, Action: action, Digest: digest}); err != nil {
		if errors.Is(err, browserops.ErrDuplicateOperation) {
			return fmt.Errorf("%w: operationId already recorded", browser.ErrUnknownOutcome)
		}
		return err
	}
	err = e.call(ctx, method, params, out)
	if err == nil {
		e.settle(ledger, id, browserops.StateExecuted, "")
		return nil
	}
	if errors.Is(err, browser.ErrNoGrant) || errors.Is(err, browser.ErrTakenOver) || errors.Is(err, browser.ErrStaleReference) {
		e.settle(ledger, id, browserops.StateNotExecuted, err.Error())
		return err
	}
	e.settle(ledger, id, browserops.StateUnknown, err.Error())
	return fmt.Errorf("%w: %s", browser.ErrUnknownOutcome, err.Error())
}

func (e *hostBrowserExecutor) Snapshot(ctx context.Context, req browser.SnapshotRequest) (browser.Snapshot, error) {
	var out struct {
		DocumentToken string `json:"documentToken"`
		URL           string `json:"url"`
		Title         string `json:"title"`
		Tree          string `json:"tree"`
		Refs          int    `json:"refs"`
	}
	err := e.call(ctx, "host/browser.snapshot", map[string]any{"tabId": req.TabID, "selector": req.Selector}, &out)
	return browser.Snapshot{DocumentToken: out.DocumentToken, URL: out.URL, Title: out.Title, Tree: out.Tree, Refs: out.Refs}, err
}

func (e *hostBrowserExecutor) Screenshot(ctx context.Context, req browser.ScreenshotRequest) (browser.Screenshot, error) {
	dir, err := e.captureDir()
	if err != nil {
		return browser.Screenshot{}, err
	}
	var out struct {
		Path   string `json:"path"`
		MIME   string `json:"mime"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	err = e.call(ctx, "host/browser.screenshot", map[string]any{"tabId": req.TabID, "ref": req.Ref, "fullPage": req.FullPage, "directory": dir}, &out)
	return browser.Screenshot{Path: out.Path, MIME: out.MIME, Width: out.Width, Height: out.Height}, err
}

// captureDir is the task-owned scratch directory the shell writes captures
// and downloads into; it lives outside the data home and is per tab.
func (e *hostBrowserExecutor) captureDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "reasonix-browser", e.tabID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func (e *hostBrowserExecutor) Downloads(ctx context.Context, req browser.DownloadsRequest) ([]browser.Download, error) {
	var out struct {
		Downloads []struct {
			ID    string `json:"id"`
			URL   string `json:"url"`
			Path  string `json:"path"`
			State string `json:"state"`
			Bytes int64  `json:"bytes"`
		} `json:"downloads"`
	}
	params := map[string]any{"tabId": req.TabID, "waitForMs": req.WaitFor.Milliseconds()}
	if err := e.call(ctx, "host/browser.downloads", params, &out); err != nil {
		return nil, err
	}
	downloads := make([]browser.Download, 0, len(out.Downloads))
	for _, d := range out.Downloads {
		downloads = append(downloads, browser.Download{ID: d.ID, URL: d.URL, Path: d.Path, State: d.State, Bytes: d.Bytes})
	}
	return downloads, nil
}

// Act reserves the operation in the ledger before the shell touches the
// page and settles it from the receipt. A lost receipt stays unknown and is
// reported as such; the ledger rejects the same operationId forever.
func (e *hostBrowserExecutor) Act(ctx context.Context, req browser.ActRequest) (browser.ActResult, error) {
	ledger, err := e.app.browserLedger()
	if err != nil {
		return browser.ActResult{}, err
	}
	if err := e.ensureGrant(ctx); err != nil {
		return browser.ActResult{}, err
	}
	digest, err := actDigest(req)
	if err != nil {
		return browser.ActResult{}, err
	}
	op := browserops.Operation{
		ID:            req.OperationID,
		SessionID:     e.browserSessionKey(),
		Generation:    e.grantID,
		TabID:         req.TabID,
		DocumentToken: req.DocumentToken,
		Action:        req.Action,
		Digest:        digest,
	}
	if err := ledger.Reserve(op); err != nil {
		if errors.Is(err, browserops.ErrDuplicateOperation) {
			return browser.ActResult{Outcome: browser.OutcomeUnknown}, fmt.Errorf("%w: operationId already recorded", browser.ErrUnknownOutcome)
		}
		return browser.ActResult{}, err
	}
	if req.Action == browser.ActionUpload {
		files, cleanup, err := e.prepareUploadFiles(req.Files)
		if err != nil {
			e.settle(ledger, req.OperationID, browserops.StateNotExecuted, err.Error())
			return browser.ActResult{}, err
		}
		defer cleanup()
		req.Files = files
	}
	var out struct {
		Executed      *bool  `json:"executed"`
		Outcome       string `json:"outcome"`
		Reason        string `json:"reason"`
		DocumentToken string `json:"documentToken"`
	}
	params := map[string]any{
		"operationId": req.OperationID, "tabId": req.TabID, "documentToken": req.DocumentToken,
		"action": req.Action, "ref": req.Ref, "text": req.Text, "keys": req.Keys,
		"options": nonNil(req.Options), "files": nonNil(req.Files), "submit": req.Submit,
		"deltaX": req.DeltaX, "deltaY": req.DeltaY,
	}
	callErr := e.call(ctx, "host/browser.act", params, &out)
	switch {
	case callErr == nil && out.Executed == nil:
		e.settle(ledger, req.OperationID, browserops.StateUnknown, "host returned no execution receipt")
		return browser.ActResult{Outcome: browser.OutcomeUnknown}, browser.ErrUnknownOutcome
	case callErr == nil && out.Outcome == browser.OutcomeUnknown:
		e.settle(ledger, req.OperationID, browserops.StateUnknown, out.Reason)
		return browser.ActResult{Outcome: browser.OutcomeUnknown}, fmt.Errorf("%w: %s", browser.ErrUnknownOutcome, out.Reason)
	case callErr == nil && *out.Executed:
		e.settle(ledger, req.OperationID, browserops.StateExecuted, "")
		return browser.ActResult{Executed: true, Outcome: browser.OutcomeExecuted, DocumentToken: out.DocumentToken}, nil
	case callErr == nil:
		e.settle(ledger, req.OperationID, browserops.StateNotExecuted, out.Reason)
		return browser.ActResult{Executed: false, Outcome: browser.OutcomeNotExecuted, Reason: out.Reason, DocumentToken: out.DocumentToken}, nil
	case errors.Is(callErr, browser.ErrStaleReference), errors.Is(callErr, browser.ErrTakenOver), errors.Is(callErr, browser.ErrNoGrant):
		e.settle(ledger, req.OperationID, browserops.StateNotExecuted, callErr.Error())
		return browser.ActResult{}, callErr
	default:
		e.settle(ledger, req.OperationID, browserops.StateUnknown, callErr.Error())
		return browser.ActResult{Outcome: browser.OutcomeUnknown}, fmt.Errorf("%w: %s", browser.ErrUnknownOutcome, callErr.Error())
	}
}

func (e *hostBrowserExecutor) settle(ledger *browserops.Ledger, id string, state browserops.State, reason string) {
	if err := ledger.Settle(id, state, reason); err != nil {
		slog.Warn("desktop browser: settle operation", "operation", id, "state", state, "err", err)
	}
}

func actDigest(req any) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
