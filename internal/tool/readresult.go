package tool

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// ReadResultProtocolVersion is the host-only read-result contract. Additive
// fields keep the version; changing an existing field's meaning bumps it.
// Version 1 envelopes are diagnostic only and never authorize a write.
const ReadResultProtocolVersion = 2

// ReadIntent records why a read happened. It is decided by the host from the
// call's arguments, never inferred from free text.
type ReadIntent string

const (
	// ReadIntentInspect is a bounded preview: completing one page completes the
	// obligation, and remaining content is not an outstanding read debt.
	ReadIntentInspect ReadIntent = "inspect"
	// ReadIntentRange is an explicit window: it completes at the window's end
	// or at a trustworthy source end.
	ReadIntentRange ReadIntent = "range"
	// ReadIntentFull promises whole-file coverage on one content version.
	ReadIntentFull ReadIntent = "full"
)

// ReadRange is a half-open interval [Start, End) over zero-based line indices,
// matching read_file's offset argument. Line N (1-based) is index N-1.
type ReadRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Empty reports whether the range covers no lines.
func (r ReadRange) Empty() bool { return r.End <= r.Start }

// Lines returns the number of lines covered.
func (r ReadRange) Lines() int {
	if r.Empty() {
		return 0
	}
	return r.End - r.Start
}

// ReadCutReason names why a delivery stopped short of the source's end.
type ReadCutReason string

const (
	ReadCutNone       ReadCutReason = ""
	ReadCutPageLimit  ReadCutReason = "page_limit"  // requested line limit reached
	ReadCutSafetyPage ReadCutReason = "safety_page" // local formatted-byte safety page
	ReadCutToolOutput ReadCutReason = "tool_output" // provider-visible byte budget
)

// ReadSourceKind names the store a read actually served.
type ReadSourceKind string

const (
	ReadSourceDisk    ReadSourceKind = "disk"
	ReadSourceOverlay ReadSourceKind = "overlay"
)

// ReadResultSource identifies where the delivered bytes came from and which
// content version they belong to.
type ReadResultSource struct {
	WorkspaceID   string         `json:"workspace_id,omitempty"`
	CanonicalPath string         `json:"canonical_path"`
	Kind          ReadSourceKind `json:"kind,omitempty"`
	// Identity binds captured disk bytes or the exact serving overlay buffer.
	// Empty means the bounded reader did not capture a versionable source.
	Identity string `json:"identity,omitempty"`
	// Snapshot is the content version of this logical read. It stays constant
	// across the pages of one read and changes when the source content changes.
	// An unversioned partial read cannot be stitched into whole-file evidence.
	Snapshot string `json:"snapshot,omitempty"`
}

// ReadResultEnvelope is host-only metadata describing what a reader actually
// delivered to the model. It never enters a provider request, and the model
// sees only the result text and its line numbers.
type ReadResultEnvelope struct {
	ProtocolVersion int              `json:"protocol_version"`
	ReadID          string           `json:"read_id,omitempty"`
	ResultRef       string           `json:"result_ref,omitempty"`
	Source          ReadResultSource `json:"source"`
	Intent          ReadIntent       `json:"intent"`
	// RequestedRange is nil when the caller requested no explicit window.
	RequestedRange  *ReadRange  `json:"requested_range,omitempty"`
	DeliveredRanges []ReadRange `json:"delivered_ranges,omitempty"`
	// WindowDigest covers exactly the delivered lines of this page. It proves
	// this window and can never stand in for a whole-file version.
	WindowDigest string `json:"window_digest,omitempty"`
	HasMore      bool   `json:"has_more"`
	// EOF reports that this delivery reached the source's end.
	EOF bool `json:"eof"`
	// SourceEnd is the zero-based end line index of the source when the reader
	// established it (EOF reached, or the file is empty). nil means the reader
	// stopped early and cannot vouch for where the source ends.
	SourceEnd  *int          `json:"source_end,omitempty"`
	NextCursor string        `json:"next_cursor,omitempty"`
	SourceCut  ReadCutReason `json:"source_cut_reason,omitempty"`
	// TransportCut names a provider-visible truncation on top of the source cut.
	TransportCut ReadCutReason `json:"transport_cut_reason,omitempty"`
}

// ReadExecutor returns metadata from the same immutable source as the output.
// Consumers must not reconstruct source identity by probing the file later.
type ReadExecutor interface {
	ExecuteRead(context.Context, json.RawMessage) (string, ReadResultEnvelope, error)
}

// ReadPathResolver uses the reader's own workspace/alias routing.
type ReadPathResolver interface {
	ResolveReadPath(json.RawMessage) (string, error)
}

