package agent

import (
	"bytes"
	"encoding/json"
)

// jsonStringOfObject reports whether raw is a JSON-encoded string whose
// content is itself a JSON object, returning the decoded object bytes. The
// capability proxy declares `arguments` as an object, but models sometimes
// emit "{\"task\": …}" — a string wrapping the object. Name injection and the
// underlying skill tools both need the object form (#8472).
func jsonStringOfObject(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return nil, false
	}
	var inner string
	if err := json.Unmarshal(trimmed, &inner); err != nil {
		return nil, false
	}
	if candidate := bytes.TrimSpace([]byte(inner)); len(candidate) > 0 && candidate[0] == '{' {
		return candidate, true
	}
	return nil, false
}
