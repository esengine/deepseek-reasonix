package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/internal/agent"
)

func TestCompleteTopicOrderPreservesTopicsMissingFromPartialClient(t *testing.T) {
	got, err := completeTopicOrder([]string{"c", "a"}, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"c", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if _, err := completeTopicOrder([]string{"a", "a"}, []string{"a", "b"}); err == nil {
		t.Fatal("duplicate topic order must fail")
	}
	if _, err := completeTopicOrder([]string{"unknown"}, []string{"a", "b"}); err == nil {
		t.Fatal("unknown topic must not be persisted")
	}
}

func TestReorderTopicsEnablesManualOrderOnlyAfterExplicitDrag(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Project"); err != nil {
		t.Fatal(err)
	}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		i := projectIndexByRoot(f.Projects, root)
		f.Projects[i].Topics = []string{"a", "b", "c"}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	for _, topic := range app.metadataProjectTopics("project", root) {
		if topic.SortOrder != -1 {
			t.Fatalf("preference-free sortOrder = %d, want -1", topic.SortOrder)
		}
	}
	if err := app.ReorderTopics("project", root, []string{"c", "a"}); err != nil {
		t.Fatal(err)
	}
	project := loadProjectsFile().Projects[projectIndexByRoot(loadProjectsFile().Projects, root)]
	if want := []string{"c", "a", "b"}; !reflect.DeepEqual(project.Topics, want) {
		t.Fatalf("topics = %v, want %v", project.Topics, want)
	}
	if !project.ManualTopicOrder {
		t.Fatal("manual topic order flag was not persisted")
	}
	for index, topic := range app.metadataProjectTopics("project", root) {
		if topic.SortOrder != index {
			t.Fatalf("topic %q sortOrder = %d, want %d", topic.TopicID, topic.SortOrder, index)
		}
	}
}

func TestSessionGroupsPersistExclusiveMembership(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Project"); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	groups := []desktopGroup{
		{ID: "one", Title: "One", TopicIDs: []string{"a", "b"}},
		{ID: "two", Title: "Two", TopicIDs: []string{"c"}},
	}
	if err := app.SaveSessionGroups("project", root, groups); err != nil {
		t.Fatal(err)
	}
	got, err := app.ListProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, groups) {
		t.Fatalf("groups = %#v, want %#v", got, groups)
	}
	if err := removeTopicFromProjectsFile("b"); err != nil {
		t.Fatal(err)
	}
	got, err = app.ListProjectGroups("project", root)
	if err != nil || len(got) != 2 || !reflect.DeepEqual(got[0].TopicIDs, []string{"a"}) {
		t.Fatalf("groups after topic deletion = %#v, err=%v", got, err)
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{
		{ID: "one", Title: "One", TopicIDs: []string{"same"}},
		{ID: "two", Title: "Two", TopicIDs: []string{"same"}},
	}); err == nil {
		t.Fatal("a topic cannot belong to multiple groups")
	}
	if _, err := app.ListProjectGroups("other", root); err == nil {
		t.Fatal("unsupported scope must fail")
	}
}

func TestProjectOrganizationSurvivesOlderBuildRoundTrip(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Project"); err != nil {
		t.Fatal(err)
	}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		f.Projects[projectIndexByRoot(f.Projects, root)].Topics = []string{"a", "b", "c"}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	if err := app.ReorderTopics("project", root, []string{"c", "a", "b"}); err != nil {
		t.Fatal(err)
	}
	wantGroups := []desktopGroup{{ID: "important", Title: "Important", TopicIDs: []string{"a", "c"}}}
	if err := app.SaveSessionGroups("project", root, wantGroups); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(desktopConfigDir(), desktopProjectOrganizationFile)); err != nil {
		t.Fatalf("organization sidecar missing: %v", err)
	}

	// Model an older binary's typed decode + save: it preserves the topic list
	// but drops every organization field it does not know.
	projectsPath := filepath.Join(desktopConfigDir(), desktopProjectsFile)
	b, err := os.ReadFile(projectsPath)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	delete(old, "globalManualTopicOrder")
	delete(old, "globalGroups")
	for _, value := range old["projects"].([]any) {
		project := value.(map[string]any)
		delete(project, "manualTopicOrder")
		delete(project, "groups")
	}
	b, err = json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectsPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := loadProjectsFile()
	project := loaded.Projects[projectIndexByRoot(loaded.Projects, root)]
	if !project.ManualTopicOrder || !reflect.DeepEqual(project.Topics, []string{"c", "a", "b"}) {
		t.Fatalf("manual organization after downgrade = flag %v topics %v", project.ManualTopicOrder, project.Topics)
	}
	if !reflect.DeepEqual(project.Groups, wantGroups) {
		t.Fatalf("groups after downgrade = %#v, want %#v", project.Groups, wantGroups)
	}
}

