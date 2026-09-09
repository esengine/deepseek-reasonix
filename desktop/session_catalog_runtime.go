package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/sessioncatalog"
)

// Sidebar reads are bound so a starved connection pool or a slow projection
// degrades into a stale page the next revision repairs, instead of a Wails call
// that never returns and a tree that never moves again.
const sessionCatalogReadTimeout = 10 * time.Second

func (a *App) catalogReadContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.bootContext(), sessionCatalogReadTimeout)
}

type catalogRuntimeSnapshot struct {
	scope            string
	workspaceRoot    string
	topicID          string
	sessionPath      string
	activity         string
	topicTitle       string
	topicTitleSource string
	ctrl             control.SessionAPI
	state            *event.RuntimeStateSnapshot
	open             bool
}

type catalogRuntimeOverlay struct {
	open    bool
	running bool
	status  string
}

func catalogRuntimeStatus(activity string, runtimeStatus control.RuntimeStatus) string {
	status := normalizeTopicStatus(activity)
	if runtimeStatus.PendingPrompt {
		return topicStatusWaitingConfirmation
	}
	if runtimeStatus.Running {
		if status == "" || status == topicStatusError || status == topicStatusPaused || status == topicStatusAwaitingDelivery {
			return topicStatusThinking
		}
		return status
	}
	if runtimeStatus.BackgroundJobs > 0 {
		return topicStatusBackgroundJob
	}
	if status == topicStatusError || status == topicStatusPaused || status == topicStatusAwaitingDelivery {
		return status
	}
	return status
}

func (a *App) catalogRuntimeOverlays() (map[string]catalogRuntimeOverlay, map[string]catalogRuntimeOverlay) {
	topics := map[string]catalogRuntimeOverlay{}
	sessions := map[string]catalogRuntimeOverlay{}
	for _, snap := range a.catalogRuntimeSnapshots() {
		path := strings.TrimSpace(snap.sessionPath)
		if snap.ctrl != nil {
			if path == "" {
				path = snap.ctrl.SessionPath()
			}
		}
		status, running := catalogControllerStatus(snap.ctrl, snap.activity)
		overlay := catalogRuntimeOverlay{open: snap.open, running: running, status: status}
		key := topicSummaryKey(snap.scope, snap.workspaceRoot, snap.topicID)
		current := topics[key]
		current.open = current.open || overlay.open
		current.running = current.running || overlay.running
		if current.status == "" {
			current.status = overlay.status
		}
		topics[key] = current
		if path != "" {
			sessions[sessionRuntimeKey(path)] = overlay
		}
	}
	return topics, sessions
}

