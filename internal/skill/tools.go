package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// SubagentRunner runs a runAs=subagent skill: it spawns an isolated child loop
// with the skill body as system prompt and `task` as its only input, returning
// the final answer. boot wires this over the agent's sub-agent machinery; nil
// means subagent skills are unavailable in this session (they error rather than
// silently inlining, which would lose the isolation the author asked for).
type SubagentRunOptions struct {
	ContinueFrom string
	ForkFrom     string
	// HostInitiated marks an explicit controller entry point such as
	// /<subagent-skill>. It may still carry a synthetic call context for nested
	// UI events, but that ephemeral event ID must not be persisted as though it
	// were a provider-visible parent tool call.
	HostInitiated bool
}

type SubagentRunner func(ctx context.Context, sk Skill, task string, opts SubagentRunOptions) (string, error)

// SubagentOutputError is implemented by host runners that can preserve a
// bounded result envelope alongside a terminal error. Tool dispatchers should
// return that output to the parent model while retaining the error for host
// status and recovery classification.
type SubagentOutputError interface {
	error
	SubagentOutput() string
}

// ProfileResolver returns the model/effort profile a subagent skill will use.
// It is optional; without one, skill frontmatter still supplies display metadata.
type ProfileResolver func(sk Skill) *event.Profile

// InstalledHook fires after install_skill writes a new file, so a host can
// refresh UI (e.g. a skills sidebar) without a reload. nil is fine.
type InstalledHook func(name, path string, scope Scope)

// run_skill

type runSkillTool struct {
	store           *Store
	runner          SubagentRunner
	profileResolver ProfileResolver
}

// NewRunSkillTool builds the general skill-invocation tool. runner may be nil
// (subagent skills then error).
func NewRunSkillTool(store *Store, runner SubagentRunner, profileResolver ...ProfileResolver) tool.Tool {
	var pr ProfileResolver
	if len(profileResolver) > 0 {
		pr = profileResolver[0]
	}
	return &runSkillTool{store: store, runner: runner, profileResolver: pr}
}

func (*runSkillTool) Name() string { return "run_skill" }

// ReadOnly is false: an invoked subagent skill could call writer tools, so
// classify conservatively to keep the parallel-dispatch path from racing two
// skill runs' writes (mirrors the `task` tool).
func (*runSkillTool) ReadOnly() bool { return false }

func (*runSkillTool) Description() string {
	return "Invoke a playbook from the Skills index pinned in the system prompt. For the built-in subagent skills (explore / research / review / security_review), prefer the dedicated top-level tools of the same name — they're easier to pick and do the same thing. Pass `name` as the BARE identifier (e.g. 'explore'), NOT the `[🧬 subagent]` tag that follows it in the index. `[🧬 subagent]` skills spawn an isolated subagent — only the final distilled answer returns; supply `arguments` describing the concrete task since the subagent has no other context. Untagged skills are inlined: the body becomes a tool result you read and follow."
}

