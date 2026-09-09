package serve

import (
	"encoding/json"
	"net/http"
	"strings"

	"reasonix/internal/control"
)

// submit runs raw user input as a turn (slash commands and @-references
// resolved by the controller). Returns 202 — output arrives on the event stream.
// An optional "format":"json_object" asks the model for structured JSON output
// on this turn (text.format on the wire).
type submitRequest struct {
	SubmissionID string `json:"submissionId"`
	Input        string `json:"input"`
	Format       string `json:"format"`
	Action       string `json:"action"`
	RecoveryID   string `json:"recoveryId"`
}

func decodeSubmitRequest(w http.ResponseWriter, r *http.Request) (submitRequest, string, bool) {
	var body submitRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Input == "" && body.Action != control.ProtocolRecoveryAction) {
		http.Error(w, "missing input", http.StatusBadRequest)
		return submitRequest{}, "", false
	}
	if len(body.SubmissionID) > 256 {
		http.Error(w, "submissionId is too long", http.StatusBadRequest)
		return submitRequest{}, "", false
	}
	body.Format = strings.TrimSpace(body.Format)
	body.Action = strings.TrimSpace(body.Action)
	if body.Action == control.ProtocolRecoveryAction && strings.TrimSpace(body.RecoveryID) == "" {
		http.Error(w, "missing recoveryId", http.StatusBadRequest)
		return submitRequest{}, "", false
	}
	switch body.Format {
	case "", "json_object":
		// Supported: empty = default text output, json_object = structured.
	default:
		http.Error(w, `unsupported format (supported: "json_object")`, http.StatusBadRequest)
		return submitRequest{}, "", false
	}
	if err := validateSubmitAction(body.Format, body.Action); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return submitRequest{}, "", false
	}
	trimmed := strings.TrimSpace(body.Input)
	// Typed recovery guidance is never dispatched as a management command.
	if body.Action != "" {
		trimmed = ""
	}
	if strings.HasPrefix(trimmed, "!") {
		http.Error(w, "shell commands are unavailable over HTTP", http.StatusForbidden)
		return submitRequest{}, "", false
	}
	return body, trimmed, true
}