func (a *App) metadataProjectTopics(scope, workspaceRoot string) []ProjectNode {
	f := loadProjectsFile()
	deleted := map[string]bool{}
	for _, topicID := range f.DeletedTopics {
		deleted[topicID] = true
	}
	ids := f.GlobalTopics
	pinnedIDs := f.GlobalPinnedTopics
	manualOrder := f.GlobalManualTopicOrder
	titleRoot := ""
	projectColor := normalizeProjectColor(f.GlobalColor)
	if scope == "project" {
		ids = nil
		pinnedIDs = nil
		manualOrder = false
		titleRoot = workspaceRoot
		for _, project := range f.Projects {
			if sameProjectRoot(project.Root, workspaceRoot) {
				ids = project.Topics
				pinnedIDs = project.PinnedTopics
				manualOrder = project.ManualTopicOrder
				projectColor = project.Color
				break
			}
		}
	}
	titles := loadTopicTitles(titleRoot)
	sources := loadTopicTitleSources(titleRoot)
	created := loadTopicCreatedAts(titleRoot)
	topicOverlays, _ := a.catalogRuntimeOverlays()
	runtimeNodes := a.runtimeOnlyProjectTopics(scope, workspaceRoot)
	runtimeByTopic := map[string]ProjectNode{}
	for _, node := range runtimeNodes {
		runtimeByTopic[node.TopicID] = node
	}
	out := []ProjectNode{}
	seen := map[string]bool{}
	for sortOrder, topicID := range pinnedTopicIDs(orderedTopicIDs(ids, titles), pinnedIDs) {
		if !manualOrder {
			sortOrder = -1
		}
		if deleted[topicID] {
			continue
		}
		seen[topicID] = true
		title := strings.TrimSpace(titles[topicID])
		if title == "" {
			title = defaultTopicTitle
		}
		kind := "topic"
		if scope != "project" {
			kind = "global_topic"
		}
		overlay := topicOverlays[topicSummaryKey(scope, workspaceRoot, topicID)]
		node := ProjectNode{
			Key: kind + "_" + topicID, Kind: kind,
			Label: a.localizedTopicTitle(title, sources[topicID]), Root: workspaceRoot,
			TopicID: topicID, ProjectColor: projectColor,
			SessionPath: a.catalogSessionPathForTopic(scope, workspaceRoot, topicID),
			CreatedAt: topicCreatedAtForTree(created, topicID), Pinned: containsDesktopString(pinnedIDs, topicID), SortOrder: sortOrder,
			Open: overlay.open, Running: overlay.running, Status: overlay.status,
			TurnsState: string(sessioncatalog.TurnsUnknown), Health: string(sessioncatalog.HealthOK),
			Children: []ProjectNode{},
		}
		if runtimeNode, ok := runtimeByTopic[topicID]; ok {
			node.Open = runtimeNode.Open
			node.Running = runtimeNode.Running
			node.Status = runtimeNode.Status
			node.Children = runtimeNode.Children
		}
		out = append(out, node)
	}
	for _, runtimeNode := range runtimeNodes {
		if seen[runtimeNode.TopicID] || deleted[runtimeNode.TopicID] {
			continue
		}
		runtimeNode.RuntimeOnly = true
		out = append(out, runtimeNode)
	}
	return out
}

func (a *App) runtimeOnlyProjectTopics(scope, workspaceRoot string) []ProjectNode {
	nodes, _ := a.runtimeOnlyProjectTopicsWithSessions(scope, workspaceRoot)
	return nodes
}

