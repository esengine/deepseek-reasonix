package provider

import (
	"encoding/json"
	"fmt"
)

// ValidateTranscript is the final provider-facing safety gate. It runs on the
// normalized request copy and rejects what normalization cannot repair:
// undecodable tool arguments and results that are missing, misordered, or
// orphaned. Empty arguments and empty IDs are legal because pairing is
// positional. It never mutates the stored session.
func ValidateTranscript(msgs []Message) error {
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case RoleAssistant:
			for k, c := range m.ToolCalls {
				if c.Arguments != "" && !json.Valid([]byte(c.Arguments)) {
					return fmt.Errorf("tool call %q has undecodable arguments", c.ID)
				}
				j := i + 1 + k
				if j >= len(msgs) || msgs[j].Role != RoleTool || msgs[j].ToolCallID != c.ID {
					return fmt.Errorf("tool call %q has no paired result", c.ID)
				}
			}
			i += len(m.ToolCalls)
		case RoleTool:
			return fmt.Errorf("orphan tool result %q", m.ToolCallID)
		}
	}
	return nil
}