type fullReadSnapshotKey struct{}

// WithFullReadSnapshot is host-only intent for a full task's continuation.
func WithFullReadSnapshot(ctx context.Context) context.Context {
	return context.WithValue(ctx, fullReadSnapshotKey{}, true)
}
func FullReadSnapshotRequested(ctx context.Context) bool {
	requested, _ := ctx.Value(fullReadSnapshotKey{}).(bool)
	return requested
}

// ReadWindow is the contiguous numbered window a reader rendered.
type ReadWindow struct {
	StartLine int
	Lines     []string
}

// Range returns the zero-based half-open interval the window covers.
func (w ReadWindow) Range() ReadRange {
	return ReadRange{Start: w.StartLine - 1, End: w.StartLine - 1 + len(w.Lines)}
}

// ParseReadWindow extracts the contiguous `   42→text` window from a reader's
// output. Non-contiguous or unnumbered output returns ok=false: callers must
// fail closed rather than stitch unrelated windows into one observation.
func ParseReadWindow(output string) (ReadWindow, bool) {
	var w ReadWindow
	for line := range strings.SplitSeq(output, "\n") {
		arrow := strings.Index(line, "→")
		if arrow <= 0 {
			continue
		}
		lineNo, err := strconv.Atoi(strings.TrimSpace(line[:arrow]))
		if err != nil || lineNo < 1 {
			continue
		}
		if len(w.Lines) == 0 {
			w.StartLine = lineNo
		} else if lineNo != w.StartLine+len(w.Lines) {
			return ReadWindow{}, false
		}
		w.Lines = append(w.Lines, line[arrow+len("→"):])
	}
	if len(w.Lines) == 0 {
		return ReadWindow{}, false
	}
	return w, true
}

// ReadTrailer is the paging state a reader appends to its own result text. The
// zero value means no trailer was present.
type ReadTrailer struct {
	NextOffset   int
	RequestedEnd int
	HasMore      bool
	LocalSafety  bool
}

// ParseReadTrailer reads the reader's own paging trailer. It is the reader's
// format, not a third party's, so the reader owns both sides of it.
func ParseReadTrailer(output string) ReadTrailer {
	const safetyPrefix = "\n[read_file local safety page; next_offset="
	if start := strings.LastIndex(output, safetyPrefix); start >= 0 && strings.HasSuffix(output, "]\n") {
		fields := strings.TrimSuffix(output[start+len(safetyPrefix):], "]\n")
		parts := strings.Fields(fields)
		if len(parts) >= 2 {
			next, nextErr := strconv.Atoi(parts[0])
			end, endErr := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(parts[1], "requested_end="), ";"))
			if nextErr == nil && endErr == nil && next >= 0 && end >= next {
				return ReadTrailer{NextOffset: next, RequestedEnd: end, HasMore: true, LocalSafety: true}
			}
		}
	}
	if n, ok := readTrailerOffset(output, "\n[PARTIAL view:", "pass offset="); ok {
		return ReadTrailer{NextOffset: n, HasMore: true}
	}
	if n, ok := readTrailerOffset(output, "\n[more lines below; pass offset=", ""); ok {
		return ReadTrailer{NextOffset: n, HasMore: true}
	}
	return ReadTrailer{}
}

func readTrailerOffset(output, marker, field string) (int, bool) {
	start := strings.LastIndex(output, marker)
	if start < 0 || !strings.HasSuffix(output, "]\n") {
		return 0, false
	}
	segment := output[start:]
	if field != "" {
		idx := strings.Index(segment, field)
		if idx < 0 {
			return 0, false
		}
		segment = segment[idx+len(field):]
	} else {
		segment = segment[len(marker):]
	}
	if end := strings.IndexAny(segment, " ]\r\n"); end >= 0 {
		segment = segment[:end]
	}
	n, err := strconv.Atoi(segment)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// WindowDigest binds one delivered window to its content. Two reads that
// deliver byte-identical lines produce the same digest; any edit inside the
// window changes it. It says nothing about lines outside the window.
func WindowDigest(canonicalPath string, w ReadWindow) string {
	h := sha256.New()
	h.Write([]byte("reasonix/read-window/v2\x00"))
	h.Write([]byte(canonicalPath))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(w.StartLine)))
	for _, line := range w.Lines {
		h.Write([]byte{0})
		h.Write([]byte(line))
	}
	return "wd2:" + hex.EncodeToString(h.Sum(nil))
}

