package evidence

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLedgerRecordsSuccessAndFailureReceipts(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "bash",
		Args:     json.RawMessage(`{"command":"go test ./..."}`),
		Success:  true,
		Command:  "go test ./...",
	})
	ledger.Record(Receipt{
		ToolName: "bash",
		Args:     json.RawMessage(`{"command":"go test ./internal/..."}`),
		Success:  false,
		Command:  "go test ./internal/...",
	})

	if !ledger.HasSuccessfulCommand("go test ./...") {
		t.Fatal("successful bash command should verify")
	}
	if ledger.HasSuccessfulCommand("go test ./internal/...") {
		t.Fatal("failed bash command must not verify")
	}
}

func TestLedgerDistinguishesWorkflowSpecificMutation(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("remember", json.RawMessage(`{"body":"ORBIT-42"}`), true, false))
	if !ledger.HasSuccessfulToolReceipt("remember") {
		t.Fatal("successful remember receipt was not found")
	}
	if ledger.HasSuccessfulMutationOtherThan("remember") {
		t.Fatal("remember-only mutation was treated as an unrelated workspace mutation")
	}
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"internal/a.go"}`), true, false))
	if !ledger.HasSuccessfulMutationOtherThan("remember") {
		t.Fatal("workspace mutation was hidden by the remember allowance")
	}
}

func TestLedgerMatchesFreshSuccessfulReviewReceipt(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("review", json.RawMessage(`{"task":"review changes"}`), true, true))
	if !ledger.HasCompletedReview() {
		t.Fatal("successful dedicated review tool should verify review evidence")
	}
	legacy := NewLedger()
	legacy.Record(ReceiptFromToolCall("task", json.RawMessage(`{"profile":"review"}`), true, true))
	if !legacy.HasCompletedReview() {
		t.Fatal("successful task(profile=review) should verify review evidence")
	}
	failed := NewLedger()
	failed.Record(ReceiptFromToolCall("task", json.RawMessage(`{"profile":"review"}`), false, true))
	if failed.HasCompletedReview() {
		t.Fatal("failed review task must not verify review evidence")
	}
	background := NewLedger()
	background.Record(ReceiptFromToolCall("task", json.RawMessage(`{"profile":"review","run_in_background":true}`), true, true))
	if background.HasCompletedReview() {
		t.Fatal("starting a background review must not count as a completed review")
	}
}

func TestLedgerRejectsReviewBeforeLatestMutation(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("review", json.RawMessage(`{"task":"review changes"}`), true, true))
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"changed.go"}`), true, false))
	if ledger.HasCompletedReview() {
		t.Fatal("review evidence before the latest mutation must be stale")
	}
}

func TestLedgerRequiresReviewCoverageAfterMutation(t *testing.T) {
	fresh := NewLedger()
	fresh.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"changed.go"}`), true, false))
	fresh.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"changed.go"}`), true, true))
	fresh.Record(ReceiptFromToolCall("review", json.RawMessage(`{"task":"review changes"}`), true, true))
	if !fresh.HasCompletedReview() {
		t.Fatal("review after reading the latest changed path should count")
	}

	unrelated := NewLedger()
	unrelated.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"changed.go"}`), true, false))
	unrelated.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"other.go"}`), true, true))
	unrelated.Record(ReceiptFromToolCall("review", json.RawMessage(`{"task":"review something else"}`), true, true))
	if unrelated.HasCompletedReview() {
		t.Fatal("review that did not inspect the latest changed path must not count")
	}
}

func TestLedgerHostReviewCoverageRequiresContentForEveryPath(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"internal/a.go"}`), true, false))
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"internal/b.go"}`), true, false))
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok {
		t.Fatal("missing mutation index")
	}
	ledger.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"internal/a.go"}`), true, true))
	if ledger.HasHostReviewCoverageAfter(mutation, []string{"internal/a.go", "internal/b.go"}) {
		t.Fatal("one path read must not cover a two-path change set")
	}
	ledger.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"internal/b.go"}`), true, true))
	if !ledger.HasHostReviewCoverageAfter(mutation, []string{"internal/a.go", "internal/b.go"}) {
		t.Fatal("fresh reads of every changed path should prove host review coverage")
	}

	diff := NewLedger()
	diff.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"internal/a.go"}`), true, false))
	diffMutation, ok := diff.LatestSuccessfulMutationIndex()
	if !ok {
		t.Fatal("missing diff mutation index")
	}
	diff.Record(Receipt{ToolName: "bash", Success: true, Command: "git diff", OutputBytes: 200})
	if !diff.HasHostReviewCoverageAfter(diffMutation, []string{"internal/a.go", "internal/b.go"}) {
		t.Fatal("an output-producing whole git diff should cover the current change set")
	}
	for _, command := range []string{"git status --short", "git diff --check"} {
		insufficient := NewLedger()
		insufficient.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"internal/a.go"}`), true, false))
		idx, ok := insufficient.LatestSuccessfulMutationIndex()
		if !ok {
			t.Fatal("missing insufficient mutation index")
		}
		insufficient.Record(Receipt{ToolName: "bash", Success: true, Command: command, OutputBytes: 200})
		if insufficient.HasHostReviewCoverageAfter(idx, []string{"internal/a.go"}) {
			t.Fatalf("%q must not count as content review", command)
		}
	}
}

