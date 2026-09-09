package transcript

import (
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"
)

const (
	defaultPageRecords = 120
	maxPageRecords     = 1000
	defaultPageBytes   = 512 << 10
	maxPageBytes       = 2 << 20
	inlineFieldBytes   = 64 << 10
	previewBytes       = 4 << 10
	contentChunkBytes  = 64 << 10
)

type PageRequest struct {
	SnapshotID string `json:"snapshotId"`
	Before     int    `json:"before"`
	Records    int    `json:"records"`
	Bytes      int    `json:"bytes"`
}

type ContentRef struct {
	SnapshotID string   `json:"snapshotId"`
	RecordID   string   `json:"recordId"`
	Path       []string `json:"path"`
	Bytes      int      `json:"bytes"`
}

type Record struct {
	ID      string       `json:"id"`
	Order   int          `json:"order"`
	Message Message      `json:"message"`
	Refs    []ContentRef `json:"refs"`
}

type Snapshot struct {
	Boundary
	Records        []Record        `json:"records"`
	Runtime        Runtime         `json:"runtime"`
	ActiveAttempts []ActiveAttempt `json:"activeAttempts"`
	ActiveRecords  []Record        `json:"activeRecords"`
	Before         int             `json:"before"`
	HasOlder       bool            `json:"hasOlder"`
	TotalRecords   int             `json:"totalRecords"`
	TotalTurns     int             `json:"totalTurns"`
	Stale          bool            `json:"stale"`
}

type ContentRequest struct {
	ContentRef
	Offset int `json:"offset"`
}

type ContentChunk struct {
	Data       string `json:"data"`
	NextOffset int    `json:"nextOffset"`
	Done       bool   `json:"done"`
	Stale      bool   `json:"stale"`
}

// Snapshot returns a bounded immutable window. Pages and content refs are
// bound to this exact revision. A bounded cache retains immutable cuts while
// streaming continues; an evicted cut returns Stale rather than mixed history.
func (p *Projection) Snapshot(req PageRequest) (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	frozen, err := p.freezeLocked(req.SnapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	if frozen == nil {
		return Snapshot{Boundary: p.boundaryLocked(), Stale: true}, nil
	}
	return frozen.snapshotCurrent(req)
}

func (p *Projection) snapshotCurrent(req PageRequest) (Snapshot, error) {
	out := Snapshot{Boundary: p.boundaryLocked(), Records: []Record{}, ActiveAttempts: []ActiveAttempt{}, ActiveRecords: []Record{}}
	if req.SnapshotID != "" && req.SnapshotID != out.SnapshotID {
		out.Stale = true
		return out, nil
	}
	limit := req.Records
	if limit <= 0 {
		limit = defaultPageRecords
	}
	limit = min(limit, maxPageRecords)
	budget := req.Bytes
	if budget <= 0 {
		budget = defaultPageBytes
	}
	budget = min(budget, maxPageBytes)
	end := len(p.buffer.messages)
	if req.SnapshotID != "" {
		end = min(max(req.Before, 0), end)
	}
	out.TotalRecords = len(p.buffer.messages)
	out.TotalTurns = p.buffer.userTurns
	used := 0
	for i := end - 1; i >= 0 && len(out.Records) < limit; i-- {
		row, err := boundedRecord(p.buffer.messages[i].materialize(), out.SnapshotID)
		if err != nil {
			return Snapshot{}, err
		}
		row.Order = i
		encoded, err := json.Marshal(row)
		if err != nil {
			return Snapshot{}, err
		}
		if len(encoded) > maxPageBytes {
			return Snapshot{}, errors.New("transcript record metadata exceeds the page limit")
		}
		if len(out.Records) > 0 && used+len(encoded) > budget {
			break
		}
		out.Records = append(out.Records, row)
		used += len(encoded)
	}
	for i, j := 0, len(out.Records)-1; i < j; i, j = i+1, j-1 {
		out.Records[i], out.Records[j] = out.Records[j], out.Records[i]
	}
	out.Before = end - len(out.Records)
	out.HasOlder = out.Before > 0
	runtime, attempts := p.runtimeLocked()
	// Detach nested prompt pointers before releasing the mutex.
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return Snapshot{}, err
	}
	if err = json.Unmarshal(encoded, &out.Runtime); err != nil {
		return Snapshot{}, err
	}
	out.ActiveAttempts = attempts
	// A running assistant can precede a large batch of tool result rows. Keep
	// every still-mutable owner in the same snapshot cut so a later delta can
	// never be appended to an unloaded prefix.
	{
		for i := range out.Before {
			m := p.buffer.messages[i].materialize()
			active := (!runtime.Status.Terminal() && m.Pending) || (m.Role == "user" && m.TurnID == runtime.TurnID && runtime.TurnID != "")
			for _, call := range m.ToolCalls {
				active = active || (!runtime.Status.Terminal() && call.Pending)
			}
			if !active {
				continue
			}
			row, err := boundedRecord(m, out.SnapshotID)
			if err != nil {
				return Snapshot{}, err
			}
			row.Order = i
			out.ActiveRecords = append(out.ActiveRecords, row)
		}
	}
	encoded, err = json.Marshal(out)
	if err != nil {
		return Snapshot{}, err
	}
	if len(encoded) > maxPageBytes {
		return Snapshot{}, errors.New("transcript snapshot metadata exceeds the page limit")
	}
	return out, nil
}

func (p *Projection) Content(req ContentRequest) (ContentChunk, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.SnapshotID == "" {
		return ContentChunk{}, errors.New("snapshotId is required")
	}
	frozen, err := p.freezeLocked(req.SnapshotID)
	if err != nil {
		return ContentChunk{}, err
	}
	if frozen == nil {
		return ContentChunk{Stale: true}, nil
	}
	return frozen.contentCurrent(req)
}

func (p *Projection) contentCurrent(req ContentRequest) (ContentChunk, error) {
	if req.SnapshotID != p.boundaryLocked().SnapshotID {
		return ContentChunk{Stale: true}, nil
	}
	if req.Offset < 0 || len(req.Path) == 0 || len(req.Path) > 16 {
		return ContentChunk{}, errors.New("invalid transcript content request")
	}
	for _, row := range p.buffer.messages {
		if row.message.RecordID != req.RecordID {
			continue
		}
		text, ok := contentStringAt(row.materialize(), req.Path)
		if !ok || req.Offset > len(text) || (req.Offset < len(text) && !utf8.RuneStart(text[req.Offset])) {
			return ContentChunk{}, errors.New("invalid transcript content offset")
		}
		end := runeBoundary(text, min(req.Offset+contentChunkBytes, len(text)))
		return ContentChunk{Data: text[req.Offset:end], NextOffset: end, Done: end == len(text)}, nil
	}
	return ContentChunk{}, errors.New("transcript record not found")
}

func boundedRecord(message Message, snapshotID string) (Record, error) {
	out := Record{ID: message.RecordID, Refs: []ContentRef{}}
	out.Message = mapContentStrings(reflect.ValueOf(message), nil, func(text string, path []string) string {
		if len(text) <= inlineFieldBytes {
			return text
		}
		out.Refs = append(out.Refs, ContentRef{SnapshotID: snapshotID, RecordID: out.ID, Path: path, Bytes: len(text)})
		return text[:runeBoundary(text, previewBytes)]
	}).Interface().(Message)
	return out, nil
}

func runeBoundary(text string, offset int) int {
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}
