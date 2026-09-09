package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxSessionGroups       = 100
	maxSessionGroupIDRunes = 160
	maxSessionGroupRunes   = 120
	maxSessionGroupTopics  = 10_000
)

type desktopProject struct {
	Root             string         `json:"root"`
	Title            string         `json:"title,omitempty"`
	Color            string         `json:"color,omitempty"`
	Topics           []string       `json:"topics"`
	PinnedTopics     []string       `json:"pinnedTopics,omitempty"`
	ManualTopicOrder bool           `json:"manualTopicOrder,omitempty"`
	Groups           []desktopGroup `json:"groups,omitempty"`
	GroupsRevision   uint64         `json:"-"`
}

type desktopGroup struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	TopicIDs []string `json:"topicIds,omitempty"`
}

type ProjectGroupsSnapshot struct {
	Groups   []desktopGroup `json:"groups"`
	Revision uint64         `json:"revision"`
	Applied  bool           `json:"applied"`
}

type desktopProjectFile struct {
	GlobalTitle            string           `json:"globalTitle,omitempty"`
	GlobalColor            string           `json:"globalColor,omitempty"`
	GlobalTopics           []string         `json:"globalTopics,omitempty"`
	GlobalPinnedTopics     []string         `json:"globalPinnedTopics,omitempty"`
	GlobalManualTopicOrder bool             `json:"globalManualTopicOrder,omitempty"`
	GlobalGroups           []desktopGroup   `json:"globalGroups,omitempty"`
	GlobalGroupsRevision   uint64           `json:"-"`
	DeletedTopics          []string         `json:"deletedTopics,omitempty"`
	PinnedProjects         []string         `json:"pinnedProjects,omitempty"`
	SidebarOrder           []string         `json:"sidebarOrder,omitempty"`
	Projects               []desktopProject `json:"projects"`
}

func normalizeGroups(groups []desktopGroup) []desktopGroup {
	out := make([]desktopGroup, 0, len(groups))
	seenGroups := make(map[string]bool, len(groups))
	seenTopics := make(map[string]bool)
	for _, group := range groups {
		group.ID = strings.TrimSpace(group.ID)
		group.Title = strings.TrimSpace(group.Title)
		if group.ID == "" || seenGroups[group.ID] {
			continue
		}
		seenGroups[group.ID] = true
		topics := make([]string, 0, len(group.TopicIDs))
		for _, topicID := range group.TopicIDs {
			topicID = strings.TrimSpace(topicID)
			if topicID == "" || seenTopics[topicID] {
				continue
			}
			seenTopics[topicID] = true
			topics = append(topics, topicID)
		}
		group.TopicIDs = topics
		out = append(out, group)
	}
	return out
}

func mergeDesktopGroups(left, right []desktopGroup) []desktopGroup {
	merged := append(append([]desktopGroup(nil), left...), right...)
	byID := make(map[string]int, len(merged))
	out := make([]desktopGroup, 0, len(merged))
	for _, group := range merged {
		group.ID = strings.TrimSpace(group.ID)
		if group.ID == "" {
			continue
		}
		if index, ok := byID[group.ID]; ok {
			if out[index].Title == "" {
				out[index].Title = strings.TrimSpace(group.Title)
			}
			out[index].TopicIDs = append(out[index].TopicIDs, group.TopicIDs...)
			continue
		}
		byID[group.ID] = len(out)
		out = append(out, group)
	}
	return normalizeGroups(out)
}

func validateSessionGroups(groups []desktopGroup) error {
	if len(groups) > maxSessionGroups {
		return fmt.Errorf("too many session groups: %d", len(groups))
	}
	seenGroups := make(map[string]bool, len(groups))
	seenTopics := make(map[string]bool)
	for _, group := range groups {
		id, title := strings.TrimSpace(group.ID), strings.TrimSpace(group.Title)
		if id == "" || title == "" {
			return fmt.Errorf("session group id and title are required")
		}
		if utf8.RuneCountInString(id) > maxSessionGroupIDRunes || utf8.RuneCountInString(title) > maxSessionGroupRunes {
			return fmt.Errorf("session group id or title is too long")
		}
		if seenGroups[id] {
			return fmt.Errorf("duplicate session group %q", id)
		}
		seenGroups[id] = true
		if len(group.TopicIDs) > maxSessionGroupTopics {
			return fmt.Errorf("session group %q has too many topics", id)
		}
		for _, topicID := range group.TopicIDs {
			topicID = strings.TrimSpace(topicID)
			if topicID == "" || seenTopics[topicID] {
				return fmt.Errorf("invalid or duplicate grouped topic %q", topicID)
			}
			seenTopics[topicID] = true
		}
	}
	return nil
}