// SourceSnapshot derives the content version of one logical read from its
// source kind and store identity. The same source yields the same snapshot
// across pages; a changed store yields a different one.
func SourceSnapshot(kind ReadSourceKind, canonicalPath, identity string) string {
	if identity == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte("reasonix/read-source/v2\x00"))
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(canonicalPath))
	h.Write([]byte{0})
	h.Write([]byte(identity))
	return "ss2:" + hex.EncodeToString(h.Sum(nil))
}

// ClipTo narrows the envelope to the numbered lines actually present in the
// provider-visible text, recording the transport cut. Callers pass the raw
// result unchanged when nothing was truncated.
func (e ReadResultEnvelope) ClipTo(visible string) ReadResultEnvelope {
	w, ok := ParseReadWindow(visible)
	if !ok {
		e.DeliveredRanges = nil
		e.WindowDigest = ""
		e.HasMore = true
		e.EOF = false
		e.SourceEnd = nil
		e.TransportCut = ReadCutToolOutput
		return e
	}
	visibleRange := w.Range()
	covered := len(e.DeliveredRanges) > 0
	for _, r := range e.DeliveredRanges {
		if r.Start < visibleRange.Start || r.End > visibleRange.End {
			covered = false
			break
		}
	}
	if covered {
		return e
	}
	var kept []ReadRange
	for _, r := range e.DeliveredRanges {
		if start, end := max(r.Start, visibleRange.Start), min(r.End, visibleRange.End); start < end {
			kept = append(kept, ReadRange{Start: start, End: end})
		}
	}
	e.DeliveredRanges = kept
	e.WindowDigest = WindowDigest(e.Source.CanonicalPath, w)
	e.HasMore = true
	e.EOF = false
	e.SourceEnd = nil
	e.TransportCut = ReadCutToolOutput
	e.NextCursor = EncodeReadCursor(ReadCursor{
		Path:      e.Source.CanonicalPath,
		Snapshot:  e.Source.Snapshot,
		ReadID:    e.ReadID,
		NextStart: visibleRange.End,
	})
	return e
}

// ReadCursor is a host-issued continuation reference. It is opaque to callers,
// bound to one session, run generation, read task, source snapshot, requested
// window, and exact next position, and is validated at the execution entry —
// decoding it is not the same as accepting it.
type ReadCursor struct {
	Version    int    `json:"v"`
	Binding    string `json:"b,omitempty"`
	SessionID  string `json:"s,omitempty"`
	RunGen     uint64 `json:"g,omitempty"`
	ReadID     string `json:"r"`
	Path       string `json:"p"`
	Snapshot   string `json:"n,omitempty"`
	RequestEnd int    `json:"e,omitempty"`
	NextStart  int    `json:"i"`
}

const readCursorPrefix = "rc2:"

// EncodeReadCursor renders a cursor as an opaque token.
func EncodeReadCursor(c ReadCursor) string {
	// A reader can encode the position it knows; the host stamps the logical
	// read id before the cursor is ever handed to a model.
	if c.Path == "" || c.NextStart < 0 {
		return ""
	}
	c.Version = ReadResultProtocolVersion
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return readCursorPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeReadCursor parses a token produced by EncodeReadCursor. It only proves
// the token is well formed; callers must still validate it against the live
// session, run, read task, and source snapshot.
func DecodeReadCursor(token string) (ReadCursor, bool) {
	rest, ok := strings.CutPrefix(token, readCursorPrefix)
	if !ok {
		return ReadCursor{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return ReadCursor{}, false
	}
	var c ReadCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return ReadCursor{}, false
	}
	if c.Version != ReadResultProtocolVersion || c.Path == "" || c.NextStart < 0 {
		return ReadCursor{}, false
	}
	return c, true
}

// Matches reports whether the cursor still belongs to this envelope: same read
// task, canonical path, content snapshot, and a start inside the delivered range.
func (c ReadCursor) Matches(e ReadResultEnvelope) bool {
	if c.ReadID != e.ReadID || c.Path != e.Source.CanonicalPath {
		return false
	}
	if c.Snapshot != "" && e.Source.Snapshot != "" && c.Snapshot != e.Source.Snapshot {
		return false
	}
	if len(e.DeliveredRanges) == 0 {
		return false
	}
	last := e.DeliveredRanges[len(e.DeliveredRanges)-1]
	return c.NextStart >= last.Start && c.NextStart <= last.End
}

// ReadEnvelopeProvider is an optional reader capability that reports what it
// delivered. output is the reader's own result text; the host clips the
// returned envelope to the provider-visible bytes before using it.
type ReadEnvelopeProvider interface {
	ReadEnvelope(ctx context.Context, args json.RawMessage, output string) (ReadResultEnvelope, bool)
}
