package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// sessionReset names the fields a new conversation starts from. taskRuntime
// gets this for free — one assignment zeroes anything unlisted — but a struct
// holding atomics and mutexes cannot be assigned, so reset must name each field
// and this list is what keeps it honest.
var sessionReset = map[string]bool{
	"mu":               true,
	"conversation":     true,
	"output":           true,
	"cacheHit":         true,
	"cacheMiss":        true,
	"missingReasoning": true,
	// A strong-projection repair belongs to the corrupted conversation; a new
	// conversation starts without it.
	"reasoningReplayStrongProjection":       true,
	"reasoningReplayStrongProjectionAnchor": true,
	"compactionMu":                          true,
	"compactionState":                       true,
	"cacheState":                            true,
	"compaction":                            true,
	"deferredTodoCompletions":               true,
}

// sessionCarryOver names the fields reset deliberately leaves alone, each with
// an owner that rebinds it. Being on this list is a claim that someone else
// sets the field for the new conversation — not that it does not matter.
var sessionCarryOver = map[string]bool{
	"compactionRunMu": true, // a singleflight latch, not conversation state
	"path":            true, // preflight rebinds on the next transcript bind
	"checkpointState": true, // preflight rebinds with the transcript
	"todoMu":          true,
	"todoState":       true, // SetSession rebuilds it from the new snapshot
	// lastPrefixShape survives the swap today; the next request compares its
	// prefix against the replaced conversation's shape. Left as found here.
	"lastPrefixShape":     true,
	"haveLastPrefixShape": true,
}

func sessionRuntimeFields(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sessionstate.go", nil, 0)
	if err != nil {
		t.Fatalf("parse sessionstate.go: %v", err)
	}
	fields := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "sessionRuntime" {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			if len(field.Names) == 0 {
				// An embedded type contributes its own name.
				if ident, ok := field.Type.(*ast.Ident); ok {
					fields[ident.Name] = true
				}
				continue
			}
			for _, name := range field.Names {
				fields[name.Name] = true
			}
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatal("sessionRuntime has no fields; the guard would pass vacuously")
	}
	return fields
}

func TestSessionRuntimeLifetimeListsCoverTheStruct(t *testing.T) {
	fields := sessionRuntimeFields(t)
	for _, list := range []map[string]bool{sessionReset, sessionCarryOver} {
		for name := range list {
			if !fields[name] {
				t.Errorf("the lifetime lists name %q, which sessionRuntime no longer has", name)
			}
		}
	}
	for name := range fields {
		switch {
		case sessionReset[name] && sessionCarryOver[name]:
			t.Errorf("sessionRuntime.%s is listed as both reset and carried", name)
		case !sessionReset[name] && !sessionCarryOver[name]:
			t.Errorf("sessionRuntime.%s is on neither list; decide whether a new conversation starts from it", name)
		}
	}
}

// The list above is a claim about reset's body, so it is read back from the
// source: a field dropped from reset stops being covered, and the mismatch
// fails here instead of surfacing as state leaking between conversations.
func TestSessionRuntimeResetAssignsEveryResetField(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sessionstate.go", nil, 0)
	if err != nil {
		t.Fatalf("parse sessionstate.go: %v", err)
	}
	touched := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "reset" {
			return true
		}
		ast.Inspect(fn, func(inner ast.Node) bool {
			sel, ok := inner.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "r" {
				touched[sel.Sel.Name] = true
			}
			return true
		})
		return false
	})
	for name := range sessionReset {
		if !touched[name] {
			t.Errorf("reset never touches sessionRuntime.%s, but the list says a new conversation starts from it", name)
		}
	}
}

func TestSetSessionRestartsTheConversationState(t *testing.T) {
	a := &Agent{}
	a.sess.cacheHit.Store(11)
	a.sess.cacheMiss.Store(7)
	a.sess.missingReasoning = missingReasoningWatch{active: true, stateRecorded: true, healthyStreak: 2}
	a.sess.reasoningReplayStrongProjection = 7
	a.sess.reasoningReplayStrongProjectionAnchor = "anchor"
	a.sess.compaction.stuck = true
	a.sess.compaction.stuckInputHash = "old-input"
	a.sess.compaction.consecutive = 3
	a.sess.compaction.failedTurn.Store(8)
	a.sess.compaction.lastTurn.Store(9)
	a.sess.compactionState = CompactionState{}
	a.sess.deferredTodoCompletions = map[string]deferredTodoCompletion{"id:b": {level: 0}}
	a.unwrittenResolve.at = time.Unix(1, 0)

	next := NewSession("")
	a.SetSession(next)

	if a.sess.session() != next {
		t.Error("SetSession did not bind the new conversation")
	}
	if a.sess.cacheHit.Load() != 0 || a.sess.cacheMiss.Load() != 0 {
		t.Errorf("cache tallies = %d/%d, want a fresh aggregate", a.sess.cacheHit.Load(), a.sess.cacheMiss.Load())
	}
	if a.sess.missingReasoning != (missingReasoningWatch{}) {
		t.Errorf("missingReasoning = %+v, want the incident to end with its conversation", a.sess.missingReasoning)
	}
	if a.sess.reasoningReplayStrongProjection != 0 {
		t.Errorf("reasoningReplayStrongProjection = %d, want it restarted", a.sess.reasoningReplayStrongProjection)
	}
	if a.sess.reasoningReplayStrongProjectionAnchor != "" {
		t.Errorf("reasoningReplayStrongProjectionAnchor = %q, want it restarted", a.sess.reasoningReplayStrongProjectionAnchor)
	}
	if a.sess.compaction.stuck || a.sess.compaction.stuckInputHash != "" || a.sess.compaction.consecutive != 0 ||
		a.sess.compaction.failedTurn.Load() != 0 || a.sess.compaction.lastTurn.Load() != 0 {
		t.Errorf("compaction progress = stuck:%t hash:%q consecutive:%d failedTurn:%d lastTurn:%d, want it restarted",
			a.sess.compaction.stuck, a.sess.compaction.stuckInputHash, a.sess.compaction.consecutive,
			a.sess.compaction.failedTurn.Load(), a.sess.compaction.lastTurn.Load())
	}
	if a.sess.cacheState != CacheStateUnknown {
		t.Errorf("cacheState = %q, want %q", a.sess.cacheState, CacheStateUnknown)
	}
	if len(a.sess.deferredTodoCompletions) != 0 {
		t.Errorf("deferred todo completions survived SetSession: %v", a.sess.deferredTodoCompletions)
	}
	if a.unwrittenResolve.at.IsZero() {
		t.Error("unwrittenResolve was cleared; the retry it owes belongs to the provider configuration, not the conversation")
	}
}