func completeTopicOrder(orderedTopicIDs, previous []string) ([]string, error) {
	available := make(map[string]bool, len(previous))
	for _, id := range previous {
		available[id] = true
	}
	seen := make(map[string]bool, len(orderedTopicIDs))
	next := make([]string, 0, len(previous))
	for _, id := range orderedTopicIDs {
		id = strings.TrimSpace(id)
		if id == "" || !available[id] {
			return nil, fmt.Errorf("unknown topic %q", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate topic %q", id)
		}
		seen[id] = true
		next = append(next, id)
	}
	for _, id := range previous {
		if !seen[id] {
			next = append(next, id)
		}
	}
	return next, nil
}

func normalizeOrganizationTarget(scope, workspaceRoot string) (string, string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "global" {
		return scope, "", nil
	}
	if scope != "project" {
		return "", "", fmt.Errorf("unsupported scope %q", scope)
	}
	workspaceRoot = normalizeProjectRoot(workspaceRoot)
	if workspaceRoot == "" {
		return "", "", fmt.Errorf("workspaceRoot is required for project scope")
	}
	return scope, workspaceRoot, nil
}

// ReorderTopics persists a manual order without dropping topics omitted by a
// partially loaded client. The manual flag preserves activity sorting for
// existing users until they explicitly drag a topic.
func (a *App) ReorderTopics(scope, workspaceRoot string, orderedTopicIDs []string) error {
	scope, workspaceRoot, err := normalizeOrganizationTarget(scope, workspaceRoot)
	if err != nil {
		return err
	}
	if len(orderedTopicIDs) == 0 {
		return fmt.Errorf("orderedTopicIDs is required")
	}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		if scope == "global" {
			next, orderErr := completeTopicOrder(orderedTopicIDs, f.GlobalTopics)
			if orderErr != nil {
				return false, orderErr
			}
			changed := !sameStringList(next, f.GlobalTopics) || !f.GlobalManualTopicOrder
			f.GlobalTopics, f.GlobalManualTopicOrder = next, true
			return changed, nil
		}
		i := projectIndexByRoot(f.Projects, workspaceRoot)
		if i < 0 {
			return false, fmt.Errorf("project %q not found", workspaceRoot)
		}
		next, orderErr := completeTopicOrder(orderedTopicIDs, f.Projects[i].Topics)
		if orderErr != nil {
			return false, orderErr
		}
		changed := !sameStringList(next, f.Projects[i].Topics) || !f.Projects[i].ManualTopicOrder
		f.Projects[i].Topics, f.Projects[i].ManualTopicOrder = next, true
		return changed, nil
	}); err != nil {
		return err
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}

func (a *App) ListProjectGroups(scope, workspaceRoot string) ([]desktopGroup, error) {
	scope, workspaceRoot, err := normalizeOrganizationTarget(scope, workspaceRoot)
	if err != nil {
		return nil, err
	}
	f := loadProjectsFile()
	if scope == "global" {
		return a.filterKnownGroupMembers(nonNilGroups(f.GlobalGroups)), nil
	}
	i := projectIndexByRoot(f.Projects, workspaceRoot)
	if i < 0 {
		return []desktopGroup{}, nil
	}
	return a.filterKnownGroupMembers(nonNilGroups(f.Projects[i].Groups)), nil
}

// filterKnownGroupMembers drops group member ids that no longer resolve to a
// live catalog topic (#9518). The project tree counts persisted topicIds, so
// stale ids (deleted sessions, tombstoned topics) inflate collapsed-group
// counts. The catalog is authoritative and cross-page; when it is not ready —
// or the membership lookup fails — groups are returned untouched, matching the
// pre-filter behavior instead of hiding valid members.
func (a *App) filterKnownGroupMembers(groups []desktopGroup) []desktopGroup {
	var ids []string
	for _, group := range groups {
		ids = append(ids, group.TopicIDs...)
	}
	if len(ids) == 0 {
		return groups
	}
	catalog := a.sessionCatalog.Load()
	if catalog == nil {
		return groups
	}
	ctx, cancel := a.catalogReadContext()
	defer cancel()
	known, err := catalog.KnownTopicIDs(ctx, ids)
	if err != nil {
		return groups
	}
	changed := false
	out := make([]desktopGroup, len(groups))
	for i, group := range groups {
		if len(group.TopicIDs) == 0 {
			out[i] = group
			continue
		}
		filtered := make([]string, 0, len(group.TopicIDs))
		groupChanged := false
		for _, id := range group.TopicIDs {
			if known[id] {
				filtered = append(filtered, id)
			} else {
				groupChanged = true
			}
		}
		if !groupChanged {
			out[i] = group
			continue
		}
		changed = true
		group.TopicIDs = filtered
		out[i] = group
	}
	if !changed {
		return groups
	}
	return out
}

func nonNilGroups(groups []desktopGroup) []desktopGroup {
	groups = normalizeGroups(groups)
	if groups == nil {
		return []desktopGroup{}
	}
	return groups
}

