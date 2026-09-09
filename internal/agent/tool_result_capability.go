package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

const (
	sessionToolResultCapabilityID = "session:tool_result"
	toolResultPageDefaultBytes    = 16 * 1024
	toolResultPageMaxBytes        = 24 * 1024
)

type toolResultSessionBinder interface {
	bindToolResultSession(func() *Session)
}

type sessionToolResultTool struct {
	session func() *Session
}

func (*sessionToolResultTool) Name() string { return tool.HostSessionToolResult }

func (*sessionToolResultTool) Description() string {
	return "Read one bounded UTF-8 page from a complete tool result retained in the current agent session. This uses tr-... result references; for a sa_... subagent reference, use read_subagent_result instead."
}

func (*sessionToolResultTool) ReadOnly() bool { return true }

func (*sessionToolResultTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"tool_call_id":{"type":"string"},
			"result_ref":{"type":"string"},
			"offset":{"type":"integer","minimum":0},
			"limit":{"type":"integer","minimum":1,"maximum":24576}
		},
		"required":["tool_call_id"]
	}`)
}

type toolResultReadParams struct {
	ToolCallID string `json:"tool_call_id"`
	ResultRef  string `json:"result_ref"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
}

type toolResultCandidate struct {
	name        string
	body        string
	resultRef   string
	recoverable bool
	requiresRef bool
}

func toolResultRef(toolCallID, body string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(toolCallID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(body))
	return fmt.Sprintf("tr-%x", h.Sum(nil)[:12])
}

func toolOutputRecoveryMarker(toolName, toolCallID, resultRef string, originalBytes, keptBytes int) string {
	namePart := boundedMarkerField(toolName, 128, "tool")
	idPart := boundedMarkerField(toolCallID, 128, "-")
	exampleID := toolCallID
	if len(exampleID) > 256 {
		exampleID = "<full tool_call_id from this tool result>"
	}
	args, _ := json.Marshal(struct {
		ToolCallID string `json:"tool_call_id"`
		ResultRef  string `json:"result_ref"`
		Offset     int    `json:"offset"`
	}{ToolCallID: exampleID, ResultRef: resultRef})
	return fmt.Sprintf(
		"\n\n…[truncated tool=%s call_id=%s result_ref=%s original_bytes=%d kept_bytes=%d — full original retained locally; recover with use_capability(action=\"call\", capability_id=\"session:tool_result\", arguments=%s). If use_capability is unavailable, re-run the original tool with narrower arguments]…\n\n",
		namePart, idPart, resultRef, originalBytes, keptBytes, args,
	)
}

// toolOutputRecoveryMarkerAt builds the recoverable provider-visible marker.
// recoverOffset is the first byte the model has not seen. It is intentionally
// read_file-only; generic head/tail previews retain their byte-compatible marker
// above and continue to recover from zero.
func toolOutputRecoveryMarkerAt(toolName, toolCallID, resultRef string, originalBytes, keptBytes, recoverOffset int) string {
	namePart := boundedMarkerField(toolName, 128, "tool")
	idPart := boundedMarkerField(toolCallID, 128, "-")
	exampleID := toolCallID
	if len(exampleID) > 256 {
		exampleID = "<full tool_call_id from this tool result>"
	}
	args, _ := json.Marshal(struct {
		ToolCallID string `json:"tool_call_id"`
		ResultRef  string `json:"result_ref"`
		Offset     int    `json:"offset"`
	}{ToolCallID: exampleID, ResultRef: resultRef, Offset: recoverOffset})
	return fmt.Sprintf(
		"\n\n…[truncated tool=%s call_id=%s result_ref=%s original_bytes=%d kept_bytes=%d next_offset=%d — full original retained locally; recover with use_capability(action=\"call\", capability_id=\"session:tool_result\", arguments=%s). Only the first %d bytes are shown; page the rest with session:tool_result if the visible part is not enough, or re-read a narrower window. If use_capability is unavailable, re-run the original tool with narrower arguments]…\n\n",
		namePart, idPart, resultRef, originalBytes, keptBytes, recoverOffset, args, keptBytes,
	)
}

func boundedMarkerField(value string, maxBytes int, fallback string) string {
	if value == "" {
		return fallback
	}
	if len(value) <= maxBytes {
		return value
	}
	return snapToRuneBoundary(value, 0, maxBytes) + "…"
}

