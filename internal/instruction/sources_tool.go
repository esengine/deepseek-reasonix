package instruction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

type sourcesTool struct {
	documents []Document
}

// NewSourcesTool returns a read-only capability for resolving the exact host
// paths behind provider-relative instruction labels such as user/AGENTS.md.
func NewSourcesTool(documents []Document) tool.Tool {
	return sourcesTool{documents: append([]Document(nil), documents...)}
}

func (sourcesTool) Name() string { return "instruction_sources" }

func (sourcesTool) Description() string {
	return "Report the exact host filesystem paths and scopes of standing instruction files active in this session. Use this when the user asks where a global, project, ancestor, or local instruction file is stored; do not infer a path from provider-relative labels or another agent application."
}

func (sourcesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t sourcesTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	if len(strings.TrimSpace(string(args))) > 0 {
		var in struct{}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	return formatSourcePaths(t.documents), nil
}

func (sourcesTool) ReadOnly() bool { return true }

func formatSourcePaths(documents []Document) string {
	if len(documents) == 0 {
		return "No standing instruction files are active."
	}
	var b strings.Builder
	b.WriteString("Active standing instruction files (low to high precedence):\n")
	for index, document := range documents {
		fmt.Fprintf(&b, "%d. scope=%s path=%s\n", index+1, document.Scope, document.Path)
		if strings.TrimSpace(document.Directory) != "" {
			fmt.Fprintf(&b, "   applies_to=%s\n", document.Directory)
		}
	}
	b.WriteString("These are the exact host-provided paths. Report them as shown; do not infer or substitute a path from another agent application.")
	return strings.TrimSpace(b.String())
}
