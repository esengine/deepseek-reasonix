package turnevent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/store"
)

func testSessionPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "session.jsonl")
}

func openTestLedger(t *testing.T, sessionPath, sessionID string) *Ledger {
	t.Helper()
	ledger, err := Open(sessionPath, sessionID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Errorf("Close test ledger: %v", err)
		}
	})
	return ledger
}

func TestLedgerPersistsMonotonicEventsAndExactlyOneTerminal(t *testing.T) {
	path := testSessionPath(t)
	l, err := Open(path, "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.SetRoutingMetadata("epoch-1", "submission-1")
	turnID, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	l.SetTranscriptSnapshot(7, "digest-1")
	started, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress)
	if err != nil || !ok {
		t.Fatalf("append started: ok=%v err=%v", ok, err)
	}
	done, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted)
	if err != nil || !ok {
		t.Fatalf("append done: ok=%v err=%v", ok, err)
	}
	if started.TurnID != turnID || done.TurnID != turnID || started.Sequence != 1 || done.Sequence != 2 {
		t.Fatalf("stamps = started(%q,%d) done(%q,%d), want turn %q seq 1,2", started.TurnID, started.Sequence, done.TurnID, done.Sequence, turnID)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnFailed); err != nil || ok {
		t.Fatalf("second terminal: ok=%v err=%v, want compare-and-append rejection", ok, err)
	}
	recs, err := l.EventsAfter(0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(recs) != 2 || recs[0].Sequence != 1 || recs[1].Sequence != 2 {
		t.Fatalf("records = %#v, want two monotonic events", recs)
	}
	if recs[1].RuntimeEpoch != "epoch-1" || recs[1].SubmissionID != "submission-1" || recs[1].TranscriptRevision != 7 || recs[1].TranscriptDigest != "digest-1" {
		t.Fatalf("terminal metadata = %+v, want routing and transcript identity", recs[1])
	}
	if latest, replayAfter := l.ProjectionCursor(); latest != 2 || replayAfter != 2 {
		t.Fatalf("terminal cursor = (%d,%d), want (2,2)", latest, replayAfter)
	}
}

func TestLedgerProjectionCursorReplaysOnlyActiveTurn(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin first: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted); err != nil || !ok {
		t.Fatalf("complete first: ok=%v err=%v", ok, err)
	}
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin second: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStatusChanged}, event.TurnQueued); err != nil || !ok {
		t.Fatalf("queue second: ok=%v err=%v", ok, err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("start second: ok=%v err=%v", ok, err)
	}
	if latest, replayAfter := l.ProjectionCursor(); latest != 3 || replayAfter != 1 {
		t.Fatalf("active cursor = (%d,%d), want (3,1)", latest, replayAfter)
	}
}

func TestLedgerSubmissionReceiptSurvivesCompletionAndIsOneShot(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	l.SetRoutingMetadata("epoch-1", "submission-1")
	first, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin first: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted); err != nil || !ok {
		t.Fatalf("complete first: ok=%v err=%v", ok, err)
	}
	if got := l.TurnIDForSubmission("submission-1"); got != first {
		t.Fatalf("receipt after completion = %q, want %q", got, first)
	}
	second, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin automatic follow-up: %v", err)
	}
	if second == first {
		t.Fatal("follow-up reused the completed turn id")
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("start follow-up: ok=%v err=%v", ok, err)
	}
	records, err := l.EventsAfter(0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	last := records[len(records)-1]
	if last.SubmissionID != "" || last.Event.SubmissionID != "" {
		t.Fatalf("automatic follow-up inherited the previous submission: %+v", last)
	}
	if last.RuntimeEpoch != "epoch-1" || last.Event.RuntimeEpoch != "epoch-1" {
		t.Fatalf("automatic follow-up lost its owning runtime epoch: %+v", last)
	}
	if got := l.TurnIDForSubmission("submission-1"); got != first {
		t.Fatalf("receipt after replacement = %q, want original %q", got, first)
	}
}

