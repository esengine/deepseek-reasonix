// Package tool defines the Tool abstraction and a Registry. Built-in tools live
// in tool/builtin and self-register via init(); plugin-provided tools are added
// to a runtime Registry alongside the enabled built-ins. The agent sees only a
// *Registry, never the global built-in set directly.
package tool

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"reasonix/internal/diff"
	"reasonix/internal/provider"
)

// Tool is a capability the model can invoke.
type Tool interface {
	Name() string
	Description() string
	// Schema returns the JSON Schema for the tool's parameters.
	Schema() json.RawMessage
	// Execute parses the model-generated raw JSON args and returns result text
	// to feed back to the model.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	// ReadOnly reports whether the tool has no observable side effects on the
	// host. The agent parallelises a batch of tool calls only when every call
	// in the batch is ReadOnly; mixed batches stay sequential so write/read
	// ordering is preserved. bash and plugin tools must return false because
	// their effects can't be inferred statically from args.
	ReadOnly() bool
}

// ExecutionPolicy optionally describes retry semantics without changing the
// provider-visible tool contract. Tools that do not implement it are treated
// conservatively: writes and shell commands are not idempotent.
type ExecutionPolicy interface {
	Idempotent(args json.RawMessage) bool
	SupportsPostcondition(args json.RawMessage) bool
}

type PostconditionState string

const (
	PostconditionUnknown   PostconditionState = "unknown"
	PostconditionSatisfied PostconditionState = "satisfied"
	PostconditionAbsent    PostconditionState = "absent"
)

// PostconditionChecker verifies an effect after a tool result was lost. It
// must be read-only and must never infer success from an unavailable signal.
type PostconditionChecker interface {
	CheckPostcondition(ctx context.Context, args json.RawMessage) (PostconditionState, string, error)
}

// CallClass is a pure, argument-aware dispatch classification. Generation is a
// target schema/lifecycle fingerprint checked again by the execution adapter;
// an empty generation keeps the call on the serial path.
type CallClass struct {
	Known        bool
	ReadOnly     bool
	ParallelSafe bool
	ResourceKey  string
	Generation   string
}

// BatchClassifier lets a fixed proxy classify its resolved target without
// executing discovery, starting a process, or making a network request.
type BatchClassifier interface {
	ClassifyCall(json.RawMessage) CallClass
}

// ContextualTool is an execution-time availability contract for tools whose
// ownership depends on the active workflow context. Provider schemas remain
// static for cache stability; the host must still consult this contract before
// permissions, hooks, leases, or Execute so stale transcripts fail closed.
type ContextualTool interface {
	ProviderVisible(context.Context) bool
}

// Previewer is an optional capability a writer Tool may implement: given the
// same raw JSON args Execute would receive, compute the file change the call
// *would* make — without touching disk. ctx must be Execute's, so the preview
// resolves through the same FileOverlay and a user never approves a diff that
// differs from what runs. Type-assert to discover support; the file-writing
// built-ins implement it, most tools do not.
type Previewer interface {
	Preview(ctx context.Context, args json.RawMessage) (diff.Change, error)
}

// PreviewChange returns the change a writer tool would make for args, or ok=false
// when there's nothing renderable: t is read-only, doesn't implement Previewer,
// the preview errored (the edit will likely fail too), or the file is binary.
func PreviewChange(ctx context.Context, t Tool, args json.RawMessage) (diff.Change, bool) {
	if t == nil || t.ReadOnly() {
		return diff.Change{}, false
	}
	pv, ok := t.(Previewer)
	if !ok {
		return diff.Change{}, false
	}
	ch, err := pv.Preview(ctx, args)
	if err != nil || ch.Binary {
		return diff.Change{}, false
	}
	return ch, true
}

// ImageTool is an optional capability a Tool may implement when its results can
// carry images alongside text (e.g. an MCP tool returning a screenshot).
// ExecuteWithImages returns the same text Execute would — including a short
// placeholder marker where each image occurred — plus the images as data URLs
// (data:<mime>;base64,<payload>). Callers with a structural image channel (the
// agent stores them on the tool message, where vision-capable providers embed
// them) use this instead of Execute; everything else falls back to Execute and
// the placeholders alone describe the images. Keeping images out of the text
// matters: tool output text is truncated at a fixed byte budget, which would
// corrupt an embedded base64 payload.
type ImageTool interface {
	ExecuteWithImages(ctx context.Context, args json.RawMessage) (text string, images []string, err error)
}

