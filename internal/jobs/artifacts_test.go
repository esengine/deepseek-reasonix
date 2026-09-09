package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
)

func TestCompletedJobPersistsOutputAndReleasesMemory(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)

	j := m.StartForSession("session", "bash", "persist", func(_ context.Context, out io.Writer) (string, error) {
		_, _ = io.WriteString(out, strings.Repeat("x", defaultTailBytes+1024))
		return "", nil
	})
	<-j.done

	j.mu.Lock()
	tailLen := len(j.tail)
	result := j.outcome.text
	artifactPath := j.artifactPath
	j.mu.Unlock()

	if tailLen != 0 {
		t.Fatalf("completed artifact-backed job kept %d tail bytes, want 0", tailLen)
	}
	if result != "" {
		t.Fatalf("completed artifact-backed job kept result %q, want empty", result)
	}
	if artifactPath == "" {
		t.Fatal("artifact path should be set")
	}

	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || len(res[0].Output) != defaultTailBytes+1024 {
		t.Fatalf("wait output len = %d, want %d", len(res[0].Output), defaultTailBytes+1024)
	}
}

func TestJobArtifactPreservesOutput(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)
	secret := "sk-real-secret-value-123456"

	j := m.StartForSession("session", "bash", "persist secret", func(_ context.Context, out io.Writer) (string, error) {
		_, _ = io.WriteString(out, "DEEPSEEK_API_KEY="+secret+"\n")
		return "Authorization: Bearer ghp_abcdefghijklmnopqrstuvwxyz", nil
	})
	<-j.done

	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 {
		t.Fatalf("wait result = %+v", res)
	}
	if !strings.Contains(res[0].Output, secret) || !strings.Contains(res[0].Output, "ghp_abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("wait output did not preserve job output:\n%s", res[0].Output)
	}

	data, err := os.ReadFile(filepath.Join(ArtifactDir(sessionPath), j.ID+jobLogExt))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !strings.Contains(string(data), secret) || !strings.Contains(string(data), "ghp_abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("artifact did not preserve job output:\n%s", data)
	}
}

func TestJobArtifactMetadataPreservesLabel(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)
	const secret = "sk-real-secret-value-123456"

	j := m.StartForSession("session", "bash", "echo DEEPSEEK_API_KEY="+secret, func(context.Context, io.Writer) (string, error) {
		return "", nil
	})
	<-j.done

	data, err := os.ReadFile(filepath.Join(ArtifactDir(sessionPath), j.ID+jobMetaExt))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), secret) {
		t.Fatalf("job metadata did not preserve label:\n%s", data)
	}
}

