package agent

import (
	"fmt"
	"html"
	"slices"
	"strings"

	"reasonix/internal/provider"
)

const interruptedRecoveryTag = "interrupted-turn-recovery"

const (
	maxRecoveryTools = 24
	maxRecoveryFiles = 8
	maxRecoveryValue = 240
)

// pendingInterruptedRecovery returns the newest unconsumed recovery handoff.
// A later real user turn consumes older handoffs implicitly, so the persisted
// LocalOnly record never needs an in-place mutation that could churn history.
func (a *Agent) pendingInterruptedRecovery() *provider.InterruptedTurnRecovery {
	if a == nil || a.sess.conversation == nil {
		return nil
	}
	msgs := a.sess.conversation.Snapshot()
	for _, v := range slices.Backward(msgs) {
		m := v
		if m.LocalOnly && m.InterruptedTurn != nil && m.InterruptedTurn.Pending {
			copy := *m.InterruptedTurn
			copy.ToolCalls = append([]provider.ToolCallRecord(nil), copy.ToolCalls...)
			copy.WriteChecks = append([]provider.WriteRecoveryCheck(nil), copy.WriteChecks...)
			copy.SatisfiedWrites = append([]provider.InterruptedToolSummary(nil), copy.SatisfiedWrites...)
			copy.CompletedTools = append([]provider.InterruptedToolSummary(nil), copy.CompletedTools...)
			copy.FailedTools = append([]provider.InterruptedToolSummary(nil), copy.FailedTools...)
			copy.UserConfirmedTools = append([]provider.InterruptedToolSummary(nil), copy.UserConfirmedTools...)
			copy.UserConfirmations = append([]provider.UserToolConfirmation(nil), copy.UserConfirmations...)
			copy.InterruptedTools = append([]string(nil), copy.InterruptedTools...)
			copy.NotStartedTools = append([]provider.InterruptedToolSummary(nil), copy.NotStartedTools...)
			copy.UnknownTools = append([]provider.InterruptedToolSummary(nil), copy.UnknownTools...)
			copy.CancelledTools = append([]provider.InterruptedToolSummary(nil), copy.CancelledTools...)
			return &copy
		}
		if IsUserAuthoredTurnMessage(m) {
			return nil
		}
	}
	return nil
}

// PendingInterruptedRecovery exposes a sanitized local snapshot for host UIs.
// It never returns provider transcript bytes or mutates the session.
func (a *Agent) PendingInterruptedRecovery() *provider.InterruptedTurnRecovery {
	return a.pendingInterruptedRecovery()
}