// runtimeOnlyProjectTopicsWithSessions also reports each runtime topic's known
// session paths so callers can resolve the topics those sessions project onto
// in the catalog (a restored tab may carry a legacy topic ID for a re-anchored
// recovery lineage).
func (a *App) runtimeOnlyProjectTopicsWithSessions(scope, workspaceRoot string) ([]ProjectNode, map[string][]string) {
	snapshots := []catalogRuntimeSnapshot{}
	for _, snapshot := range a.catalogRuntimeSnapshots() {
		if scope == "project" {
			if snapshot.scope != "project" || !sameProjectRoot(snapshot.workspaceRoot, workspaceRoot) {
				continue
			}
		} else if snapshot.scope == "project" {
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	return a.runtimeProjectTopicNodes(scope, workspaceRoot, snapshots, true)
}

func (a *App) runtimeProjectTopicNodes(scope, workspaceRoot string, snapshots []catalogRuntimeSnapshot, previews bool) ([]ProjectNode, map[string][]string) {
	byTopic := map[string][]catalogRuntimeSnapshot{}
	sessionsByTopic := map[string][]string{}
	for _, snapshot := range snapshots {
		if snapshot.sessionPath == "" && snapshot.ctrl != nil {
			snapshot.sessionPath = snapshot.ctrl.SessionPath()
		}
		byTopic[snapshot.topicID] = append(byTopic[snapshot.topicID], snapshot)
		if path := strings.TrimSpace(snapshot.sessionPath); path != "" {
			sessionsByTopic[snapshot.topicID] = append(sessionsByTopic[snapshot.topicID], path)
		}
	}
	topicIDs := make([]string, 0, len(byTopic))
	for topicID := range byTopic {
		topicIDs = append(topicIDs, topicID)
	}
	sort.Strings(topicIDs)
	out := []ProjectNode{}
	for _, topicID := range topicIDs {
		sessions := byTopic[topicID]
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].sessionPath < sessions[j].sessionPath })
		kind := "topic"
		sessionKind := "session"
		if scope != "project" {
			kind = "global_topic"
			sessionKind = "global_session"
		}
		label := defaultTopicTitle
		if strings.TrimSpace(sessions[0].topicTitle) != "" {
			label = sessions[0].topicTitle
		}
		node := ProjectNode{
			Key: kind + "_" + topicID, Kind: kind, Label: a.localizedTopicTitle(label, sessions[0].topicTitleSource),
			Root: workspaceRoot, TopicID: topicID, TurnsState: string(sessioncatalog.TurnsUnknown),
			Health: string(sessioncatalog.HealthOK), Children: []ProjectNode{},
		}
		for _, session := range sessions {
			status, running := catalogControllerStatus(session.ctrl, session.activity)
			if session.state != nil {
				status, running = catalogStateStatus(*session.state, session.activity)
			}
			if len(sessions) == 1 {
				node.Open = session.open
				node.Running = running
				node.Status = status
				continue
			}
			path := strings.TrimSpace(session.sessionPath)
			sessionLabel := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if sessionLabel == "" || sessionLabel == "." {
				sessionLabel = label
			}
			preview := ""
			if previews {
				preview = sessionPreviewForPath(path)
			}
			node.Children = append(node.Children, ProjectNode{
				Key: projectSessionNodeKey(scope, path), Kind: sessionKind, Label: sessionLabel,
				Root: workspaceRoot, TopicID: topicID, SessionPath: path, Preview: preview,
				Open: session.open, Running: running, Status: status,
				TurnsState: string(sessioncatalog.TurnsUnknown), Health: string(sessioncatalog.HealthOK),
				Children: []ProjectNode{},
			})
		}
		out = append(out, node)
	}
	return out, sessionsByTopic
}

func (a *App) metadataTopicPage(req ProjectTopicPageRequest) ProjectTopicPage {
	items := a.metadataProjectTopics(req.Scope, req.WorkspaceRoot)
	manualOrder := manualTopicOrderFor(req.Scope, req.WorkspaceRoot)
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query != "" {
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Label), query) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	sort.SliceStable(items, func(i, j int) bool {
		return projectTopicLess(items[i], items[j], req.SortMode, manualOrder)
	})
	start := 0
	if lastID, ok := strings.CutPrefix(req.Cursor, "meta:"); ok {
		for index, item := range items {
			if item.TopicID == lastID {
				start = index + 1
				break
			}
		}
	} else if strings.TrimSpace(req.Cursor) != "" {
		start = len(items)
		for index, item := range items {
			var after bool
			var err error
			if manualOrder {
				after, err = sessioncatalog.TopicSortKeyAfterOrderedCursor(
					req.Cursor, item.Pinned, item.SortOrder,
					projectTopicSortValue(item.CreatedAt, item.LastActivityAt, req.SortMode), item.TopicID,
				)
			} else {
				after, err = sessioncatalog.TopicSortKeyAfterCursor(
					req.Cursor, item.Pinned,
					projectTopicSortValue(item.CreatedAt, item.LastActivityAt, req.SortMode), item.TopicID,
				)
			}
			if err != nil {
				start = 0
				break
			}
			if after {
				start = index
				break
			}
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = sessioncatalog.DefaultLimit
	}
	if limit > sessioncatalog.MaxLimit {
		limit = sessioncatalog.MaxLimit
	}
	end := min(start+limit, len(items))
	page := ProjectTopicPage{Items: append([]ProjectNode(nil), items[start:end]...)}
	if end < len(items) && end > start {
		page.NextCursor = "meta:" + items[end-1].TopicID
	}
	return page
}