func TestListArtifactViewsVerifiesTerminalArtifactPresence(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	dir := ArtifactDir(sessionPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	type artifactCase struct {
		id       string
		status   Status
		metaOK   bool
		metaErr  string
		artifact string
		legacy   bool
		want     bool
	}
	cases := []artifactCase{
		{id: "task-running", status: Running, metaOK: true, artifact: "file", want: false},
		{id: "task-unknown", status: Status("future"), metaOK: true, artifact: "file", want: false},
		{id: "task-missing", status: Done, metaOK: true, want: false},
		{id: "task-error", status: Done, metaOK: true, metaErr: "write failed", artifact: "file", want: false},
		{id: "task-directory", status: Done, metaOK: true, artifact: "directory", want: false},
		{id: "task-complete", status: Done, metaOK: true, artifact: "file", want: true},
		{id: "task-legacy", status: Done, metaOK: true, artifact: "file", legacy: true, want: true},
	}
	for _, tc := range cases {
		logName := tc.id + jobLogExt
		metaLogPath := logName
		if tc.legacy {
			metaLogPath = ""
		}
		if err := writeMeta(filepath.Join(dir, tc.id+jobMetaExt), artifactMeta{
			ID:               tc.id,
			Kind:             "task",
			Status:           tc.status,
			StartedAt:        time.Now().Add(-time.Minute).UnixMilli(),
			FinishedAt:       time.Now().UnixMilli(),
			ArtifactComplete: tc.metaOK,
			ArtifactError:    tc.metaErr,
			LogPath:          metaLogPath,
		}); err != nil {
			t.Fatalf("write %s metadata: %v", tc.id, err)
		}
		switch tc.artifact {
		case "file":
			if err := os.WriteFile(filepath.Join(dir, logName), []byte("persisted output"), 0o600); err != nil {
				t.Fatalf("write %s artifact: %v", tc.id, err)
			}
		case "directory":
			if err := os.Mkdir(filepath.Join(dir, logName), 0o700); err != nil {
				t.Fatalf("create %s artifact directory: %v", tc.id, err)
			}
		}
	}

	views, err := ListArtifactViews(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(views))
	for _, view := range views {
		got[view.ID] = view.ArtifactComplete
	}
	if len(got) != len(cases) {
		t.Fatalf("artifact views = %+v, want %d entries", views, len(cases))
	}
	for _, tc := range cases {
		complete, ok := got[tc.id]
		if !ok {
			t.Errorf("%s artifact view is missing", tc.id)
			continue
		}
		if complete != tc.want {
			t.Errorf("%s artifact complete = %v, want %v", tc.id, complete, tc.want)
		}
	}
}

func TestJobArtifactUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file ACLs are not represented by Unix permission bits")
	}
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)

	// Simulate a legacy artifact at the path the first job will reuse.
	dir := ArtifactDir(sessionPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "bash-1"+jobLogExt)
	if err := os.WriteFile(logPath, []byte("old redacted output"), 0o644); err != nil {
		t.Fatal(err)
	}

	j := m.StartForSession("session", "bash", "echo API_KEY=raw-secret", func(_ context.Context, out io.Writer) (string, error) {
		_, _ = io.WriteString(out, "API_KEY=raw-secret\n")
		return "", nil
	})
	<-j.done
	if j.ID != "bash-1" {
		t.Fatalf("job id = %q, want bash-1", j.ID)
	}
	assertPrivateArtifactMode(t, logPath)
	assertPrivateArtifactMode(t, filepath.Join(dir, j.ID+jobMetaExt))
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("artifact dir mode = %04o, want 0700", got)
	}
}

func assertPrivateArtifactMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode = %04o, want 0600", path, got)
	}
}

func TestRestoreSessionArtifactsAndAdvanceSequence(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	first := NewManager(event.Discard)
	first.SetActiveSessionPath("session", sessionPath)
	j := first.StartForSession("session", "task", "answer", func(context.Context, io.Writer) (string, error) {
		return "persisted answer", nil
	})
	<-j.done
	first.Close()

	second := NewManager(event.Discard)
	defer second.Close()
	second.SetActiveSessionPath("session", sessionPath)

	res := second.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || !strings.Contains(res[0].Output, "persisted answer") {
		t.Fatalf("restored wait = %+v, want persisted answer", res)
	}
	if got := second.WaitForSession(context.Background(), "session", nil, 1); len(got) != 0 {
		t.Fatalf("wait without ids should ignore restored completed artifacts, got %+v", got)
	}
	if got := second.LeaseEvidenceForSession("session", j.ID); len(got.Receipts) != 0 {
		t.Fatalf("mutation-free task restored mutation evidence: %+v", got)
	}

	next := second.StartForSession("session", "bash", "next", func(context.Context, io.Writer) (string, error) {
		return "", nil
	})
	<-next.done
	if next.ID == j.ID {
		t.Fatalf("new job reused restored id %q", next.ID)
	}
}

func TestRestoreRunningArtifactAsInterrupted(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	dir := ArtifactDir(sessionPath)
	metaPath := filepath.Join(dir, "task-1"+jobMetaExt)
	if err := writeMeta(metaPath, artifactMeta{
		ID:               "task-1",
		Kind:             "task",
		Status:           Running,
		StartedAt:        time.Now().Add(-time.Minute).UnixMilli(),
		ArtifactComplete: true,
	}); err != nil {
		t.Fatal(err)
	}

	m := NewManager(event.Discard, WithSessionOwnershipProbe(func(path string) bool {
		return path == sessionPath
	}))
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)

	if got := m.RunningForSession("session"); len(got) != 0 {
		t.Fatalf("restored stale job remained live: %+v", got)
	}
	if m.KillForSession("session", "task-1") {
		t.Fatal("restored interrupted job must not be killable")
	}
	result := m.WaitForSession(context.Background(), "session", []string{"task-1"}, 1)
	if len(result) != 1 || result[0].Status != Interrupted {
		t.Fatalf("restored result = %+v, want interrupted", result)
	}

	persisted, err := readMeta(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != Interrupted || persisted.FinishedAt == 0 || persisted.ArtifactComplete {
		t.Fatalf("persisted restored metadata = %+v", persisted)
	}
}