// PlanModeClassifier is an optional capability a Tool may implement to declare
// its stance on running during the planning phase. It is deliberately distinct
// from ReadOnly(): a tool can be side-effect-free yet belong only to the
// post-approval execution phase (complete_step reports ReadOnly()==true but must
// not run while planning), or be a delegation that is safe only in a read-only
// variant (read_only_task). A false result is an explicit phase opt-out; tools
// without this interface continue to the ordinary Permissions/Sandbox path.
type PlanModeClassifier interface {
	PlanModeSafe() bool
}

// ReadOnlyExecutionHostMutation marks a target that is logically read-only but
// must first mutate host state to become executable, such as starting an
// on-demand MCP process. Strict read-only agents reject these targets even when
// their eventual remote operation is trusted read-only.
type ReadOnlyExecutionHostMutation interface {
	ReadOnlyExecutionHostMutation() bool
}

// ReadOnlyExecutionBlockReason lets a deferred capability explain which
// parent-session action is required when a strict read-only child cannot run
// it. The reason is host-local and never enters provider tool schemas.
type ReadOnlyExecutionBlockReason interface {
	ReadOnlyExecutionBlockReason() string
}

// MCPMetadata exposes the original MCP identity behind a model-visible
// "mcp__<server>__<tool>" adapter. The model name may be normalized for provider
// function-name rules; host policy and diagnostics use the raw server-local
// tool name.
type MCPMetadata interface {
	MCPServerName() string
	MCPRawToolName() string
}

// MCPVisibleMetadata exposes the server-local name after any host-configured
// prefix stripping. It is the short name authors usually write in skills.
type MCPVisibleMetadata interface {
	MCPVisibleToolName() string
}

// MCPPackageMetadata identifies the plugin package that contributed an MCP
// server. Empty means the server came from ordinary user/workspace config.
type MCPPackageMetadata interface {
	MCPPackageName() string
}

// MCPBinding describes one stable MCP capability and the exact provider-visible
// name currently bound to it. Bindings are host metadata only: they never add
// aliases to provider schemas or alter schema ordering.
type MCPBinding struct {
	Package      string
	Server       string
	RawName      string
	VisibleName  string
	CallableName string
	CapabilityID string
}

// MCPAnnotations exposes safety-relevant annotations reported by an installed
// MCP server. These hints do not change the provider-visible tool contract;
// execution policy consumes them locally.
type MCPAnnotations interface {
	MCPDestructiveHint() bool
}

// MCPServerAuthorization reports whether the user installed this MCP server or
// authorized its exact project identity. Authorization belongs to the server,
// not to individual tools; readOnly/destructive metadata is checked separately.
type MCPServerAuthorization interface {
	MCPServerAuthorized() bool
}

// readerExecutionIntentKey carries a per-call, immutable authorization basis:
// the call was approved as a non-destructive reader. The MCP dispatcher makes
// the final, linearizable check against live security state and must never
// promote such a call into a writer lane; drift after authorization returns an
// error instead of executing.
type readerExecutionIntentKey struct{}

// nonDestructiveMCPExecutionIntentKey carries a per-call, immutable
// authorization basis for Planner-trusted MCP: the server is authorized and the
// live tool is non-destructive, even when it lacks readOnlyHint. The MCP
// dispatcher re-checks authorization and destructiveHint before tools/call;
// drift returns a retryable error with zero execution.
type nonDestructiveMCPExecutionIntentKey struct{}

// planReplacementAuthorizationKey carries a one-call authorization from the
// host's Auto plan gate. It lets todo_write replace the current in_progress
// step after the plan transition has been reviewed, without weakening ordinary
// todo continuity or exposing an authorization bit in the model-visible schema.
type planReplacementAuthorizationKey struct{}

// WithReaderExecutionIntent marks ctx as a reader-authorized MCP invocation.
func WithReaderExecutionIntent(ctx context.Context) context.Context {
	return context.WithValue(ctx, readerExecutionIntentKey{}, true)
}

