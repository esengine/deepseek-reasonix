package serve

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"reasonix/internal/control"
)

const maxUploadBytes = 64 << 20 // 64 MiB, matching maxImageAttachmentBytes

// POST /attachments — upload an image and get back a reference path.
//
// Request body (JSON):
//
//	{"data": "<base64-encoded image>", "mime": "image/png"}
//
// Returns:
//
//	{"ref": ".reasonix/attachments/clipboard-<ts>-<seq>.png"}
//
// The caller then sends `POST /submit {"input": "@<ref> ..."}` to include
// the image in a message.  The agent's existing @ref parsing and multimodal
// pipeline handles the rest — no protocol changes needed.
func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	if s.ctl() == nil {
		http.Error(w, "no active session", http.StatusBadRequest)
		return
	}
	root := s.ctl().WorkspaceRoot()
	if root == "" {
		http.Error(w, "workspace root unknown", http.StatusBadRequest)
		return
	}

	var req struct {
		Data string `json:"data"` // base64-encoded image bytes
		Mime string `json:"mime"` // optional, e.g. "image/png"
	}
	// Cap the request body before decoding so oversized uploads can't exhaust
	// memory. maxUploadBytes is the decoded image size; base64 inflates by ~4/3
	// in transit, so cap the raw body at maxUploadBytes*2 (64 MiB + headroom).
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes*2)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}
	if req.Data == "" {
		http.Error(w, "data field required (base64-encoded image)", http.StatusBadRequest)
		return
	}

	raw, err := decodeBase64Flexible(req.Data)
	if err != nil {
		http.Error(w, "invalid base64 data", http.StatusBadRequest)
		return
	}
	if len(raw) > maxUploadBytes {
		http.Error(w, "file too large (max 64 MiB)", http.StatusRequestEntityTooLarge)
		return
	}

	// Normalize MIME: strip parameters like "; charset=..."
	mime := strings.TrimSpace(req.Mime)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}

	path, err := control.SaveImageBytesInRoot(root, mime, raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]string{"ref": path})
}

// decodeBase64Flexible decodes standard or URL-safe base64, tolerating missing
// padding (unpadded encoding) which many clients produce. It trims surrounding
// whitespace first. The strconv-based length check mirrors Go's raw unpadded
// decode: padding is appended only when the remainder calls for it.
func decodeBase64Flexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	encs := []*base64.Encoding{base64.StdEncoding, base64.URLEncoding}
	for i := 0; i < len(encs); i++ {
		raw, err := encs[i].DecodeString(s)
		if err == nil {
			return raw, nil
		}
		// Missing-padding error: append the needed '='s and retry.
		if rem := len(s) % 4; rem > 0 {
			if padded, e := encs[i].DecodeString(s + strings.Repeat("=", 4-rem)); e == nil {
				return padded, nil
			}
		}
	}
	return nil, fmt.Errorf("invalid base64 data")
}