func (*runSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Skill identifier as it appears in the pinned Skills index (e.g. 'explore', 'review'). Case-sensitive. Just the identifier, not the [🧬 subagent] tag."},
  "arguments":{"type":"string","description":"Free-form arguments. For inline skills: appended as an 'Arguments:' line; the skill's own instructions decide how to use them. For subagent skills: REQUIRED — becomes the entire task the subagent receives."},
  "continue_from":{"type":"string","description":"Continue a prior compatible subagent transcript in the current conversation context. Only valid for runAs=subagent skills. Pass only the 'sa_...' value from the prior result's 'Subagent reference: ...' line."}
},
"required":["name"]
}`)
}

// ValidateArguments enforces the conditional subagent task contract before a
// runner is started. The provider-visible schema stays stable because whether a
// skill is inline or isolated is catalog data, not a new tool shape.
func (t *runSkillTool) ValidateArguments(args json.RawMessage) []tool.ArgumentViolation {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(args, &p) != nil {
		return nil // The ordinary JSON Schema/parser owns malformed JSON.
	}
	name := cleanSkillName(p.Name)
	sk, ok := t.store.Read(name)
	if !ok || sk.RunAs != RunSubagent || strings.TrimSpace(p.Arguments) != "" {
		return nil
	}
	return []tool.ArgumentViolation{{
		Path:     "/arguments",
		Keyword:  "required",
		Expected: "a non-empty string describing the concrete subagent task",
	}}
}

func (t *runSkillTool) CapabilityArguments(capabilityID string) (tool.CapabilityArgumentContract, bool) {
	name := strings.TrimSpace(strings.TrimPrefix(capabilityID, "skill:"))
	sk, ok := t.store.Read(name)
	if !ok {
		return tool.CapabilityArgumentContract{}, false
	}
	required := ""
	if sk.RunAs == RunSubagent {
		required = `,"required":["arguments"]`
	}
	schema := json.RawMessage(`{"type":"object","properties":{"arguments":{"type":"string","description":"Concrete task or inline skill arguments."},"continue_from":{"type":"string","description":"Optional compatible subagent reference."}}` + required + `}`)
	example, _ := json.Marshal(map[string]any{
		"action":        "call",
		"capability_id": "skill:" + name,
		"arguments": map[string]any{
			"arguments": "specific task for " + name,
		},
	})
	return tool.CapabilityArgumentContract{Schema: schema, Example: example}, true
}

func (t *runSkillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Continue  string `json:"continue_from"`
		Fork      string `json:"fork_from"`
	}
	if err := json.Unmarshal(unwrapJSONStringArgs(args), &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return "", fmt.Errorf("run_skill requires a 'name' argument (got %q, which is just a marker/tag)", p.Name)
	}
	sk, ok := t.store.Read(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableNames(t.store))
	}
	if err := t.store.ValidateInvocation(sk); err != nil {
		return "", fmt.Errorf("run_skill: %w", err)
	}
	sk = t.store.Prepare(sk)
	rawArgs := strings.TrimSpace(p.Arguments)
	opts := SubagentRunOptions{ContinueFrom: strings.TrimSpace(p.Continue), ForkFrom: strings.TrimSpace(p.Fork)}
	if opts.ContinueFrom != "" && opts.ForkFrom != "" {
		return "", fmt.Errorf("run_skill: continue_from and fork_from are mutually exclusive; pass only continue_from")
	}

	if sk.RunAs == RunSubagent {
		if t.runner == nil {
			return "", fmt.Errorf("run_skill: skill %q is runAs=subagent but no subagent runner is configured in this session", name)
		}
		if rawArgs == "" {
			return "", fmt.Errorf("run_skill: skill %q is a subagent and requires 'arguments' — the subagent has no other context, so describe the concrete task", name)
		}
		out, err := t.runner(ctx, sk, rawArgs, opts)
		if err != nil {
			var outputErr SubagentOutputError
			if errors.As(err, &outputErr) && strings.TrimSpace(outputErr.SubagentOutput()) != "" {
				return outputErr.SubagentOutput(), err
			}
			return "", err
		}
		return tool.GuardSubagentHostDecisionText(out), nil
	}
	if opts.ContinueFrom != "" || opts.ForkFrom != "" {
		return "", fmt.Errorf("run_skill: subagent continuation is only valid for runAs=subagent skills")
	}
	return renderInline(sk, rawArgs), nil
}

func (t *runSkillTool) ResolveProfile(args json.RawMessage) *event.Profile {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return nil
	}
	sk, ok := t.store.Read(name)
	if !ok || sk.RunAs != RunSubagent {
		return nil
	}
	return t.profileForSkill(sk)
}

func (t *runSkillTool) profileForSkill(sk Skill) *event.Profile {
	return profileForSkill(sk, t.profileResolver)
}

// read_only_skill

type readOnlySkillTool struct {
	store           *Store
	runner          SubagentRunner
	profileResolver ProfileResolver
}

// NewReadOnlySkillTool builds an explicitly read-only skill entry point. Inline
// skills are rendered like read_skill; subagent skills run through a host-provided
// read-only subagent runner with no continuation/fork controls.
func NewReadOnlySkillTool(store *Store, runner SubagentRunner, profileResolver ...ProfileResolver) tool.Tool {
	var pr ProfileResolver
	if len(profileResolver) > 0 {
		pr = profileResolver[0]
	}
	return &readOnlySkillTool{store: store, runner: runner, profileResolver: pr}
}

func (*readOnlySkillTool) Name() string { return "read_only_skill" }

func (*readOnlySkillTool) ReadOnly() bool { return true }

// PlanModeSafe reports true because this explicit read-only capability is also
// semantically valid during the planning phase.
func (*readOnlySkillTool) PlanModeSafe() bool { return true }

func (*readOnlySkillTool) Description() string {
	return "Invoke a skill in read-only mode. Inline skills are loaded into context like read_skill. `[🧬 subagent]` skills run in an isolated ephemeral read-only subagent with only read-only research tools and safe foreground bash; no writes, installers, memory mutation, continuation/fork, background jobs, or writer-capable delegation are available. Read-only nested delegation may be available until max_subagent_depth is reached. Pass `name` as the bare skill identifier and `arguments` as the concrete task."
}

func (*readOnlySkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Skill identifier as it appears in the pinned Skills index. Just the identifier, not the [🧬 subagent] tag."},
  "arguments":{"type":"string","description":"Free-form arguments. For inline skills: appended as an 'Arguments:' line. For subagent skills: REQUIRED — becomes the read-only subagent's entire task."}
},
"required":["name"]
}`)
}