func TestRestoreRunningArtifactDefersTombstoneWhenRepairWriteFails(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	dir := ArtifactDir(sessionPath)
	metaPath := filepath.Join(dir, "task-1"+jobMetaExt)
	if err := writeMeta(metaPath, artifactMeta{
		ID:        "task-1",
		Kind:      "task",
		Status:    Running,
		StartedAt: time.Now().Add(-time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	originalRepair := repairArtifactMeta
	repairArtifactMeta = func(string, artifactMeta) error {
		return errors.New("simulated repair failure")
	}
	t.Cleanup(func() { repairArtifactMeta = originalRepair })

	sink := &recordingSink{}
	m := NewManager(sink, WithSessionOwnershipProbe(func(path string) bool {
		return path == sessionPath
	}))
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)

	if result := m.WaitForSession(context.Background(), "session", []string{"task-1"}, 1); len(result) != 0 {
		t.Fatalf("failed repair published an in-memory tombstone: %+v", result)
	}
	persisted, err := readMeta(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != Running || persisted.FinishedAt != 0 {
		t.Fatalf("failed repair changed durable metadata: %+v", persisted)
	}
	m.mu.Lock()
	loaded := m.loaded["session"]
	m.mu.Unlock()
	if loaded {
		t.Fatal("failed repair marked the session loaded and prevented retry")
	}
	sink.mu.Lock()
	events := append([]event.Event(nil), sink.events...)
	sink.mu.Unlock()
	if len(events) != 1 || events[0].Kind != event.Notice || events[0].Level != event.LevelWarn ||
		events[0].Text != "Background job recovery did not complete." ||
		!strings.Contains(events[0].Detail, "task-1") || !strings.Contains(events[0].Detail, "simulated repair failure") {
		t.Fatalf("repair failure notice = %+v", events)
	}

	repairArtifactMeta = originalRepair
	m.SetActiveSessionPath("session", sessionPath)
	result := m.WaitForSession(context.Background(), "session", []string{"task-1"}, 1)
	if len(result) != 1 || result[0].Status != Interrupted {
		t.Fatalf("retried repair result = %+v, want interrupted", result)
	}
	m.mu.Lock()
	loaded = m.loaded["session"]
	m.mu.Unlock()
	if !loaded {
		t.Fatal("successful retry did not mark the session loaded")
	}
	persisted, err = readMeta(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != Interrupted || persisted.FinishedAt == 0 || persisted.ArtifactComplete {
		t.Fatalf("retried repair metadata = %+v, want durable interrupted tombstone", persisted)
	}
}

func TestRestoreRunningArtifactFromClosedOwnerAsInterrupted(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	dir := ArtifactDir(sessionPath)
	metaPath := filepath.Join(dir, "task-1"+jobMetaExt)
	owner := NewManager(event.Discard)
	ownerID := owner.ownerID
	owner.Close()
	if err := writeMeta(metaPath, artifactMeta{
		ID:        "task-1",
		Kind:      "task",
		OwnerID:   ownerID,
		Status:    Running,
		StartedAt: time.Now().Add(-time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	restored := NewManager(event.Discard, WithSessionOwnershipProbe(func(path string) bool {
		return path == sessionPath
	}))
	defer restored.Close()
	restored.SetActiveSessionPath("session", sessionPath)
	result := restored.WaitForSession(context.Background(), "session", []string{"task-1"}, 1)
	if len(result) != 1 || result[0].Status != Interrupted {
		t.Fatalf("restored result = %+v, want interrupted after the original owner closed", result)
	}
}

func TestRestoreRunningArtifactWithoutSessionOwnershipDefersRepair(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	metaPath := filepath.Join(ArtifactDir(sessionPath), "task-1"+jobMetaExt)
	if err := writeMeta(metaPath, artifactMeta{
		ID:        "task-1",
		Kind:      "task",
		Status:    Running,
		StartedAt: time.Now().Add(-time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	observer := NewManager(event.Discard)
	observer.SetActiveSessionPath("session", sessionPath)
	if result := observer.WaitForSession(context.Background(), "session", []string{"task-1"}, 1); len(result) != 0 {
		observer.Close()
		t.Fatalf("unowned observer published a running artifact: %+v", result)
	}
	meta, err := readMeta(metaPath)
	if err != nil {
		observer.Close()
		t.Fatal(err)
	}
	if meta.Status != Running || meta.FinishedAt != 0 {
		observer.Close()
		t.Fatalf("unowned observer rewrote running metadata: %+v", meta)
	}
	observer.Close()

	owner := NewManager(event.Discard, WithSessionOwnershipProbe(func(path string) bool {
		return path == sessionPath
	}))
	defer owner.Close()
	owner.SetActiveSessionPath("session", sessionPath)
	result := owner.WaitForSession(context.Background(), "session", []string{"task-1"}, 1)
	if len(result) != 1 || result[0].Status != Interrupted {
		t.Fatalf("owned reload = %+v, want interrupted", result)
	}
}

func TestRestoreDoesNotInterruptJobOwnedByLiveManager(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	first := NewManager(event.Discard)
	defer first.Close()
	first.SetActiveSessionPath("session", sessionPath)

	release := make(chan struct{})
	job := first.StartForSession("session", "task", "running", func(context.Context, io.Writer) (string, error) {
		<-release
		return "done", nil
	})
	metaPath := filepath.Join(ArtifactDir(sessionPath), job.ID+jobMetaExt)

	second := NewManager(event.Discard)
	defer second.Close()
	second.SetActiveSessionPath("session", sessionPath)

	meta, err := readMeta(metaPath)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if meta.Status != Running {
		close(release)
		t.Fatalf("a replacement manager interrupted a still-live job: status=%q", meta.Status)
	}
	if got := second.RunningForSession("session"); len(got) != 0 {
		close(release)
		t.Fatalf("replacement manager published an unowned live job: %+v", got)
	}

	close(release)
	<-job.done
	second.SetActiveSessionPath("session", sessionPath)
	result := second.WaitForSession(context.Background(), "session", []string{job.ID}, 1)
	if len(result) != 1 || result[0].Status != Done {
		t.Fatalf("replacement manager did not load the terminal artifact after owner completion: %+v", result)
	}
}

func TestRunningJobArtifactMetadataIsIncomplete(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)

	release := make(chan struct{})
	job := m.StartForSession("session", "task", "running", func(context.Context, io.Writer) (string, error) {
		<-release
		return "done", nil
	})
	metaPath := filepath.Join(ArtifactDir(sessionPath), job.ID+jobMetaExt)
	meta, err := readMeta(metaPath)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if meta.Status != Running || meta.FinishedAt != 0 || meta.ArtifactComplete {
		close(release)
		t.Fatalf("running metadata = %+v", meta)
	}

	close(release)
	<-job.done
	meta, err = readMeta(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != Done || meta.FinishedAt == 0 || !meta.ArtifactComplete {
		t.Fatalf("terminal metadata = %+v", meta)
	}
}

func TestTaskMutationEvidencePersistsWithoutSensitiveReceiptData(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	first := NewManager(event.Discard)
	first.SetActiveSessionPath("session", sessionPath)
	const secret = "private-receipt-value-123456"
	j := first.StartForSession("session", "task", "writer", func(ctx context.Context, _ io.Writer) (string, error) {
		PublishEvidence(ctx, evidence.ChildEvidenceSummary{Receipts: []evidence.Receipt{
			{
				ToolName: "write_file",
				Args:     json.RawMessage(`{"path":"internal/agent/task.go","content":"` + secret + `"}`),
				Success:  true,
				Write:    true,
				Mutation: true,
				Paths:    []string{"internal/agent/task.go"},
			},
			{
				ToolName: "bash",
				Success:  true,
				Command:  "go test ./... --token=" + secret,
			},
		}})
		return "persisted answer", nil
	})
	<-j.done
	first.Close()

	metaPath := filepath.Join(ArtifactDir(sessionPath), j.ID+jobMetaExt)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, leaked := range []string{secret, "content", "go test ./..."} {
		if strings.Contains(text, leaked) {
			t.Fatalf("job metadata persisted sensitive receipt data %q:\n%s", leaked, text)
		}
	}
	for _, want := range []string{`"mutationEvidenceVersion": 1`, `"risk": "medium"`, `"internal/agent/task.go"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("job metadata missing %q:\n%s", want, text)
		}
	}

	second := NewManager(event.Discard)
	defer second.Close()
	second.SetActiveSessionPath("session", sessionPath)
	summary := second.LeaseEvidenceForSession("session", j.ID)
	if len(summary.Receipts) != 1 {
		t.Fatalf("restored evidence = %+v, want one synthetic mutation", summary)
	}
	receipt := summary.Receipts[0]
	if !receipt.Success || !receipt.Mutation || !receipt.Write || receipt.ToolName != recoveredBackgroundTaskToolName {
		t.Fatalf("restored receipt = %+v, want successful recovered mutation", receipt)
	}
	if len(receipt.Paths) != 1 || filepath.ToSlash(receipt.Paths[0]) != "internal/agent/task.go" {
		t.Fatalf("restored paths = %v, want internal/agent/task.go", receipt.Paths)
	}
	if len(receipt.Args) != 0 || receipt.Command != "" || receipt.Read {
		t.Fatalf("restored receipt retained stale sign-off data: %+v", receipt)
	}

	ledger := evidence.NewLedger()
	ledger.MergeChild(summary)
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok || ledger.HasSuccessfulReviewAfter(mutation) || ledger.HasSuccessfulVerificationCommand() {
		t.Fatalf("restored evidence bypassed fresh review/verification: %+v", ledger.Summary())
	}
	if got := ledger.MutationRiskAfter(mutation); got != evidence.RiskMedium {
		t.Fatalf("restored mutation risk = %s, want medium", got)
	}
	// Lease does not consume: the receipts stay available until the collecting
	// turn commits. Only then is the persisted summary drained.
	if again := second.LeaseEvidenceForSession("session", j.ID); len(again.Receipts) != 1 {
		t.Fatalf("restored evidence not re-leasable before commit: %+v", again)
	}
	second.CommitEvidenceForSession("session", j.ID)
	if afterCommit := second.LeaseEvidenceForSession("session", j.ID); len(afterCommit.Receipts) != 0 {
		t.Fatalf("committed evidence still leasable: %+v", afterCommit)
	}

	// The commit drained the persisted copy too — a further restart must not
	// offer the same mutation again.
	third := NewManager(event.Discard)
	defer third.Close()
	third.SetActiveSessionPath("session", sessionPath)
	if thirdLease := third.LeaseEvidenceForSession("session", j.ID); len(thirdLease.Receipts) != 0 {
		t.Fatalf("committed evidence resurrected after restart: %+v", thirdLease)
	}
}

func TestHighRiskTaskMutationEvidenceRestoresAsOpaque(t *testing.T) {
	meta := artifactMeta{
		Kind:                    "task",
		MutationEvidenceVersion: mutationEvidenceVersion,
		MutationEvidence: &artifactMutationEvidence{
			Risk:  string(evidence.RiskHigh),
			Paths: []string{"ordinary-looking.go"},
		},
	}
	summary := mutationEvidenceFromArtifact(meta)
	if len(summary.Receipts) != 1 || len(summary.Receipts[0].Paths) != 0 {
		t.Fatalf("high-risk restored evidence = %+v, want opaque mutation", summary)
	}
	ledger := evidence.NewLedger()
	ledger.MergeChild(summary)
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok || ledger.MutationRiskAfter(mutation) != evidence.RiskHigh {
		t.Fatalf("high-risk mutation was downgraded during recovery: %+v", ledger.Summary())
	}
}

func TestLegacyTaskArtifactRecoversAsOpaqueHighRiskMutation(t *testing.T) {
	// A pre-feature artifact (no mutationEvidenceVersion) proves only that the
	// mutation state was never recorded — not that the task made no changes. A
	// legacy background writer task collected after upgrade could carry real,
	// unreviewed edits, so recovery must be conservative: an opaque RiskHigh
	// mutation that forces fresh inspection and review rather than silently
	// skipping it.
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	dir := ArtifactDir(sessionPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task-1"+jobLogExt), []byte("legacy answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMeta(filepath.Join(dir, "task-1"+jobMetaExt), artifactMeta{
		ID:               "task-1",
		Kind:             "task",
		Status:           Done,
		ArtifactComplete: true,
		LogPath:          "task-1" + jobLogExt,
	}); err != nil {
		t.Fatal(err)
	}

	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)
	summary := m.LeaseEvidenceForSession("session", "task-1")
	if len(summary.Receipts) != 1 || !summary.HasMutation() || len(summary.MutationPaths()) != 0 {
		t.Fatalf("legacy task evidence = %+v, want one opaque mutation", summary)
	}
	ledger := evidence.NewLedger()
	ledger.MergeChild(summary)
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok || ledger.MutationRiskAfter(mutation) != evidence.RiskHigh {
		t.Fatalf("legacy task mutation was not recovered conservatively: %+v", ledger.Summary())
	}
}

func TestFutureVersionTaskArtifactRecoversAsOpaqueHighRiskMutation(t *testing.T) {
	// A meta written by a newer build (unknown non-zero version) may contain
	// real evidence in a shape this build cannot parse: recover it as an opaque
	// mutation so downgrade coexistence cannot skip review.
	meta := artifactMeta{
		Kind:                    "task",
		MutationEvidenceVersion: mutationEvidenceVersion + 1,
	}
	summary := mutationEvidenceFromArtifact(meta)
	if len(summary.Receipts) != 1 || !summary.HasMutation() || len(summary.MutationPaths()) != 0 {
		t.Fatalf("future-version evidence = %+v, want one opaque mutation", summary)
	}
	ledger := evidence.NewLedger()
	ledger.MergeChild(summary)
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok || ledger.MutationRiskAfter(mutation) != evidence.RiskHigh {
		t.Fatalf("future-version mutation was not recovered conservatively: %+v", ledger.Summary())
	}
}

func TestLeasedEvidenceResurrectsUntilCommitted(t *testing.T) {
	// Collection is provisional. A lease that is never committed — the
	// collecting turn was cancelled, errored, or the process exited before
	// delivery — must leave the mutation recoverable after a restart, so a
	// background change can never ship unreviewed. Only a commit drains the
	// persisted copy.
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	first := NewManager(event.Discard)
	first.SetActiveSessionPath("session", sessionPath)
	j := first.StartForSession("session", "task", "writer", func(ctx context.Context, _ io.Writer) (string, error) {
		PublishEvidence(ctx, evidence.ChildEvidenceSummary{Receipts: []evidence.Receipt{{
			ToolName: "write_file", Success: true, Write: true, Mutation: true, Paths: []string{"changed.go"},
		}}})
		return "done", nil
	})
	<-j.done
	// Lease without committing (the turn never delivered), then restart.
	if leased := first.LeaseEvidenceForSession("session", j.ID); !leased.HasMutation() {
		t.Fatalf("live lease = %+v, want the published mutation", leased)
	}
	first.Close()

	data, err := os.ReadFile(filepath.Join(ArtifactDir(sessionPath), j.ID+jobMetaExt))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"mutationEvidence"`) {
		t.Fatalf("uncommitted lease drained the persisted mutation summary:\n%s", data)
	}

	second := NewManager(event.Discard)
	defer second.Close()
	second.SetActiveSessionPath("session", sessionPath)
	if summary := second.LeaseEvidenceForSession("session", j.ID); !summary.HasMutation() {
		t.Fatalf("uncommitted evidence lost after restart: %+v", summary)
	}
	// Committing after the restart drains it; a further restart offers nothing.
	second.CommitEvidenceForSession("session", j.ID)
	second.Close()
	third := NewManager(event.Discard)
	defer third.Close()
	third.SetActiveSessionPath("session", sessionPath)
	if summary := third.LeaseEvidenceForSession("session", j.ID); len(summary.Receipts) != 0 {
		t.Fatalf("committed evidence resurrected after restart: %+v", summary)
	}
}

func TestFinishDestroySessionPurgesOwnedJobs(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j := m.StartForSession("session", "task", "done", func(context.Context, io.Writer) (string, error) {
		return "answer", nil
	})
	<-j.done

	done := m.DestroySession("session")
	if len(done) != 0 {
		t.Fatalf("finished job should not need destroy wait, got %d handles", len(done))
	}
	m.FinishDestroySession("session")

	if _, _, ok := m.OutputForSession("session", j.ID); ok {
		t.Fatalf("destroyed session job %s should be purged", j.ID)
	}
}

func TestSetActiveSessionPathMigratesRunningJobArtifacts(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()

	wroteBefore := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	j := m.StartForSession("session", "bash", "migrate", func(_ context.Context, out io.Writer) (string, error) {
		_, _ = io.WriteString(out, "before\n")
		close(wroteBefore)
		<-release
		_, _ = io.WriteString(out, "after\n")
		return "", nil
	})
	<-wroteBefore
	j.mu.Lock()
	oldPath := j.artifactPath
	j.mu.Unlock()

	m.SetActiveSessionPath("session", sessionPath)
	j.mu.Lock()
	gotPath := j.artifactPath
	j.mu.Unlock()
	if gotPath != oldPath {
		t.Fatalf("running artifact path = %q, want unchanged %q before completion", gotPath, oldPath)
	}

	releaseOnce.Do(func() { close(release) })
	<-j.done
	j.mu.Lock()
	donePath := j.artifactPath
	j.mu.Unlock()
	if !strings.HasPrefix(donePath, ArtifactDir(sessionPath)+string(filepath.Separator)) {
		t.Fatalf("completed artifact path = %q, want under %q", donePath, ArtifactDir(sessionPath))
	}
	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || !strings.Contains(res[0].Output, "before\n") || !strings.Contains(res[0].Output, "after\n") {
		t.Fatalf("wait after migration = %+v, want before and after output", res)
	}
}

func TestArtifactFailureDoesNotFailSuccessfulJob(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(ArtifactDir(sessionPath), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)

	j := m.StartForSession("session", "task", "artifact fail", func(context.Context, io.Writer) (string, error) {
		return "successful result", nil
	})
	<-j.done

	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || res[0].Status != Done {
		t.Fatalf("wait = %+v, want one done result", res)
	}
	if !strings.Contains(res[0].Output, "successful result") || !strings.Contains(res[0].Output, "job artifact incomplete:") {
		t.Fatalf("output = %q, want result and artifact warning", res[0].Output)
	}
}

func TestMigrateArtifactDirFallsBackToCopyWhenRenameFails(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "bash-1.log"), []byte("persisted output"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldRename := renamePath
	renamePath = func(_, _ string) error {
		return errors.New("forced rename failure")
	}
	t.Cleanup(func() { renamePath = oldRename })

	if err := migrateArtifactDir(src, dst); err != nil {
		t.Fatalf("migrateArtifactDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "bash-1.log"))
	if err != nil {
		t.Fatalf("read migrated artifact: %v", err)
	}
	if string(got) != "persisted output" {
		t.Fatalf("migrated artifact = %q, want persisted output", got)
	}
	if runtime.GOOS != "windows" {
		assertPrivateArtifactMode(t, filepath.Join(dst, "bash-1.log"))
	}
	if _, err := os.Stat(filepath.Join(src, "bash-1.log")); !os.IsNotExist(err) {
		t.Fatalf("source artifact should be removed after copy fallback, stat err = %v", err)
	}
}

func TestSetActiveSessionPathAdoptsUnscopedTemporaryJobs(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()

	j := m.StartForSession("", "task", "temporary", func(context.Context, io.Writer) (string, error) {
		return "temporary answer", nil
	})
	<-j.done

	m.SetActiveSessionPath("session", sessionPath)
	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || !strings.Contains(res[0].Output, "temporary answer") {
		t.Fatalf("adopted wait = %+v, want temporary answer", res)
	}
	if _, _, ok := m.OutputForSession("", j.ID); !ok {
		t.Fatalf("legacy unscoped lookup should still find adopted job %s", j.ID)
	}
	if _, err := os.Stat(filepath.Join(ArtifactDir(sessionPath), j.ID+jobLogExt)); err != nil {
		t.Fatalf("adopted artifact should be under persistent sidecar: %v", err)
	}
}

func TestSetActiveSessionPathAdoptsUnscopedJobsOnMigrationFailure(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(ArtifactDir(sessionPath), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(event.Discard)
	defer m.Close()

	j := m.StartForSession("", "task", "temporary", func(context.Context, io.Writer) (string, error) {
		return "temporary answer", nil
	})
	<-j.done

	m.SetActiveSessionPath("session", sessionPath)

	out, status, ok := m.OutputForSession("session", j.ID)
	if !ok || status != Done {
		t.Fatalf("scoped output ok/status = %v/%s, want true/done", ok, status)
	}
	if !strings.Contains(out, "temporary answer") || !strings.Contains(out, "job artifact incomplete: migration:") {
		t.Fatalf("scoped output = %q, want answer and migration error", out)
	}
	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || !strings.Contains(res[0].Output, "job artifact incomplete: migration:") {
		t.Fatalf("scoped wait = %+v, want migration error", res)
	}
	if note := m.DrainCompletedNoteForSession("session"); !strings.Contains(note, j.ID) {
		t.Fatalf("adopted completion note = %q, want job id %s", note, j.ID)
	}
}

func TestSetActiveSessionPathReportsMigrationFailure(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(ArtifactDir(sessionPath), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	var noticesMu sync.Mutex
	var notices []event.Event
	m := NewManager(event.FuncSink(func(e event.Event) {
		if e.Kind == event.Notice {
			noticesMu.Lock()
			notices = append(notices, e)
			noticesMu.Unlock()
		}
	}))
	defer m.Close()

	j := m.StartForSession("session", "task", "migrate fail", func(context.Context, io.Writer) (string, error) {
		return "answer", nil
	})
	m.SetActiveSessionPath("session", sessionPath)
	<-j.done

	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || !strings.Contains(res[0].Output, "job artifact incomplete: migration:") {
		t.Fatalf("wait after migration failure = %+v, want artifact error", res)
	}
	noticesMu.Lock()
	capturedNotices := append([]event.Event(nil), notices...)
	noticesMu.Unlock()
	found := false
	for _, notice := range capturedNotices {
		if notice.Text == "Job artifact migration failed." && strings.Contains(notice.Detail, "job artifact migration failed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("migration failure notice not emitted, got %+v", capturedNotices)
	}
}

func TestOutputReadsArtifactFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bash-1.log")
	prefix := strings.Repeat("x", 2*defaultTailBytes)
	suffix := "new output\n"
	if err := os.WriteFile(path, []byte(prefix+suffix), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(event.Discard)
	defer m.Close()
	j := &Job{
		ID:           "bash-1",
		Kind:         "bash",
		SessionID:    "session",
		status:       Running,
		readOffset:   int64(len(prefix)),
		artifactPath: path,
		done:         make(chan struct{}),
	}
	m.jobs[jobKey("session", j.ID)] = j
	m.order = append(m.order, jobKey("session", j.ID))

	text, status, ok := m.OutputForSession("session", j.ID)
	if !ok || status != Running {
		t.Fatalf("OutputForSession ok/status = %v/%s, want true/running", ok, status)
	}
	if text != suffix {
		t.Fatalf("OutputForSession text = %q, want %q", text, suffix)
	}
	if j.readOffset != int64(len(prefix)+len(suffix)) {
		t.Fatalf("readOffset = %d, want %d", j.readOffset, len(prefix)+len(suffix))
	}
}

func TestRestoredArtifactsAreScopedBySession(t *testing.T) {
	root := t.TempDir()
	pathA := filepath.Join(root, "a.jsonl")
	pathB := filepath.Join(root, "b.jsonl")

	first := NewManager(event.Discard)
	first.SetActiveSessionPath("session-a", pathA)
	jobA := first.StartForSession("session-a", "bash", "a", func(_ context.Context, out io.Writer) (string, error) {
		_, _ = io.WriteString(out, "from-a")
		return "", nil
	})
	<-jobA.done
	first.Close()

	second := NewManager(event.Discard)
	second.SetActiveSessionPath("session-b", pathB)
	jobB := second.StartForSession("session-b", "bash", "b", func(_ context.Context, out io.Writer) (string, error) {
		_, _ = io.WriteString(out, "from-b")
		return "", nil
	})
	<-jobB.done
	second.Close()

	if jobA.ID != "bash-1" || jobB.ID != "bash-1" {
		t.Fatalf("test setup expected duplicate ids, got %s and %s", jobA.ID, jobB.ID)
	}

	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session-a", pathA)
	m.SetActiveSessionPath("session-b", pathB)

	resA := m.WaitForSession(context.Background(), "session-a", []string{"bash-1"}, 1)
	if len(resA) != 1 || resA[0].Output != "from-a" {
		t.Fatalf("session-a wait = %+v, want from-a", resA)
	}
	resB := m.WaitForSession(context.Background(), "session-b", []string{"bash-1"}, 1)
	if len(resB) != 1 || resB[0].Output != "from-b" {
		t.Fatalf("session-b wait = %+v, want from-b", resB)
	}

	next := m.StartForSession("session-b", "bash", "next", func(context.Context, io.Writer) (string, error) {
		return "", nil
	})
	<-next.done
	if next.ID == "bash-1" {
		t.Fatal("new job should not reuse restored bash-1")
	}
}