func TestLedgerRejectsStatusRegressionAndKeepsCancellationSticky(t *testing.T) {
	l := openTestLedger(t, testSessionPath(t), "session")
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("start: ok=%v err=%v", ok, err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStatusChanged}, event.TurnQueued); err == nil || ok {
		t.Fatalf("status regression: ok=%v err=%v, want rejection", ok, err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStatusChanged}, event.TurnCancelling); err != nil || !ok {
		t.Fatalf("cancel: ok=%v err=%v", ok, err)
	}
	stamped, ok, err := l.Append(event.Event{Kind: event.PromptAnswered}, event.TurnInProgress)
	if err != nil || !ok || stamped.Status != event.TurnCancelling || l.CurrentStatus() != event.TurnCancelling {
		t.Fatalf("late prompt answer = (%+v,%v,%v), current=%q, want sticky cancelling", stamped, ok, err, l.CurrentStatus())
	}
}

func TestLedgerRepairsTornTailAndKeepsValidPrefix(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append: ok=%v err=%v", ok, err)
	}
	ledgerPath := store.SessionTurnEventLog(path)
	f, err := os.OpenFile(ledgerPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open ledger tail: %v", err)
	}
	if _, err := f.WriteString(`{"schemaVersion":1,"seq":2`); err != nil {
		t.Fatalf("write torn tail: %v", err)
	}
	_ = f.Close()

	reopened := openTestLedger(t, path, "session")
	recs, err := reopened.EventsAfter(0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(recs) != 2 || recs[0].Status != event.TurnInProgress || recs[1].Status != event.TurnInterrupted {
		t.Fatalf("recovered records = %#v, want valid start plus interrupted terminal", recs)
	}
	damaged, err := os.ReadFile(store.SessionTurnEventLogDamaged(path))
	if err != nil {
		t.Fatalf("read damaged tail: %v", err)
	}
	if len(damaged) == 0 {
		t.Fatal("damaged tail was not isolated")
	}
}

func TestLedgerIsolatesNonMonotonicTail(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append: ok=%v err=%v", ok, err)
	}
	ledgerPath := store.SessionTurnEventLog(path)
	f, err := os.OpenFile(ledgerPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	if _, err := f.WriteString(`{"schemaVersion":1,"sessionId":"session","turnId":"duplicate","seq":1,"kind":"turn_started","status":"in_progress","createdAt":1,"event":{"kind":"turn_started"}}` + "\n"); err != nil {
		t.Fatalf("append duplicate sequence: %v", err)
	}
	_ = f.Close()

	reopened := openTestLedger(t, path, "session")
	recs, err := reopened.EventsAfter(0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(recs) != 2 || recs[0].Sequence != 1 || recs[1].Status != event.TurnInterrupted {
		t.Fatalf("records = %#v, want valid seq 1 plus interrupted recovery", recs)
	}
	damaged, err := os.ReadFile(store.SessionTurnEventLogDamaged(path))
	if err != nil || len(damaged) == 0 {
		t.Fatalf("non-monotonic tail was not isolated: bytes=%d err=%v", len(damaged), err)
	}
}

func TestLedgerRecoveryClosesRunningToolsWithoutReplay(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append start: ok=%v err=%v", ok, err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{
		ID: "write-1", Name: "write_file", Args: `{"path":"important.txt"}`,
	}}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append dispatch: ok=%v err=%v", ok, err)
	}

	reopened := openTestLedger(t, path, "session")
	recs, err := reopened.EventsAfter(0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("records = %#v, want start, dispatch, synthetic result, terminal", recs)
	}
	result := recs[2]
	if result.Kind != "tool_result" || result.Event.Tool == nil || result.Event.Tool.ID != "write-1" || result.Event.Tool.Err == "" {
		t.Fatalf("synthetic result = %#v, want interrupted write-1", result)
	}
	if result.Event.Tool.Args != "" || result.Event.Tool.Output != "" {
		t.Fatalf("synthetic result must not replay tool input/output: %#v", result.Event.Tool)
	}
	if recs[3].Kind != "turn_done" || recs[3].Status != event.TurnInterrupted {
		t.Fatalf("terminal = %#v, want interrupted turn_done", recs[3])
	}
}

