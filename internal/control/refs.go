package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"reasonix/internal/fileref"
	"reasonix/internal/instruction"
	"reasonix/internal/proc"
	"reasonix/internal/secrets"
)

// maxFileRefBytes caps how much of an @-referenced file is injected into a
// message, so "@somehuge.log" can't blow the context window. The head is kept
// and the rest noted as truncated.
const maxFileRefBytes = 64 * 1024

const pdfExtractTimeout = 8 * time.Second
const pdfExtractWaitDelay = 1 * time.Second

var extractPDFText = extractPDFTextDefault

type pdfExtractResult struct {
	text      string
	tool      string
	truncated bool
}

// refKind distinguishes the two things an @reference can resolve to.
type refKind int

const (
	refResource    refKind = iota // an MCP resource: @<server>:<uri>
	refFile                       // a local file or directory: @<path>
	refImage                      // a local image attachment: @.reasonix/attachments/<file>
	refRemoteImage                // an http(s) image URL
	refFileID                     // a Files API file-api- id
)

// ref is a resolved @reference found in a submitted line.
type ref struct {
	kind        refKind
	server      string // refResource
	uri         string // refResource
	path        string // refFile, relative to baseDir when baseDir is set
	baseDir     string // refFile override for session-authorized external roots
	displayPath string // refFile label/path exposed in the resolved context block
	raw         string // the original token after '@', for labelling
}

// ExternalFolderRefEntry is a session-authorized entry under a dropped external
// folder. Path is the opaque @ token path to submit; display fields are safe for
// UI labels and transcripts.
type ExternalFolderRefEntry struct {
	Name        string
	Path        string
	DisplayName string
	DisplayPath string
	IsDir       bool
}

var pathLocationSuffixRe = regexp.MustCompile(`:\d+(?::\d+)?:?$`)

const externalFolderRefPrefix = "__reasonix_external_folder"

// parseRefTokens extracts the deduped, punctuation-trimmed tokens following '@'
// in a line. A token is a run of non-whitespace bytes, except that a
// backslash-escaped space or tab is part of the token with the backslash
// dropped — that is how a path containing spaces survives the
// whitespace-delimited grammar (EscapeRefPath produces that form). Any other
// backslash stays literal so Windows separators keep their meaning. Pure:
// classification (server? file?) happens in classifyRef.
func parseRefTokens(line string) []string {
	var toks []string
	seen := map[string]bool{}
	for i := 0; i < len(line); i++ {
		if line[i] != '@' {
			continue
		}
		var b strings.Builder
		j := i + 1
		for j < len(line) {
			ch := line[j]
			if ch == '\\' && j+1 < len(line) && (line[j+1] == ' ' || line[j+1] == '\t') {
				b.WriteByte(line[j+1])
				j += 2
				continue
			}
			if isRefTokenBoundary(ch) {
				break
			}
			b.WriteByte(ch)
			j++
		}
		i = j - 1
		t := strings.TrimRight(b.String(), ".,;!?)]}")
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		toks = append(toks, t)
	}
	return toks
}

