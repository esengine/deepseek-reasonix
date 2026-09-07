package skill

import (
	"bytes"
	"encoding/json"
)

// unwrapJSONStringArgs tolerates tool arguments that arrive as a JSON-encoded
// string of the intended object. Providers occasionally double-encode — the
// capability proxy declares `arguments` as an object, but models emit
// "{\"task\": …}" (a string containing JSON), and unmarshaling that into the
// arg struct fails with "cannot unmarshal string into Go value of type
// struct {…}" and kills the call (#8472). One unwrap level is enough; a
// string containing a string containing an object is not a shape any real
// caller produces, and silently unwrapping deeper would mask genuine misuse.
func unwrapJSONStringArgs(args []byte) []byte {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return args
	}
	var inner string
	if err := json.Unmarshal(trimmed, &inner); err != nil {
		return args
	}
	// Only accept the unwrap when the decoded string is itself a JSON object;
	// a plain string argument ("some text") stays a plain string.
	if candidate := bytes.TrimSpace([]byte(inner)); len(candidate) > 0 && candidate[0] == '{' {
		return candidate
	}
	return args
}