// HasReaderExecutionIntent reports whether this call entered through the
// non-destructive reader lane.
func HasReaderExecutionIntent(ctx context.Context) bool {
	intent, _ := ctx.Value(readerExecutionIntentKey{}).(bool)
	return intent
}

// WithNonDestructiveMCPExecutionIntent marks ctx as a Planner-trusted MCP
// invocation: authorized server, non-destructive live tool. Unlike the reader
// lane it does not require readOnlyHint.
func WithNonDestructiveMCPExecutionIntent(ctx context.Context) context.Context {
	return context.WithValue(ctx, nonDestructiveMCPExecutionIntentKey{}, true)
}

// HasNonDestructiveMCPExecutionIntent reports whether this call entered through
// the Planner non-destructive MCP lane.
func HasNonDestructiveMCPExecutionIntent(ctx context.Context) bool {
	intent, _ := ctx.Value(nonDestructiveMCPExecutionIntentKey{}).(bool)
	return intent
}

// WithPlanReplacementAuthorization marks one reviewed todo_write invocation as
// allowed to replace its current step. Callers must not reuse the returned
// context for unrelated tool calls.
func WithPlanReplacementAuthorization(ctx context.Context) context.Context {
	return context.WithValue(ctx, planReplacementAuthorizationKey{}, true)
}

// HasPlanReplacementAuthorization reports whether the host approved replacing
// the active step for this exact tool invocation.
func HasPlanReplacementAuthorization(ctx context.Context) bool {
	authorized, _ := ctx.Value(planReplacementAuthorizationKey{}).(bool)
	return authorized
}

// SnipHint describes how context maintenance should shorten a stale, oversized
// result this tool produced. Head/Tail are the line counts kept from each end
// when the result has many lines; HeadChars/TailChars bound the kept runes when
// the result is one giant line. A zero value is invalid — implementers return
// positive counts. The geometry lives on the tool, not in a lookup table keyed
// by name, so renaming a tool carries its snip policy with it and a new tool
// cannot silently fall back to a generic default unnoticed (the contract test
// forces every registered tool to either implement SnipHinter or opt into the
// read-only/side-effecting default explicitly).
type SnipHint struct {
	Head      int
	Tail      int
	HeadChars int
	TailChars int
}

// SnipHinter is an optional capability a Tool implements when its output has a
// known shape that a generic head/tail split would garble — e.g. read_file
// front-loads the most relevant lines, while bash output is equally meaningful
// at both ends. Type-assert a Tool to discover support; tools that omit it take
// the ReadOnly-tiered default in the maintainer.
type SnipHinter interface {
	SnipHint() SnipHint
}

// process-global built-in set (populated by builtin subpackage init)

var builtins = map[string]Tool{}

// RegisterBuiltin registers a compile-time built-in tool. Intended for init().
// It panics on a duplicate name, which is a compile-time wiring mistake.
func RegisterBuiltin(t Tool) {
	name := t.Name()
	if _, dup := builtins[name]; dup {
		panic("tool: duplicate built-in " + name)
	}
	builtins[name] = t
}

// Builtins returns all registered built-in tools, sorted by name.
func Builtins() []Tool {
	names := make([]string, 0, len(builtins))
	for n := range builtins {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, builtins[n])
	}
	return out
}

// LookupBuiltin returns a registered built-in by name.
func LookupBuiltin(name string) (Tool, bool) {
	t, ok := builtins[name]
	return t, ok
}

// per-run registry instance

// Registry is a per-run set of tools: enabled built-ins plus plugin tools.
type Registry struct {
	mu        sync.RWMutex
	tools     map[string]Tool
	order     []string
	canon     map[string]json.RawMessage
	suspended map[string]bool
	// providerVisible, when non-nil, restricts Schemas/ContractEntries to the
	// listed tool names. Get/Execute still resolve every registered tool so
	// use_capability can dispatch tool:<name> without changing the provider
	// schema. Nil means every registered tool is provider-visible (tests and
	// legacy direct construction).
	providerVisible map[string]bool
	schemaRev       atomic.Uint64
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}, canon: map[string]json.RawMessage{}, suspended: map[string]bool{}}
}