func TestLedgerAcceptsCollectedStructuredReviewReport(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"changed.go"}`), true, false))
	ledger.Record(Receipt{ToolName: "review_report", Success: true, Args: json.RawMessage(`{
		"kind":"review",
		"verdict":"block",
		"reviewed_paths":["changed.go"],
		"findings":[{"severity":"critical","summary":"must fix","path":"changed.go"}]
	}`)})
	if !ledger.HasCompletedReview() {
		t.Fatal("a structured report completes the review activity even when its verdict requires follow-up")
	}
}

func TestLedgerHasWriteOrCommandSince(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{ToolName: "todo_write", Success: true, Todos: []TodoItem{{Content: "edit", Status: "in_progress"}}})
	ledger.Record(Receipt{ToolName: "write_file", Success: true, Write: true, Paths: []string{"a.go"}})
	ledger.Record(Receipt{ToolName: "bash", Success: false, Command: "go test ./..."})
	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "edit"})

	if got := ledger.Len(); got != 4 {
		t.Fatalf("Len() = %d, want 4", got)
	}
	if !ledger.HasWriteOrCommandSince(0) {
		t.Fatal("write receipt at index 1 should count from index 0")
	}
	if !ledger.HasWriteOrCommandSince(-1) {
		t.Fatal("negative index should behave like 0")
	}
	if ledger.HasWriteOrCommandSince(2) {
		t.Fatal("failed command and bookkeeping receipts must not count as progress")
	}
	ledger.Record(Receipt{ToolName: "bash", Success: true, Command: "go test ./..."})
	if !ledger.HasWriteOrCommandSince(2) {
		t.Fatal("successful command receipt after index should count as progress")
	}
	var nilLedger *Ledger
	if nilLedger.HasWriteOrCommandSince(0) {
		t.Fatal("nil ledger must report no progress")
	}
}

func TestLedgerMatchesFileReadAndWriteReceipts(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{ToolName: "read_file", Success: true, Paths: []string{`internal/tool/builtin/completestep.go`}, Read: true})
	ledger.Record(Receipt{ToolName: "write_file", Success: true, Paths: []string{`internal/evidence/evidence.go`}, Write: true})
	ledger.Record(Receipt{ToolName: "edit_file", Success: false, Paths: []string{`failed.go`}, Write: true})

	if !ledger.HasSuccessfulReadOrWrite([]string{`internal\tool\builtin\completestep.go`}) {
		t.Fatal("read receipt should verify the same path across separators")
	}
	if !ledger.HasSuccessfulWrite([]string{`internal/evidence/evidence.go`}) {
		t.Fatal("write receipt should verify written path")
	}
	if ledger.HasSuccessfulWrite([]string{`failed.go`}) {
		t.Fatal("failed write receipt must not verify")
	}
}

func TestLedgerReportsAnchorRefreshReadsAfterWrites(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{ToolName: "write_file", Success: true, Paths: []string{`src\a.go`}, Write: true})
	writeIndex, ok := ledger.LatestSuccessfulWriteIndex([]string{`src/a.go`})
	if !ok {
		t.Fatal("expected latest write index")
	}
	if ledger.HasSuccessfulAnchorRefreshReadAfter([]string{`src/a.go`}, writeIndex) {
		t.Fatal("read-after-write should be false before a read")
	}

	ledger.Record(Receipt{ToolName: "grep", Success: true, Paths: []string{`src/a.go`}, Read: true, Args: json.RawMessage(`{"path":"src/a.go","pattern":"func"}`)})
	if ledger.HasSuccessfulAnchorRefreshReadAfter([]string{`src/a.go`}, writeIndex) {
		t.Fatal("grep should not refresh anchor edit state")
	}
	ledger.Record(Receipt{ToolName: "read_file", Success: true, Paths: []string{`src/a.go`}, Read: true, Args: json.RawMessage(`{"path":"src/a.go","offset":100,"limit":20}`)})
	if ledger.HasSuccessfulAnchorRefreshReadAfter([]string{`src/a.go`}, writeIndex) {
		t.Fatal("windowed read_file should not refresh anchor edit state")
	}
	ledger.Record(Receipt{ToolName: "read_file", Success: true, Paths: []string{`src/a.go`}, Read: true, Args: json.RawMessage(`{"path":"src/a.go"}`)})
	if !ledger.HasSuccessfulAnchorRefreshReadAfter([]string{`src/a.go`}, writeIndex) {
		t.Fatal("read-after-write should be true after a successful read")
	}
}

func TestLedgerReportsFinalReadinessReceiptsAfterWriter(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{ToolName: "bash", Success: true, Command: "go test ./..."})
	ledger.Record(Receipt{ToolName: "write_file", Success: true, Paths: []string{"changed.go"}, Write: true})
	ledger.Record(Receipt{ToolName: "bash", Success: false, Command: "git diff --check"})
	ledger.Record(Receipt{ToolName: "todo_write", Success: true, Todos: []TodoItem{{Content: "Edit code", Status: "in_progress"}}})

	writer, ok := ledger.LatestSuccessfulWriterIndex()
	if !ok {
		t.Fatal("expected latest successful writer")
	}
	if ledger.HasSuccessfulCommandAfter("go test ./...", writer) {
		t.Fatal("command before latest writer must not satisfy final readiness")
	}
	if ledger.HasSuccessfulCommandAfter("git diff --check", writer) {
		t.Fatal("failed command must not satisfy final readiness")
	}
	if ledger.HasSuccessfulCompleteStepAfter(writer) {
		t.Fatal("missing complete_step must not satisfy final readiness")
	}
	if !ledger.HasSuccessfulTodoWrite() {
		t.Fatal("successful todo_write receipt should be reported")
	}

	ledger.Record(Receipt{ToolName: "bash", Success: true, Command: "git diff --check"})
	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "Edit code"})
	if !ledger.HasSuccessfulCommandAfter("git diff --check", writer) {
		t.Fatal("command after latest writer should satisfy final readiness")
	}
	if !ledger.HasSuccessfulCompleteStepAfter(writer) {
		t.Fatal("complete_step after latest writer should satisfy final readiness")
	}
}

func TestLedgerResetClearsTurnReceipts(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{ToolName: "bash", Success: true, Command: "go test ./..."})

	ledger.Reset()

	if ledger.HasSuccessfulCommand("go test ./...") {
		t.Fatal("reset should clear prior-turn evidence")
	}
}

func TestContextCarriesLedger(t *testing.T) {
	ledger := NewLedger()
	ctx := WithLedger(context.Background(), ledger)

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("ledger missing from context")
	}
	if got != ledger {
		t.Fatal("context returned a different ledger")
	}
}

func TestReceiptFromToolCallExtractsEvidenceFields(t *testing.T) {
	bash := ReceiptFromToolCall("bash", json.RawMessage(`{"command":"git diff --check"}`), true, false)
	if bash.Command != "git diff --check" {
		t.Fatalf("bash command = %q", bash.Command)
	}
	if bash.Write {
		t.Fatal("bash should not be treated as a verified file writer")
	}

	write := ReceiptFromToolCall("write_file", json.RawMessage(`{"path":"internal/evidence/evidence.go","content":"x"}`), true, false)
	if !write.Write || len(write.Paths) != 1 || write.Paths[0] != `internal/evidence/evidence.go` {
		t.Fatalf("write receipt not extracted: %+v", write)
	}

	read := ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"internal/tool/builtin/completestep.go"}`), true, true)
	if !read.Read || len(read.Paths) != 1 {
		t.Fatalf("read receipt not extracted: %+v", read)
	}

	glob := ReceiptFromToolCall("glob", json.RawMessage(`{"pattern":"**/*.go"}`), true, true)
	if !glob.Read {
		t.Fatalf("generic read-only tool should be treated as read context: %+v", glob)
	}
}

func TestReceiptFromToolCallExtractsTodoWriteItems(t *testing.T) {
	receipt := ReceiptFromToolCall("todo_write", json.RawMessage(`{"todos":[
		{"content":"Add parser","status":"in_progress","activeForm":"Adding parser"},
		{"content":"Wire parser","status":"pending","level":1}
	]}`), true, true)

	if len(receipt.Todos) != 2 {
		t.Fatalf("todos not extracted: %+v", receipt)
	}
	if receipt.Todos[0].Content != "Add parser" || receipt.Todos[0].Status != "in_progress" || receipt.Todos[0].ActiveForm != "Adding parser" {
		t.Fatalf("first todo not extracted: %+v", receipt.Todos[0])
	}
	if receipt.Todos[1].Level != 1 {
		t.Fatalf("todo level not extracted: %+v", receipt.Todos[1])
	}
}