// GetProjectGroups is the versioned organization read used by current
// frontends. ListProjectGroups remains for old binaries during the transition.
func (a *App) GetProjectGroups(scope, workspaceRoot string) (ProjectGroupsSnapshot, error) {
	scope, workspaceRoot, err := normalizeOrganizationTarget(scope, workspaceRoot)
	if err != nil {
		return ProjectGroupsSnapshot{Groups: []desktopGroup{}}, err
	}
	f := loadProjectsFile()
	if scope == "global" {
		return ProjectGroupsSnapshot{Groups: a.filterKnownGroupMembers(nonNilGroups(f.GlobalGroups)), Revision: f.GlobalGroupsRevision, Applied: true}, nil
	}
	i := projectIndexByRoot(f.Projects, workspaceRoot)
	if i < 0 {
		return ProjectGroupsSnapshot{Groups: []desktopGroup{}, Applied: true}, nil
	}
	return ProjectGroupsSnapshot{Groups: a.filterKnownGroupMembers(nonNilGroups(f.Projects[i].Groups)), Revision: f.Projects[i].GroupsRevision, Applied: true}, nil
}

func (a *App) SaveSessionGroups(scope, workspaceRoot string, groups []desktopGroup) error {
	scope, workspaceRoot, err := normalizeOrganizationTarget(scope, workspaceRoot)
	if err != nil {
		return err
	}
	if err := validateSessionGroups(groups); err != nil {
		return err
	}
	normalized := normalizeGroups(groups)
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		deleted := make(map[string]bool, len(f.DeletedTopics))
		for _, topicID := range f.DeletedTopics {
			deleted[topicID] = true
		}
		candidate := groupsWithoutDeletedTopics(normalized, deleted)
		if scope == "global" {
			if equalGroups(candidate, f.GlobalGroups) {
				return false, nil
			}
			f.GlobalGroups = candidate
			f.GlobalGroupsRevision++
			return true, nil
		}
		i := projectIndexByRoot(f.Projects, workspaceRoot)
		if i < 0 {
			return false, fmt.Errorf("project %q not found", workspaceRoot)
		}
		if equalGroups(candidate, f.Projects[i].Groups) {
			return false, nil
		}
		f.Projects[i].Groups = candidate
		f.Projects[i].GroupsRevision++
		return true, nil
	}); err != nil {
		return err
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}

// SaveSessionGroupsVersioned is a compare-and-swap over one workspace. It
// prevents two windows (and archive cleanup) from overwriting each other's
// full group snapshots. On conflict the current state is returned so the
// frontend can reapply its semantic mutation and retry.
func (a *App) SaveSessionGroupsVersioned(scope, workspaceRoot string, expectedRevision uint64, groups []desktopGroup) (ProjectGroupsSnapshot, error) {
	scope, workspaceRoot, err := normalizeOrganizationTarget(scope, workspaceRoot)
	if err != nil {
		return ProjectGroupsSnapshot{Groups: []desktopGroup{}}, err
	}
	if err := validateSessionGroups(groups); err != nil {
		return ProjectGroupsSnapshot{Groups: []desktopGroup{}}, err
	}
	normalized := normalizeGroups(groups)
	result := ProjectGroupsSnapshot{Groups: []desktopGroup{}}
	changed := false
	err = updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		deleted := make(map[string]bool, len(f.DeletedTopics))
		for _, topicID := range f.DeletedTopics {
			deleted[topicID] = true
		}
		candidate := groupsWithoutDeletedTopics(normalized, deleted)
		currentGroups, currentRevision := f.GlobalGroups, f.GlobalGroupsRevision
		var project *desktopProject
		if scope == "project" {
			i := projectIndexByRoot(f.Projects, workspaceRoot)
			if i < 0 {
				return false, fmt.Errorf("project %q not found", workspaceRoot)
			}
			project = &f.Projects[i]
			currentGroups, currentRevision = project.Groups, project.GroupsRevision
		}
		if currentRevision != expectedRevision {
			result = ProjectGroupsSnapshot{Groups: nonNilGroups(currentGroups), Revision: currentRevision, Applied: false}
			return false, nil
		}
		if equalGroups(candidate, currentGroups) {
			result = ProjectGroupsSnapshot{Groups: nonNilGroups(currentGroups), Revision: currentRevision, Applied: true}
			return false, nil
		}
		currentRevision++
		if project == nil {
			f.GlobalGroups, f.GlobalGroupsRevision = candidate, currentRevision
		} else {
			project.Groups, project.GroupsRevision = candidate, currentRevision
		}
		result = ProjectGroupsSnapshot{Groups: nonNilGroups(candidate), Revision: currentRevision, Applied: true}
		changed = true
		return true, nil
	})
	if err != nil {
		return ProjectGroupsSnapshot{Groups: []desktopGroup{}}, err
	}
	if changed {
		a.emitProjectTreeMetadataChanged()
	}
	return result, nil
}

func equalGroups(left, right []desktopGroup) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ID != right[i].ID || left[i].Title != right[i].Title || !sameStringList(left[i].TopicIDs, right[i].TopicIDs) {
			return false
		}
	}
	return true
}

func groupsWithoutTopic(groups []desktopGroup, topicID string) ([]desktopGroup, bool) {
	next := make([]desktopGroup, len(groups))
	changed := false
	for i, group := range groups {
		next[i] = group
		next[i].TopicIDs = removeString(group.TopicIDs, topicID)
		changed = changed || !sameStringList(next[i].TopicIDs, group.TopicIDs)
	}
	return next, changed
}