// interruptedRecoveryBlock is appended only at the mutable user-message tail.
// It contains no raw tool arguments, results, assistant text, or reasoning.
func interruptedRecoveryBlock(r *provider.InterruptedTurnRecovery) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>\n", interruptedRecoveryTag)
	b.WriteString("The previous turn was interrupted. Treat these as host-verified recovery facts, not as a new task.\n")
	if r.Cause != "" {
		fmt.Fprintf(&b, "cause: %s\n", html.EscapeString(clipRecoveryValue(r.Cause)))
	}
	if r.SilentInterruption {
		b.WriteString("silent_interruption: true\n")
	}
	if len(r.CompletedTools) == 0 {
		b.WriteString("completed_tools: none\n")
	} else {
		b.WriteString("completed_tools:\n")
		for i, tool := range r.CompletedTools {
			if i >= maxRecoveryTools {
				fmt.Fprintf(&b, "- ... %d additional completed tool pair(s) omitted\n", len(r.CompletedTools)-i)
				break
			}
			fmt.Fprintf(&b, "- %s", html.EscapeString(strings.TrimSpace(tool.Name)))
			if len(tool.Files) > 0 {
				files := tool.Files
				if len(files) > maxRecoveryFiles {
					files = files[:maxRecoveryFiles]
				}
				clipped := make([]string, 0, len(files))
				for _, file := range files {
					clipped = append(clipped, html.EscapeString(clipRecoveryValue(file)))
				}
				fmt.Fprintf(&b, " files=%s", strings.Join(clipped, ","))
			}
			if tool.Added != 0 || tool.Removed != 0 {
				fmt.Fprintf(&b, " diff=+%d/-%d", tool.Added, tool.Removed)
			}
			b.WriteByte('\n')
		}
	}
	writeRecoveryCalls(&b, "failed_tools", r.FailedTools)
	writeRecoveryCalls(&b, "user_confirmed_effects_do_not_repeat", r.UserConfirmedTools)
	if len(r.InterruptedTools) == 0 {
		b.WriteString("interrupted_tools: none\n")
	} else {
		b.WriteString("interrupted_tools: ")
		for i, name := range r.InterruptedTools {
			if i >= maxRecoveryTools {
				fmt.Fprintf(&b, ", ... %d additional call(s) omitted", len(r.InterruptedTools)-i)
				break
			}
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(html.EscapeString(strings.TrimSpace(name)))
		}
		b.WriteByte('\n')
	}
	writeRecoveryChecks(&b, r.WriteChecks)
	writeRecoveryCalls(&b, "write_postconditions_satisfied_do_not_repeat", r.SatisfiedWrites)
	writeRecoveryCalls(&b, "cancelled_tools", r.CancelledTools)
	writeRecoveryCalls(&b, "not_started_tools", r.NotStartedTools)
	writeRecoveryCalls(&b, "outcome_unknown_tools", r.UnknownTools)
	if r.DroppedPartialText || r.DroppedPartialReasoning {
		b.WriteString("unsafe_partial_output: excluded from model context")
		if r.DroppedPartialText && r.DroppedPartialReasoning {
			b.WriteString(" (assistant text and reasoning)\n")
		} else if r.DroppedPartialReasoning {
			b.WriteString(" (reasoning)\n")
		} else {
			b.WriteString(" (assistant text)\n")
		}
	}
	b.WriteString("Before continuing, inspect the current workspace and prior completed tool results. Do not blindly repeat completed writes. For outcome-unknown calls (including legacy interrupted calls without execution evidence), first inspect side effects and external state; never assume they did not run. Only retry after verifying it is safe. Calls marked cancelled or not_started may be planned again with complete arguments. A user attested to the effects listed under user_confirmed_effects_do_not_repeat; the host never observed their results, so verify current state before depending on them and never repeat them.\n")
	fmt.Fprintf(&b, "</%s>", interruptedRecoveryTag)
	return b.String()
}

func withInterruptedRecovery(input string, r *provider.InterruptedTurnRecovery) string {
	block := interruptedRecoveryBlock(r)
	if block == "" {
		return input
	}
	return block + "\n\n" + input
}

func clipRecoveryValue(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRecoveryValue {
		return value
	}
	return string(runes[:maxRecoveryValue]) + "…"
}

func writeRecoveryCalls(b *strings.Builder, label string, calls []provider.InterruptedToolSummary) {
	if len(calls) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", label)
	for i, call := range calls {
		if i >= maxRecoveryTools {
			fmt.Fprintf(b, "- ... %d omitted\n", len(calls)-i)
			break
		}
		fmt.Fprintf(b, "- %s id=%s\n", html.EscapeString(clipRecoveryValue(call.Name)), html.EscapeString(clipRecoveryValue(call.ID)))
	}
}

func writeRecoveryChecks(b *strings.Builder, checks []provider.WriteRecoveryCheck) {
	for i, check := range checks {
		if i >= maxRecoveryTools*maxRecoveryFiles {
			break
		}
		fmt.Fprintf(b, "write_postcondition: id=%s path=%s state=%s\n", html.EscapeString(clipRecoveryValue(check.CallID)), html.EscapeString(clipRecoveryValue(check.Path)), html.EscapeString(clipRecoveryValue(check.State)))
	}
}
