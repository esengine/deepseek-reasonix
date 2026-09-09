package agent

import (
	"fmt"
)

// contextBudgetTag is the per-turn transient block carrying the context
// budget line (#9520). It rides the user turn head like the other transient
// blocks and is stripped from previews/titles via TransientUserBlockTags.
const contextBudgetTag = "context-budget"

// contextBudgetWarnRatio: at or above this fraction of the compaction trigger
// the budget line upgrades to the "approaching" wording. Deliberately plain
// arithmetic — no crossing/re-arm state machine, the copy carries the phase.
const contextBudgetWarnRatio = 0.85

// ContextBudgetBlock renders the one-line budget the model can rely on every
// round (#9520): estimated prompt tokens, the window, and where auto
// compaction fires. Near the trigger it appends the planning guidance so the
// model knows older turns are summarized, not lost — the anxiety that drives
// rushed or abandoned large tasks is the failure mode this prevents.
func ContextBudgetBlock(used, trigger, window int) string {
	if window <= 0 || used <= 0 || window <= used {
		return ""
	}
	line := fmt.Sprintf("<%s>context: %dk/%dk tokens (%d%%); auto-compaction at %d%%</%s>",
		contextBudgetTag, used/1000, window/1000, used*100/window, trigger*100/window, contextBudgetTag)
	if trigger > 0 && float64(used) >= float64(trigger)*contextBudgetWarnRatio {
		line += "\napproaching auto-compaction; older context will be summarized, not lost — plan remaining work in waves, and record key progress and decisions in a project document now so they stay cheap to recover after the summary."
	}
	return line
}

// WithContextBudget prefixes the user turn with the context-budget block.
// The numbers come from the same estimators the ContextManager compares
// against its thresholds, so the line can never disagree with what the host
// is about to do. Returns content unchanged when the window is unknown.
func (a *Agent) WithContextBudget(content string) string {
	if a == nil {
		return content
	}
	window := a.effectiveContextWindow()
	if window <= 0 {
		return content
	}
	block := ContextBudgetBlock(a.ContextUsedTokens(), a.compactTrigger(), window)
	if block == "" || hasLeadingInjectedBlock(content, contextBudgetTag) {
		return content
	}
	return block + "\n\n" + content
}