func (t *readOnlySkillTool) ValidateArguments(args json.RawMessage) []tool.ArgumentViolation {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(args, &p) != nil {
		return nil
	}
	name := cleanSkillName(p.Name)
	sk, ok := t.store.Read(name)
	if !ok || sk.RunAs != RunSubagent || strings.TrimSpace(p.Arguments) != "" {
		return nil
	}
	return []tool.ArgumentViolation{{
		Path:     "/arguments",
		Keyword:  "required",
		Expected: "a non-empty string describing the concrete read-only subagent task",
	}}
}

func (t *readOnlySkillTool) CapabilityArguments(capabilityID string) (tool.CapabilityArgumentContract, bool) {
	name := strings.TrimSpace(strings.TrimPrefix(capabilityID, "skill:"))
	sk, ok := t.store.Read(name)
	if !ok {
		return tool.CapabilityArgumentContract{}, false
	}
	required := ""
	if sk.RunAs == RunSubagent {
		required = `,"required":["arguments"]`
	}
	schema := json.RawMessage(`{"type":"object","properties":{"arguments":{"type":"string","description":"Concrete read-only task or inline skill arguments."}}` + required + `}`)
	example, _ := json.Marshal(map[string]any{
		"action":        "call",
		"capability_id": "skill:" + name,
		"arguments": map[string]any{
			"arguments": "specific read-only task for " + name,
		},
	})
	return tool.CapabilityArgumentContract{Schema: schema, Example: example}, true
}

func (t *readOnlySkillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(unwrapJSONStringArgs(args), &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return "", fmt.Errorf("read_only_skill requires a 'name' argument (got %q, which is just a marker/tag)", p.Name)
	}
	sk, ok := t.store.Read(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableNames(t.store))
	}
	if err := t.store.ValidateInvocation(sk); err != nil {
		return "", fmt.Errorf("read_only_skill: %w", err)
	}
	sk = t.store.Prepare(sk)
	rawArgs := strings.TrimSpace(p.Arguments)
	if sk.RunAs == RunSubagent {
		if t.runner == nil {
			return "", fmt.Errorf("read_only_skill: skill %q is runAs=subagent but no read-only subagent runner is configured in this session", name)
		}
		if rawArgs == "" {
			return "", fmt.Errorf("read_only_skill: skill %q is a subagent and requires 'arguments' — the subagent has no other context, so describe the concrete read-only task", name)
		}
		out, err := t.runner(ctx, sk, rawArgs, SubagentRunOptions{})
		if err != nil {
			var outputErr SubagentOutputError
			if errors.As(err, &outputErr) && strings.TrimSpace(outputErr.SubagentOutput()) != "" {
				return outputErr.SubagentOutput(), err
			}
			return "", err
		}
		return tool.GuardSubagentHostDecisionText(out), nil
	}
	return renderInline(sk, rawArgs), nil
}

func (t *readOnlySkillTool) ResolveProfile(args json.RawMessage) *event.Profile {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return nil
	}
	sk, ok := t.store.Read(name)
	if !ok || sk.RunAs != RunSubagent {
		return nil
	}
	return profileForSkill(sk, t.profileResolver)
}

func profileForSkill(sk Skill, resolver ProfileResolver) *event.Profile {
	if resolver != nil {
		if pr := resolver(sk); pr != nil {
			return pr
		}
	}
	model, effort := strings.TrimSpace(sk.Model), strings.TrimSpace(sk.Effort)
	if model == "" && effort == "" {
		return nil
	}
	return &event.Profile{Model: model, Effort: effort}
}