func TestVersionedSessionGroupsRejectStaleFullState(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Project"); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{ID: "one", Title: "One", TopicIDs: []string{"a"}}}); err != nil {
		t.Fatal(err)
	}
	base, err := app.GetProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.SaveSessionGroupsVersioned("project", root, base.Revision, []desktopGroup{{ID: "one", Title: "Renamed", TopicIDs: []string{"a"}}})
	if err != nil || !first.Applied {
		t.Fatalf("first CAS = %#v, err=%v", first, err)
	}
	stale, err := app.SaveSessionGroupsVersioned("project", root, base.Revision, []desktopGroup{
		{ID: "one", Title: "One", TopicIDs: []string{"a"}},
		{ID: "two", Title: "Two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Applied || stale.Revision != first.Revision || len(stale.Groups) != 1 || stale.Groups[0].Title != "Renamed" {
		t.Fatalf("stale CAS = %#v, want current renamed state", stale)
	}
	rebased := append(nonNilGroups(stale.Groups), desktopGroup{ID: "two", Title: "Two"})
	merged, err := app.SaveSessionGroupsVersioned("project", root, stale.Revision, rebased)
	if err != nil || !merged.Applied || len(merged.Groups) != 2 || merged.Groups[0].Title != "Renamed" {
		t.Fatalf("rebased CAS = %#v, err=%v", merged, err)
	}

	beforeArchive, _ := app.GetProjectGroups("project", root)
	if err := removeTopicFromProjectsFile("a"); err != nil {
		t.Fatal(err)
	}
	resurrect, err := app.SaveSessionGroupsVersioned("project", root, beforeArchive.Revision, beforeArchive.Groups)
	if err != nil {
		t.Fatal(err)
	}
	if resurrect.Applied || len(resurrect.Groups[0].TopicIDs) != 0 {
		t.Fatalf("archive removal was overwritten by stale save: %#v", resurrect)
	}
	if err := app.SaveSessionGroups("project", root, beforeArchive.Groups); err != nil {
		t.Fatal(err)
	}
	legacy, _ := app.GetProjectGroups("project", root)
	if len(legacy.Groups[0].TopicIDs) != 0 {
		t.Fatalf("legacy full-state save resurrected archived membership: %#v", legacy)
	}
}

func TestListProjectGroupsMissingProjectReturnsJSONArray(t *testing.T) {
	isolateDesktopUserDirs(t)
	groups, err := NewApp().ListProjectGroups("project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Fatalf("missing project groups JSON = %s, want []", b)
	}
}

func TestGetProjectGroupsFiltersStaleMemberIDs(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Grouped"); err != nil {
		t.Fatal(err)
	}
	sessionDir := desktopSessionDir(root)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(sessionDir, "live.jsonl")
	if err := os.WriteFile(livePath, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.UpdateBranchMeta(livePath, false, func(meta *agent.BranchMeta) error {
		meta.TopicID = "topic-live"
		meta.TopicTitle = "Live Topic"
		meta.WorkspaceRoot = root
		meta.Scope = "project"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	installSessionCatalogForTest(t, app, sessionDir, "project", root)

	groups := []desktopGroup{
		{ID: "g1", Title: "Live", TopicIDs: []string{"topic-live", "topic-gone", "topic-archived"}},
		{ID: "g2", Title: "Empty", TopicIDs: nil},
	}
	if err := app.SaveSessionGroups("project", root, groups); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.GetProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Groups) != 2 {
		t.Fatalf("groups = %d, want 2 (empty groups stay)", len(snapshot.Groups))
	}
	if want := []string{"topic-live"}; !reflect.DeepEqual(snapshot.Groups[0].TopicIDs, want) {
		t.Fatalf("live group members = %v, want %v", snapshot.Groups[0].TopicIDs, want)
	}
	listed, err := app.ListProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"topic-live"}; !reflect.DeepEqual(listed[0].TopicIDs, want) {
		t.Fatalf("listed live group members = %v, want %v", listed[0].TopicIDs, want)
	}
}