// isRefTokenBoundary matches the whitespace class the old `@([^\s]+)` token
// regexp stopped at.
func isRefTokenBoundary(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

// EscapeRefPath returns path with spaces and tabs backslash-escaped so the
// result survives whitespace-delimited @-token parsing (parseRefTokens
// reverses it). Every other byte, including backslashes, passes through
// unchanged so Windows separators keep their meaning.
func EscapeRefPath(path string) string {
	if !strings.ContainsAny(path, " \t") {
		return path
	}
	var b strings.Builder
	b.Grow(len(path) + 8)
	for i := range len(path) {
		if path[i] == ' ' || path[i] == '\t' {
			b.WriteByte('\\')
		}
		b.WriteByte(path[i])
	}
	return b.String()
}

// UnescapeRefPath reverses EscapeRefPath: a backslash before a space or tab is
// dropped; any other backslash stays literal.
func UnescapeRefPath(path string) string {
	if !strings.Contains(path, `\`) {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	for i := range len(path) {
		if path[i] == '\\' && i+1 < len(path) && (path[i+1] == ' ' || path[i+1] == '\t') {
			continue
		}
		b.WriteByte(path[i])
	}
	return b.String()
}

// classifyRef decides what a token refers to. A "server:uri" token whose server
// is connected is an MCP resource; otherwise a token that names an existing path
// is a file. Anything else (an @mention, an email) is not a reference. exists is
// injected so the rule is testable without touching the filesystem.
func classifyRef(token string, known map[string]bool, exists func(string) bool) (ref, bool) {
	if i := strings.Index(token, ":"); i > 0 && i+1 < len(token) && known[token[:i]] {
		return ref{kind: refResource, server: token[:i], uri: token[i+1:], raw: token}, true
	}
	if isAttachmentRef(token) && exists(token) {
		if isImageAttachmentRef(token) {
			return ref{kind: refImage, path: token, raw: token}, true
		}
		return ref{kind: refFile, path: token, raw: token}, true
	}
	if exists(token) {
		return ref{kind: refFile, path: token, raw: token}, true
	}
	return ref{}, false
}

func isAttachmentRef(token string) bool {
	return strings.HasPrefix(filepath.ToSlash(token), ".reasonix/attachments/")
}

func isImageAttachmentRef(token string) bool {
	switch strings.ToLower(filepath.Ext(token)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".tif", ".tiff":
		return true
	}
	return false
}

// RegisterExternalFolderRef authorizes one dropped directory outside the
// workspace as a structured @reference for this controller session. The returned
// token is path-like and whitespace-free so it survives the existing @ token
// parser even when the real directory path contains spaces or Windows drive
// punctuation.
func (c *Controller) RegisterExternalFolderRef(path string) (token, displayPath string, err error) {
	if c == nil {
		return "", "", fmt.Errorf("controller is not ready")
	}
	abs, err := normalizeExternalFolderRoot(path)
	if err != nil {
		return "", "", err
	}
	token = externalFolderRefToken(abs)
	c.externalFolderRefsMu.Lock()
	if c.externalFolderRefs == nil {
		c.externalFolderRefs = map[string]string{}
	}
	c.externalFolderRefs[token] = abs
	c.externalFolderRefsMu.Unlock()
	if c.externalFolderToolRefs != nil {
		c.externalFolderToolRefs.RegisterReadRoot(token, abs)
	}
	// Also grant write access so the agent can edit files in the external folder.
	if c.writeAccess.roots != nil {
		c.writeAccess.roots.GrantVerifiedSession([]string{abs})
	}
	return token, filepath.ToSlash(abs), nil
}

func normalizeExternalFolderRoot(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", os.ErrInvalid
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = filepath.Clean(resolved)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return abs, nil
}

func externalFolderRefToken(abs string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	hash := hex.EncodeToString(sum[:])[:12]
	name := safeExternalFolderRefComponent(filepath.Base(abs))
	return externalFolderRefPrefix + "/" + hash + "/" + name
}

func safeExternalFolderRefComponent(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "folder"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "folder"
	}
	return out
}

func normalizeExternalFolderRefToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "@")
	token = filepath.ToSlash(token)
	token = strings.TrimRight(token, "/")
	return token
}

func (c *Controller) externalFolderRef(token string) (ref, bool) {
	_, rel, abs, ok := c.externalFolderRefTarget(token)
	if !ok {
		return ref{}, false
	}
	displayPath := externalFolderDisplayPath(abs, rel)
	return ref{kind: refFile, path: rel, baseDir: abs, displayPath: displayPath, raw: token}, true
}

func (c *Controller) externalFolderRefTarget(token string) (rootToken, rel, abs string, ok bool) {
	key := normalizeExternalFolderRefToken(token)
	if !strings.HasPrefix(key, externalFolderRefPrefix+"/") {
		return "", "", "", false
	}
	c.externalFolderRefsMu.RLock()
	defer c.externalFolderRefsMu.RUnlock()
	if abs, ok := c.externalFolderRefs[key]; ok {
		return key, ".", abs, true
	}
	for registered, abs := range c.externalFolderRefs {
		if !strings.HasPrefix(key, registered+"/") {
			continue
		}
		sub, ok := cleanExternalFolderSubpath(strings.TrimPrefix(key, registered+"/"))
		if !ok {
			return "", "", "", false
		}
		return registered, sub, abs, true
	}
	return "", "", "", false
}

func cleanExternalFolderSubpath(sub string) (string, bool) {
	sub = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(sub)), "/")
	if sub == "" || sub == "." {
		return ".", true
	}
	cleaned := filepath.Clean(filepath.FromSlash(sub))
	if cleaned == "." {
		return ".", true
	}
	if !filepath.IsLocal(cleaned) {
		return "", false
	}
	return filepath.ToSlash(cleaned), true
}

func externalFolderDisplayPath(abs, rel string) string {
	if rel == "" || rel == "." {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(filepath.Join(abs, filepath.FromSlash(rel)))
}

func externalFolderDisplayName(abs, rel string) string {
	name := filepath.Base(abs)
	if rel != "" && rel != "." {
		name = filepath.ToSlash(filepath.Join(name, filepath.FromSlash(rel)))
	}
	return name
}

// ListExternalFolderRefDir lists one directory level under a registered
// external folder token. handled is true only when tokenPath targets a
// registered external folder; callers can fall back to workspace listing when it
// is false.
func (c *Controller) ListExternalFolderRefDir(tokenPath string) (entries []ExternalFolderRefEntry, handled bool) {
	rootToken, rel, abs, ok := c.externalFolderRefTarget(tokenPath)
	if !ok {
		return nil, false
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, true
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil || !info.IsDir() {
		return nil, true
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, true
	}
	dirEntries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, true
	}
	dirs, files := []ExternalFolderRefEntry{}, []ExternalFolderRefEntry{}
	for _, e := range dirEntries {
		name := e.Name()
		if skipRefDirEntry(name, e.IsDir()) {
			continue
		}
		childRel := name
		if rel != "." {
			childRel = filepath.ToSlash(filepath.Join(rel, name))
		}
		item := ExternalFolderRefEntry{
			Name:        name,
			Path:        rootToken + "/" + childRel,
			DisplayName: name,
			DisplayPath: externalFolderDisplayPath(abs, childRel),
			IsDir:       e.IsDir(),
		}
		if e.IsDir() {
			dirs = append(dirs, item)
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, item)
	}
	sortExternalFolderRefEntries(dirs)
	sortExternalFolderRefEntries(files)
	return append(dirs, files...), true
}

// SearchExternalFolderRefs finds entries under all registered external folders.
// Returned Path values are opaque token paths, so selecting one stays within the
// current session's authorization boundary.
func (c *Controller) SearchExternalFolderRefs(query string, limit int) []ExternalFolderRefEntry {
	query = strings.TrimSpace(query)
	if limit <= 0 || len(query) < 2 || strings.ContainsAny(query, `/\`) {
		return nil
	}
	c.externalFolderRefsMu.RLock()
	roots := make([]struct {
		token string
		abs   string
	}, 0, len(c.externalFolderRefs))
	for token, abs := range c.externalFolderRefs {
		roots = append(roots, struct {
			token string
			abs   string
		}{token: token, abs: abs})
	}
	c.externalFolderRefsMu.RUnlock()
	sort.Slice(roots, func(i, j int) bool {
		return externalFolderDisplayPath(roots[i].abs, ".") < externalFolderDisplayPath(roots[j].abs, ".")
	})
	out := make([]ExternalFolderRefEntry, 0, limit)
	queryLower := strings.ToLower(query)
	for _, root := range roots {
		if len(out) >= limit {
			break
		}
		if info, err := os.Stat(root.abs); err != nil || !info.IsDir() {
			continue
		}
		if strings.Contains(strings.ToLower(filepath.Base(root.abs)), queryLower) {
			out = append(out, ExternalFolderRefEntry{
				Name:        filepath.Base(root.abs),
				Path:        root.token,
				DisplayName: externalFolderDisplayName(root.abs, "."),
				DisplayPath: externalFolderDisplayPath(root.abs, "."),
				IsDir:       true,
			})
			if len(out) >= limit {
				break
			}
		}
		for _, result := range fileref.Search(root.abs, query, limit-len(out)) {
			rel := filepath.ToSlash(result.Path)
			out = append(out, ExternalFolderRefEntry{
				Name:        rel,
				Path:        root.token + "/" + rel,
				DisplayName: externalFolderDisplayName(root.abs, rel),
				DisplayPath: externalFolderDisplayPath(root.abs, rel),
				IsDir:       result.IsDir,
			})
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func sortExternalFolderRefEntries(entries []ExternalFolderRefEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].DisplayName) < strings.ToLower(entries[j].DisplayName)
	})
}

func skipRefDirEntry(name string, isDir bool) bool {
	switch name {
	case ".DS_Store", "Thumbs.db":
		return true
	}
	if !isDir {
		return false
	}
	switch name {
	case ".codex", ".git", ".idea", ".npm", ".pnpm-store", ".vscode", "__pycache__", "build", "dist", "node_modules":
		return true
	}
	return false
}

// detectRefs finds the @references in a line: MCP resources for connected
// servers, and local paths that exist on disk.
func (c *Controller) detectRefs(line string) []ref {
	return c.detectRefsMode(line, false)
}

func (c *Controller) detectRefsMode(line string, scopedOnly bool) []ref {
	known := map[string]bool{}
	for _, n := range c.mcp.serverNames() {
		known[n] = true
	}

	var refs []ref
	for _, tok := range parseRefTokens(line) {
		if i := strings.Index(tok, ":"); i > 0 && i+1 < len(tok) && known[tok[:i]] {
			refs = append(refs, ref{kind: refResource, server: tok[:i], uri: tok[i+1:], raw: tok})
			continue
		}
		if r, ok := c.externalFolderRef(tok); ok {
			refs = append(refs, r)
			continue
		}
		if r, ok := classifyVisionToken(tok); ok {
			refs = append(refs, r)
			continue
		}
		if c.workspaceRoot != "" {
			if rel, ok := workspaceRefPath(tok, c.workspaceRoot); ok {
				kind := refFile
				if isAttachmentRef(rel) && isImageAttachmentRef(rel) {
					kind = refImage
				}
				refs = append(refs, ref{kind: kind, path: rel, raw: tok})
			}
			continue
		}
		if scopedOnly {
			continue
		}
		if r, ok := classifyRef(tok, known, func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		}); ok {
			refs = append(refs, r)
		}
	}
	return refs
}

// HasRefs reports whether a line contains any resolvable @references, so a
// frontend can decide to resolve off its event loop only when needed.
func (c *Controller) HasRefs(line string) bool {
	return len(c.detectRefs(line)) > 0
}

// inputImages resolves image @-references in the turn input to data URLs so the
// turn can carry them to a vision-capable model. Best-effort: an unreadable image
// is skipped — the @ref still lands as text via ResolveRefs.
func (c *Controller) inputImages(line string) []string {
	if !c.imageInputEnabled() {
		return nil
	}
	return c.resolveInputImageCandidates(line)
}

// resolveInputImageCandidates resolves authorized image references without
// consulting the active model capability. The parent controller uses this only
// to hand candidates to a child; the child decides whether to embed them.
func (c *Controller) resolveInputImageCandidates(line string) []string {
	var urls []string
	seen := map[string]bool{}
	for _, r := range append(c.detectRefs(line), bareVisionRefs(line)...) {
		baseDir := c.workspaceRoot
		if r.baseDir != "" {
			baseDir = r.baseDir
		}
		url, err := c.visionRefImageValue(r, baseDir)
		if err != nil || url == "" || seen[url] {
			continue
		}
		seen[url] = true
		urls = append(urls, url)
	}
	return urls
}

func visionFileImageDataURL(path, baseDir string) (string, error) {
	absPath, absBase, ok := resolveAbsRef(path, baseDir)
	if !ok {
		return "", os.ErrNotExist
	}
	if absBase == "" {
		return "", fmt.Errorf("workspace root is required for file image references")
	}

	root, err := os.OpenRoot(absBase)
	if err != nil {
		return "", err
	}
	defer root.Close()

	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", err
	}

	info, err := root.Lstat(rel)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("image path must not be a symlink")
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > maxImageAttachmentBytes {
		return "", fmt.Errorf("image must be between 1 byte and 64 MB")
	}
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, opened) {
		return "", fmt.Errorf("image changed while opening")
	}
	return dataURLFromImageReader(f, path)
}

func dataURLFromImageReader(r io.Reader, path string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxImageAttachmentBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || len(raw) > maxImageAttachmentBytes {
		return "", fmt.Errorf("image must be between 1 byte and 64 MB")
	}
	mime := detectedImageMime(raw)
	if mime == "" {
		return "", fmt.Errorf("%s is not a supported image", path)
	}
	raw, mime = compressForVision(raw, mime)
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// resolveBareNames batch-resolves simple filenames (no path separator) that
// don't exist in cwd. It walks the working tree once and matches every
// unresolved name against the set, stopping when all are found. This runs in
// the async ResolveRefs path, never on the TUI event loop.
func resolveBareNames(refs []ref, workspaceRoot string) []ref {
	need := map[string]*ref{}
	var names []string
	for i := range refs {
		r := &refs[i]
		if r.kind != refFile || r.path != "" || !isSafeBareRefName(r.raw) {
			continue
		}
		if workspaceRoot != "" {
			if rel, ok := workspaceRefPath(r.raw, workspaceRoot); ok {
				r.path = rel
				continue
			}
		}
		need[r.raw] = r
		names = append(names, r.raw)
	}
	if len(names) == 0 {
		return refs
	}
	found := 0
	cwd := workspaceRoot
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	_ = filepath.WalkDir(cwd, func(p string, d os.DirEntry, wErr error) error {
		if wErr != nil || found == len(names) {
			return filepath.SkipAll
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".DS_Store", "__pycache__", ".idea", ".vscode":
				return filepath.SkipDir
			}
			return nil
		}
		if r, ok := need[d.Name()]; ok {
			rel, _ := filepath.Rel(cwd, p)
			r.path = filepath.ToSlash(rel)
			delete(need, d.Name())
			found++
		}
		return nil
	})
	return refs
}

func isSafeBareRefName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	return filepath.Base(name) == name && filepath.IsLocal(name)
}

// FileRefLine reports whether a submitted line is nothing but a path to an
// existing file — a dragged or pasted file lands as its bare path, which on
// POSIX starts with '/' and would otherwise be misread as a slash command. The
// returned string is that path turned into an @reference so it attaches.
func FileRefLine(line string) (string, bool) {
	p := strings.Trim(strings.TrimSpace(line), `"'`)
	if p == "" {
		return "", false
	}
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		return "", false
	}
	return "@" + EscapeRefPath(p), true
}

// SlashCodeCommentLine reports whether a slash-prefixed line is ordinary source
// text rather than a Reasonix slash command.
func SlashCodeCommentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*")
}

