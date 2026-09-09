// Package builtin provides Reasonix's compile-time built-in tools. Each tool
// self-registers via init(); main blank-imports this package to wire them in.
package builtin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/text/transform"

	fileenc "reasonix/internal/fileutil/encoding"
	"reasonix/internal/tool"
)

const (
	readFileBinaryPeek        = 8 * 1024   // bytes scanned for NUL before reading further
	readFileDetectSample      = 256 * 1024 // bytes sampled for encoding detection before streaming
	readFileMaxLineBytes      = 1024 * 1024
	readFileMaxFormattedBytes = 8 << 20
	readFileCountBudget       = 32 * 1024 // bytes scanned past the window to size the trailer total
)

func init() { tool.RegisterBuiltin(readFile{}) }

// readFile reads a text file. workDir, when non-empty, is the directory a
// relative path is resolved against (see resolveIn). paths maps session-scoped
// external read aliases to local roots without changing the model-visible tool
// schema. forbidRoots lists directories the tool may not read from (resolved,
// absolute paths).
type readFile struct {
	workDir     string
	paths       *PathResolver
	forbidRoots []string
	// overlay, when non-nil, serves content from the host transport (unsaved
	// editor buffers) before falling back to disk. Consulted only after path
	// resolution and read confinement, and never for external alias paths.
	overlay     FileOverlay
	captured    *tool.ReadResultSource
	rawSnapshot []byte
}

const (
	readFileDefaultLimit = 2000 // lines returned when limit is unset
)

// readFileParams is one validated read_file call with defaults applied.
type readFileParams struct {
	Path        string
	Intent      tool.ReadIntent
	WindowGiven bool
	Offset      int
	Limit       int
}

const (
	readFileEmptyOutput = "(empty file)"
	readFilePastEOFTail = " is past EOF — file has "
)

// readWindowGiven reports whether the call named an explicit line window.
func readWindowGiven(args json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return false
	}
	_, offset := fields["offset"]
	_, limit := fields["limit"]
	return offset || limit
}

// parseReadFileParams validates one read_file call and applies the documented
// defaults, so Execute and ReadEnvelope agree on what was requested.
func parseReadFileParams(args json.RawMessage) (readFileParams, error) {
	var p struct {
		Path   string `json:"path"`
		Intent string `json:"intent,omitempty"`
		Offset int    `json:"offset,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return readFileParams{}, fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return readFileParams{}, fmt.Errorf("path is required")
	}
	windowGiven := readWindowGiven(args)
	intent, err := readIntentFor(p.Intent, windowGiven)
	if err != nil {
		return readFileParams{}, err
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Limit <= 0 {
		p.Limit = readFileDefaultLimit
	}
	return readFileParams{Path: p.Path, Intent: intent, WindowGiven: windowGiven, Offset: p.Offset, Limit: p.Limit}, nil
}

// readIntentFor resolves the effective read intent and rejects combinations
// that would leave the caller unsure which promise the call made.
func readIntentFor(explicit string, windowGiven bool) (tool.ReadIntent, error) {
	switch tool.ReadIntent(strings.TrimSpace(explicit)) {
	case "":
		if windowGiven {
			return tool.ReadIntentRange, nil
		}
		return tool.ReadIntentInspect, nil
	case tool.ReadIntentInspect:
		return tool.ReadIntentInspect, nil
	case tool.ReadIntentRange:
		if !windowGiven {
			return "", fmt.Errorf("intent=range requires an explicit offset or limit; pass the window to read, or use intent=inspect for a bounded preview")
		}
		return tool.ReadIntentRange, nil
	case tool.ReadIntentFull:
		if windowGiven {
			return "", fmt.Errorf("intent=full cannot be combined with offset or limit; omit them to scan the whole file, or use intent=range for one window")
		}
		return tool.ReadIntentFull, nil
	default:
		return "", fmt.Errorf("intent must be inspect, range, or full (got %q)", explicit)
	}
}

func (readFile) Name() string { return "read_file" }

func (readFile) Description() string {
	return "Read a text file with optional line offset/limit. Output prefixes each line with its 1-based number (e.g. `   42→...`) so subsequent edit_file calls can target exact lines. Use `offset` and `limit` to page through large files; the tool reports total length and pagination hints in a trailer. Set `intent` to state why you are reading: inspect (default, a bounded preview), range (an explicit window), or full (scan the whole file). Independent reads with no data dependency should be issued in the same round."
}

func (readFile) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"File path"},
  "intent":{"type":"string","enum":["inspect","range","full"],"description":"Why you are reading. inspect (default): a bounded preview; one page is a complete answer. range (default when offset or limit is given): an explicit window. full: scan the whole file, paging until every line has been delivered."},
  "cursor":{"type":"string","description":"Continuation cursor returned by a previous read_file result. It names the exact next position; pass it back unchanged instead of computing an offset."},
  "offset":{"type":"integer","description":"0-based line offset to start reading from (default 0)","minimum":0},
  "limit":{"type":"integer","description":"Maximum lines to return (default 2000)","minimum":1}
},
"required":["path"]
}`)
}