func TestReceiptFromToolCallExtractsCompleteStep(t *testing.T) {
	receipt := ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Add parser",
		"result":"parser added",
		"evidence":[{"kind":"manual","summary":"checked manually"}]
	}`), true, true)

	if receipt.Step != "Add parser" {
		t.Fatalf("complete_step step = %q", receipt.Step)
	}
	if !receipt.StepProof {
		t.Fatalf("complete_step evidence proof not extracted: %+v", receipt)
	}
	if receipt.Read {
		t.Fatalf("complete_step should not be treated as read-only context: %+v", receipt)
	}

	missingResult := ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Add parser",
		"evidence":[{"kind":"manual","summary":"checked manually"}]
	}`), false, true)
	if missingResult.StepProof {
		t.Fatalf("complete_step without result should not count as proof: %+v", missingResult)
	}

	missingCommand := ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Add parser",
		"result":"parser added",
		"evidence":[{"kind":"verification","summary":"checked manually"}]
	}`), false, true)
	if missingCommand.StepProof {
		t.Fatalf("verification evidence without command should not count as proof: %+v", missingCommand)
	}

	emptyProof := ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Add parser",
		"result":"parser added",
		"evidence":[]
	}`), false, true)
	if emptyProof.StepProof {
		t.Fatalf("empty complete_step evidence should not count as proof: %+v", emptyProof)
	}
}