func TestLedgerBootstrapsLegacyTranscriptWithoutRewritingIt(t *testing.T) {
	path := testSessionPath(t)
	original := []byte("legacy provider transcript\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	l, err := Open(path, "legacy")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	recs, err := l.EventsAfter(0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(recs) != 1 || recs[0].Kind != "turn_status" || recs[0].Status != event.TurnCompleted {
		t.Fatalf("bootstrap = %#v, want one completed turn_status", recs)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("legacy transcript changed: %q", got)
	}
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin after bootstrap: %v", err)
	}
}

func TestLedgerReplayEmptyArrayAndUnknownFields(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	recs, err := l.EventsAfter(99)
	if err != nil {
		t.Fatalf("EventsAfter empty: %v", err)
	}
	if recs == nil || len(recs) != 0 {
		t.Fatalf("empty replay = %#v, want non-nil empty slice", recs)
	}
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append: ok=%v err=%v", ok, err)
	}
	ledgerPath := store.SessionTurnEventLog(path)
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	data = append(data[:len(data)-2], []byte(`,"futureField":{"nested":true}}\n`)...)
	if err := os.WriteFile(ledgerPath, data, 0o600); err != nil {
		t.Fatalf("rewrite with unknown field: %v", err)
	}
	openTestLedger(t, path, "session")
}

func TestLedgerPersistentHandleTerminalSyncAndProjectionAckOpenCounts(t *testing.T) {
	l, err := Open(testSessionPath(t), "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.RequireProjectionAck(true)
	turnID, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for _, e := range []event.Event{
		{Kind: event.TurnStarted},
		{Kind: event.Text, Text: "one"},
		{Kind: event.Text, Text: "two"},
	} {
		if _, ok, appendErr := l.Append(e, event.TurnInProgress); appendErr != nil || !ok {
			t.Fatalf("Append(%v): ok=%v err=%v", e.Kind, ok, appendErr)
		}
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted); err != nil || !ok {
		t.Fatalf("terminal: ok=%v err=%v", ok, err)
	}
	beforeAck := l.MetricsSnapshot()
	if beforeAck.OpenCount != 1 || beforeAck.SyncCount != 1 || beforeAck.CloseCount != 1 {
		t.Fatalf("WAL lifecycle = open:%d sync:%d close:%d, want 1/1/1", beforeAck.OpenCount, beforeAck.SyncCount, beforeAck.CloseCount)
	}
	if err := l.AcknowledgeProjection(turnID); err != nil {
		t.Fatalf("AcknowledgeProjection: %v", err)
	}
	afterAck := l.MetricsSnapshot()
	if afterAck.OpenCount != 2 || afterAck.SyncCount != 1 || afterAck.CloseCount != 2 {
		t.Fatalf("WAL+ack lifecycle = open:%d sync:%d close:%d, want 2/1/2", afterAck.OpenCount, afterAck.SyncCount, afterAck.CloseCount)
	}
}

func TestLedgerCompactionWaitsForProjectionAckAndReplayResetsAtCheckpoint(t *testing.T) {
	path := testSessionPath(t)
	l, err := Open(path, "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.RequireProjectionAck(true)
	l.SetRoutingMetadata("epoch", "submission")
	turnID, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	l.SetTranscriptSnapshot(9, "digest")
	if _, ok, err := l.Append(event.Event{Kind: event.Text, Text: "display-only"}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("text: ok=%v err=%v", ok, err)
	}
	done, ok, err := l.Append(event.Event{Kind: event.TurnDone, Outcome: "partial"}, event.TurnInterrupted)
	if err != nil || !ok {
		t.Fatalf("terminal: ok=%v err=%v", ok, err)
	}
	if err := l.Compact(); err != nil {
		t.Fatalf("Compact before ack: %v", err)
	}
	if pending := l.PendingProjections(); len(pending) != 1 || pending[0].TurnID != turnID || len(pending[0].Events) != 2 {
		t.Fatalf("pending projection = %+v, want retained terminal turn", pending)
	}
	if err := l.AcknowledgeProjection(turnID); err != nil {
		t.Fatalf("AcknowledgeProjection: %v", err)
	}
	if err := l.Compact(); err != nil {
		t.Fatalf("Compact after ack: %v", err)
	}
	if pending := l.PendingProjections(); len(pending) != 0 {
		t.Fatalf("pending after checkpoint = %+v", pending)
	}
	reset, err := l.Replay(done.Sequence - 1)
	if err != nil {
		t.Fatalf("Replay reset: %v", err)
	}
	if !reset.ResetRequired || reset.FloorSequence != done.Sequence+1 || reset.LatestSequence != done.Sequence || reset.NextAfterSequence != done.Sequence || reset.Events == nil {
		t.Fatalf("reset replay = %+v", reset)
	}
	current, err := l.Replay(done.Sequence)
	if err != nil || current.ResetRequired || current.Events == nil || len(current.Events) != 0 {
		t.Fatalf("checkpoint replay = %+v err=%v", current, err)
	}
	if got := l.TurnIDForSubmission("submission"); got != turnID {
		t.Fatalf("submission receipt = %q, want %q", got, turnID)
	}
	data, err := os.ReadFile(store.SessionTurnEventLog(path))
	if err != nil {
		t.Fatalf("read compacted ledger: %v", err)
	}
	if len(data) >= 64<<10 || !bytes.Contains(data, []byte(`"recordType":"checkpoint"`)) || bytes.Contains(data, []byte("display-only")) {
		t.Fatalf("unsafe or oversized checkpoint: bytes=%d data=%s", len(data), data)
	}
}

func TestLedgerReplayPaginationAndOutOfRangeCursor(t *testing.T) {
	l := openTestLedger(t, testSessionPath(t), "session")
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := range replayMaxEvents + 25 {
		if _, ok, err := l.Append(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: "tool", Name: "bash", Output: "tick"}}, event.TurnInProgress); err != nil || !ok {
			t.Fatalf("Append %d: ok=%v err=%v", i, ok, err)
		}
	}
	first, err := l.Replay(0)
	if err != nil || len(first.Events) != replayMaxEvents || !first.HasMore || first.NextAfterSequence != replayMaxEvents {
		t.Fatalf("first page = events:%d next:%d more:%v err=%v", len(first.Events), first.NextAfterSequence, first.HasMore, err)
	}
	second, err := l.Replay(first.NextAfterSequence)
	if err != nil || len(second.Events) != 25 || second.HasMore {
		t.Fatalf("second page = events:%d more:%v err=%v", len(second.Events), second.HasMore, err)
	}
	outOfRange, err := l.Replay(second.LatestSequence + 1)
	if err != nil || !outOfRange.ResetRequired {
		t.Fatalf("out-of-range replay = %+v err=%v", outOfRange, err)
	}
}

