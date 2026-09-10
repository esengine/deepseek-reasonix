package cli

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/provider"
)

type transcriptSourceKind uint8

const (
	transcriptSourceFixed transcriptSourceKind = iota
	transcriptSourceMarkdown
	transcriptSourceUser
	transcriptSourceReasoning
	transcriptSourceToolCard
	transcriptSourceBanner
	transcriptSourceReplayBundle
	transcriptSourceTurnReceipt
	transcriptSourceSubagentProgress
)

// transcriptSource retains only the semantic inputs needed to reproduce a
// width-dependent transcript block. It deliberately sits beside []string
// instead of replacing it: the rendered slice remains the fast path for every
// frame and preserves the many index-based live tool/reasoning updates.
type transcriptSource struct {
	kind transcriptSourceKind
	raw  string
	aux  string
	// copyRendered mirrors an already-rendered fixed block with internal copy
	// spans. It is never displayed or persisted; selection copy consumes the
	// spans to omit decorations whose provenance would otherwise be ambiguous.
	copyRendered string
	planMode     bool
	maxLines     int
	history      []provider.Message
}

func (m *chatTUI) ensureTranscriptSources() {
	if len(m.transcriptSources) > len(m.transcript) {
		m.transcriptSources = m.transcriptSources[:len(m.transcript)]
	}
	for len(m.transcriptSources) < len(m.transcript) {
		m.transcriptSources = append(m.transcriptSources, transcriptSource{kind: transcriptSourceFixed})
	}
}

func (m *chatTUI) appendTranscriptBlock(rendered string, source transcriptSource) {
	m.ensureTranscriptSources()
	m.transcript = append(m.transcript, rendered)
	m.transcriptSources = append(m.transcriptSources, source)
	// Wrap cache extends on next Update via append-only path.
}

func (m *chatTUI) setTranscriptBlock(index int, rendered string, source transcriptSource) {
	if index < 0 || index >= len(m.transcript) {
		return
	}
	m.ensureTranscriptSources()
	m.transcript[index] = rendered
	m.transcriptSources[index] = source
	// In-place rewrite: drop wrap from this block onward so the next sync
	// re-wraps the mutated block and everything after it.
	m.invalidateWrapFrom(index)
}

func (m *chatTUI) removeTranscriptBlock(index int) {
	if index < 0 || index >= len(m.transcript) {
		return
	}
	m.ensureTranscriptSources()
	m.transcript = append(m.transcript[:index], m.transcript[index+1:]...)
	m.transcriptSources = append(m.transcriptSources[:index], m.transcriptSources[index+1:]...)
	m.invalidateWrapFrom(index)
}

func (m *chatTUI) truncateTranscriptBlocks(length int) {
	length = min(max(length, 0), len(m.transcript))
	m.ensureTranscriptSources()
	m.transcript = m.transcript[:length]
	m.transcriptSources = m.transcriptSources[:length]
	m.invalidateWrapFrom(length)
}

func (m *chatTUI) renderTranscriptSource(source transcriptSource, terminalWidth int) string {
	contentWidth := transcriptContentWidth(terminalWidth, m.nativeScrollback)
	switch source.kind {
	case transcriptSourceMarkdown:
		return renderAssistantMarkdown(source.raw, contentWidth)
	case transcriptSourceUser:
		return renderUserBubble(source.raw, terminalWidth, source.planMode)
	case transcriptSourceReasoning:
		return reasoningBlock(source.raw, terminalWidth, source.maxLines)
	case transcriptSourceToolCard:
		return toolCard(source.raw, source.aux, terminalWidth)
	case transcriptSourceBanner:
		return strings.TrimRight(renderTUIBanner(m.label, source.raw, contentWidth), "\n")
	case transcriptSourceReplayBundle:
		return m.renderReplayBundle(source, contentWidth, renderAssistantMarkdown)
	case transcriptSourceTurnReceipt:
		return renderTurnReceiptBand(source.raw, contentWidth)
	case transcriptSourceSubagentProgress:
		if sp := m.subagentProgress[source.raw]; sp != nil {
			return m.subagentProgressBlock(source.raw, sp)
		}
		return ""
	default:
		return ""
	}
}