// SlashPathLineRef reports whether a slash-prefixed line starts with a local file
// path, including common compiler-location suffixes like ":12" or ":12:34".
// It returns an @reference for the file so diagnostics that begin with an
// absolute path can keep their original text while also attaching file context.
func SlashPathLineRef(line, baseDir string) (string, bool) {
	token, ok := leadingSlashPathToken(line)
	if !ok {
		return "", false
	}
	for _, p := range pathTokenCandidates(token) {
		if fileRefExists(p, baseDir) {
			return "@" + p, true
		}
	}
	return "", false
}

// SlashPathLikeLine reports whether a slash-prefixed line looks like a POSIX
// absolute path rather than a slash command. It intentionally stays conservative:
// unknown "/foo" remains an unknown command, while "/foo/bar..." is sent as
// ordinary prompt text even if the path no longer exists.
func SlashPathLikeLine(line string) bool {
	token, ok := leadingSlashPathToken(line)
	if !ok {
		return false
	}
	for _, p := range pathTokenCandidates(token) {
		if strings.Contains(p[1:], "/") {
			return true
		}
	}
	return false
}

func leadingSlashPathToken(line string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return "", false
	}
	token := strings.Trim(fields[0], `"'`)
	if !strings.HasPrefix(token, "/") || strings.HasPrefix(token, "//") {
		return "", false
	}
	return token, true
}