func TestReceiptFromToolCallExtractsCompleteStepIndex(t *testing.T) {
	receipt := ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step_index":2,
		"result":"done",
		"evidence":[{"kind":"manual","summary":"checked"}]
	}`), true, true)

	if receipt.Step != "2" {
		t.Fatalf("step index not extracted as step identity: %+v", receipt)
	}
	if !receipt.StepProof {
		t.Fatalf("step proof not detected: %+v", receipt)
	}
}

func TestLedgerMatchesLatestSuccessfulTodoStep(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  false,
		Todos:    []TodoItem{{Content: "Failed only", Status: "in_progress"}},
	})
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []TodoItem{
			{Content: "Add parser", Status: "in_progress", ActiveForm: "Adding parser"},
			{Content: "Wire parser", Status: "completed"},
			{Content: "Document parser", Status: "pending"},
		},
	})

	for _, step := range []string{"Add parser", "Adding parser", "2"} {
		match, ok := ledger.MatchLatestTodoStep(step)
		if !ok {
			t.Fatalf("latest todo receipt missing for %q", step)
		}
		if !match.Found {
			t.Fatalf("step %q did not match latest todo list", step)
		}
		if step == "2" && match.Content != "Wire parser" {
			t.Fatalf("numeric step matched %q, want Wire parser", match.Content)
		}
	}

	match, ok := ledger.MatchLatestTodoStep("Failed only")
	if !ok {
		t.Fatal("successful todo receipt should exist")
	}
	if match.Found {
		t.Fatal("failed todo_write receipt must not match")
	}
}

func TestMatchTodoStepToleratesCitationDrift(t *testing.T) {
	// Verbatim shape from discussion #3970: todo authored with a fullwidth
	// colon, cited back with a halfwidth one — and stuck forever.
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []TodoItem{
			{Content: "Phase 4：环境准备", Status: "completed"},
			{Content: "Phase 5：脚本编辑与执行代码", Status: "in_progress"},
			{Content: "Review notes", Status: "pending"},
		},
	})

	matches := map[string]int{
		"Phase 5: 脚本编辑与执行代码":  2,
		"phase 5：脚本编辑与执行代码":   2,
		"  Phase　5：脚本编辑与执行代码": 2,
		"脚本编辑与执行代码":           2,
		"Phase 4：环境":          1,
		"REVIEW NOTES":        3,
		"２":                   2,
	}
	for step, want := range matches {
		match, ok := ledger.MatchLatestTodoStep(step)
		if !ok || !match.Found {
			t.Fatalf("step %q should match todo %d, got found=%v", step, want, match.Found)
		}
		if match.Index != want {
			t.Errorf("step %q matched todo %d, want %d", step, match.Index, want)
		}
	}

	for _, step := range []string{"deploy backend", "代码", "Phase 9：不存在的阶段"} {
		if match, _ := ledger.MatchLatestTodoStep(step); match.Found {
			t.Errorf("step %q should not match, got todo %d (%q)", step, match.Index, match.Content)
		}
	}
}

func TestMatchTodoStepAmbiguousContainmentStaysUnmatched(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []TodoItem{
			{Content: "Deploy backend service", Status: "in_progress"},
			{Content: "Deploy backend worker", Status: "pending"},
		},
	})
	if match, _ := ledger.MatchLatestTodoStep("Deploy backend"); match.Found {
		t.Fatalf("ambiguous citation should stay unmatched, got todo %d (%q)", match.Index, match.Content)
	}
	if match, _ := ledger.MatchLatestTodoStep("Deploy backend worker"); !match.Found || match.Index != 2 {
		t.Fatal("exact citation must still resolve despite shared prefix")
	}
}

func TestLedgerRequiresCompleteStepForNewCompletedTodos(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []TodoItem{
			{Content: "Add parser", Status: "in_progress"},
			{Content: "Already done", Status: "completed"},
		},
	})

	current := []TodoItem{
		{Content: "Add parser", Status: "completed"},
		{Content: "Already done", Status: "completed"},
	}
	missing, hasBaseline := ledger.UnverifiedCompletedTodos(current)
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline")
	}
	if len(missing) != 1 || missing[0].Content != "Add parser" {
		t.Fatalf("missing = %+v, want only Add parser", missing)
	}

	ledger.Record(Receipt{ToolName: "complete_step", Success: false, Step: "Add parser"})
	missing, hasBaseline = ledger.UnverifiedCompletedTodos(current)
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline after failed complete_step")
	}
	if len(missing) != 1 || missing[0].Content != "Add parser" {
		t.Fatalf("failed complete_step without proof-bearing recovery should not authorize completion, missing = %+v", missing)
	}

	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "Add parser"})
	missing, hasBaseline = ledger.UnverifiedCompletedTodos(current)
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline after successful complete_step")
	}
	if len(missing) != 0 {
		t.Fatalf("successful complete_step should authorize completion, missing = %+v", missing)
	}
}

func TestLedgerMatchesCompletionByActiveFormAndNumber(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []TodoItem{
			{Content: "Add parser", Status: "in_progress", ActiveForm: "Adding parser"},
			{Content: "Wire parser", Status: "in_progress"},
		},
	})

	current := []TodoItem{
		{Content: "Add parser", Status: "completed", ActiveForm: "Adding parser"},
		{Content: "Wire parser", Status: "in_progress"},
	}
	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "Adding parser"})
	missing, hasBaseline := ledger.UnverifiedCompletedTodos(current)
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline")
	}
	if len(missing) != 0 {
		t.Fatalf("activeForm complete_step should authorize completion, missing = %+v", missing)
	}

	current = []TodoItem{
		{Content: "Add parser", Status: "completed", ActiveForm: "Adding parser"},
		{Content: "Wire parser", Status: "completed"},
	}
	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "2"})
	missing, hasBaseline = ledger.UnverifiedCompletedTodos(current)
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline")
	}
	if len(missing) != 0 {
		t.Fatalf("numeric complete_step should authorize completion, missing = %+v", missing)
	}
}

func TestLedgerNumericCompleteStepDoesNotAuthorizeReplacedTodo(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []TodoItem{{Content: "Add parser", Status: "in_progress"}},
	})
	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "1"})

	missing, hasBaseline := ledger.UnverifiedCompletedTodos([]TodoItem{
		{Content: "Ship parser", Status: "completed"},
	})
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline")
	}
	if len(missing) != 1 || missing[0].Content != "Ship parser" {
		t.Fatalf("numeric complete_step should not authorize a replaced todo, missing = %+v", missing)
	}
}

func TestLedgerNumericCompleteStepFollowsReorderedSignedTodo(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []TodoItem{
			{Content: "Add parser", Status: "in_progress"},
			{Content: "Write tests", Status: "pending"},
		},
	})
	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "1"})

	missing, hasBaseline := ledger.UnverifiedCompletedTodos([]TodoItem{
		{Content: "Write tests", Status: "pending"},
		{Content: "Add parser", Status: "completed"},
	})
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline")
	}
	if len(missing) != 0 {
		t.Fatalf("numeric complete_step should follow the signed todo identity after reorder, missing = %+v", missing)
	}
}

func TestLedgerNoBaselineDoesNotConstrainCompletedTodos(t *testing.T) {
	ledger := NewLedger()
	missing, hasBaseline := ledger.UnverifiedCompletedTodos([]TodoItem{
		{Content: "Add parser", Status: "completed"},
	})

	if hasBaseline {
		t.Fatal("empty ledger should not report a prior todo_write baseline")
	}
	if len(missing) != 0 {
		t.Fatalf("no baseline should not report missing completions, got %+v", missing)
	}
}

func TestValidateSerialTodosRejectsInvalidOrdering(t *testing.T) {
	tests := []struct {
		name  string
		todos []TodoItem
		want  string
	}{
		{
			name: "completed after current",
			todos: []TodoItem{
				{Content: "first", Status: "in_progress"},
				{Content: "second", Status: "completed"},
			},
			want: "completed after unfinished",
		},
		{
			name: "multiple current items",
			todos: []TodoItem{
				{Content: "first", Status: "in_progress"},
				{Content: "second", Status: "in_progress"},
			},
			want: "second in_progress",
		},
		{
			name:  "pending without current",
			todos: []TodoItem{{Content: "first", Status: "pending"}},
			want:  "no in_progress",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSerialTodos(tc.todos); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateSerialTodos() error = %v, want %q", err, tc.want)
			}
		})
	}

	valid := []TodoItem{
		{Content: "done", Status: "completed"},
		{Content: "current", Status: "in_progress"},
		{Content: "later", Status: "pending"},
	}
	if err := ValidateSerialTodos(valid); err != nil {
		t.Fatalf("valid serial list rejected: %v", err)
	}
}

func TestNormalizeSerialTodosRepairsLegacyOutOfOrderState(t *testing.T) {
	got := NormalizeSerialTodos([]TodoItem{
		{Content: "first", Status: "in_progress"},
		{Content: "second", Status: "completed"},
		{Content: "third", Status: "in_progress"},
	})
	want := []string{"in_progress", "pending", "pending"}
	for i := range want {
		if got[i].Status != want[i] {
			t.Fatalf("todo %d status = %q, want %q: %+v", i+1, got[i].Status, want[i], got)
		}
	}
}

func TestRepairSerialTodoUpdateWithDeferredKeepsStrictValidatorNarrow(t *testing.T) {
	previous := []TodoItem{
		{Content: "A", Status: "in_progress", StepID: "a"},
		{Content: "B", Status: "pending", StepID: "b"},
		{Content: "C", Status: "pending", StepID: "c"},
	}
	next := []TodoItem{
		{Content: "A", Status: "in_progress", StepID: "a"},
		{Content: "B", Status: "completed", StepID: "b"},
		{Content: "C", Status: "completed", StepID: "c"},
	}
	if err := ValidateSerialTodos(next); err == nil {
		t.Fatal("the strict validator unexpectedly accepted out-of-order completions")
	}

	canonical, deferred, repaired := RepairSerialTodoUpdateWithDeferred(previous, next)
	if !repaired {
		t.Fatal("the narrow pending-completion repair was not applied")
	}
	wantCanonical := []TodoItem{
		{Content: "A", Status: "in_progress", StepID: "a"},
		{Content: "B", Status: "pending", StepID: "b"},
		{Content: "C", Status: "pending", StepID: "c"},
	}
	if !reflect.DeepEqual(canonical, wantCanonical) {
		t.Fatalf("canonical = %+v, want %+v", canonical, wantCanonical)
	}
	if got := []string{deferred[0].StepID, deferred[1].StepID}; !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("deferred identities = %v, want [b c]", got)
	}
	if err := ValidateSerialTodos(canonical); err != nil {
		t.Fatalf("repaired canonical list is not strict serial state: %v", err)
	}

	for _, tc := range []struct {
		name     string
		previous []TodoItem
		next     []TodoItem
	}{
		{
			name:     "reorder",
			previous: previous,
			next: []TodoItem{
				{Content: "A", Status: "in_progress", StepID: "a"},
				{Content: "C", Status: "completed", StepID: "c"},
				{Content: "B", Status: "completed", StepID: "b"},
			},
		},
		{
			name:     "remove",
			previous: previous,
			next: []TodoItem{
				{Content: "A", Status: "in_progress", StepID: "a"},
				{Content: "B", Status: "completed", StepID: "b"},
			},
		},
		{
			name:     "duplicate identity",
			previous: previous,
			next: []TodoItem{
				{Content: "A", Status: "in_progress", StepID: "a"},
				{Content: "B", Status: "completed", StepID: "b"},
				{Content: "B again", Status: "completed", StepID: "b"},
			},
		},
		{
			name: "completed regression",
			previous: []TodoItem{
				{Content: "done", Status: "completed", StepID: "done"},
				{Content: "A", Status: "in_progress", StepID: "a"},
				{Content: "B", Status: "pending", StepID: "b"},
			},
			next: []TodoItem{
				{Content: "done", Status: "pending", StepID: "done"},
				{Content: "A", Status: "in_progress", StepID: "a"},
				{Content: "B", Status: "completed", StepID: "b"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, repaired := RepairSerialTodoUpdateWithDeferred(tc.previous, tc.next); repaired {
				t.Fatalf("invalid %s update was repaired", tc.name)
			}
		})
	}
}

func TestRepairSerialTodoUpdateUsesNormalizedTextFallback(t *testing.T) {
	previous := []TodoItem{
		{Content: "A", Status: "in_progress"},
		{Content: "Run   Tests", Status: "pending"},
	}
	next := []TodoItem{
		{Content: "A", Status: "in_progress"},
		{Content: "run tests", Status: "completed"},
	}
	canonical, deferred, repaired := RepairSerialTodoUpdateWithDeferred(previous, next)
	if !repaired || len(deferred) != 1 || deferred[0].Content != "run tests" {
		t.Fatalf("normalized text fallback = canonical:%+v deferred:%+v repaired:%t", canonical, deferred, repaired)
	}
	if canonical[1].Status != "pending" {
		t.Fatalf("fallback-matched completion was not restored to pending: %+v", canonical)
	}
}

func TestValidateSerialTodosAcceptsPhaseChains(t *testing.T) {
	tests := []struct {
		name  string
		todos []TodoItem
	}{
		{
			name: "entered phase with an active sub-step",
			todos: []TodoItem{
				{Content: "Phase", Status: "pending"},
				{Content: "sub one", Status: "in_progress", Level: 1},
			},
		},
		{
			name: "single current item mid-chain",
			todos: []TodoItem{
				{Content: "Phase", Status: "pending"},
				{Content: "sub one", Status: "completed", Level: 1},
				{Content: "sub two", Status: "in_progress", Level: 1},
				{Content: "sub three", Status: "pending", Level: 1},
				{Content: "Later phase", Status: "pending"},
				{Content: "later sub", Status: "pending", Level: 1},
			},
		},
		{
			name: "phase awaiting sign-off after its sub-steps",
			todos: []TodoItem{
				{Content: "Phase", Status: "in_progress"},
				{Content: "sub one", Status: "completed", Level: 1},
				{Content: "sub two", Status: "completed", Level: 1},
				{Content: "next", Status: "pending"},
			},
		},
		{
			name: "completed phase segment before the current one",
			todos: []TodoItem{
				{Content: "Phase", Status: "completed"},
				{Content: "sub one", Status: "completed", Level: 1},
				{Content: "Second phase", Status: "pending"},
				{Content: "sub two", Status: "in_progress", Level: 1},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSerialTodos(tc.todos); err != nil {
				t.Fatalf("valid phase chain rejected: %v", err)
			}
		})
	}
}

func TestValidateSerialTodosRejectsInvalidPhaseChains(t *testing.T) {
	tests := []struct {
		name  string
		todos []TodoItem
		want  string
	}{
		{
			name: "phase completed before its sub-steps",
			todos: []TodoItem{
				{Content: "Phase", Status: "completed"},
				{Content: "sub one", Status: "in_progress", Level: 1},
			},
			want: "sub-step 2 \"sub one\" is unfinished",
		},
		{
			name: "phase in_progress while sub-steps are unfinished",
			todos: []TodoItem{
				{Content: "Phase", Status: "in_progress"},
				{Content: "sub one", Status: "pending", Level: 1},
			},
			want: "cannot be in_progress while sub-step 2",
		},
		{
			name: "phase and sub-step both in_progress",
			todos: []TodoItem{
				{Content: "Phase", Status: "in_progress"},
				{Content: "sub one", Status: "in_progress", Level: 1},
			},
			want: "second in_progress item",
		},
		{
			name: "orphan sub-step with no phase above it",
			todos: []TodoItem{
				{Content: "sub one", Status: "in_progress", Level: 1},
				{Content: "Next", Status: "pending"},
			},
			want: "no phase above it",
		},
		{
			name: "completed segment after the current chain",
			todos: []TodoItem{
				{Content: "Phase", Status: "pending"},
				{Content: "sub one", Status: "in_progress", Level: 1},
				{Content: "Second phase", Status: "completed"},
				{Content: "sub two", Status: "completed", Level: 1},
			},
			want: "completed after unfinished",
		},
		{
			name: "stale sub-step progress before the current item",
			todos: []TodoItem{
				{Content: "Phase", Status: "pending"},
				{Content: "sub one", Status: "completed", Level: 1},
				{Content: "sub two", Status: "pending", Level: 1},
				{Content: "Second phase", Status: "pending"},
				{Content: "sub three", Status: "in_progress", Level: 1},
			},
			want: "in_progress after pending work",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSerialTodos(tc.todos); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateSerialTodos() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeSerialTodosRepairsPhaseChains(t *testing.T) {
	got := NormalizeSerialTodos([]TodoItem{
		{Content: "Phase", Status: "completed"},
		{Content: "sub one", Status: "completed", Level: 1},
		{Content: "sub two", Status: "pending", Level: 1},
		{Content: "Second phase", Status: "completed"},
		{Content: "sub three", Status: "completed", Level: 1},
	})
	want := []string{"pending", "completed", "in_progress", "pending", "pending"}
	for i := range want {
		if got[i].Status != want[i] {
			t.Fatalf("todo %d status = %q, want %q: %+v", i+1, got[i].Status, want[i], got)
		}
	}

	signable := NormalizeSerialTodos([]TodoItem{
		{Content: "Phase", Status: "pending"},
		{Content: "sub one", Status: "completed", Level: 1},
		{Content: "sub two", Status: "completed", Level: 1},
	})
	if signable[0].Status != "in_progress" {
		t.Fatalf("phase with completed sub-steps should normalize to in_progress for sign-off: %+v", signable)
	}
}

func TestAdvanceSerialTodoWalksPhaseChain(t *testing.T) {
	todos := []TodoItem{
		{Content: "Phase", Status: "pending"},
		{Content: "sub one", Status: "in_progress", Level: 1},
		{Content: "sub two", Status: "pending", Level: 1},
		{Content: "Second phase", Status: "pending"},
		{Content: "sub three", Status: "pending", Level: 1},
	}
	statuses := func() []string {
		out := make([]string, len(todos))
		for i, todo := range todos {
			out[i] = todo.Status
		}
		return out
	}

	if AdvanceSerialTodo(todos, 0) {
		t.Fatalf("pending phase completed ahead of its sub-steps: %v", statuses())
	}
	if !AdvanceSerialTodo(todos, 1) {
		t.Fatal("current sub-step did not complete")
	}
	if got, want := statuses(), []string{"pending", "completed", "in_progress", "pending", "pending"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after first sub-step statuses = %v, want %v", got, want)
	}
	if !AdvanceSerialTodo(todos, 2) {
		t.Fatal("last sub-step did not complete")
	}
	if got := todos[0].Status; got != "in_progress" {
		t.Fatalf("phase status after its sub-steps = %q, want in_progress for sign-off", got)
	}
	if !AdvanceSerialTodo(todos, 0) {
		t.Fatal("phase with completed sub-steps did not complete")
	}
	if got, want := statuses(), []string{"completed", "completed", "completed", "pending", "in_progress"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after phase sign-off statuses = %v, want next sub-step promoted under its pending phase: %v", got, want)
	}
	if !AdvanceSerialTodo(todos, 4) {
		t.Fatal("second chain sub-step did not complete")
	}
	if got := todos[3].Status; got != "in_progress" {
		t.Fatalf("second phase after its sub-step = %q, want in_progress", got)
	}
	if !AdvanceSerialTodo(todos, 3) {
		t.Fatal("second phase did not sign off")
	}
	for i, todo := range todos {
		if todo.Status != "completed" {
			t.Fatalf("todo %d = %+v, want completed", i+1, todo)
		}
	}
}

func TestAdvanceSerialTodoAdvancesOrphanSubStep(t *testing.T) {
	todos := []TodoItem{
		{Content: "orphan sub", Status: "in_progress", Level: 1},
		{Content: "Next step", Status: "pending"},
	}
	if !AdvanceSerialTodo(todos, 0) {
		t.Fatal("orphan sub-step did not complete")
	}
	if todos[0].Status != "completed" || todos[1].Status != "in_progress" {
		t.Fatalf("orphan completion must promote the next pending unit: %+v", todos)
	}

	chained := []TodoItem{
		{Content: "orphan sub", Status: "in_progress", Level: 1},
		{Content: "Phase", Status: "pending"},
		{Content: "sub one", Status: "pending", Level: 1},
	}
	if !AdvanceSerialTodo(chained, 0) {
		t.Fatal("orphan sub-step before a phase did not complete")
	}
	if chained[1].Status != "pending" || chained[2].Status != "in_progress" {
		t.Fatalf("orphan completion before a phase must promote the phase's first sub-step: %+v", chained)
	}
}

func TestSuccessfulProgressSignaturesIgnoreExactRepeatsAndTrackTodoClear(t *testing.T) {
	ledger := NewLedger()
	read := ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"a.go"}`), true, true)
	read.OutputBytes = 10
	ledger.Record(read)
	ledger.Record(read)
	ledger.Record(ReceiptFromToolCall("todo_write", json.RawMessage(`{"todos":[]}`), true, true))
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"a.go","old_string":"a","new_string":"b"}`), true, false))
	ledger.Record(ReceiptFromToolCall("bash", json.RawMessage(`{"command":"go test ./..."}`), true, false))

	sigs := ledger.SuccessfulProgressSignaturesSince(0)
	if len(sigs) != 5 {
		t.Fatalf("progress signatures = %d, want two reads plus todo clear, mutation, and command", len(sigs))
	}
	if sigs[0] != sigs[1] {
		t.Fatalf("exact repeated reads should have the same signature: %q != %q", sigs[0], sigs[1])
	}
	if sigs[1] == sigs[2] || sigs[2] == sigs[3] || sigs[3] == sigs[4] {
		t.Fatalf("distinct host work collapsed to one signature: %v", sigs)
	}
}

func TestLedgerNumericCompleteStepAuthorizesRephrasedTodo(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []TodoItem{
			{Content: "Add parser", Status: "in_progress"},
			{Content: "Write tests", Status: "pending"},
		},
	})
	ledger.Record(Receipt{ToolName: "complete_step", Success: true, Step: "1"})

	// The model rephrased item 1 (added detail) but it's the same task.
	missing, hasBaseline := ledger.UnverifiedCompletedTodos([]TodoItem{
		{Content: "Add parser with streaming support", Status: "completed"},
		{Content: "Write tests", Status: "pending"},
	})
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline")
	}
	if len(missing) != 0 {
		t.Fatalf("rephrased todo at same index should be authorized by content overlap, missing = %+v", missing)
	}

	// The model also rephrased item 2; still ok because the new text contains the old.
	missing, hasBaseline = ledger.UnverifiedCompletedTodos([]TodoItem{
		{Content: "Add parser with streaming support", Status: "completed"},
		{Content: "Write tests and benchmarks", Status: "completed"},
	})
	if !hasBaseline {
		t.Fatal("expected prior todo_write baseline for second rephrase")
	}
	// Item 1 is already authorized; item 2 is also rephrased but lacks a
	// complete_step — so it should still be flagged.
	if len(missing) != 1 || missing[0].Content != "Write tests and benchmarks" {
		t.Fatalf("rephrased todo without complete_step should still be missing, got %+v", missing)
	}
}

func TestToolCallMutatesForDeliveryProfile(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     string
		readOnly bool
		want     bool
	}{
		{name: "trusted reader", toolName: "read_file", args: `{"path":"a.go"}`, readOnly: true},
		{name: "file writer", toolName: "edit_file", args: `{"path":"a.go"}`, want: true},
		{name: "delegated task meta", toolName: "task", args: `{"prompt":"fix it"}`},
		{name: "run_skill meta", toolName: "run_skill", args: `{"name":"review"}`},
		{name: "review meta", toolName: "review", args: `{"task":"review changes"}`},
		{name: "security_review meta", toolName: "security_review", args: `{"task":"security"}`},
		{name: "use_capability meta", toolName: "use_capability", args: `{"action":"inspect","capability_id":"mcp-server:github"}`},
		{name: "test command", toolName: "bash", args: `{"command":"go test ./..."}`},
		{name: "npx vitest pipeline", toolName: "bash", args: `{"command":"npx vitest run src/lib/foo.test.ts 2>&1 | tail -40"}`},
		{name: "node syntax check", toolName: "bash", args: `{"command":"node --check app.js"}`},
		{name: "node syntax check pipeline", toolName: "bash", args: `{"command":"tail -n +2 app.html | head -n 20 | node --check"}`},
		{name: "node eval stays opaque", toolName: "bash", args: `{"command":"node -e 'console.log(1)'"}`, want: true},
		{name: "node conditions flag stays opaque", toolName: "bash", args: `{"command":"node -C production server.js"}`, want: true},
		{name: "node test runner", toolName: "bash", args: `{"command":"node --test"}`},
		{name: "node test snapshot update stays opaque", toolName: "bash", args: `{"command":"node --test --test-update-snapshots"}`, want: true},
		{name: "node test reporter file stays opaque", toolName: "bash", args: `{"command":"node --test --test-reporter=junit --test-reporter-destination=result.txt"}`, want: true},
		{name: "node test rerun state stays opaque", toolName: "bash", args: `{"command":"node --test --test-rerun-failures=state.json"}`, want: true},
		{name: "node test cpu profile stays opaque", toolName: "bash", args: `{"command":"node --test --cpu-prof"}`, want: true},
		{name: "diff review", toolName: "bash", args: `{"command":"git diff --check"}`},
		{name: "PowerShell network probe does not write workspace", toolName: "bash", args: `{"command":"Test-NetConnection -ComputerName example.com -Port 443"}`},
		{name: "resolved read-only substitution", toolName: "bash", args: `{"command":"basename \"$(pwd)\""}`},
		{name: "writer in substitution", toolName: "bash", args: `{"command":"basename \"$(touch out)\""}`, want: true},
		{name: "formatter write", toolName: "bash", args: `{"command":"gofmt -w internal/a.go"}`, want: true},
		{name: "file redirect", toolName: "bash", args: `{"command":"printf x > generated.txt"}`, want: true},
		{name: "compound verification", toolName: "bash", args: `{"command":"go test ./... 2>&1 | tail -20"}`},
		{name: "pytest snapshot update stays opaque", toolName: "bash", args: `{"command":"pytest --snapshot-update"}`, want: true},
		{name: "pytest junitxml report stays opaque", toolName: "bash", args: `{"command":"pytest --junitxml=report.xml"}`, want: true},
		{name: "gotestsum junitfile stays opaque", toolName: "bash", args: `{"command":"gotestsum --junitfile out.xml ./..."}`, want: true},
		{name: "go test coverprofile stays opaque", toolName: "bash", args: `{"command":"go test -coverprofile=cover.out ./..."}`, want: true},
		{name: "go test blockprofile stays opaque", toolName: "bash", args: `{"command":"go test -blockprofile=block.out ./..."}`, want: true},
		{name: "go test trace stays opaque", toolName: "bash", args: `{"command":"go test -trace trace.out ./..."}`, want: true},
		{name: "go test compile binary stays opaque", toolName: "bash", args: `{"command":"go test -c ./internal/evidence"}`, want: true},
		{name: "go test dotted cpuprofile stays opaque", toolName: "bash", args: `{"command":"go test ./internal/evidence -test.cpuprofile=cpu.out -count=1"}`, want: true},
		{name: "go test artifacts stays opaque", toolName: "bash", args: `{"command":"go test -artifacts ./..."}`, want: true},
		{name: "jest output file stays opaque", toolName: "bash", args: `{"command":"npm test -- --json --outputFile=result.json"}`, want: true},
		{name: "mypy report stays opaque", toolName: "bash", args: `{"command":"mypy --txt-report reports src/"}`, want: true},
		{name: "plain pytest", toolName: "bash", args: `{"command":"pytest"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolCallMutates(tt.toolName, json.RawMessage(tt.args), tt.readOnly); got != tt.want {
				t.Fatalf("ToolCallMutates(%q, %s) = %v, want %v", tt.toolName, tt.args, got, tt.want)
			}
		})
	}
}