// readSkillTool loads an inline skill body into context without running anything.
type readSkillTool struct {
	store *Store
}

// NewReadSkillTool builds a read-only inline-skill loader so a plan can consult
// playbooks without starting a subagent.
func NewReadSkillTool(store *Store) tool.Tool { return &readSkillTool{store: store} }

func (*readSkillTool) Name() string { return "read_skill" }

// ReadOnly is true: read_skill only renders an inline skill body, with no
// subagent or side effects.
func (*readSkillTool) ReadOnly() bool { return true }

func (*readSkillTool) Description() string {
	return "Load an inline playbook from the Skills index into your context WITHOUT running anything — the skill body returns as a tool result you read and follow. This is the read-only alternative when no subagent execution is needed. Pass `name` as the BARE identifier (e.g. 'commit'), NOT the `[🧬 subagent]` tag. Subagent-tagged skills are rejected: use run_skill (or the dedicated tool) for those, since they execute work."
}

func (*readSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Inline skill identifier as it appears in the pinned Skills index. Just the identifier, not the [🧬 subagent] tag."},
  "arguments":{"type":"string","description":"Optional free-form arguments, appended as an 'Arguments:' line; the skill's own instructions decide how to use them."}
},
"required":["name"]
}`)
}

func (t *readSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return "", fmt.Errorf("read_skill requires a 'name' argument (got %q, which is just a marker/tag)", p.Name)
	}
	sk, ok := t.store.Read(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableNames(t.store))
	}
	if err := t.store.ValidateInvocation(sk); err != nil {
		return "", fmt.Errorf("read_skill: %w", err)
	}
	sk = t.store.Prepare(sk)
	if sk.RunAs == RunSubagent {
		return "", fmt.Errorf("read_skill: skill %q is a subagent and must be executed, not read — use run_skill (or the dedicated %s tool)", name, name)
	}
	return renderInline(sk, strings.TrimSpace(p.Arguments)), nil
}

// dedicated subagent wrappers (explore / research / review / security_review)

type subagentSkillTool struct {
	toolName    string
	skillName   string
	description string
	taskDesc    string
	store       *Store
	runner      SubagentRunner
	profile     ProfileResolver
}

func (t *subagentSkillTool) Name() string        { return t.toolName }
func (*subagentSkillTool) ReadOnly() bool        { return false }
func (t *subagentSkillTool) Description() string { return t.description }

func (t *subagentSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","description":` +
		strconv.Quote(t.taskDesc) + `},"continue_from":{"type":"string","description":"Continue a prior compatible subagent transcript in the current conversation context. Pass only the 'sa_...' value from the prior result's 'Subagent reference: ...' line."}},"required":["task"]}`)
}

