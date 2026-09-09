package agent

import (
	"context"
	"slices"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncontext"
)

// TurnContextBundle carries role-specific runtime snapshots without changing
// Runner.Run. BootstrapOnly lets synthetic continuations repair an upgraded
// legacy session that has no snapshot, while never consuming later updates.
type TurnContextBundle struct {
	Executor      sessioncontext.Snapshot
	Planner       sessioncontext.Snapshot
	BootstrapOnly bool
}

type turnContextBundleKey struct{}
type turnContextRoleKey struct{}

type turnContextRole uint8

const (
	turnContextExecutor turnContextRole = iota
	turnContextPlanner
)

type turnContextDiagnostics struct {
	snapshot sessioncontext.Snapshot
	stats    sessioncontext.Diagnostics
	target   string
	reasons  []string
}

// WithTurnContextBundle attaches provider-visible runtime context to one host
// turn. Empty bundles preserve the old context identity and request fast path.
func WithTurnContextBundle(ctx context.Context, bundle TurnContextBundle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if bundle.Executor.Content == "" && bundle.Planner.Content == "" {
		return ctx
	}
	return context.WithValue(ctx, turnContextBundleKey{}, bundle)
}

// WithoutTurnContextBundle prevents a child agent from inheriting the parent's
// provider-visible runtime snapshot through context.Context. Child sessions
// receive only the explicit task/workspace context assembled by their caller;
// fork/continue transcript inheritance remains an explicit session operation.
func WithoutTurnContextBundle(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnContextBundleKey{}, struct{}{})
}

func (a *Agent) prepareProviderTurn(ctx context.Context, input string) string {
	return a.withTurnPreferences(input)
}

func withPlannerTurnContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnContextRoleKey{}, turnContextPlanner)
}

func turnContextFromContext(ctx context.Context) (sessioncontext.Snapshot, bool, turnContextRole) {
	if ctx == nil {
		return sessioncontext.Snapshot{}, false, turnContextExecutor
	}
	bundle, ok := ctx.Value(turnContextBundleKey{}).(TurnContextBundle)
	if !ok {
		return sessioncontext.Snapshot{}, false, turnContextExecutor
	}
	role, _ := ctx.Value(turnContextRoleKey{}).(turnContextRole)
	snapshot := bundle.Executor
	if role == turnContextPlanner {
		snapshot = bundle.Planner
	}
	if snapshot.Content == "" {
		return sessioncontext.Snapshot{}, false, role
	}
	parsed, valid := sessioncontext.Parse(snapshot.Content)
	if !valid || parsed.Digest != snapshot.Digest {
		return sessioncontext.Snapshot{}, false, role
	}
	return parsed, bundle.BootstrapOnly, role
}

// AppendTurnContext appends the role-appropriate snapshot exactly once per
// digest. It derives state from model-visible history so resume, rewind, fork,
// and compaction need no persisted sidecar field.
func (a *Agent) AppendTurnContext(ctx context.Context) bool {
	return a.appendTurnContextAndMessages(ctx)
}

// AppendTurnContextAndUser appends a role-appropriate snapshot and the real
// user message in one Session.AddBatch. This keeps mid-turn autosave from
// persisting a context-only admission boundary.
func (a *Agent) AppendTurnContextAndUser(ctx context.Context, user provider.Message) bool {
	if a == nil || a.sess.session() == nil {
		return false
	}
	if user.ID == "" {
		user.ID = turnUserMessageID(ctx, a.sess.session())
	}
	appendedContext := a.appendTurnContextAndMessages(ctx, user)
	emitAdmittedUserMessage(a.svc.sink, user)
	return appendedContext
}

func (a *Agent) appendTurnContextAndMessages(ctx context.Context, messages ...provider.Message) bool {
	if a == nil {
		return false
	}
	sess := a.sess.session()
	if sess == nil {
		return false
	}
	contextMessage, appendContext := a.prepareTurnContext(ctx)
	if !appendContext && len(messages) == 0 {
		return false
	}
	batch := make([]provider.Message, 0, len(messages)+1)
	if appendContext {
		batch = append(batch, contextMessage)
	}
	batch = append(batch, messages...)
	sess.AddBatch(batch...)
	return appendContext
}