func (readFile) ReadOnly() bool { return true }

// ObserveModelText extracts the exact numbered window returned by read_file.
// It intentionally parses the already-produced output instead of rereading
// the file, so overlay and encoding routing remain identical to what the model
// saw and truncated results can still be promoted through RawContent.
func (r readFile) ObserveModelText(args json.RawMessage, output string) (tool.ModelTextObservation, bool) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil || strings.TrimSpace(p.Path) == "" {
		return tool.ModelTextObservation{}, false
	}
	window, ok := tool.ParseReadWindow(output)
	if !ok {
		return tool.ModelTextObservation{}, false
	}
	hashes := make([]string, len(window.Lines))
	for i, line := range window.Lines {
		sum := sha256.Sum256([]byte(line))
		hashes[i] = hex.EncodeToString(sum[:])
	}
	rp := resolveReadablePath(r.workDir, p.Path, r.paths)
	return tool.ModelTextObservation{
		Path:       rp.Path,
		StartLine:  window.StartLine,
		LineHashes: hashes,
		Version:    tool.WindowDigest(rp.Path, window),
	}, true
}

// ReadEnvelope reports what one read_file call delivered. The source identity
// comes from the store that actually served the content, the snapshot stays
// constant across the pages of one logical read, and the window digest covers
// only this page's delivered lines. read_id, result_ref and workspace_id are
// host identity the agent fills in.
func (r readFile) ReadEnvelope(ctx context.Context, args json.RawMessage, output string) (tool.ReadResultEnvelope, bool) {
	p, err := parseReadFileParams(args)
	if err != nil {
		return tool.ReadResultEnvelope{}, false
	}
	rp := resolveReadablePath(r.workDir, p.Path, r.paths)
	env := tool.ReadResultEnvelope{
		ProtocolVersion: tool.ReadResultProtocolVersion,
		Source:          tool.ReadResultSource{CanonicalPath: rp.Path},
		Intent:          p.Intent,
	}
	if p.WindowGiven {
		requested := tool.ReadRange{Start: p.Offset, End: p.Offset + p.Limit}
		env.RequestedRange = &requested
	}

	// The store that served the content owns the identity: an unsaved editor
	// buffer must never be proven by the disk file's identity.
	if r.captured != nil {
		env.Source = *r.captured
	}
	env.Source.Snapshot = tool.SourceSnapshot(env.Source.Kind, rp.Path, env.Source.Identity)

	window, hasWindow := tool.ParseReadWindow(output)
	if hasWindow {
		env.DeliveredRanges = []tool.ReadRange{window.Range()}
		env.WindowDigest = tool.WindowDigest(rp.Path, window)
	}
	trailer := tool.ParseReadTrailer(output)
	env.HasMore = trailer.HasMore
	env.EOF = !trailer.HasMore
	switch {
	case trailer.LocalSafety:
		env.SourceCut = tool.ReadCutSafetyPage
	case trailer.HasMore:
		env.SourceCut = tool.ReadCutPageLimit
	}
	if env.EOF {
		if end, ok := readSourceEnd(output, window, hasWindow); ok {
			env.SourceEnd = &end
		}
	}
	if trailer.HasMore {
		env.NextCursor = tool.EncodeReadCursor(tool.ReadCursor{
			Path:      rp.Path,
			Snapshot:  env.Source.Snapshot,
			NextStart: trailer.NextOffset,
		})
	}
	return env, true
}

// overlayText mirrors Execute's overlay routing so the envelope names the same
// store that produced the delivered bytes.
func (r readFile) overlayText(ctx context.Context, rp ResolvedPath) (string, bool) {
	if r.overlay == nil || rp.External || !filepath.IsAbs(rp.Path) {
		return "", false
	}
	return r.overlay.ReadTextFile(ctx, rp.Path)
}