func (t *subagentSkillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Task     string `json:"task"`
		Continue string `json:"continue_from"`
		Fork     string `json:"fork_from"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	task := strings.TrimSpace(p.Task)
	if task == "" {
		return "", fmt.Errorf("%s requires a non-empty 'task' argument — describe the concrete question", t.toolName)
	}
	sk, ok := t.store.Read(t.skillName)
	if !ok {
		return "", fmt.Errorf("%s: built-in skill %q is not registered", t.toolName, t.skillName)
	}
	if err := t.store.ValidateInvocation(sk); err != nil {
		return "", fmt.Errorf("%s: %w", t.toolName, err)
	}
	sk = t.store.Prepare(sk)
	// A user file overriding the built-in name with runAs:inline would lose
	// isolation if dispatched here — bounce to run_skill where inline is defined.
	if sk.RunAs != RunSubagent {
		return "", fmt.Errorf("%s: skill %q is overridden as inline; invoke it via run_skill instead", t.toolName, t.skillName)
	}
	if t.runner == nil {
		return "", fmt.Errorf("%s: no subagent runner is configured in this session", t.toolName)
	}
	opts := SubagentRunOptions{ContinueFrom: strings.TrimSpace(p.Continue), ForkFrom: strings.TrimSpace(p.Fork)}
	if opts.ContinueFrom != "" && opts.ForkFrom != "" {
		return "", fmt.Errorf("%s: continue_from and fork_from are mutually exclusive; pass only continue_from", t.toolName)
	}
	out, err := t.runner(ctx, sk, task, opts)
	if err != nil {
		return "", err
	}
	return tool.GuardSubagentHostDecisionText(out), nil
}

func (t *subagentSkillTool) ResolveProfile(json.RawMessage) *event.Profile {
	sk, ok := t.store.Read(t.skillName)
	if !ok || sk.RunAs != RunSubagent {
		return nil
	}
	if t.profile != nil {
		if pr := t.profile(sk); pr != nil {
			return pr
		}
	}
	model, effort := strings.TrimSpace(sk.Model), strings.TrimSpace(sk.Effort)
	if model == "" && effort == "" {
		return nil
	}
	return &event.Profile{Model: model, Effort: effort}
}

// BuiltinSubagentTools returns top-level wrapper tools for the built-in subagent
// skills, named after the verb so the model picks them naturally (affordance >
// prompt rules). Each is skipped when its underlying skill isn't present (e.g. a
// user disabled it), so the tool set never advertises a phantom skill.
func BuiltinSubagentTools(store *Store, runner SubagentRunner, profileResolver ...ProfileResolver) []tool.Tool {
	var pr ProfileResolver
	if len(profileResolver) > 0 {
		pr = profileResolver[0]
	}
	specs := []struct {
		toolName, skillName, description, taskDesc string
	}{
		{"explore", "explore",
			"Run a focused read-only codebase investigation in an isolated subagent. Use for broad survey questions across many files — 'find all places that X', 'how does Y work across the project', 'audit Z'. Returns one distilled answer with file:line citations. Its reads + reasoning never enter your context, unlike chained read_file.",
			"Concrete investigation question. The subagent has none of your context — write a self-contained prompt naming the symbol / pattern / behavior to survey."},
		{"research", "research",
			"Combine web_fetch + code reading in an isolated subagent. Use when the answer needs both an external reference and local verification — 'is X supported by lib Y', 'compare our impl against the spec'. Returns one synthesis citing code (file:line) and web (URL).",
			"Concrete research question. The subagent has none of your context — name the external thing to look up and the local code to compare against."},
		{"review", "review",
			"Review the pending changes (current branch diff) in an isolated subagent — flags correctness / security / missing-tests / hidden behavior per file:line. Read-only; you decide what to act on. Use before suggesting a PR-shaped change or after finishing a multi-step edit.",
			"What to focus the review on (e.g. 'focus on the auth changes' or 'general'). The subagent reads the diff itself."},
		{"security_review", "security-review",
			"Security-focused review of the current branch diff in an isolated subagent — injection / authz / secrets / deserialization / path-traversal / crypto, severity-tagged. Read-only. Use when shipping changes that touch auth, input parsing, file IO, or external requests.",
			"Optional scope hint (e.g. 'focus on token handling in internal/auth/') or 'full' for everything in the diff."},
	}
	var out []tool.Tool
	for _, s := range specs {
		// Skill profiles are diagnostic-only; do not hide builtin subagent
		// entry points based on the session role setting.
		if _, ok := store.Read(s.skillName); !ok {
			continue
		}
		out = append(out, &subagentSkillTool{
			toolName:    s.toolName,
			skillName:   s.skillName,
			description: s.description,
			taskDesc:    s.taskDesc,
			store:       store,
			runner:      runner,
			profile:     pr,
		})
	}
	return out
}

// install_skill

type installSkillTool struct {
	store       *Store
	onInstalled InstalledHook
}

// NewInstallSkillTool builds the skill-authoring tool. onInstalled may be nil.
func NewInstallSkillTool(store *Store, onInstalled InstalledHook) tool.Tool {
	return &installSkillTool{store: store, onInstalled: onInstalled}
}

func (*installSkillTool) Name() string   { return "install_skill" }
func (*installSkillTool) ReadOnly() bool { return false }

func (t *installSkillTool) Description() string {
	scope := "'global' (only option — no project workspace) writes to the Reasonix home skills directory."
	if t.store.HasProjectScope() {
		scope = "'project' (default) writes to <repo>/.reasonix/skills/ (this workspace only); 'global' writes to the Reasonix home skills directory (every project)."
	}
	return "Author and save a new skill — a reusable playbook future turns invoke via run_skill (or /<name>). Runnable immediately this turn; appears in the pinned Skills index on the next launch. " + scope
}

func (*installSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Identifier — letters/digits/_/-/., 1-64 chars, starts alphanumeric. Becomes the skill folder name under the selected skills directory."},
  "description":{"type":"string","description":"≤120-char one-liner shown in the pinned Skills index — future agents read it to decide whether to invoke."},
  "body":{"type":"string","description":"Markdown playbook. For subagent skills, write the subagent's persona/rules — it gets no context besides 'arguments' at runtime."},
  "scope":{"type":"string","enum":["project","global"],"description":"Where to write. Defaults to project when a workspace exists, else global."},
  "runAs":{"type":"string","enum":["inline","subagent"],"description":"inline (default) folds the body into the parent turn; subagent spawns an isolated child loop returning only its final answer (use for context-heavy work)."},
  "model":{"type":"string","description":"Optional model override for runAs=subagent (a configured provider/model name). Ignored otherwise."},
  "effort":{"type":"string","description":"Optional effort for runAs=subagent (e.g. high, max). Ignored otherwise."},
  "allowedTools":{"type":"array","items":{"type":"string"},"description":"Optional tool allowlist for runAs=subagent (e.g. ['read_file','grep'])."}
},
"required":["name","description","body"]
}`)
}