func TestLedgerUnsupportedSchemaLeavesOriginalUntouched(t *testing.T) {
	path := testSessionPath(t)
	ledgerPath := store.SessionTurnEventLog(path)
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	original := []byte(`{"schemaVersion":99,"recordType":"checkpoint"}` + "\n")
	if err := os.WriteFile(ledgerPath, original, 0o600); err != nil {
		t.Fatalf("write future ledger: %v", err)
	}
	_, err := Open(path, "session")
	var unsupported *UnsupportedSchemaError
	if !errors.As(err, &unsupported) || unsupported.Version != 99 {
		t.Fatalf("Open error = %v, want unsupported schema 99", err)
	}
	got, readErr := os.ReadFile(ledgerPath)
	if readErr != nil || !bytes.Equal(got, original) {
		t.Fatalf("future ledger changed: bytes=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(store.SessionTurnEventLogDamaged(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("future schema was isolated as damage: %v", statErr)
	}
}

func TestLedgerContinuesActiveV1TurnBeforeUpgradingToV2(t *testing.T) {
	path := testSessionPath(t)
	ledgerPath := store.SessionTurnEventLog(path)
	v1 := Envelope{
		SchemaVersion: legacySchemaVersion, SessionID: "session", TurnID: "legacy-turn",
		Sequence: 1, Kind: "turn_started", Status: event.TurnInProgress, CreatedAt: 1,
		Event: eventwire.Event{Kind: "turn_started"},
	}
	line, err := json.Marshal(v1)
	if err != nil {
		t.Fatalf("marshal v1: %v", err)
	}
	if err := os.WriteFile(ledgerPath, append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	l, err := Open(path, "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	recovered, err := l.EventsAfter(0)
	if err != nil || len(recovered) != 2 || recovered[1].Status != event.TurnInterrupted {
		t.Fatalf("v1 recovery = %+v err=%v", recovered, err)
	}
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin v2 turn: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted); err != nil || !ok {
		t.Fatalf("complete v2 turn: ok=%v err=%v", ok, err)
	}

	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read upgraded ledger: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 3 || bytes.Contains(lines[0], []byte(`"recordType"`)) || bytes.Contains(lines[1], []byte(`"recordType"`)) || !bytes.Contains(lines[2], []byte(`"recordType":"event"`)) {
		t.Fatalf("v1/v2 append boundary changed: %s", data)
	}
}

func TestLedgerCheckpointKeepsOnlyRecentTerminalSummariesAndReceipts(t *testing.T) {
	path := testSessionPath(t)
	l, err := Open(path, "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.RequireProjectionAck(true)
	turns := make([]string, 20)
	for i := range turns {
		submission := fmt.Sprintf("submission-%02d", i)
		l.SetRoutingMetadata("epoch", submission)
		turns[i], err = l.Begin()
		if err != nil {
			t.Fatalf("Begin %d: %v", i, err)
		}
		if _, ok, appendErr := l.Append(event.Event{Kind: event.TurnDone, Outcome: "completed"}, event.TurnCompleted); appendErr != nil || !ok {
			t.Fatalf("complete %d: ok=%v err=%v", i, ok, appendErr)
		}
		if err := l.AcknowledgeProjection(turns[i]); err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
	}
	if err := l.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	reopened, err := Open(path, "session")
	if err != nil {
		t.Fatalf("reopen checkpoint: %v", err)
	}
	if got := len(reopened.summaries); got != terminalSummaryLimit {
		t.Fatalf("terminal summaries = %d, want %d", got, terminalSummaryLimit)
	}
	if reopened.summaries[0].TurnID != turns[4] || reopened.summaries[len(reopened.summaries)-1].TurnID != turns[19] {
		t.Fatalf("summary window = first:%q last:%q", reopened.summaries[0].TurnID, reopened.summaries[len(reopened.summaries)-1].TurnID)
	}
	if got := reopened.TurnIDForSubmission("submission-03"); got != "" {
		t.Fatalf("expired submission receipt = %q, want bounded eviction", got)
	}
	if got := reopened.TurnIDForSubmission("submission-19"); got != turns[19] {
		t.Fatalf("recent submission receipt = %q, want %q", got, turns[19])
	}
	data, err := os.ReadFile(store.SessionTurnEventLog(path))
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if bytes.Contains(data, []byte("submission-03")) || !bytes.Contains(data, []byte("submission-19")) {
		t.Fatalf("checkpoint did not enforce the bounded receipt window: %s", data)
	}
}

func TestLedgerRebuildsCompleteTerminalSummaryBeforeCheckpoint(t *testing.T) {
	path := testSessionPath(t)
	l, err := Open(path, "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.RequireProjectionAck(true)
	l.SetRoutingMetadata("epoch", "submission")
	turnID, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	l.SetTranscriptSnapshot(7, "digest")
	if _, ok, appendErr := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); appendErr != nil || !ok {
		t.Fatalf("start: ok=%v err=%v", ok, appendErr)
	}
	if _, ok, appendErr := l.Append(event.Event{Kind: event.TurnDone, Outcome: "partial"}, event.TurnInterrupted); appendErr != nil || !ok {
		t.Fatalf("complete: ok=%v err=%v", ok, appendErr)
	}
	if err := l.AcknowledgeProjection(turnID); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path, "session")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.summaries) != 1 {
		t.Fatalf("summaries = %+v, want one reconstructed terminal", reopened.summaries)
	}
	summary := reopened.summaries[0]
	if summary.TurnID != turnID || summary.Outcome != "partial" || summary.Status != event.TurnInterrupted {
		t.Fatalf("summary identity = %+v", summary)
	}
	if summary.StartedAt <= 0 || summary.FinishedAt < summary.StartedAt || summary.DurationMs != summary.FinishedAt-summary.StartedAt {
		t.Fatalf("summary timing = %+v", summary)
	}
	if summary.RuntimeEpoch != "epoch" || summary.SubmissionID != "submission" || summary.TranscriptRevision != 7 || summary.TranscriptDigest != "digest" {
		t.Fatalf("summary metadata = %+v", summary)
	}
}

func TestLedgerCompactionFailurePreservesOriginalSidecar(t *testing.T) {
	path := testSessionPath(t)
	l, err := Open(path, "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.RequireProjectionAck(true)
	turnID, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.Text, Text: "retained until atomic replace"}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("text: ok=%v err=%v", ok, err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted); err != nil || !ok {
		t.Fatalf("terminal: ok=%v err=%v", ok, err)
	}
	if err := l.AcknowledgeProjection(turnID); err != nil {
		t.Fatalf("ack: %v", err)
	}
	ledgerPath := store.SessionTurnEventLog(path)
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read original sidecar: %v", err)
	}
	originalWriter := atomicWriteLedgerFile
	atomicWriteLedgerFile = func(string, []byte, os.FileMode) error { return errors.New("simulated atomic replace failure") }
	t.Cleanup(func() { atomicWriteLedgerFile = originalWriter })

	if err := l.Compact(); err == nil {
		t.Fatal("Compact succeeded through an injected atomic replacement failure")
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read sidecar after failed compact: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("failed compaction changed the original sidecar\nbefore=%s\nafter=%s", before, after)
	}
	if metrics := l.MetricsSnapshot(); metrics.CompactionFailures != 1 {
		t.Fatalf("compaction failures = %d, want 1", metrics.CompactionFailures)
	}
}