func (a *App) projectNodeFromCatalogTopic(topic sessioncatalog.TopicRecord, topicOverlays, sessionOverlays map[string]catalogRuntimeOverlay, preferred map[string]struct{}) (ProjectNode, bool) {
	kind := "topic"
	if topic.Scope == "global" {
		kind = "global_topic"
	}
	recoveryOnly := topic.RecoveryState == "recovery_only" && recoveryOnlyHasContent(topic.Sessions)
	canonicalRecovery := topic.RecoveryState == "adopted" || topic.RecoveryState == "preferred"
	recoveryState := ""
	recoveryBranchCount := 0
	recoveryUnresolvedCount := 0
	recoveryCleanupEligibleCount := 0
	if recoveryOnly {
		recoveryState = topic.RecoveryState
		recoveryBranchCount = topic.RecoveryBranchCount
		recoveryUnresolvedCount = topic.RecoveryUnresolvedCount
		recoveryCleanupEligibleCount = topic.RecoveryCleanupEligibleCount
	} else if canonicalRecovery {
		recoveryState = topic.RecoveryState
	}
	overlay := topicOverlays[topicSummaryKey(topic.Scope, topic.WorkspaceRoot, topic.TopicID)]
	node := ProjectNode{
		Key: kind + "_" + topic.TopicID, Kind: kind, Label: a.localizedTopicTitle(topic.Title, topic.TitleSource),
		Root: topic.WorkspaceRoot, TopicID: topic.TopicID, Turns: topic.Turns,
		// RepresentativePath is the topic's automatic open target (the
		// canonical main transcript). Surfacing it here lets session-scoped
		// context-menu actions — e.g. "merge recovery copies" — work on
		// topics that were never opened in this window.
		SessionPath: topic.RepresentativePath,
		Preview:    topicSessionPreview(topic.Sessions, topic.RepresentativePath),
		TurnsState: string(topic.TurnsState), Health: string(topic.Health),
		CreatedAt: topic.CreatedAt, LastActivityAt: topic.LastActivityAt,
		Pinned: topic.Pinned, SortOrder: topic.SortOrder,
		Recovered: recoveryOnly || canonicalRecovery, RecoveryState: recoveryState,
		RecoveryBranchCount: recoveryBranchCount, RecoveryUnresolvedCount: recoveryUnresolvedCount,
		RecoveryCleanupEligibleCount: recoveryCleanupEligibleCount,
		Open:                         overlay.open, Running: overlay.running, Status: overlay.status,
		// Ordinary tree is zero-config: never surface recovery counts, badges,
		// or forced-handling status. History "other saved versions" owns that.
		Children: []ProjectNode{},
	}
	// Fall back to topic-local preference when the workspace map is unavailable
	// so multi-fork topics still collapse instead of listing every replica.
	localPreferred := preferred
	if localPreferred == nil {
		localPreferred = sessioncatalog.PreferredOrdinarySessionPaths(topic.Sessions)
	}
	visible := make([]sessioncatalog.SessionRecord, 0, len(topic.Sessions))
	runtimeSessions := make([]runtimeSessionStatus, 0, len(topic.Sessions))
	for _, session := range topic.Sessions {
		sessionOverlay := sessionOverlays[sessionRuntimeKey(session.Path)]
		// Aggregate open/running state from every physical member onto the
		// single logical row — never expand recovery runtimes as children.
		if sessionOverlay.open {
			node.Open = true
		}
		if sessionOverlay.running {
			node.Running = true
			if node.Status == "" {
				node.Status = sessionOverlay.status
			}
		}
		// 1.23 ordinary-list contract: hide idle covered copies and non-
		// preferred conflict forks. Open/running recovery is still not a
		// second row — status is already aggregated above.
		if !sessioncatalog.OrdinaryTreeSession(session, false, false, localPreferred) {
			continue
		}
		visible = append(visible, session)
		runtimeSessions = append(runtimeSessions, runtimeSessionStatus{
			open: sessionOverlay.open, running: sessionOverlay.running,
		})
	}
	summary := topicSummaryFromCatalogTopic(topic, visible)
	if !recoveryOnly && topicHiddenAsRecoveryOnly(summary, topic.Pinned, append(runtimeSessions, runtimeSessionStatus{
		open: overlay.open || node.Open, running: overlay.running || node.Running,
	})) {
		return ProjectNode{Children: []ProjectNode{}}, false
	}
	if a.ordinaryTreeHidesUnindexedBlank(topic) {
		return ProjectNode{Children: []ProjectNode{}}, false
	}
	// After filtering non-preferred recovery forks, a topic may have nothing
	// left. A recovery-only topic still gets one logical row so the user can
	// reach its saved content; the physical copies remain history-only.
	if len(visible) == 0 {
		if recoveryOnly {
			representative := recoveryOnlyRepresentative(topic.Sessions)
			node.Recovered = true
			node.Turns = representative.Turns
			node.Preview = strings.TrimSpace(representative.Preview)
			node.SessionPath = representative.Path
			return node, true
		}
		if topic.Pinned || overlay.open || overlay.running || node.Open || node.Running {
			return node, true
		}
		return ProjectNode{Children: []ProjectNode{}}, false
	}
	// Ordinary list is always one logical row. Multiple normal non-recovery
	// sessions under one topic also collapse: open/running already aggregated.
	// History "other saved versions" is the only place physical forks appear.
	if live := a.liveSessionPathForTopic(topic.Scope, topic.WorkspaceRoot, topic.TopicID); live != "" {
		node.SessionPath = live
	} else if rep := strings.TrimSpace(topic.RepresentativePath); rep != "" {
		node.SessionPath = rep
	} else if path := sessioncatalog.CanonicalSessionPathForTopic(visible, ""); path != "" {
		node.SessionPath = path
	} else if len(visible) == 1 {
		node.SessionPath = visible[0].Path
	}
	return node, true
}