func pathTokenCandidates(token string) []string {
	token = strings.TrimRight(strings.Trim(token, `"'`), ".,;!?)]}")
	if token == "" {
		return nil
	}
	candidates := []string{token}
	if stripped := pathLocationSuffixRe.ReplaceAllString(token, ""); stripped != token {
		candidates = append(candidates, stripped)
	}
	return candidates
}

func fileRefExists(path, baseDir string) bool {
	if baseDir != "" {
		rel, _, absBase, ok := workspaceRel(path, baseDir)
		if !ok {
			return false
		}
		root, err := os.OpenRoot(absBase)
		if err != nil {
			return false
		}
		defer root.Close()
		info, err := root.Stat(rel)
		return err == nil && !info.IsDir()
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func workspaceRefPath(path, baseDir string) (string, bool) {
	rel, _, absBase, ok := workspaceRel(path, baseDir)
	if !ok {
		return "", false
	}
	root, err := os.OpenRoot(absBase)
	if err != nil {
		return "", false
	}
	defer root.Close()
	if _, err := root.Stat(rel); err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func workspaceRel(path, baseDir string) (rel, absPath, absBase string, ok bool) {
	absPath, absBase, ok = resolveAbsRef(path, baseDir)
	if !ok || absBase == "" {
		return "", "", "", false
	}
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil || !filepath.IsLocal(rel) {
		return "", "", "", false
	}
	return rel, absPath, absBase, true
}

// ResolveRefs resolves the @references in a line into a single tagged context
// block (file/dir contents, MCP resource bodies), plus per-reference error
// strings for any that failed. An empty block means no references resolved.
// Safe to call off a frontend's event loop; honours ctx for the resource reads.
func (c *Controller) ResolveRefs(ctx context.Context, line string) (block string, errs []string) {
	return c.resolveRefs(ctx, line, false)
}

// ResolveScopedRefs is the HTTP/frontend variant: file references are honored
// only when they can be resolved under the controller workspace root.
func (c *Controller) ResolveScopedRefs(ctx context.Context, line string) (block string, errs []string) {
	return c.resolveRefs(ctx, line, true)
}

func (c *Controller) resolveRefs(ctx context.Context, line string, scopedOnly bool) (block string, errs []string) {
	refs := c.detectRefsMode(line, scopedOnly)
	refs = resolveBareNames(refs, c.workspaceRoot)
	var b strings.Builder
	includedInstructionPaths := map[string]bool{}
	includedInstructionBodies := map[string]bool{}
	if current := c.memory.current(); current != nil {
		for _, doc := range current.Docs {
			includedInstructionPaths[cleanAbsPath(doc.Path)] = true
			includedInstructionBodies[doc.Body] = true
		}
	}
	for _, r := range refs {
		switch r.kind {
		case refResource:
			text, err := c.mcp.readResource(ctx, r.server, r.uri)
			if err != nil {
				errs = append(errs, "@"+r.raw+" — "+err.Error())
				continue
			}
			appendRefBlock(&b, "resource", `ref="@`+r.raw+`"`, text)
		case refFile:
			baseDir := c.workspaceRoot
			if r.baseDir != "" {
				baseDir = r.baseDir
			}
			text, isDir, err := readFileRefWithVision(r.path, baseDir, c.imageInputEnabled())
			if err != nil {
				errs = append(errs, "@"+r.raw+" — "+err.Error())
				continue
			}
			pathInstructions, diagnostics := c.resolveReferencedInstructions(r, baseDir, includedInstructionPaths, includedInstructionBodies)
			if pathInstructions != "" {
				appendRefBlock(&b, "path-instructions", `target="`+html.EscapeString(displayPathForRef(r))+`"`, pathInstructions)
			}
			for _, diagnostic := range diagnostics {
				errs = append(errs, "@"+r.raw+" — "+diagnostic.Message)
			}
			tag := "file"
			if isDir {
				tag = "dir"
			}
			displayPath := r.path
			if r.displayPath != "" {
				displayPath = r.displayPath
			}
			appendRefBlock(&b, tag, `path="`+displayPath+`"`, text)
		case refImage, refRemoteImage, refFileID:
			appendRefBlock(&b, "image", `path="`+r.path+`"`, imageAttachmentNote(r.path, c.imageInputEnabled()))
		}
	}
	return b.String(), errs
}

func (c *Controller) resolveReferencedInstructions(r ref, baseDir string, includedPaths, includedBodies map[string]bool) (string, []instruction.Diagnostic) {
	mem := c.memory.current()
	if mem == nil || strings.TrimSpace(c.workspaceRoot) == "" || r.baseDir != "" {
		return "", nil
	}
	absPath, absBase, ok := resolveAbsRef(r.path, baseDir)
	if !ok || cleanAbsPath(absBase) != cleanAbsPath(c.workspaceRoot) {
		return "", nil
	}
	targetDir := absPath
	if info, err := os.Stat(absPath); err != nil || !info.IsDir() {
		targetDir = filepath.Dir(absPath)
	}
	resolved := instruction.Resolve(instruction.ResolveOptions{
		WorkspaceRoot: c.workspaceRoot,
		TargetDir:     targetDir,
		UserDir:       mem.UserDir,
	})
	var delta []instruction.Document
	for _, doc := range resolved.Documents {
		pathKey := cleanAbsPath(doc.Path)
		if includedPaths[pathKey] || includedBodies[doc.Body] {
			continue
		}
		includedPaths[pathKey] = true
		includedBodies[doc.Body] = true
		delta = append(delta, doc)
	}
	return instruction.Block(delta), resolved.Diagnostics
}

func displayPathForRef(r ref) string {
	if r.displayPath != "" {
		return r.displayPath
	}
	return r.path
}

func cleanAbsPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func appendRefBlock(b *strings.Builder, tag, attr, body string) {
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	fmt.Fprintf(b, "<%s %s>\n%s\n</%s>", tag, attr, body, tag)
}

// maxDirEntries caps how many directory entries are injected so @some-huge-dir
// can't blow the context window.
const maxDirEntries = 100

const maxDirDepth = 16

func directoryRefNote() string {
	return fmt.Sprintf("[directory listing only; file contents are not inlined. Mention a listed file path to read its content. Common generated/vendor folders are skipped. Listing is capped at %d entries and %d nested levels.]", maxDirEntries, maxDirDepth)
}

// readFileRef reads an @-referenced path for injection. A directory yields a
// recursive listing capped at maxDirEntries; a binary file (NUL in the first
// 8 KiB) is noted rather than dumped; a large file is truncated to
// maxFileRefBytes with a marker. isDir lets the caller pick the wrapping tag.
// When baseDir is non-empty the read is sandboxed under it via os.Root so
// user-supplied paths cannot escape the workspace; otherwise the path is
// used as-is (CLI single-workspace compatibility).
func readFileRef(path, baseDir string) (content string, isDir bool, err error) {
	return readFileRefWithVision(path, baseDir, false)
}

func readFileRefWithVision(path, baseDir string, vision bool) (content string, isDir bool, err error) {
	absPath, absBase, ok := resolveAbsRef(path, baseDir)
	if !ok {
		return "", false, os.ErrNotExist
	}
	if absBase == "" {
		return readFileRefUnscoped(absPath, vision)
	}

	root, rerr := os.OpenRoot(absBase)
	if rerr != nil {
		return "", false, rerr
	}
	defer root.Close()

	rel, rerr := filepath.Rel(absBase, absPath)
	if rerr != nil {
		return "", false, rerr
	}
	displayPath := filepath.ToSlash(rel)

	info, err := root.Stat(rel)
	if err != nil {
		return "", false, err
	}
	if info.IsDir() {
		var b strings.Builder
		b.WriteString(directoryRefNote())
		b.WriteString("\n\n")
		n := 0
		err := walkRootDir(root, rel, rel, &b, &n, 0)
		if n >= maxDirEntries {
			b.WriteString("\n…[truncated; directory has more entries]…")
		}
		if err != nil {
			return "", true, err
		}
		return b.String(), true, nil
	}

	if strings.EqualFold(filepath.Ext(rel), ".pdf") {
		return readPDFRef(absPath, info.Size()), false, nil
	}

	f, err := root.Open(rel)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	buf := make([]byte, maxFileRefBytes+1)
	n, rerr := io.ReadFull(f, buf)
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
		return "", false, rerr
	}
	data := buf[:n]

	if mime := imageMime(data, rel); mime != "" {
		return imageFileRefNote(displayPath, mime, info.Size(), true, vision), false, nil
	}
	if bytes.IndexByte(data[:min(n, 8192)], 0) >= 0 {
		return fmt.Sprintf("[binary file %s, %d bytes — not shown]", displayPath, info.Size()), false, nil
	}
	if n > maxFileRefBytes {
		return string(data[:maxFileRefBytes]) + fmt.Sprintf("\n…[truncated; file is %d bytes]…", info.Size()), false, nil
	}
	return string(data), false, nil
}

// readFileRefUnscoped is the legacy readFileRef body kept for CLI single-workspace
// compatibility, where no controller-scoped sandbox is in effect.
func readFileRefUnscoped(path string, vision bool) (content string, isDir bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	if info.IsDir() {
		var b strings.Builder
		b.WriteString(directoryRefNote())
		b.WriteString("\n\n")
		n := 0
		err := filepath.WalkDir(path, func(p string, d os.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if n >= maxDirEntries {
				return filepath.SkipAll
			}
			if p == path {
				return nil
			}
			if skipRefDirEntry(d.Name(), d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, rErr := filepath.Rel(path, p)
			if rErr != nil {
				rel = p
			}
			rel = strings.ReplaceAll(rel, string(os.PathSeparator), "/")
			if d.IsDir() {
				rel += "/"
			}
			b.WriteString(rel)
			b.WriteByte('\n')
			n++
			return nil
		})
		if n >= maxDirEntries {
			b.WriteString("\n…[truncated; directory has more entries]…")
		}
		if err != nil {
			return "", true, err
		}
		return b.String(), true, nil
	}

	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		return readPDFRef(path, info.Size()), false, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	buf := make([]byte, maxFileRefBytes+1)
	n, rerr := io.ReadFull(f, buf)
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
		return "", false, rerr
	}
	data := buf[:n]

	if mime := imageMime(data, path); mime != "" {
		return imageFileRefNote(path, mime, info.Size(), false, vision), false, nil
	}
	if bytes.IndexByte(data[:min(n, 8192)], 0) >= 0 {
		return fmt.Sprintf("[binary file %s, %d bytes — not shown]", path, info.Size()), false, nil
	}
	if n > maxFileRefBytes {
		return string(data[:maxFileRefBytes]) + fmt.Sprintf("\n…[truncated; file is %d bytes]…", info.Size()), false, nil
	}
	return string(data), false, nil
}

// walkRootDir walks a directory under a sandboxed *os.Root and writes each
// entry relative to base (skipping noisy ones like .git and node_modules) into b
// until n hits maxDirEntries.
func walkRootDir(root *os.Root, dir, base string, b *strings.Builder, n *int, depth int) error {
	if depth > maxDirDepth || *n >= maxDirEntries {
		return nil
	}
	f, err := root.Open(dir)
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	for _, e := range entries {
		if *n >= maxDirEntries {
			return nil
		}
		name := e.Name()
		child := filepath.ToSlash(filepath.Join(dir, name))
		entry := name
		if rel, err := filepath.Rel(base, child); err == nil && filepath.IsLocal(rel) {
			entry = filepath.ToSlash(rel)
		}
		if skipRefDirEntry(name, e.IsDir()) {
			continue
		}
		if e.IsDir() {
			entry += "/"
		}
		b.WriteString(entry)
		b.WriteByte('\n')
		*n++
		if e.IsDir() {
			if err := walkRootDir(root, child, base, b, n, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveAbsRef resolves the user-supplied @-reference path against baseDir
// and returns the absolute path plus the absolute base root to sandbox I/O
// under. With a baseDir, the path is confined under it (a relative path that
// escapes via ".." is rejected). With an empty baseDir, the path is returned
// as-is and the caller falls back to plain os.Stat/os.Open so CLI usage
// (where there is no controller-scoped workspace) keeps working.
func resolveAbsRef(path, baseDir string) (absPath, absBase string, ok bool) {
	if baseDir == "" {
		return path, "", true
	}
	absBase = baseDir
	if !filepath.IsAbs(absBase) {
		var err error
		absBase, err = filepath.Abs(absBase)
		if err != nil {
			return "", "", false
		}
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(absBase, cleaned)
	}
	rel, err := filepath.Rel(absBase, cleaned)
	if err != nil || !filepath.IsLocal(rel) {
		return "", "", false
	}
	return cleaned, absBase, true
}

func readPDFRef(path string, size int64) string {
	result, err := extractPDFText(path)
	if err != nil {
		return fmt.Sprintf("[PDF file %s, %d bytes — text extraction unavailable: %v. If this is a scanned/image-only PDF, use OCR or an available multimodal/vision tool with this path.]", path, size, err)
	}
	text := strings.TrimSpace(result.text)
	if text == "" {
		return fmt.Sprintf("[PDF file %s, %d bytes — no extractable text found. It may be scanned/image-only; use OCR or an available multimodal/vision tool with this path.]", path, size)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[PDF text extracted from %s using %s", path, result.tool)
	if result.truncated {
		fmt.Fprintf(&b, "; truncated to the first %d bytes", maxFileRefBytes)
	}
	b.WriteString("]\n")
	b.WriteString(text)
	return b.String()
}

func extractPDFTextDefault(path string) (pdfExtractResult, error) {
	var firstErr error
	if pdftotext, err := exec.LookPath("pdftotext"); err == nil {
		if text, truncated, err := runPDFTextCommand(pdftotext, []string{"-enc", "UTF-8", "-layout", path, "-"}); err == nil {
			return pdfExtractResult{text: text, tool: "pdftotext", truncated: truncated}, nil
		} else {
			firstErr = err
		}
	}
	python, err := findPython()
	if err != nil {
		if firstErr != nil {
			return pdfExtractResult{}, fmt.Errorf("pdftotext failed (%w), and Python PDF libraries are not available", firstErr)
		}
		return pdfExtractResult{}, fmt.Errorf("pdftotext and Python PDF libraries are not available")
	}
	text, truncated, err := runPDFTextCommand(python, []string{"-c", pythonPDFExtractScript, path})
	if err != nil {
		if firstErr != nil {
			return pdfExtractResult{}, fmt.Errorf("pdftotext failed (%w), Python PDF extraction failed (%w)", firstErr, err)
		}
		return pdfExtractResult{}, err
	}
	return pdfExtractResult{text: text, tool: "Python PDF library", truncated: truncated}, nil
}

func findPython() (string, error) {
	for _, name := range []string{"python3", "python", "py"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("python not found")
}

func runPDFTextCommand(name string, args []string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pdfExtractTimeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, name, args...)
	cmd.Env = secrets.ProcessEnv()
	setShellKillTree(cmd)
	cmd.WaitDelay = pdfExtractWaitDelay
	proc.HideWindow(cmd)
	var stdout limitedBuffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	waitErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", false, fmt.Errorf("PDF text extraction timed out")
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			if stderr.Truncated() {
				msg += "\n…[truncated]…"
			}
			return "", false, fmt.Errorf("%w: %s", waitErr, msg)
		}
		return "", false, waitErr
	}
	return stdout.String(), stdout.Truncated(), nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := maxFileRefBytes - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func (b *limitedBuffer) Truncated() bool { return b.truncated }

const pythonPDFExtractScript = `
import sys

path = sys.argv[1]

try:
    from pypdf import PdfReader
except Exception:
    try:
        from PyPDF2 import PdfReader
    except Exception:
        PdfReader = None

if PdfReader is not None:
    reader = PdfReader(path)
    for page in reader.pages:
        text = page.extract_text() or ""
        if text:
            print(text)
    sys.exit(0)

try:
    import pdfplumber
except Exception as exc:
    raise SystemExit("no supported Python PDF library found") from exc

with pdfplumber.open(path) as pdf:
    for page in pdf.pages:
        text = page.extract_text() or ""
        if text:
            print(text)
`

func imageMime(data []byte, path string) string {
	mime := http.DetectContentType(data[:min(len(data), 512)])
	if strings.HasPrefix(mime, "image/") {
		return mime
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".tiff", ".tif":
		return "image/tiff"
	}
	return ""
}