func (m chatTUI) renderReplayBundle(
	source transcriptSource,
	contentWidth int,
	renderAssistant func(string, int) string,
) string {
	return m.renderReplayBundleWithRenderers(source, contentWidth, renderAssistant, reasoningBlock)
}

func (m chatTUI) renderReplayBundleWithRenderers(
	source transcriptSource,
	contentWidth int,
	renderAssistant func(string, int) string,
	renderReasoning func(string, int, int) string,
) string {
	var b strings.Builder
	b.WriteString(renderTUIBanner(m.label, source.raw, contentWidth))
	for _, section := range replaySectionsForWithRenderers(
		source.history,
		contentWidth,
		renderAssistant,
		renderReasoning,
	) {
		b.WriteString(section)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m chatTUI) renderReplayBundleCopy(
	source transcriptSource,
	contentWidth int,
	prefix string,
) string {
	assistantIndex := 0
	return m.renderReplayBundleWithRenderers(source, contentWidth, func(raw string, width int) string {
		messagePrefix := prefix + "-" + strconv.Itoa(assistantIndex)
		assistantIndex++
		return renderAssistantMarkdownCopy(raw, width, messagePrefix)
	}, reasoningBlockCopy)
}

const assistantTranscriptIndent = "  "

// renderAssistantMarkdown gives assistant prose the same explicit transcript
// identity that user, reasoning, tool, and receipt blocks already have. The
// body keeps a restrained two-cell gutter instead of using a heavy card, and
// rendering at the reduced width keeps every indented row inside the viewport.
func renderAssistantMarkdown(raw string, contentWidth int) string {
	contentWidth = max(contentWidth, 1)
	indent := assistantTranscriptIndent
	if contentWidth <= visibleWidth(indent) {
		indent = ""
	}
	bodyWidth := max(contentWidth-visibleWidth(indent), 1)
	renderer := newMarkdownRenderer(bodyWidth)
	rendered := renderer.Render(raw)
	if rendered == "" {
		rendered = raw
	}
	body := strings.TrimRight(rendered, "\n")
	header := indent + accent("◆") + " " + bold("Reasonix")
	if body == "" {
		return header
	}
	return header + "\n\n" + indentTranscriptBlock(body, indent)
}

// renderAssistantMarkdownCopy mirrors renderAssistantMarkdown's visible output
// and adds zero-width copy spans for math reconstruction and generated gutters.
func renderAssistantMarkdownCopy(raw string, contentWidth int, prefix string) string {
	contentWidth = max(contentWidth, 1)
	indent := assistantTranscriptIndent
	if contentWidth <= visibleWidth(indent) {
		indent = ""
	}
	bodyWidth := max(contentWidth-visibleWidth(indent), 1)
	renderer := newMarkdownRenderer(bodyWidth)
	rendered := renderer.RenderCopy(raw, prefix)
	if rendered == "" {
		rendered = raw
	}
	body := strings.TrimRight(rendered, "\n")
	header := indent + accent("◆") + " " + bold("Reasonix")
	if body == "" {
		return header
	}
	return header + "\n\n" + indentTranscriptBlock(body, indent)
}

func indentTranscriptBlock(block, indent string) string {
	if indent == "" || block == "" {
		return block
	}
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if line != "" {
			if rest, ok := strings.CutPrefix(line, copyOmitSpanStart); ok {
				lines[i] = copyOmitSpanStart + indent + rest
			} else {
				lines[i] = indent + line
			}
		}
	}
	return strings.Join(lines, "\n")
}