func (a *App) ordinaryTreeHidesUnindexedBlank(topic sessioncatalog.TopicRecord) bool {
	if topic.Pinned || topic.Turns > 0 {
		return false
	}
	if !isDefaultTopicTitle(topic.Title) && strings.TrimSpace(topic.Title) != "" {
		return false
	}
	for _, session := range topic.Sessions {
		if session.Turns > 0 || strings.TrimSpace(session.Preview) != "" {
			return false
		}
	}
	return !topicIndexedInRegistry(topic.Scope, topic.WorkspaceRoot, topic.TopicID)
}

func topicSummaryFromCatalogTopic(topic sessioncatalog.TopicRecord, visible []sessioncatalog.SessionRecord) topicSummary {
	summary := topicSummary{turns: topic.Turns, lastActivityAt: topic.LastActivityAt}
	if len(visible) == 0 {
		// Catalog still has only covered recovery copies for this topic.
		if topic.RecoveryState == "recovery_only" {
			summary.hasRecoveryOnly = true
		}
		return summary
	}
	for _, session := range visible {
		if session.RecoveryCopy {
			summary.hasRecoveryOnly = true
			continue
		}
		if session.Recovered || strings.TrimSpace(session.RecoveryDigest) != "" {
			summary.hasAdoptedRecovery = true
			if session.Turns > summary.adoptedRecoveryTurns {
				summary.adoptedRecoveryTurns = session.Turns
			}
			continue
		}
		summary.hasNormalSession = true
	}
	if topic.RecoveryState == "recovery_only" && !summary.hasNormalSession && !summary.hasAdoptedRecovery {
		summary.hasRecoveryOnly = true
	}
	return summary
}