func TestRunnerWriteOutputFlagsCannotMasqueradeAsVerification(t *testing.T) {
	// Snapshot flags rewrite checked-in fixtures and report/profile flags
	// write explicit output paths; both must stay opaque mutations so the
	// files they produce still require review and sign-off.
	for _, command := range []string{
		"pytest --snapshot-update",
		"pytest --junitxml=report.xml",
		"mypy --junit-xml report.xml src/",
		"gotestsum --junitfile out.xml ./...",
		"go test -coverprofile=cover.out ./...",
		"go test --coverprofile cover.out ./...",
		"go test -blockprofile=block.out ./...",
		"go test -mutexprofile mutex.out ./...",
		"go test -trace trace.out ./...",
		"go test -c ./internal/evidence",
		"go test -o evidence.test -c ./internal/evidence",
		"go test ./internal/evidence -test.cpuprofile=cpu.out -count=1",
		"go test -test.trace trace.out ./...",
		"go test -artifacts ./...",
		"go test ./... -args -test.testlogfile=log.txt",
		"go test -test.gocoverdir=covdir ./...",
		"gotestsum -- -test.coverprofile=cover.out ./...",
		"npm test -- --updateSnapshot",
		"npx vitest run --update",
		"npx vitest run --update=true",
		"npx vitest run -u=true",
		"npx jest --updateSnapshot=true",
		"npx vitest run --coverage",
		"npx vitest run --outputFile=result.json",
		"npx --yes vitest run",
		"npm test -- --json --outputFile=result.json",
		"yarn test --outputFile.json=result.json",
		"pytest --report-log=log.jsonl",
		"mypy --txt-report reports src/",
		"mypy --html-report html src/",
		"mypy --xml-report=reports src/",
		"mypy --cobertura-xml-report reports src/",
	} {
		if bashCommandIsVerification(command) {
			t.Fatalf("%q writes files and must not be classified as verification", command)
		}
		if !ToolCallMutates("bash", json.RawMessage(`{"command":"`+command+`"}`), false) {
			t.Fatalf("%q must remain an opaque mutation", command)
		}
	}
	for _, command := range []string{
		"pytest",
		"gotestsum ./...",
		"go test -cover ./...",
		"go test -count=1 ./...",
		"go test -test.v -test.run TestFoo ./...",
		"pytest --trace",
		"npm test -- --json",
		"npx vitest run src/lib/foo.test.ts 2>&1 | tail -40",
		"npx jest src/lib/foo.test.ts",
		"mypy src/",
		"mypy --strict src/",
	} {
		if !bashCommandIsVerification(command) {
			t.Fatalf("%q should remain a verification command", command)
		}
	}
}