func digestText(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// readSourceEnd recovers the source's zero-based end line index from the
// reader's own result text: a complete window ends at its last line, and the
// empty-file / past-EOF markers state the count directly.
func readSourceEnd(output string, window tool.ReadWindow, hasWindow bool) (int, bool) {
	if hasWindow {
		return window.Range().End, true
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == readFileEmptyOutput {
		return 0, true
	}
	if rest, ok := strings.CutPrefix(trimmed, "(offset "); ok {
		if _, tail, found := strings.Cut(rest, readFilePastEOFTail); found {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(tail), " lines)")); err == nil && n >= 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// SnipHint front-loads file content: the most relevant lines are near the top,
// so keep a generous head and a short tail when an old read is shortened.
func (readFile) SnipHint() tool.SnipHint {
	return tool.SnipHint{Head: 120, Tail: 12, HeadChars: 12000, TailChars: 2000}
}

func (r readFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseReadFileParams(args)
	if err != nil {
		return "", err
	}
	rp := resolveReadablePath(r.workDir, p.Path, r.paths)
	p.Path = rp.Path
	displayPath := rp.DisplayPath
	if confineRead(r.forbidRoots, p.Path) {
		err := &os.PathError{Op: "open", Path: p.Path, Err: os.ErrNotExist}
		if rp.External {
			return "", fmt.Errorf("read %s: %s", displayPath, rp.ErrorText(err))
		}
		return "", err
	}
	if r.rawSnapshot != nil {
		return r.scanEncoded(readContextReader{ctx, bytes.NewReader(r.rawSnapshot)}, p.Offset, p.Limit)
	}

	// The host overlay (unsaved editor buffers) wins over the disk when it can
	// serve the path. Content arrives already decoded as text, so the encoding
	// and binary-detection pipeline below applies to the disk fallback only.
	if r.overlay != nil && !rp.External && filepath.IsAbs(p.Path) {
		if content, ok := r.overlay.ReadTextFile(ctx, p.Path); ok {
			return r.scan(readContextReader{ctx, strings.NewReader(content)}, p.Offset, p.Limit)
		}
	}

	// A directory can be os.Open'd but not read as text — catch it up front with
	// an actionable message (and avoid the doubled "read X: read X:" the scanner's
	// error would otherwise produce) so the model switches to the ls tool.
	if info, err := os.Stat(p.Path); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file — use the ls tool to list it, or read a specific file inside it", displayPath)
	}

	f, err := os.Open(p.Path)
	if err != nil {
		if rp.External {
			return "", fmt.Errorf("read %s: %s", displayPath, rp.ErrorText(err))
		}
		return "", fmt.Errorf("read %s: %w", displayPath, err)
	}
	defer f.Close()
	return r.scanEncoded(readContextReader{ctx, f}, p.Offset, p.Limit)
}

func (r readFile) scanEncoded(f io.Reader, offset, limit int) (string, error) {

	// Peek the first 8 KiB to reject binary files cheaply (a NUL byte) before
	// reading further — keeps a multi-GB archive from being slurped just to be
	// discarded.
	peek := make([]byte, readFileBinaryPeek)
	pn, perr := io.ReadFull(f, peek)
	peek = peek[:pn]
	peekEOF := perr != nil // whole file fit in the peek (EOF / ErrUnexpectedEOF)

	// BOM check first: UTF-16 files contain 0x00 for every ASCII character, so a
	// naive NUL check would misidentify them as binary.
	switch fileenc.DetectQuick(peek) {
	case fileenc.UTF16LE, fileenc.UTF16BE:
		enc := fileenc.DetectQuick(peek)
		return r.scan(transform.NewReader(io.MultiReader(bytes.NewReader(peek), f), fileenc.Decoder(enc)), offset, limit)
	case fileenc.UTF8BOM:
		// Strip the 3-byte BOM; the content is valid UTF-8 and streams directly.
		body := peek
		if len(body) >= 3 {
			body = body[3:]
		}
		return r.scan(io.MultiReader(bytes.NewReader(body), f), offset, limit)
	}

	// BOM-less UTF-16 (Windows source files) has a NUL for every ASCII char but
	// no BOM, so it reaches here; recognise it by its NUL pattern and decode it
	// rather than rejecting it as binary.
	if k, ok := fileenc.DetectUTF16NoBOM(peek); ok {
		return r.scan(transform.NewReader(io.MultiReader(bytes.NewReader(peek), f), fileenc.Decoder(k)), offset, limit)
	}

	if bytes.IndexByte(peek, 0) >= 0 {
		return "", fmt.Errorf("binary file (NUL byte detected); use a binary inspection tool")
	}

	// Read up to a bounded sample for encoding detection, then stream the rest —
	// so a large text file isn't slurped whole just to return a few lines.
	head := peek
	if !peekEOF {
		more := make([]byte, readFileDetectSample-len(peek))
		mn, merr := io.ReadFull(f, more)
		head = append(peek, more[:mn]...)
		peekEOF = merr != nil
	}

	// Detect from a char-safe slice: when more file follows, trim to the last
	// newline so the sample never ends mid multi-byte sequence (UTF-8 and GB18030
	// are ASCII-transparent, so '\n' is always a clean boundary).
	sample := head
	if !peekEOF {
		if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
			sample = head[:i+1]
		}
	}
	enc, _ := fileenc.Detect(sample)

	src := io.MultiReader(bytes.NewReader(head), f)
	if dec := fileenc.Decoder(enc); dec != nil {
		return r.scan(transform.NewReader(src, dec), offset, limit)
	}
	return r.scan(src, offset, limit)
}

// scan reads lines from src and returns the formatted output with line numbers.
func (r readFile) scan(src io.Reader, offset, limit int) (string, error) {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), readFileMaxLineBytes)

	var collected []string
	textBytes := 0
	lineNo := 0
	safetyPaged := false
	capped := false
	requestedEnd := offset + limit
	for scanner.Scan() {
		lineNo++
		if lineNo <= offset {
			continue
		}
		if len(collected) < limit {
			line := scanner.Text()
			count := len(collected) + 1
			width := len(strconv.Itoa(offset + count))
			nextOffset := offset + count
			bodyBytes := textBytes + len(line) + count*(width+len("→")+1)
			trailer := readFileSafetyTrailer(nextOffset, requestedEnd)
			if bodyBytes+len(trailer) > readFileMaxFormattedBytes {
				safetyPaged = true
				break
			}
			collected = append(collected, line)
			textBytes += len(line)
			continue
		}
		// Size the trailer total from a bounded look-ahead: a 3-line window on
		// a multi-GB file must not drain the file. Past the budget the total
		// is reported as a lower bound.
		counted := 0
		for scanner.Scan() {
			lineNo++
			counted += len(scanner.Bytes()) + 1
			if counted >= readFileCountBudget {
				capped = true
				break
			}
		}
		break
	}
	if err := scanReadFileError(scanner.Err()); err != nil {
		return "", err
	}

	if lineNo == 0 {
		return readFileEmptyOutput, nil
	}
	if len(collected) == 0 {
		return fmt.Sprintf("(offset %d%s%d lines)", offset, readFilePastEOFTail, lineNo), nil
	}

	maxShown := offset + len(collected)
	w := len(fmt.Sprint(maxShown))

	var b strings.Builder
	for i, line := range collected {
		fmt.Fprintf(&b, "%*d→%s\n", w, offset+i+1, line)
	}
	if safetyPaged {
		b.WriteString(readFileSafetyTrailer(offset+len(collected), requestedEnd))
	} else if maxShown < lineNo || capped {
		fmt.Fprintf(&b, "\n[PARTIAL view: showing lines %d-%d of %s. The file continues; pass offset=%d to read on, or grep for the section you need. A partial read is fine when the visible range is sufficient.]\n",
			offset+1, maxShown, formatReadFileTotal(lineNo, capped), offset+len(collected))
	}
	return b.String(), nil
}

func scanReadFileError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "token too long") {
		return fmt.Errorf("scan: source line exceeds the 1 MiB local safety limit: %w", err)
	}
	return fmt.Errorf("scan: %w", err)
}

func formatReadFileTotal(lineNo int, capped bool) string {
	if capped {
		return fmt.Sprintf("%d+", lineNo)
	}
	return strconv.Itoa(lineNo)
}

func readFileSafetyTrailer(nextOffset, requestedEnd int) string {
	return fmt.Sprintf("\n[read_file local safety page; next_offset=%d requested_end=%d; output capped at 8 MiB for this call]\n", nextOffset, requestedEnd)
}