// SetProviderVisibleTools restricts the provider-visible schema surface to the
// given names while keeping all registered tools executable via Get. Passing
// nil clears the restriction. Names are normalized with strings.TrimSpace.
func (r *Registry) SetProviderVisibleTools(names []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if names == nil {
		if r.providerVisible != nil {
			r.providerVisible = nil
			r.schemaRev.Add(1)
		}
		return
	}
	visible := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			visible[name] = true
		}
	}
	changed := len(visible) != len(r.providerVisible) || r.providerVisible == nil
	if !changed {
		for name := range visible {
			if !r.providerVisible[name] {
				changed = true
				break
			}
		}
	}
	r.providerVisible = visible
	if changed {
		r.schemaRev.Add(1)
	}
}

// ProviderVisible reports whether name is currently provider-visible.
func (r *Registry) ProviderVisible(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.providerVisible == nil {
		return true
	}
	return r.providerVisible[strings.TrimSpace(name)]
}

func (r *Registry) isProviderVisibleLocked(name string) bool {
	if r.providerVisible == nil {
		return true
	}
	return r.providerVisible[name]
}

// Add inserts (or replaces) a tool, preserving first-seen order. The schema is
// canonicalized once here — it never changes after registration, so Schemas()
// (called every turn) reuses the result instead of re-marshaling.
func (r *Registry) Add(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := t.Name()
	for prefix := range r.suspended {
		if strings.HasPrefix(name, prefix) {
			return
		}
	}
	if _, ok := r.tools[name]; !ok {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
	r.canon[name] = provider.CanonicalizeSchema(t.Schema())
	r.schemaRev.Add(1)
}

// MCPNamePrefix is the namespace every MCP tool name carries: the
// model-visible name is "mcp__<server>__<tool>".
const MCPNamePrefix = "mcp__"

// SplitMCPName splits a model-visible MCP tool name "mcp__<server>__<tool>" into
// its server and tool parts. ok is false for non-MCP (built-in) names and for
// malformed names missing either part.
func SplitMCPName(name string) (server, tool string, ok bool) {
	if !strings.HasPrefix(name, MCPNamePrefix) {
		return "", "", false
	}
	rest := name[len(MCPNamePrefix):]
	parts := strings.SplitN(rest, "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// RemovePrefix unregisters every tool whose name starts with prefix — used to
// drop an MCP server's "mcp__<server>__" namespace when it's disconnected — and
// returns the count removed.
func (r *Registry) RemovePrefix(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := r.order[:0]
	removed := 0
	for _, name := range r.order {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			delete(r.canon, name)
			removed++
			continue
		}
		kept = append(kept, name)
	}
	r.order = kept
	if removed > 0 {
		r.schemaRev.Add(1)
	}
	return removed
}

// SuspendPrefix unregisters matching tools and prevents future Add calls for
// that prefix until ResumePrefix is called. It is used for per-session MCP
// disables where an in-flight background handshake may otherwise swap tools back
// into this registry after the user turned the server off.
func (r *Registry) SuspendPrefix(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.suspended[prefix] = true
	kept := r.order[:0]
	removed := 0
	for _, name := range r.order {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			delete(r.canon, name)
			removed++
			continue
		}
		kept = append(kept, name)
	}
	r.order = kept
	if removed > 0 {
		r.schemaRev.Add(1)
	}
	return removed
}

// ResumePrefix allows future Add calls for a previously suspended prefix.
func (r *Registry) ResumePrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.suspended, prefix)
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	return t, ok
}

// MCPBindings returns live MCP capability bindings in canonical-name order.
func (r *Registry) MCPBindings() []MCPBinding {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]MCPBinding, 0, len(r.tools))
	for _, t := range r.tools {
		if b, ok := mcpBinding(t); ok {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CallableName < out[j].CallableName })
	return out
}

// ResolveCall resolves an exact provider-visible name or a unique portable MCP
// reference. Exact names always win. Ambiguous aliases return their canonical
// candidates and are never executed.
func (r *Registry) ResolveCall(name string) (resolved Tool, canonical string, candidates []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if t, ok := r.tools[name]; ok {
		return t, name, nil
	}
	matches := map[string]Tool{}
	for canonicalName, t := range r.tools {
		b, ok := mcpBinding(t)
		if !ok {
			continue
		}
		if slices.Contains(mcpBindingAliases(b), name) {
			matches[canonicalName] = t
		}
	}
	if len(matches) == 1 {
		for canonicalName, t := range matches {
			return t, canonicalName, nil
		}
	}
	if len(matches) > 1 {
		candidates = make([]string, 0, len(matches))
		for canonicalName := range matches {
			candidates = append(candidates, canonicalName)
		}
		sort.Strings(candidates)
	}
	return nil, "", candidates
}