func (a *App) listProjectTopics(req ProjectTopicPageRequest) (ProjectTopicPage, error) {
	catalog := a.sessionCatalog.Load()
	if catalog == nil {
		return a.metadataTopicPage(req), nil
	}
	availability := a.catalogWorkspaceAvailability(catalog, req.Scope, req.WorkspaceRoot)
	if !availability.usable {
		// A freshly opened catalog cache is live but empty until the first directory
		// scan. Treat that the same as "catalog unavailable" so upgrade does
		// not blank the sidebar that desktop-projects.json still knows about.
		page := a.metadataTopicPage(req)
		page = availability.decorate(page, catalog.Status().Revision)
		return a.withLiveTopics(catalog, req, page), nil
	}
	page, err := a.catalogTopicPage(catalog, req)
	if err != nil {
		return page, err
	}
	// Metadata is a continuity source while some directories are pending or
	// degraded. Once every target has completed, the catalog is authoritative:
	// retaining metadata-only shells would resurrect recovery copies or deleted
	// sessions that the completed scan deliberately folded/removed.
	if !availability.complete {
		page = a.mergeMetadataTopics(req, page)
	}
	page = availability.decorate(page, max(page.Revision, catalog.Status().Revision))
	return a.withLiveTopics(catalog, req, page), nil
}

func normalizeDesktopTopicScope(scope, workspaceRoot string) (string, string) {
	if strings.TrimSpace(scope) != "project" {
		return "global", ""
	}
	return "project", strings.TrimSpace(workspaceRoot)
}

// withLiveTopics restores topics the catalog does not (yet) carry. A tab is
// authoritative for its own existence, while the catalog is a projection that
// can lag a fresh session, fall behind a stalled writer, or run degraded — and
// the sidebar must never hide a conversation this app is running. Only an
// uncursored page merges, so keyset pagination past it stays the catalog's.
func (a *App) withLiveTopics(catalog *sessioncatalog.Catalog, req ProjectTopicPageRequest, page ProjectTopicPage) ProjectTopicPage {
	if strings.TrimSpace(req.Cursor) != "" {
		return page
	}
	indexed := make(map[string]bool, len(page.Items))
	for _, item := range page.Items {
		indexed[item.TopicID] = true
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	live := []ProjectNode{}
	runtimeNodes, sessionsByTopic := a.runtimeOnlyProjectTopicsWithSessions(req.Scope, req.WorkspaceRoot)
	ctx, cancel := a.catalogReadContext()
	defer cancel()
	for _, node := range runtimeNodes {
		if indexed[node.TopicID] {
			continue
		}
		// A restored tab may still carry a legacy topic ID for a recovery
		// session the catalog re-anchored onto the root logical topic. That
		// logical row already represents the conversation, so a second
		// runtime-only row would break the one-row ordinary-list contract.
		if liveTopicProjectedOnPage(ctx, catalog, sessionsByTopic[node.TopicID], indexed) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(node.Label), query) {
			continue
		}
		live = append(live, node)
	}
	if len(live) == 0 {
		return page
	}
	f := loadProjectsFile()
	deleted := map[string]bool{}
	for _, topicID := range f.DeletedTopics {
		deleted[topicID] = true
	}
	created := loadTopicCreatedAts(topicTitleRoot(req.Scope, req.WorkspaceRoot))
	kept := page.Items[:0:0]
	for _, node := range live {
		if deleted[node.TopicID] {
			continue
		}
		node.RuntimeOnly = true
		node.CreatedAt = topicCreatedAtForTree(created, node.TopicID)
		node.LastActivityAt = node.CreatedAt
		kept = append(kept, node)
	}
	page.Items = append(kept, page.Items...)
	return page
}

// liveTopicProjectedOnPage reports whether every catalog-known session of a
// runtime-only topic already projects onto a topic on this page. Any session
// the catalog has not indexed yet keeps the live row (that is the lag case
// withLiveTopics exists for), and an off-page projection also keeps it so an
// open conversation never disappears from the first page.
func liveTopicProjectedOnPage(ctx context.Context, catalog *sessioncatalog.Catalog, paths []string, indexed map[string]bool) bool {
	if catalog == nil || len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		record, ok, err := catalog.GetSession(ctx, path)
		if err != nil || !ok {
			return false
		}
		if !indexed[record.TopicID] {
			return false
		}
	}
	return true
}