func TestToolCallRequiresAcceptanceCriteriaForExecutionCommands(t *testing.T) {
	if !ToolCallRequiresAcceptanceCriteria("bash", json.RawMessage(`{"command":"go test ./..."}`), false) {
		t.Fatal("verification command should require delivery acceptance criteria")
	}
	if !ToolCallRequiresAcceptanceCriteria("bash", json.RawMessage(`{"command":"npm run test"}`), false) {
		t.Fatal("npm run test should require delivery acceptance criteria")
	}
	if !ToolCallRequiresAcceptanceCriteria("bash", json.RawMessage(`{"command":"git diff --check"}`), false) {
		t.Fatal("git diff --check is a verification command and should require acceptance criteria")
	}
	if !ToolCallRequiresAcceptanceCriteria("bash", json.RawMessage(`{"command":"node --check app.js"}`), false) {
		t.Fatal("node --check is a verification command and should require acceptance criteria")
	}
}

func TestBashToolCallMixesMutationAndVerification(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{
			name:    "temporary JavaScript extraction",
			command: `python3 -c 'open("/tmp/snake_check.js","w").write("x")' && node --check /tmp/snake_check.js`,
			want:    true,
		},
		{name: "generated code before tests", command: "go generate ./... && go test ./...", want: true},
		{name: "read-only extraction pipeline", command: "tail -n +2 snake.js | head -n 20 | node --check -"},
		{name: "plain verifier", command: "go test ./..."},
		{name: "plain mutation", command: "gofmt -w main.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"command": tt.command})
			if err != nil {
				t.Fatal(err)
			}
			if got := BashToolCallMixesMutationAndVerification(args); got != tt.want {
				t.Fatalf("BashToolCallMixesMutationAndVerification(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestBashToolCallMasksVerificationExit(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: `tail -n +2 snake.html | head -n 20 | node --check -; echo "EXIT: $?"`, want: true},
		{command: `go test ./...; printf 'status=%s\n' "$?"`, want: true},
		{command: `tail -n +2 snake.html | head -n 20 | node --check -`},
		{command: `go test ./...`},
		{command: `echo "$?"`},
		{command: `echo done; go test ./...`},
	}
	for _, tt := range tests {
		args, err := json.Marshal(map[string]string{"command": tt.command})
		if err != nil {
			t.Fatal(err)
		}
		if got := BashToolCallMasksVerificationExit(args); got != tt.want {
			t.Errorf("BashToolCallMasksVerificationExit(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestBashToolCallUsesOpaqueInlineInterpreter(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: `node -e 'require("fs").readFileSync("snake.html")'`, want: true},
		{command: `node --input-type=module --eval 'console.log(1)'`, want: true},
		{command: `python3 -c 'print(open("snake.html").read())'`, want: true},
		{command: `ruby -e 'puts 1'`, want: true},
		{command: `php -r 'echo 1;'`, want: true},
		{command: `deno eval 'console.log(1)'`, want: true},
		{command: "tail -n +2 snake.html | node --check -"},
		{command: "node --test"},
		{command: "python3 -m pytest"},
		{command: "node scripts/check.js"},
	}
	for _, tt := range tests {
		args, err := json.Marshal(map[string]string{"command": tt.command})
		if err != nil {
			t.Fatal(err)
		}
		if got := BashToolCallUsesOpaqueInlineInterpreter(args); got != tt.want {
			t.Errorf("BashToolCallUsesOpaqueInlineInterpreter(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestLedgerDeliverySignoffAcceptsNodeSyntaxCheckAfterMutation(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"app.js"}`), true, false))
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok {
		t.Fatal("expected mutation receipt")
	}
	ledger.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"app.js"}`), true, true))
	command := "node --check app.js"
	ledger.Record(ReceiptFromToolCall("bash", json.RawMessage(`{"command":"node --check app.js"}`), true, false))
	ledger.Record(ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Check JavaScript",
		"result":"syntax valid",
		"evidence":[{"kind":"verification","summary":"syntax valid","command":"node --check app.js"}]
	}`), true, true))

	if !IsVerificationCommand(command) {
		t.Fatal("node --check should be recognized as a delivery verification")
	}
	if latest, ok := ledger.LatestSuccessfulMutationIndex(); !ok || latest != mutation {
		t.Fatalf("node --check moved latest mutation from %d to %d (ok=%v)", mutation, latest, ok)
	}
	if !ledger.HasSuccessfulReviewAfter(mutation) {
		t.Fatal("expected post-mutation read to satisfy review")
	}
	if !ledger.HasSuccessfulDeliverySignoffAfter(mutation) {
		t.Fatal("expected node --check to satisfy delivery sign-off")
	}
}

func TestNodeEvalCannotMasqueradeAsDeliveryVerification(t *testing.T) {
	command := `node -e 'require("fs").readFileSync("app.js")'`
	if IsVerificationCommand(command) {
		t.Fatal("arbitrary node eval must not be recognized as delivery verification")
	}
	if !ToolCallMutates("bash", json.RawMessage(`{"command":"node -e 'require(\"fs\").readFileSync(\"app.js\")'"}`), false) {
		t.Fatal("arbitrary node eval must remain an opaque mutation")
	}
}

func TestNodeConditionsFlagCannotMasqueradeAsDeliveryVerification(t *testing.T) {
	// Node CLI flags are case-sensitive: -C is --conditions and executes the
	// target script, unlike the syntax-only -c/--check.
	command := "node -C production server.js"
	if IsVerificationCommand(command) {
		t.Fatal("node -C (--conditions) executes the script and must not be recognized as delivery verification")
	}
	if !ToolCallMutates("bash", json.RawMessage(`{"command":"node -C production server.js"}`), false) {
		t.Fatal("node -C (--conditions) must remain an opaque mutation")
	}
}

func TestNodeTestRunnerWriteFlagsCannotMasqueradeAsDeliveryVerification(t *testing.T) {
	if !IsVerificationCommand("node --test") {
		t.Fatal("plain node --test should be recognized as a delivery verification")
	}
	// Test-runner state/report flags and Node runtime profiling/tracing flags
	// create or update files. They must stay opaque mutations so those files
	// still require review and sign-off.
	for _, command := range []string{
		"node --test --test-update-snapshots",
		"node --test --test-reporter=junit --test-reporter-destination=result.txt",
		"node --test --test-reporter junit --test-reporter-destination result.txt",
		"node --test --test-rerun-failures=state.json",
		"node --test --test-rerun-failures state.json",
		"node --test --cpu-prof",
		"node --test --heap-prof",
		"node --test --heapsnapshot-near-heap-limit=1",
		"node --test --heapsnapshot-signal=SIGUSR2",
		"node --test --localstorage-file=localstorage.json",
		"node --test --perf-basic-prof",
		"node --test --perf-basic-prof-only-functions",
		"node --test --perf-prof",
		"node --test --prof",
		"node --test --redirect-warnings=warnings.log",
		"node --test --report-on-fatalerror",
		"node --test --report-on-signal",
		"node --test --report-uncaught-exception",
		"node --test --tls-keylog=tls.log",
		"node --test --trace-events-enabled",
	} {
		if IsVerificationCommand(command) {
			t.Fatalf("%q writes files and must not be recognized as delivery verification", command)
		}
	}
}

func TestLedgerReviewAfterRestoredCheckpointBaseline(t *testing.T) {
	// A negative index is the restored-checkpoint baseline: the mutation
	// happened before a controller rebuild or cold resume, so its receipt (and
	// touched paths) are not in this ledger. Fresh review-shaped receipts must
	// still be able to satisfy the review gate.
	if NewLedger().HasSuccessfulReviewAfter(-1) {
		t.Fatal("an empty ledger must not satisfy the checkpoint-baseline review")
	}

	read := NewLedger()
	read.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"internal/parser.go"}`), true, true))
	if !read.HasSuccessfulReviewAfter(-1) {
		t.Fatal("a successful read must satisfy review for a restored mutation baseline")
	}

	diff := NewLedger()
	diff.Record(ReceiptFromToolCall("bash", json.RawMessage(`{"command":"git diff"}`), true, false))
	if !diff.HasSuccessfulReviewAfter(-1) {
		t.Fatal("a git diff inspection must satisfy review for a restored mutation baseline")
	}

	failed := NewLedger()
	failed.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"internal/parser.go"}`), false, true))
	if failed.HasSuccessfulReviewAfter(-1) {
		t.Fatal("a failed read must not satisfy the checkpoint-baseline review")
	}

	opaque := NewLedger()
	opaque.Record(ReceiptFromToolCall("bash", json.RawMessage(`{"command":"echo done"}`), true, false))
	if opaque.HasSuccessfulReviewAfter(-1) {
		t.Fatal("a non-review command must not satisfy the checkpoint-baseline review")
	}
}