func (t *sessionToolResultTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p toolResultReadParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("session tool result: invalid args: %w", err)
	}
	p.ToolCallID = strings.TrimSpace(p.ToolCallID)
	p.ResultRef = strings.TrimSpace(p.ResultRef)
	if strings.HasPrefix(p.ToolCallID, "sa_") || strings.HasPrefix(p.ResultRef, "sa_") {
		return "", fmt.Errorf("session tool result: %q is a subagent reference; use read_subagent_result with ref", firstNonEmpty(p.ResultRef, p.ToolCallID))
	}
	if p.ToolCallID == "" {
		return "", fmt.Errorf("session tool result: tool_call_id is required")
	}
	if p.Offset < 0 {
		return "", fmt.Errorf("session tool result: offset must be non-negative")
	}
	if p.Limit == 0 {
		p.Limit = toolResultPageDefaultBytes
	}
	if p.Limit < 1 || p.Limit > toolResultPageMaxBytes {
		return "", fmt.Errorf("session tool result: limit must be between 1 and %d bytes", toolResultPageMaxBytes)
	}
	if t == nil || t.session == nil {
		return "", fmt.Errorf("session tool result: current session is unavailable")
	}
	session := t.session()
	if session == nil {
		return "", fmt.Errorf("session tool result: current session is unavailable")
	}
	candidate, err := findToolResultCandidate(session.Snapshot(), p.ToolCallID, p.ResultRef)
	if err != nil {
		return "", err
	}
	if !candidate.recoverable {
		return "", fmt.Errorf("session tool result: full result is unavailable for this legacy truncated record; re-run %s with narrower arguments", candidate.name)
	}
	if !utf8.ValidString(candidate.body) {
		return "", fmt.Errorf("session tool result: retained result is not valid UTF-8")
	}
	if p.Offset > len(candidate.body) {
		return "", fmt.Errorf("session tool result: offset %d exceeds total_bytes %d", p.Offset, len(candidate.body))
	}
	if p.Offset < len(candidate.body) && !utf8.RuneStart(candidate.body[p.Offset]) {
		return "", fmt.Errorf("session tool result: offset %d is not a UTF-8 character boundary", p.Offset)
	}

	end := min(len(candidate.body), p.Offset+p.Limit)
	for end > p.Offset && end < len(candidate.body) && !utf8.RuneStart(candidate.body[end]) {
		end--
	}
	if end == p.Offset && end < len(candidate.body) {
		return "", fmt.Errorf("session tool result: limit %d ends inside the next UTF-8 character; increase limit", p.Limit)
	}
	digest := sha256.Sum256([]byte(candidate.body))
	header, _ := json.Marshal(struct {
		ResultRef  string `json:"result_ref"`
		Offset     int    `json:"offset"`
		NextOffset int    `json:"next_offset"`
		TotalBytes int    `json:"total_bytes"`
		SHA256     string `json:"sha256"`
		Complete   bool   `json:"complete"`
	}{
		ResultRef: candidate.resultRef, Offset: p.Offset, NextOffset: end,
		TotalBytes: len(candidate.body), SHA256: hex.EncodeToString(digest[:]), Complete: end == len(candidate.body),
	})
	return string(header) + "\n" + candidate.body[p.Offset:end], nil
}