func TestProjectionAckDoesNotFailWhenBestEffortCompactionFails(t *testing.T) {
	path := testSessionPath(t)
	l, err := Open(path, "session")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.RequireProjectionAck(true)
	l.compactBytes = 1
	turnID, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.Text, Text: "keep the WAL"}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("text: ok=%v err=%v", ok, err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted); err != nil || !ok {
		t.Fatalf("terminal: ok=%v err=%v", ok, err)
	}

	originalWriter := atomicWriteLedgerFile
	atomicWriteLedgerFile = func(string, []byte, os.FileMode) error { return errors.New("simulated atomic replace failure") }
	if err := l.AcknowledgeProjection(turnID); err != nil {
		t.Fatalf("durable projection ack inherited best-effort compaction failure: %v", err)
	}
	if metrics := l.MetricsSnapshot(); metrics.CompactionFailures != 1 {
		t.Fatalf("compaction failures = %d, want 1", metrics.CompactionFailures)
	}
	if pending := l.PendingProjections(); len(pending) != 0 {
		t.Fatalf("durably acknowledged projection remained pending: %+v", pending)
	}

	atomicWriteLedgerFile = originalWriter
	t.Cleanup(func() { atomicWriteLedgerFile = originalWriter })
	if err := l.AcknowledgeProjection(turnID); err != nil {
		t.Fatalf("retry acknowledged projection: %v", err)
	}
	if l.compactedThrough == 0 {
		t.Fatal("idempotent acknowledgement did not retry checkpoint compaction")
	}
}