func renderTurnReceiptBand(receipt string, contentWidth int) string {
	if strings.TrimSpace(ansi.Strip(receipt)) == "" {
		return ""
	}
	contentWidth = max(contentWidth, 1)
	if contentWidth <= visibleWidth(statusFooterIndent) {
		rule := themeFg(activeCLITheme.border, strings.Repeat("─", contentWidth))
		return rule + "\n" + wrapTranscript(receipt, contentWidth)
	}
	indent := statusFooterIndent
	innerWidth := contentWidth - visibleWidth(indent)
	rule := indent + themeFg(activeCLITheme.border, strings.Repeat("─", innerWidth))
	body := wrapTranscript(receipt, contentWidth)
	return rule + "\n" + body
}

func (m *chatTUI) reflowTranscript(terminalWidth int) {
	m.ensureTranscriptSources()
	for i, source := range m.transcriptSources {
		if source.kind == transcriptSourceFixed {
			continue
		}
		m.transcript[i] = m.renderTranscriptSource(source, terminalWidth)
	}
}

func (m *chatTUI) commitTranscriptSource(source transcriptSource) {
	rendered := m.renderTranscriptSource(source, m.width)
	*m.pendingCommit = append(*m.pendingCommit, rendered)
	m.appendTranscriptBlock(rendered, source)
}

// transcriptResizeAnchor identifies the transcript block at the top of the
// viewport plus the relative row within it. Reflow can change a block's line
// count, so preserving a raw Y offset would jump to unrelated content.
type transcriptResizeAnchor struct {
	block    int
	fraction float64
	valid    bool
}

func captureTranscriptResizeAnchor(blocks []string, width, yOffset int) transcriptResizeAnchor {
	if width <= 0 || len(blocks) == 0 {
		return transcriptResizeAnchor{}
	}
	remaining := max(yOffset, 0)
	for i, block := range blocks {
		lines := transcriptBlockLineCount(block, width)
		if remaining < lines {
			fraction := 0.0
			if lines > 1 {
				fraction = float64(remaining) / float64(lines-1)
			}
			return transcriptResizeAnchor{block: i, fraction: fraction, valid: true}
		}
		remaining -= lines
	}
	return transcriptResizeAnchor{block: len(blocks) - 1, fraction: 1, valid: true}
}

func (a transcriptResizeAnchor) yOffset(blocks []string, width int) int {
	if !a.valid || len(blocks) == 0 || width <= 0 {
		return 0
	}
	block := min(max(a.block, 0), len(blocks)-1)
	offset := 0
	for i := range block {
		offset += transcriptBlockLineCount(blocks[i], width)
	}
	lines := transcriptBlockLineCount(blocks[block], width)
	if lines > 1 {
		offset += int(math.Round(a.fraction * float64(lines-1)))
	}
	return offset
}

func transcriptBlockLineCount(block string, width int) int {
	return strings.Count(wrapTranscript(block, width), "\n") + 1
}

// wrapTranscript wraps the joined transcript to width for the viewport, keeping
// SGR balanced across wrap points. ansi.Hardwrap leaves a style that spans a
// break open at the line end (e.g. a wrapped dim link tail), which bleeds the
// attribute into the padding and the next row on stricter terminals (Warp).
// lipgloss closes the active style at each line end and reopens it at the next.
func wrapTranscript(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

type clipboardCopyMsg struct {
	text       string
	err        error
	osc52      bool
	statusHint bool
	seq        int
}

var writeNativeClipboardText = writeClipboardText

func remoteClipboardSession() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" || os.Getenv("SSH_TTY") != ""
}

// copyToClipboard prefers the operating system clipboard in a local session,
// where success can be verified (pbcopy on macOS, the selected Wayland/X11
// utility on Linux, and the Win32 clipboard on Windows). SSH cannot reliably
// reach the user's local desktop clipboard, so it deliberately falls back to
// OSC 52. A failed local write also falls back, but the UI labels that path as
// an unverified terminal request rather than claiming a successful copy.
func copyToClipboard(text string) tea.Cmd {
	return copyToClipboardWithStatus(text, 0, false)
}