func findToolResultCandidate(msgs []provider.Message, toolCallID, resultRef string) (toolResultCandidate, error) {
	candidates := make([]toolResultCandidate, 0, 2)
	for _, msg := range slices.Backward(msgs) {
		if msg.Role != provider.RoleTool || msg.ToolCallID != toolCallID {
			continue
		}
		body := msg.RawContent
		recoverable := body != ""
		if body == "" {
			body = msg.Content
			recoverable = !looksLikeTruncatedToolResult(msg.Content)
		}
		ref := toolResultRef(toolCallID, body)
		requiresRef := strings.Contains(msg.Content, "…[truncated tool=") && strings.Contains(msg.Content, " result_ref=")
		if !recoverable && requiresRef {
			if markerRef, ok := toolResultRefFromMarker(msg.Content); ok {
				ref = markerRef
			}
		}
		candidate := toolResultCandidate{
			name: msg.Name, body: body, resultRef: ref, recoverable: recoverable,
			requiresRef: requiresRef,
		}
		if resultRef != "" {
			if ref == resultRef {
				return candidate, nil
			}
			continue
		}
		candidates = append(candidates, candidate)
	}
	if resultRef != "" {
		return toolResultCandidate{}, fmt.Errorf("session tool result: result_ref %q was not found for tool_call_id %q", resultRef, toolCallID)
	}
	if len(candidates) == 0 {
		return toolResultCandidate{}, fmt.Errorf("session tool result: tool_call_id %q was not found in the current session", toolCallID)
	}
	if len(candidates) == 1 {
		if candidates[0].requiresRef {
			return toolResultCandidate{}, fmt.Errorf("session tool result: result_ref is required for this truncated result; use result_ref=%s from its marker", candidates[0].resultRef)
		}
		return candidates[0], nil
	}
	refs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.resultRef)
	}
	return toolResultCandidate{}, fmt.Errorf("session tool result: tool_call_id %q is ambiguous; retry with one of result_ref=%s", toolCallID, strings.Join(refs, ","))
}

func toolResultRefFromMarker(content string) (string, bool) {
	const markerStart = "…[truncated tool="
	start := strings.Index(content, markerStart)
	if start < 0 {
		return "", false
	}
	marker := content[start:]
	if end := strings.Index(marker, "]…"); end >= 0 {
		marker = marker[:end]
	}
	for field := range strings.FieldsSeq(marker) {
		ref, ok := strings.CutPrefix(field, "result_ref=")
		if !ok || len(ref) != len("tr-")+24 || !strings.HasPrefix(ref, "tr-") {
			continue
		}
		if decoded, err := hex.DecodeString(strings.TrimPrefix(ref, "tr-")); err == nil && len(decoded) == 12 {
			return ref, true
		}
	}
	return "", false
}

func looksLikeTruncatedToolResult(content string) bool {
	return strings.Contains(content, "…[truncated tool=") ||
		strings.Contains(content, snippedMarker) ||
		strings.Contains(content, prunedMarker) ||
		strings.Contains(content, toolPruneMarker)
}

func (a *Agent) bindToolResultSessionCapability() {
	if a == nil || a.svc.tools == nil {
		return
	}
	proxy, ok := a.svc.tools.Get("use_capability")
	if !ok {
		return
	}
	binder, ok := proxy.(toolResultSessionBinder)
	if !ok {
		return
	}
	binder.bindToolResultSession(func() *Session { return a.Session() })
}

func (t *UseCapabilityTool) bindToolResultSession(session func() *Session) {
	if t == nil {
		return
	}
	t.toolResultMu.Lock()
	t.toolResultSession = session
	t.toolResultMu.Unlock()
}

func (t *UseCapabilityTool) currentToolResultTarget() tool.Tool {
	if t == nil {
		return nil
	}
	t.toolResultMu.RLock()
	session := t.toolResultSession
	t.toolResultMu.RUnlock()
	if session == nil {
		return nil
	}
	return &sessionToolResultTool{session: session}
}

func (t *UseCapabilityTool) resolveSessionToolResult(args json.RawMessage, base tool.ResolvedCall) (tool.ResolvedCall, error) {
	target := t.currentToolResultTarget()
	if target == nil {
		return tool.ResolvedCall{}, fmt.Errorf("capability %q is unavailable without a current agent session", sessionToolResultCapabilityID)
	}
	base.TargetName = target.Name()
	base.Target = target
	base.Args = args
	base.ReadOnly = true
	return base, nil
}

func (t *UseCapabilityTool) inspectSessionToolResult() (string, error) {
	if t.currentToolResultTarget() == nil {
		return "", fmt.Errorf("capability %q is unavailable without a current agent session", sessionToolResultCapabilityID)
	}
	payload := map[string]any{
		"id": sessionToolResultCapabilityID, "kind": "session", "name": "tool_result",
		"description": "Read one bounded page from a complete tool result retained in this agent's current session.",
		"status":      "ready", "read_only": true,
		"arguments": map[string]any{
			"tool_call_id": "required", "result_ref": "required for new truncated results; optional for unambiguous legacy records",
			"offset": 0, "limit_default": toolResultPageDefaultBytes, "limit_max": toolResultPageMaxBytes,
		},
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	return string(b), err
}