func BenchmarkLedgerReplayMillionEventCheckpointTail(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ledgerPath := store.SessionTurnEventLog(path)
	cp := checkpointRecord{SchemaVersion: schemaVersion, RecordType: "checkpoint", SessionID: "session", CompactedThroughSequence: 1_000_000, ProjectionCommittedThrough: 1_000_000, TerminalSummaries: []TerminalSummary{}}
	data, _ := json.Marshal(cp)
	data = append(data, '\n')
	for seq := uint64(1_000_001); seq <= 1_000_100; seq++ {
		kind := "tool_progress"
		status := event.TurnInProgress
		if seq == 1_000_100 {
			kind = "turn_done"
			status = event.TurnCompleted
		}
		rec := diskEventRecord{RecordType: "event", Envelope: Envelope{SchemaVersion: schemaVersion, SessionID: "session", TurnID: "tail", Sequence: seq, Kind: kind, Status: status, CreatedAt: 1, Event: eventwire.Event{Kind: kind}}}
		line, _ := json.Marshal(rec)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(ledgerPath, data, 0o600); err != nil {
		b.Fatal(err)
	}
	l, err := Open(path, "session")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		view, err := l.Replay(1_000_000)
		if err != nil || len(view.Events) != 100 {
			b.Fatalf("Replay: events=%d err=%v", len(view.Events), err)
		}
	}
}
