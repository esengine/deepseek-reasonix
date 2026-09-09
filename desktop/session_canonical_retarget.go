package main

import (
	"context"
	"fmt"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/sessioncatalog"
)

func (a *App) resolveOpenTopicSessionPath(scope, workspaceRoot, sessionPath string) (string, string) {
	actualRoot := workspaceRoot
	if scope == "global" {
		actualRoot = globalWorkspaceRoot()
	}
	// Keep a live controller on this path (including paused). Opening a
	// different ordinary session of the same topic must still switch.
	if continued := a.continuePathForOpen(sessionPath); continued != "" {
		if a.sessionHasLiveController(sessionPath) {
			return actualRoot, sessionPath
		}
		sessionPath = continued
	}
	return actualRoot, sessionPath
}

func (a *App) sessionHasLiveController(path string) bool {
	key := sessionRuntimeKey(path)
	if a == nil || key == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tabs := range []map[string]*WorkspaceTab{a.tabs, a.detachedSessions} {
		for _, tab := range tabs {
			if tab != nil && tab.Ctrl != nil && sessionRuntimeKey(tab.currentSessionPath()) == key {
				return true
			}
		}
	}
	return false
}

func (a *App) skipContinuationRebind(tab *WorkspaceTab, target string) bool {
	if tab == nil || tab.Ctrl == nil {
		return false
	}
	next := a.continuePathForOpen(tab.currentSessionPath())
	return next != "" && sessionRuntimeKey(next) == sessionRuntimeKey(target)
}

func (a *App) continuePathForOpen(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	catalog := a.sessionCatalog.Load()
	if catalog == nil {
		return ""
	}
	ctx := context.Background()
	rec, ok, err := catalog.GetSession(ctx, path)
	if err != nil || !ok {
		return a.continuePathForMissingParent(ctx, catalog, path)
	}
	if rec.TopicID == "" {
		return ""
	}
	topic, ok, err := catalog.GetTopic(ctx, sessioncatalog.TopicKey{Scope: rec.Scope, WorkspaceRoot: rec.WorkspaceRoot, TopicID: rec.TopicID})
	if err != nil || !ok {
		return ""
	}
	return sessioncatalog.OrdinaryContinuePath(topic.Sessions, path)
}

func (a *App) continuePathForMissingParent(ctx context.Context, catalog *sessioncatalog.Catalog, path string) string {
	parentID := agent.BranchID(path)
	if parentID == "" {
		return ""
	}
	// desktop-tabs.json may still name a parent that lineage folded off the
	// ordinary row. Look up the topic by the filename id.
	for _, target := range a.sessionCatalogTargets() {
		page, err := catalog.ListTopics(ctx, sessioncatalog.TopicPageRequest{
			Scope: target.Scope, WorkspaceRoot: target.WorkspaceRoot, Limit: sessioncatalog.MaxLimit,
		})
		if err != nil {
			continue
		}
		for _, topic := range page.Items {
			for _, session := range topic.Sessions {
				if session.ParentID == parentID || strings.TrimSpace(session.RecoveryGroupID) == parentID {
					if next := sessioncatalog.OrdinaryContinuePath(topic.Sessions, path); next != "" {
						return next
					}
				}
			}
		}
	}
	return ""
}

func (a *App) resumeSessionPageForTab(tabID, path string, limit int) (HistoryPage, error) {
	return a.resumeSessionForTranscript(tabID, path, limit, true)
}

func (a *App) resumeSessionForTranscript(tabID, path string, limit int, includeHistory bool) (HistoryPage, error) {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab == nil || ctrl == nil {
		return HistoryPage{}, fmt.Errorf("tab is not ready")
	}
	if continued := a.continuePathForOpen(path); continued != "" {
		current := tab.currentSessionPath()
		if !tab.hasActiveRuntimeWork() || sessionRuntimeKey(current) != sessionRuntimeKey(path) {
			path = continued
		}
	}
	sessionPath, _, err := validateSessionPath(controllerSessionDir(ctrl), path)
	if err != nil {
		return HistoryPage{}, err
	}
	loaded, err := loadResumableSession(sessionPath)
	if err != nil {
		return HistoryPage{}, err
	}
	if sessionRuntimeKey(tab.currentSessionPath()) != sessionRuntimeKey(sessionPath) {
		if err := a.rebindTabToLoadedSessionPath(tab, sessionPath, loaded); err != nil {
			return HistoryPage{}, err
		}
	}
	a.setTabReadOnly(tab.ID, false)
	if !includeHistory {
		return HistoryPage{}, nil
	}
	return a.HistoryPageForTab(tab.ID, 0, limit), nil
}

func (a *App) retargetOpenTabsToContinuations() {
	if a == nil {
		return
	}
	type candidate struct {
		tab     *WorkspaceTab
		current string
	}
	a.mu.RLock()
	items := make([]candidate, 0, len(a.tabs)+len(a.detachedSessions))
	collect := func(tab *WorkspaceTab) {
		if tab == nil || tab.hasActiveRuntimeWork() {
			return
		}
		items = append(items, candidate{tab: tab, current: tab.currentSessionPath()})
	}
	for _, tab := range a.tabs {
		collect(tab)
	}
	for _, tab := range a.detachedSessions {
		collect(tab)
	}
	a.mu.RUnlock()
	type pending struct {
		tab  *WorkspaceTab
		next string
	}
	ready := make([]pending, 0, len(items))
	for _, item := range items {
		next := a.continuePathForOpen(item.current)
		if next == "" || sessionRuntimeKey(next) == sessionRuntimeKey(item.current) {
			continue
		}
		ready = append(ready, pending{tab: item.tab, next: next})
	}
	for _, item := range ready {
		if item.tab.hasActiveRuntimeWork() {
			continue
		}
		if item.tab.Ctrl == nil {
			a.mu.Lock()
			if !item.tab.hasActiveRuntimeWork() && (a.tabs[item.tab.ID] == item.tab || a.detachedSessions[sessionRuntimeKey(item.tab.currentSessionPath())] == item.tab) {
				item.tab.SessionPath = item.next
				a.saveTabsLocked()
			}
			a.mu.Unlock()
			continue
		}
		if err := a.rebindTabToSessionPath(item.tab, item.next); err != nil {
			continue
		}
	}
}