func TestLedgerDeliverySignoffRequiresPostMutationVerificationAndReview(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("todo_write", json.RawMessage(`{"todos":[{"content":"Ship parser","status":"in_progress"}]}`), true, true))
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"internal/parser.go"}`), true, false))
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok {
		t.Fatal("expected mutation receipt")
	}
	ledger.Record(ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"internal/parser.go"}`), true, true))
	ledger.Record(ReceiptFromToolCall("bash", json.RawMessage(`{"command":"go test ./internal/..."}`), true, false))
	ledger.Record(ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Ship parser",
		"result":"parser shipped",
		"evidence":[{"kind":"verification","summary":"tests passed","command":"go test ./internal/..."}]
	}`), true, true))

	if !ledger.HasSuccessfulAcceptanceCriteria() {
		t.Fatal("expected non-empty todo_write to establish acceptance criteria")
	}
	if !ledger.HasSuccessfulReviewAfter(mutation) {
		t.Fatal("expected post-mutation read to satisfy review")
	}
	if !ledger.HasSuccessfulDeliverySignoffAfter(mutation) {
		t.Fatal("expected post-mutation verification cited by complete_step")
	}
}

func TestLedgerDeliverySignoffRejectsPreMutationVerification(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("bash", json.RawMessage(`{"command":"go test ./..."}`), true, false))
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"main.go"}`), true, false))
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok {
		t.Fatal("expected mutation receipt")
	}
	ledger.Record(ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"change",
		"result":"changed",
		"evidence":[{"kind":"verification","summary":"tests passed before edit","command":"go test ./..."}]
	}`), true, true))
	if ledger.HasSuccessfulDeliverySignoffAfter(mutation) {
		t.Fatal("pre-mutation verification must not sign off changed work")
	}
}

func TestLedgerDeliverySignoffRejectsInspectionCommandMasqueradingAsVerification(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"main.go"}`), true, false))
	mutation, ok := ledger.LatestSuccessfulMutationIndex()
	if !ok {
		t.Fatal("expected mutation receipt")
	}
	ledger.Record(ReceiptFromToolCall("bash", json.RawMessage(`{"command":"git status --short"}`), true, false))
	ledger.Record(ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"change",
		"result":"changed",
		"evidence":[{"kind":"verification","summary":"claimed verification","command":"git status --short"}]
	}`), true, true))
	if ledger.HasSuccessfulDeliverySignoffAfter(mutation) {
		t.Fatal("inspection-only git status must not count as delivery verification")
	}
}
