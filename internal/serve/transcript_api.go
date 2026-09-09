package serve

import (
	"encoding/json"
	"net/http"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/transcript"
)

func (s *Server) registerTranscriptRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /transcript/snapshot", s.transcriptSnapshot)
	mux.HandleFunc("GET /transcript/page", s.transcriptSnapshot)
	mux.HandleFunc("GET /transcript/content", s.transcriptContent)
	mux.HandleFunc("GET /transcript/replay", s.transcriptReplay)
}

// transcriptRead binds each read to the selected controller. A file mirror
// cannot claim a live event cursor and explicitly declines this protocol.
func (s *Server) transcriptRead(w http.ResponseWriter, r *http.Request, read func(control.TranscriptProjectionAPI) (any, error)) {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	ctrl := s.ctl()
	path := agent.CanonicalSessionPath(ctrl.SessionPath())
	if raw := r.URL.Query().Get("session"); raw != "" {
		requested, err := s.resolveSessionPath(raw)
		if err != nil || agent.CanonicalSessionPath(requested) != path {
			http.Error(w, "transcript session is not bound to this runtime", http.StatusConflict)
			return
		}
	}
	api, ok := ctrl.(control.TranscriptProjectionAPI)
	if !ok || s.sessionMirrored(path) {
		http.Error(w, "transcript projection is unavailable", http.StatusNotImplemented)
		return
	}
	value, err := read(api)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func transcriptRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	encoded := r.URL.Query().Get("request")
	if encoded == "" {
		return true
	}
	if len(encoded) > 8192 || json.Unmarshal([]byte(encoded), dst) != nil {
		http.Error(w, "invalid transcript request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) transcriptSnapshot(w http.ResponseWriter, r *http.Request) {
	var req transcript.PageRequest
	if !transcriptRequest(w, r, &req) {
		return
	}
	s.transcriptRead(w, r, func(api control.TranscriptProjectionAPI) (any, error) { return api.TranscriptSnapshot(req) })
}

func (s *Server) transcriptContent(w http.ResponseWriter, r *http.Request) {
	var req transcript.ContentRequest
	if !transcriptRequest(w, r, &req) {
		return
	}
	s.transcriptRead(w, r, func(api control.TranscriptProjectionAPI) (any, error) { return api.TranscriptContent(req) })
}

func (s *Server) transcriptReplay(w http.ResponseWriter, r *http.Request) {
	var req control.TranscriptReplayRequest
	if !transcriptRequest(w, r, &req) {
		return
	}
	s.transcriptRead(w, r, func(api control.TranscriptProjectionAPI) (any, error) { return api.TranscriptReplay(req) })
}