func (a *App) catalogTopicPage(catalog *sessioncatalog.Catalog, req ProjectTopicPageRequest) (ProjectTopicPage, error) {
	out := ProjectTopicPage{Items: []ProjectNode{}}
	manualOrder := manualTopicOrderFor(req.Scope, req.WorkspaceRoot)
	limit := req.Limit
	if limit <= 0 {
		limit = sessioncatalog.DefaultLimit
	}
	if limit > sessioncatalog.MaxLimit {
		limit = sessioncatalog.MaxLimit
	}
	topicOverlays, sessionOverlays := a.catalogRuntimeOverlays()
	ctx, cancel := a.catalogReadContext()
	defer cancel()
	// Workspace-wide preference collapses cross-topic recovery replicas that
	// share a lineage but were indexed as separate topic rows.
	preferred, prefErr := catalog.PreferredOrdinarySessionPaths(ctx, req.Scope, req.WorkspaceRoot)
	if prefErr != nil {
		preferred = nil
	}
	cursor := req.Cursor
	// Keep scanning past pages that are entirely idle recovery copies so the
	// sidebar never shows an empty "no sessions" state when later pages still
	// have ordinary topics.
	for {
		page, err := catalog.ListTopics(ctx, sessioncatalog.TopicPageRequest{
			Scope: req.Scope, WorkspaceRoot: req.WorkspaceRoot, Cursor: cursor,
			Limit: limit, Query: req.Query, TimeFilter: req.TimeFilter, SortMode: req.SortMode,
			ManualOrder: manualOrder,
		})
		if err != nil {
			return out, err
		}
		out.Revision = page.Revision
		for i, topic := range page.Items {
			node, ok := a.projectNodeFromCatalogTopic(topic, topicOverlays, sessionOverlays, preferred)
			if !ok {
				continue
			}
			out.Items = append(out.Items, node)
			if len(out.Items) == limit {
				if i+1 < len(page.Items) || page.NextCursor != "" {
					out.NextCursor = encodeProjectTopicCursor(topic, req.SortMode, manualOrder)
				}
				return out, nil
			}
		}
		if page.NextCursor == "" {
			out.NextCursor = ""
			return out, nil
		}
		cursor = page.NextCursor
	}
}

func projectTopicSortValue(createdAt, lastActivityAt int64, sortMode string) int64 {
	if strings.TrimSpace(sortMode) == "created" {
		if createdAt > 0 {
			return createdAt
		}
		return lastActivityAt
	}
	if lastActivityAt > 0 {
		return lastActivityAt
	}
	return createdAt
}

func (a *App) GetTopicSummary(key ProjectTopicKey) (ProjectNode, error) {
	if catalog := a.sessionCatalog.Load(); catalog != nil {
		ctx, cancel := a.catalogReadContext()
		defer cancel()
		topic, ok, err := catalog.GetTopic(ctx, sessioncatalog.TopicKey{
			Scope: key.Scope, WorkspaceRoot: key.WorkspaceRoot, TopicID: key.TopicID,
		})
		if err != nil {
			return ProjectNode{Children: []ProjectNode{}}, err
		}
		if ok {
			topicOverlays, sessionOverlays := a.catalogRuntimeOverlays()
			preferred, _ := catalog.PreferredOrdinarySessionPaths(ctx, key.Scope, key.WorkspaceRoot)
			if node, visible := a.projectNodeFromCatalogTopic(topic, topicOverlays, sessionOverlays, preferred); visible {
				return node, nil
			}
			return ProjectNode{Children: []ProjectNode{}}, nil
		}
	}
	page, err := a.ListProjectTopics(ProjectTopicPageRequest{
		Scope: key.Scope, WorkspaceRoot: key.WorkspaceRoot, Limit: sessioncatalog.MaxLimit,
	})
	if err != nil {
		return ProjectNode{Children: []ProjectNode{}}, err
	}
	for _, node := range page.Items {
		if node.TopicID == key.TopicID {
			return node, nil
		}
	}
	return ProjectNode{Children: []ProjectNode{}}, nil
}