func copyToClipboardWithStatus(text string, seq int, statusHint bool) tea.Cmd {
	return func() tea.Msg {
		if remoteClipboardSession() {
			return clipboardCopyMsg{text: text, osc52: true, statusHint: statusHint, seq: seq}
		}
		return clipboardCopyMsg{
			text:       text,
			err:        writeNativeClipboardText(text),
			statusHint: statusHint,
			seq:        seq,
		}
	}
}

// copyNoticeTTL is how long the "copied to clipboard" status-line hint stays
// visible after a selection copy (mouse drag, right-click, or Ctrl+C) before
// copyNoticeExpireMsg clears it.
const copyNoticeTTL = 1500 * time.Millisecond

// copyNoticeExpireMsg clears the transient copy notice — but only if seq still
// matches m.copyNoticeSeq, so an older copy's timer can't stomp a newer notice
// (e.g. drag-copy immediately followed by a right-click re-copy).
type copyNoticeExpireMsg struct{ seq int }

// copySelectionWithNotice copies text to the clipboard and arms the status-line
// "copied to clipboard" hint, bumping copyNoticeSeq so any in-flight expiry tick
// from a prior copy is superseded rather than racing this one.
func (m *chatTUI) copySelectionWithNotice(text string) tea.Cmd {
	m.copyNoticeSeq++
	seq := m.copyNoticeSeq
	return copyToClipboardWithStatus(text, seq, true)
}

func copyNoticeExpire(seq int) tea.Cmd {
	return tea.Tick(copyNoticeTTL, func(time.Time) tea.Msg {
		return copyNoticeExpireMsg{seq: seq}
	})
}

// autoScrollMsg drives one step of edge-drag scrolling while a selection is held
// against the top or bottom of the transcript.
type autoScrollMsg struct{}

func autoScrollTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return autoScrollMsg{} })
}

// edgeScrollDir reports the auto-scroll direction for a drag at screen row y in
// a viewport of `height` rows: -1 at the top edge, +1 at the bottom, 0 between.
func edgeScrollDir(y, height int) int {
	switch {
	case y <= 0:
		return -1
	case y >= height-1:
		return 1
	default:
		return 0
	}
}

// selPos is a caret position in the wrapped transcript: a content-line index
// (absolute, scroll-independent) and a visual column.
type selPos struct{ line, col int }

// selection is the live left-drag text selection over the transcript. anchor is
// where the drag began, head where it currently is; active gates rendering and
// copy. Coordinates are absolute content lines so scrolling never moves them.
type selection struct {
	active       bool
	anchor, head selPos
}

func (s selection) ordered() (start, end selPos) {
	if s.anchor.line > s.head.line || (s.anchor.line == s.head.line && s.anchor.col > s.head.col) {
		return s.head, s.anchor
	}
	return s.anchor, s.head
}

func (s selection) empty() bool { return s.anchor == s.head }

var (
	selStyle         = lipgloss.NewStyle().Reverse(true)
	scrollThumbStyle lipgloss.Style
	scrollTrackStyle lipgloss.Style
)

// renderTranscript draws the viewport's visible window with a scrollbar in the
// last column and the active selection reverse-highlighted. The content lines
// (m.wrappedLines) are already padded to cw by wrapTranscript, so this stays
// cheap per frame — important because a drag re-renders on every mouse move.
func (m chatTUI) renderTranscript() string {
	h := m.viewport.Height()
	if h <= 0 {
		return ""
	}
	cw := m.viewport.Width() // content width; the scrollbar occupies one more column
	lines := m.wrappedLines
	total := len(lines)
	yoff := m.viewport.YOffset()
	start, end := m.sel.ordered()
	thumbStart, thumbSize := scrollbarThumb(h, yoff, total)
	blank := strings.Repeat(" ", cw)

	rows := make([]string, h)
	bar := make([]string, h)
	for r := range h {
		idx := yoff + r
		line := blank // off-content rows fill to width
		if idx >= 0 && idx < total {
			line = lines[idx] // already cw-wide from wrapTranscript
		}
		if m.sel.active && !m.sel.empty() {
			if lo, hi, ok := selSpan(idx, start, end, cw); ok {
				line = lipgloss.StyleRanges(line, lipgloss.NewRange(lo, hi, selStyle))
			}
		}
		rows[r] = line
		bar[r] = scrollbarCell(r, total, h, thumbStart, thumbSize)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(rows, "\n"), strings.Join(bar, "\n"))
}