func (a *Agent) prepareTurnContext(ctx context.Context) (provider.Message, bool) {
	if a == nil {
		return provider.Message{}, false
	}
	sess := a.sess.session()
	if sess == nil {
		return provider.Message{}, false
	}
	snapshot, bootstrapOnly, role := turnContextFromContext(ctx)
	if snapshot.Content == "" {
		return provider.Message{}, false
	}
	visible := a.modelVisibleMessages()
	previous, found := latestTurnContextSnapshot(visible)
	a.turn.sessionContext = turnContextDiagnostics{
		snapshot: snapshot,
		stats:    sessioncontext.SectionDiagnostics(snapshot),
		target:   role.String(),
	}
	if found {
		if bootstrapOnly || previous.Digest == snapshot.Digest {
			return provider.Message{}, false
		}
		a.turn.sessionContext.reasons = changedTurnContextReasons(previous, snapshot)
	} else {
		reason := "first_seen"
		if bootstrapOnly || hasPriorConversation(visible) {
			reason = "legacy_resume"
		}
		a.turn.sessionContext.reasons = []string{reason}
	}
	return HostGeneratedUserMessage(snapshot.Content), true
}

func latestTurnContextSnapshot(messages []provider.Message) (sessioncontext.Snapshot, bool) {
	for i := range slices.Backward(messages) {
		message := messages[i]
		if message.Role != provider.RoleUser || message.Origin != provider.MessageOriginHost {
			continue
		}
		if snapshot, ok := sessioncontext.Parse(message.Content); ok {
			return snapshot, true
		}
	}
	return sessioncontext.Snapshot{}, false
}

func isSessionContextMessage(message provider.Message) bool {
	if message.Role != provider.RoleUser || message.Origin != provider.MessageOriginHost {
		return false
	}
	return sessioncontext.IsContent(message.Content)
}

func (r turnContextRole) String() string {
	if r == turnContextPlanner {
		return "planner"
	}
	return "executor"
}

func hasPriorConversation(messages []provider.Message) bool {
	for _, message := range messages {
		if message.Role != provider.RoleSystem && !message.LocalOnly {
			return true
		}
	}
	return false
}

func changedTurnContextReasons(previous, current sessioncontext.Snapshot) []string {
	var reasons []string
	if previous.Sections.Environment != current.Sections.Environment || previous.Sections.Workspace != current.Sections.Workspace {
		reasons = append(reasons, "runtime_changed")
	}
	if previous.Sections.BackgroundMemory != current.Sections.BackgroundMemory {
		reasons = append(reasons, "memory_changed")
	}
	if previous.Sections.SkillsCatalog != current.Sections.SkillsCatalog {
		reasons = append(reasons, "skills_changed")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "snapshot_changed")
	}
	return reasons
}

func (a *Agent) attachSessionContextDiagnostics(diagnostics *CacheDiagnostics) {
	if a == nil || diagnostics == nil || a.turn.sessionContext.snapshot.Content == "" {
		return
	}
	diagnostics.SessionContext = eventSessionContextDiagnostics(a.turn.sessionContext)
}

func eventSessionContextDiagnostics(observed turnContextDiagnostics) *event.SessionContextDiagnostics {
	if observed.snapshot.Content == "" {
		return nil
	}
	section := func(stat sessioncontext.SectionStat) event.SessionContextSectionDiagnostics {
		return event.SessionContextSectionDiagnostics{Digest: stat.Digest, Chars: stat.Chars}
	}
	return &event.SessionContextDiagnostics{
		Version: observed.snapshot.Version, Digest: observed.snapshot.Digest,
		TargetRole: observed.target, Reasons: append([]string(nil), observed.reasons...),
		Environment: section(observed.stats.Environment), Workspace: section(observed.stats.Workspace),
		BackgroundMemory: section(observed.stats.BackgroundMemory), SkillsCatalog: section(observed.stats.SkillsCatalog),
	}
}

func captureTurnContextShape(system string, schemas []provider.ToolSchema, rewriteVersion int, messages []provider.Message) PrefixShape {
	shape := CaptureShape(system, schemas, rewriteVersion)
	if snapshot, ok := latestTurnContextSnapshot(messages); ok {
		shape.SessionContextDigest = snapshot.Digest
		shape.PrefixHash = shortHash(map[string]string{
			"stable": shape.PrefixHash, "session_context": snapshot.Digest,
		})
	}
	return shape
}