func (a *App) GetSessionCatalogStatus() SessionCatalogStatus {
	return a.currentSessionCatalogStatus()
}

// ListProjectTree is the one-release compatibility wrapper. It composes only
// catalog pages and project shells; it never migrates, scans, or decodes a
// session synchronously.
func (a *App) ListProjectTree() []ProjectNode {
	snapshot := a.GetProjectTreeSnapshot()
	hasGlobal := false
	for _, project := range snapshot.Projects {
		if project.Kind == "global_folder" {
			hasGlobal = true
			break
		}
	}
	if !hasGlobal && len(a.metadataProjectTopics("global", "")) > 0 {
		f := loadProjectsFile()
		label := strings.TrimSpace(f.GlobalTitle)
		if label == "" {
			label = "Global"
		}
		snapshot.Projects = append(snapshot.Projects, ProjectNode{
			Key: "global_folder", Kind: "global_folder", Label: label,
			Root: globalWorkspaceRoot(), ProjectColor: normalizeProjectColor(f.GlobalColor), Children: []ProjectNode{},
		})
		snapshot.Projects = applyPinnedProjectOrder(applyProjectTreeOrder(snapshot.Projects, f.SidebarOrder), f.PinnedProjects)
	}
	for index := range snapshot.Projects {
		project := &snapshot.Projects[index]
		// The lightweight snapshot carries pinned topic shells for collapsed
		// folders. This compatibility wrapper rebuilds the complete child list,
		// so start clean to avoid duplicating those shells with catalog rows.
		project.Children = []ProjectNode{}
		if project.Remote != nil {
			continue
		}
		scope := "project"
		root := project.Root
		if project.Kind == "global_folder" {
			scope = "global"
			root = ""
		}
		cursor := ""
		for {
			page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Cursor: cursor, Limit: sessioncatalog.MaxLimit})
			if err != nil {
				break
			}
			project.Children = append(project.Children, page.Items...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(project.Children) == 0 {
			project.Children = a.metadataProjectTopics(scope, root)
		}
	}
	return snapshot.Projects
}

func (a *App) catalogSessionPathForTopic(scope, workspaceRoot, topicID string) string {
	if strings.TrimSpace(topicID) == "" {
		return ""
	}
	catalog := a.sessionCatalog.Load()
	if catalog == nil {
		return ""
	}
	topic, ok, err := catalog.GetTopic(a.bootContext(), sessioncatalog.TopicKey{Scope: scope, WorkspaceRoot: workspaceRoot, TopicID: topicID})
	if err != nil || !ok || len(topic.Sessions) == 0 {
		return ""
	}
	if representative := strings.TrimSpace(topic.RepresentativePath); representative != "" {
		return representative
	}
	if canonical := sessioncatalog.CanonicalSessionPathForTopic(topic.Sessions, ""); canonical != "" {
		return canonical
	}
	preferred := sessioncatalog.PreferredOrdinarySessionPaths(topic.Sessions)
	sort.SliceStable(topic.Sessions, func(i, j int) bool {
		// Prefer ordinary-tree survivors, then real conversations over copies.
		iPref := sessioncatalog.OrdinaryTreeSession(topic.Sessions[i], false, false, preferred)
		jPref := sessioncatalog.OrdinaryTreeSession(topic.Sessions[j], false, false, preferred)
		if iPref != jPref {
			return iPref
		}
		if topic.Sessions[i].RecoveryCopy != topic.Sessions[j].RecoveryCopy {
			return !topic.Sessions[i].RecoveryCopy
		}
		return topic.Sessions[i].LastActivityAt > topic.Sessions[j].LastActivityAt
	})
	return topic.Sessions[0].Path
}