func mcpBinding(t Tool) (MCPBinding, bool) {
	meta, ok := t.(MCPMetadata)
	if !ok {
		return MCPBinding{}, false
	}
	server := strings.TrimSpace(meta.MCPServerName())
	raw := strings.TrimSpace(meta.MCPRawToolName())
	if server == "" || raw == "" {
		return MCPBinding{}, false
	}
	visible := raw
	if v, ok := t.(MCPVisibleMetadata); ok && strings.TrimSpace(v.MCPVisibleToolName()) != "" {
		visible = strings.TrimSpace(v.MCPVisibleToolName())
	}
	pkg := ""
	if p, ok := t.(MCPPackageMetadata); ok {
		pkg = strings.TrimSpace(p.MCPPackageName())
	}
	return MCPBinding{
		Package:      pkg,
		Server:       server,
		RawName:      raw,
		VisibleName:  visible,
		CallableName: t.Name(),
		CapabilityID: "mcp-tool:" + server + "/" + raw,
	}, true
}

func mcpBindingAliases(b MCPBinding) []string {
	aliases := []string{
		b.RawName,
		b.VisibleName,
		b.Server + "/" + b.RawName,
		b.Server + "/" + b.VisibleName,
		b.CapabilityID,
		"mcp-tool:" + b.Server + "/" + b.VisibleName,
		"mcp__" + portableMCPPart(b.Server) + "__" + portableMCPPart(b.RawName),
		"mcp__" + portableMCPPart(b.Server) + "__" + portableMCPPart(b.VisibleName),
	}
	if b.Package != "" {
		prefix := "mcp__plugin_" + portableMCPPart(b.Package) + "_" + portableMCPPart(b.Server) + "__"
		aliases = append(aliases, prefix+portableMCPPart(b.RawName), prefix+portableMCPPart(b.VisibleName))
	}
	return aliases
}

// MCPBindingAliases returns accepted portable references for a binding. The
// canonical provider-visible name remains MCPBinding.CallableName.
func MCPBindingAliases(b MCPBinding) []string {
	return append([]string(nil), mcpBindingAliases(b)...)
}

func portableMCPPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.order)
}

// SchemaRevision changes whenever the provider-visible tool set changes.
func (r *Registry) SchemaRevision() uint64 {
	if r == nil {
		return 0
	}
	return r.schemaRev.Load()
}

// Names returns the registered tool names in insertion order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Schemas exports tool definitions in stable name order for the provider.
// When a provider-visible allowlist is set, only those tools appear.
func (r *Registry) Schemas() []provider.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if r.isProviderVisibleLocked(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	out := make([]provider.ToolSchema, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		if t == nil {
			continue
		}
		out = append(out, provider.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  r.canon[name],
		})
	}
	return out
}

// AllNames returns every registered tool name, including tools hidden from the
// provider-visible schema. Used by capability catalogs and diagnostics.
func (r *Registry) AllNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// SchemasForContext returns the contextual projection for host metadata and
// diagnostics. Provider requests intentionally use Schemas so phase changes do
// not churn the cache-stable tool contract.
func (r *Registry) SchemasForContext(ctx context.Context) []provider.ToolSchema {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	names := append([]string(nil), r.order...)
	entries := make(map[string]struct {
		t    Tool
		data json.RawMessage
	}, len(names))
	for _, name := range names {
		if t := r.tools[name]; t != nil {
			entries[name] = struct {
				t    Tool
				data json.RawMessage
			}{t: t, data: r.canon[name]}
		}
	}
	r.mu.RUnlock()
	sort.Strings(names)
	out := make([]provider.ToolSchema, 0, len(names))
	for _, name := range names {
		entry, ok := entries[name]
		if !ok || entry.t == nil {
			continue
		}
		if contextual, ok := entry.t.(ContextualTool); ok && !contextual.ProviderVisible(ctx) {
			continue
		}
		out = append(out, provider.ToolSchema{Name: entry.t.Name(), Description: entry.t.Description(), Parameters: entry.data})
	}
	return out
}