// selSpan returns the [lo, hi) visual-column span of the selection on content
// line idx (false when the line is outside the selection). cw bounds the span
// so a multi-line selection highlights through the right edge.
func selSpan(idx int, start, end selPos, cw int) (lo, hi int, ok bool) {
	if idx < start.line || idx > end.line {
		return 0, 0, false
	}
	lo, hi = 0, cw
	if idx == start.line {
		lo = start.col
	}
	if idx == end.line {
		hi = end.col
	}
	if hi > cw {
		hi = cw
	}
	if lo >= hi {
		return 0, 0, false
	}
	return lo, hi, true
}

// scrollbarThumb returns the thumb's [start, start+size) row span for a viewport
// of `height` rows showing `total` content lines scrolled to `yoff`.
func scrollbarThumb(height, yoff, total int) (start, size int) {
	if total <= height {
		return 0, 0 // no overflow → no thumb
	}
	size = max(height*height/total, 1)
	maxYoff := total - height
	start = min(yoff*(height-size)/maxYoff, height-size)
	return start, size
}

func scrollbarYOffset(height, row, total, grabOffset int) int {
	if total <= height {
		return 0
	}
	_, thumbSize := scrollbarThumb(height, 0, total)
	maxTop := height - thumbSize
	if maxTop <= 0 {
		return 0
	}
	top := min(max(row-grabOffset, 0), maxTop)
	maxYoff := total - height
	return (top*maxYoff + maxTop/2) / maxTop
}

func scrollbarCell(row, total, height, thumbStart, thumbSize int) string {
	if total <= height {
		return " "
	}
	if row >= thumbStart && row < thumbStart+thumbSize {
		return scrollThumbStyle.Render("█")
	}
	return scrollTrackStyle.Render("│")
}

func (m chatTUI) inScrollbar(x, y int) bool {
	if m.nativeScrollback {
		return false
	}
	h := m.viewport.Height()
	return h > 0 && y >= 0 && y < h && x == m.viewport.Width() && len(m.wrappedLines) > h
}

func (m chatTUI) scrollbarGrabRowOffset(row int) int {
	thumbStart, thumbSize := scrollbarThumb(m.viewport.Height(), m.viewport.YOffset(), len(m.wrappedLines))
	if row >= thumbStart && row < thumbStart+thumbSize {
		return row - thumbStart
	}
	return thumbSize / 2
}

func (m *chatTUI) dragScrollbar(row int) {
	m.viewport.SetYOffset(scrollbarYOffset(m.viewport.Height(), row, len(m.wrappedLines), m.scrollbarGrabOffset))
	// Sync immediately so a streaming event between drag motions cannot see a
	// stale followTail and yank the reader back to the bottom (#6430/#6978).
	m.syncScrollModeAfterGesture()
}

// transcriptCaret maps a screen cell (x, y) in the transcript region to an
// absolute content position, clamping to the visible window.
func (m chatTUI) transcriptCaret(x, y int) selPos {
	h := m.viewport.Height()
	if y < 0 {
		y = 0
	}
	if y > h-1 {
		y = h - 1
	}
	if x < 0 {
		x = 0
	}
	if cw := m.viewport.Width(); x > cw {
		x = cw
	}
	return selPos{line: m.viewport.YOffset() + y, col: x}
}