func (t *installSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Body         string   `json:"body"`
		Scope        string   `json:"scope"`
		RunAs        string   `json:"runAs"`
		Model        string   `json:"model"`
		Effort       string   `json:"effort"`
		AllowedTools []string `json:"allowedTools"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := strings.TrimSpace(p.Name)
	desc := strings.TrimSpace(collapseSpaces(p.Description))
	if name == "" {
		return "", fmt.Errorf("install_skill requires a non-empty 'name'")
	}
	if desc == "" {
		return "", fmt.Errorf("install_skill requires a non-empty 'description' — it is what appears in the Skills index")
	}
	if strings.TrimSpace(p.Body) == "" {
		return "", fmt.Errorf("install_skill requires a non-empty 'body' — the playbook the skill executes")
	}

	scope := ScopeGlobal
	switch strings.TrimSpace(p.Scope) {
	case "global":
		scope = ScopeGlobal
	case "project":
		scope = ScopeProject
	default:
		if t.store.HasProjectScope() {
			scope = ScopeProject
		}
	}
	if scope == ScopeProject && !t.store.HasProjectScope() {
		return "", fmt.Errorf("install_skill: scope='project' requires a workspace — use scope='global'")
	}

	runAs := RunInline
	if strings.TrimSpace(p.RunAs) == "subagent" {
		runAs = RunSubagent
	}

	content := RenderSkillFile(SkillFileOptions{
		Name:         name,
		Description:  desc,
		Body:         p.Body,
		RunAs:        runAs,
		Model:        strings.TrimSpace(p.Model),
		Effort:       strings.TrimSpace(p.Effort),
		AllowedTools: p.AllowedTools,
	})
	path, err := t.store.CreateWithContent(name, scope, content)
	if err != nil {
		return "", err
	}
	if t.onInstalled != nil {
		t.onInstalled(name, path, scope)
	}
	res, _ := json.Marshal(map[string]any{
		"ok":    true,
		"name":  name,
		"scope": string(scope),
		"path":  path,
		"runAs": string(runAs),
		"note":  "Callable immediately in this tool loop via run_skill({name}) or /" + name + ". It will appear in session-context on the next real user turn.",
	})
	return string(res), nil
}

// SkillFileOptions configures a rendered skill markdown file's frontmatter.
// Shared by the model-facing install_skill tool and host-side authoring
// surfaces (e.g. a desktop subagent-profile settings page) so both produce
// identical, correctly-escaped frontmatter instead of hand-built YAML.
type SkillFileOptions struct {
	Name         string
	Description  string
	Body         string
	RunAs        RunAs
	Model        string // subagent-only; ignored when RunAs != RunSubagent
	Effort       string // subagent-only; ignored when RunAs != RunSubagent
	AllowedTools []string
	// ReadOnly, when true, emits frontmatter read-only: true so the profile
	// runs against the read-only registry. Omitted/false keeps the legacy
	// writable default for older profiles.
	ReadOnly bool
	Color    string // optional display tag; emitted regardless of RunAs
	// Invocation, when "manual", keeps the written skill out of automatic
	// session-context discovery — invocable by name only, never
	// model-discovered. Anything else (including empty) is the default "auto".
	Invocation string
}

// skillFileFrontmatter is the YAML shape RenderSkillFile emits. Field order is
// the emission order (yaml.v3 preserves struct order); values are marshaled by
// yaml.v3 so free-text fields with colons, '#', quotes, or newlines are
// escaped correctly instead of corrupting the block — an unparseable
// frontmatter would make the loader fall back to an EMPTY field map, silently
// resetting runAs to inline and invocation to auto (see frontmatter.Split).
type skillFileFrontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Color        string   `yaml:"color,omitempty"`
	Invocation   string   `yaml:"invocation,omitempty"`
	RunAs        string   `yaml:"runAs,omitempty"`
	Model        string   `yaml:"model,omitempty"`
	Effort       string   `yaml:"effort,omitempty"`
	ReadOnly     *bool    `yaml:"read-only,omitempty"`
	AllowedTools []string `yaml:"allowed-tools,omitempty,flow"`
}

// RenderSkillFile assembles a skill file's frontmatter + body. Subagent-only
// fields (model, effort, allowed-tools, read-only) are emitted only when
// RunAs=subagent; color and invocation are independent of RunAs.
func RenderSkillFile(opts SkillFileOptions) string {
	fm := skillFileFrontmatter{
		Name:        opts.Name,
		Description: opts.Description,
		Color:       strings.TrimSpace(opts.Color),
	}
	if strings.EqualFold(strings.TrimSpace(opts.Invocation), "manual") {
		fm.Invocation = "manual"
	}
	if opts.RunAs == RunSubagent {
		fm.RunAs = string(RunSubagent)
		fm.Model = strings.TrimSpace(opts.Model)
		fm.Effort = strings.TrimSpace(opts.Effort)
		if opts.ReadOnly {
			v := true
			fm.ReadOnly = &v
		}
		for _, t := range opts.AllowedTools {
			if t = strings.TrimSpace(t); t != "" {
				fm.AllowedTools = append(fm.AllowedTools, t)
			}
		}
	}
	// Marshaling a flat struct of strings cannot fail.
	raw, _ := yaml.Marshal(fm)
	return "---\n" + string(raw) + "---\n\n" + strings.TrimRight(opts.Body, " \t\r\n") + "\n"
}

// shared helpers

// Render builds a skill's invocation text: a header (name, description, source)
// followed by the body and any arguments. Used directly when a user invokes a
// skill via "/<name>" (sent as a turn); the run_skill tool wraps the same text
// in a skill-pin sentinel (see renderInline).
func Render(sk Skill, args string) string {
	var b strings.Builder
	b.WriteString("# Skill: " + sk.Name)
	if sk.Description != "" {
		b.WriteString("\n> " + sk.Description)
	}
	b.WriteString("\n(scope: " + string(sk.Scope) + " · " + sk.Path + ")\n\n")
	b.WriteString(sk.Body)
	if args != "" {
		b.WriteString("\n\nArguments: " + args)
	}
	return b.String()
}

// renderInline wraps Render's output in a skill-pin sentinel so context
// compaction preserves the body verbatim instead of paraphrasing it.
func renderInline(sk Skill, args string) string {
	return "<skill-pin name=" + strconv.Quote(sk.Name) + ">\n" + Render(sk, args) + "\n</skill-pin>"
}

var bracketTagRe = regexp.MustCompile(`\[[^\]]*\]`)

// cleanSkillName extracts the bare identifier from a possibly-decorated name:
// models sometimes copy the index's "explore [🧬 subagent]" verbatim into the
// `name` arg. Drop any [..] tag, then take the first token starting alphanumeric.
func cleanSkillName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	stripped := strings.TrimSpace(bracketTagRe.ReplaceAllString(raw, " "))
	for tok := range strings.FieldsSeq(stripped) {
		if c := tok[0]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return tok
		}
	}
	return ""
}

// collapseSpaces turns any run of whitespace (incl. newlines) into a single
// space, so a multi-line description stays a one-liner in the index.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// availableNames lists the discoverable skill names for an error message.
func availableNames(store *Store) string {
	skills := store.List()
	if len(skills) == 0 {
		return "(none — no skills defined)"
	}
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}
