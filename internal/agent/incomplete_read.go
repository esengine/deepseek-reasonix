package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"reasonix/internal/tool"
)

type incompleteReadPhase uint8

const (
	incompleteReadAutoResultPage incompleteReadPhase = iota
	incompleteReadAutoSourcePage
	incompleteReadStrategy
	incompleteReadStrategyResultPage
	incompleteReadStrategySourcePage
)

type incompleteReadAction uint8

const (
	incompleteReadActionNone incompleteReadAction = iota
	incompleteReadActionAutoResult
	incompleteReadActionAutoSource
	incompleteReadActionStrategySearch
	incompleteReadActionStrategyRead
	incompleteReadActionStrategyResult
	incompleteReadActionStrategySource
	incompleteReadActionStrategyReceipt
)

type incompleteReadFileVersion struct {
	size    int64
	modTime int64
	valid   bool
}

type incompleteReadSearch struct {
	callID     string
	pattern    string
	matchLines []int
}

type incompleteReadWindow struct {
	callID    string
	startLine int
	endLine   int
	observed  []tool.ModelTextObservation
}

type incompleteReadReceipt struct {
	searchIDs  []string
	readIDs    []string
	conclusion string
}

// incompleteRead tracks one logical read_file request. Automatic phases prove
// complete coverage. Strategy phases deliberately prove only the exact windows
// named by a validated receipt and never promote that into whole-file evidence.
type incompleteRead struct {
	key              string
	readID           string
	requestPath      string
	path             string
	phase            incompleteReadPhase
	fullRead         bool
	cumulativeBytes  int
	cumulativeTokens int
	toolCallID       string
	resultRef        string
	nextByteOffset   int
	totalBytes       int
	sha256           string
	nextSourceOffset int
	nextSourceLimit  int
	sourceWindowEnd  int
	pendingObserved  []tool.ModelTextObservation
	strategyVersion  incompleteReadFileVersion
	searches         map[string]incompleteReadSearch
	reads            map[string]incompleteReadWindow
	targetReadID     string
	targetObserved   []tool.ModelTextObservation
	targetEnd        int
	pendingReceipt   *incompleteReadReceipt
	readTool         tool.Tool
	strategyRevision uint64
}

// incompleteReadState is shared by all calls in one Agent.Run. Parallel calls
// are committed to it in provider order by executeBatch.finalize.
type incompleteReadState struct {
	mu sync.Mutex
	// legacyImplicitFullReads restores the pre-intent rule for diagnosis; it is
	// fixed for the run and never enters provider bytes.
	legacyImplicitFullReads bool
	entries                 map[string]*incompleteRead
	order                   []string
	budgetInitialized       bool
	budgetMaxTokens         int
	budgetConsumedTokens    int
	roundProgress           bool
	roundViolation          bool
	consecutiveViolations   int
	failure                 *IncompleteReadError
}

type incompleteReadTransition struct {
	record           []tool.ModelTextObservation
	detected         bool
	completed        bool
	strategyRequired bool
	strategyProgress bool
	localSafetyPaged bool
	readID           string
	path             string
	totalBytes       int
	totalTokens      int
	limitTokens      int
}

type incompleteReadRoundResult struct {
	record      []tool.ModelTextObservation
	resolvedIDs []string
	pause       *IncompleteReadError
}

type incompleteReadDeferred struct {
	plan         *toolCallPlan
	rawOutput    string
	readObserver bool
	visibleFull  bool
}

func modelTextObservationFor(plan *toolCallPlan, output string) (tool.ModelTextObservation, bool) {
	if plan == nil || output == "" {
		return tool.ModelTextObservation{}, false
	}
	observer, ok := plan.runTool.(tool.ModelTextObserver)
	if !ok {
		observer, ok = plan.execTool.(tool.ModelTextObserver)
	}
	if !ok {
		return tool.ModelTextObservation{}, false
	}
	return observer.ObserveModelText(json.RawMessage(plan.runArgs), output)
}

func snapshotIncompleteReadFile(path string) incompleteReadFileVersion {
	info, err := os.Stat(path)
	if err != nil {
		return incompleteReadFileVersion{}
	}
	return incompleteReadFileVersion{size: info.Size(), modTime: info.ModTime().UnixNano(), valid: true}
}

func sameIncompleteReadFileVersion(a, b incompleteReadFileVersion) bool {
	if !a.valid || !b.valid {
		return a.valid == b.valid
	}
	return a.size == b.size && a.modTime == b.modTime
}

func readPathMatches(candidate string, entry *incompleteRead) bool {
	if entry == nil || strings.TrimSpace(candidate) == "" {
		return false
	}
	clean := filepath.Clean(candidate)
	return clean == filepath.Clean(entry.path) || clean == filepath.Clean(entry.requestPath)
}

func (s *incompleteReadState) ensureEntriesLocked() {
	if s.entries == nil {
		s.entries = make(map[string]*incompleteRead)
	}
}

func (s *incompleteReadState) addEntryLocked(entry *incompleteRead) {
	s.ensureEntriesLocked()
	if _, exists := s.entries[entry.key]; !exists {
		s.order = append(s.order, entry.key)
	}
	s.entries[entry.key] = entry
}

func (s *incompleteReadState) removeEntryLocked(key string) {
	delete(s.entries, key)
	for i, candidate := range s.order {
		if candidate == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *incompleteReadState) firstLocked() *incompleteRead {
	for len(s.order) > 0 {
		key := s.order[0]
		if entry := s.entries[key]; entry != nil {
			return entry
		}
		s.order = s.order[1:]
	}
	return nil
}

func (s *incompleteReadState) hasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure != nil || s.firstLocked() != nil
}

func (s *incompleteReadState) hasStrategy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry != nil && (entry.phase == incompleteReadStrategy || entry.phase == incompleteReadStrategyResultPage || entry.phase == incompleteReadStrategySourcePage) {
			return true
		}
	}
	return false
}

func (s *incompleteReadState) currentFailure() *IncompleteReadError {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		return nil
	}
	copy := *s.failure
	return &copy
}

func (s *incompleteReadState) setFailureLocked(entry *incompleteRead, reason string) {
	if s.failure != nil {
		return
	}
	err := &IncompleteReadError{Reason: reason}
	if entry != nil {
		err.Path = entry.path
		err.ToolCallID = entry.toolCallID
		err.ResultRef = entry.resultRef
		err.NextOffset = entry.nextByteOffset
		err.ConsumedBytes = entry.cumulativeBytes
		err.TotalBytes = entry.totalBytes
	}
	s.failure = err
}

func (s *incompleteReadState) reserveAutomaticLocked(tokens int, budget readAutoRecoveryBudget) bool {
	if !budget.known || budget.maxTokens <= 0 || budget.headroomTokens < tokens {
		return false
	}
	if !s.budgetInitialized {
		s.budgetInitialized = true
		s.budgetMaxTokens = budget.maxTokens
	}
	if s.budgetConsumedTokens+tokens > s.budgetMaxTokens {
		return false
	}
	s.budgetConsumedTokens += tokens
	return true
}

func incompleteReadID(callID, resultRef, path string) string {
	sum := sha256.Sum256([]byte(callID + "\x00" + resultRef + "\x00" + path))
	return "ir-" + hex.EncodeToString(sum[:8])
}

func (s *incompleteReadState) enterStrategyLocked(entry *incompleteRead, budget readAutoRecoveryBudget) incompleteReadTransition {
	entry.phase = incompleteReadStrategy
	entry.toolCallID = ""
	entry.resultRef = ""
	entry.nextByteOffset = 0
	entry.totalBytes = 0
	entry.sha256 = ""
	entry.nextSourceOffset = 0
	entry.nextSourceLimit = 0
	entry.strategyVersion = snapshotIncompleteReadFile(entry.path)
	if entry.searches == nil {
		entry.searches = make(map[string]incompleteReadSearch)
	}
	if entry.reads == nil {
		entry.reads = make(map[string]incompleteReadWindow)
	}
	s.addEntryLocked(entry)
	s.roundProgress = true
	return incompleteReadTransition{
		detected: true, strategyRequired: true, readID: entry.readID, path: entry.path,
		totalBytes: entry.cumulativeBytes, totalTokens: entry.cumulativeTokens,
		limitTokens: budget.maxTokens,
	}
}

func (s *incompleteReadState) observeReadFile(
	plan *toolCallPlan,
	rawOutput, visibleOutput string,
	observed tool.ModelTextObservation,
	hasObservation bool,
	resultTokens int,
	budget readAutoRecoveryBudget,
) incompleteReadTransition {
	args, ok := parseReadFileArgs(plan.runArgs)
	if !ok {
		return incompleteReadTransition{}
	}
	trailer := parseReadFileTrailer(rawOutput)
	recoveryOffset, truncated := readFileRecoveryOffset(visibleOutput)
	if !truncated && len(rawOutput) > maxToolOutputBytes {
		recoveryOffset = 0
		truncated = true
	}
	resultRef := toolResultRef(plan.call.ID, rawOutput)
	sum := sha256.Sum256([]byte(rawOutput))
	digest := hex.EncodeToString(sum[:])
	resolvedPath := args.Path
	if hasObservation && observed.Path != "" {
		resolvedPath = observed.Path
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.entries[plan.incompleteReadRoot]
	if plan.incompleteReadRoot != "" && entry == nil {
		s.setFailureLocked(nil, "the required read_file continuation lost its host state")
		return incompleteReadTransition{}
	}

	if plan.incompleteReadAction == incompleteReadActionStrategyRead || plan.incompleteReadAction == incompleteReadActionStrategySource {
		return s.observeStrategyReadLocked(entry, plan, args, rawOutput, truncated, recoveryOffset, resultRef, digest, trailer, observed, hasObservation, resultTokens, budget)
	}

	fullRead := entry != nil && entry.fullRead || args.fullRead() || s.legacyImplicitFullRead(args)
	if entry == nil {
		entry = &incompleteRead{
			requestPath: args.Path,
			path:        resolvedPath,
			fullRead:    fullRead,
			searches:    make(map[string]incompleteReadSearch),
			reads:       make(map[string]incompleteReadWindow),
		}
		entry.readID = incompleteReadID(plan.call.ID, resultRef, entry.path)
		entry.key = entry.readID
	}
	if entry.readTool == nil {
		entry.readTool = plan.runTool
		if entry.readTool == nil {
			entry.readTool = plan.execTool
		}
	}

	needsContinuation := truncated || trailer.localSafety || (fullRead && trailer.hasMore)
	if needsContinuation && !s.reserveAutomaticLocked(resultTokens, budget) {
		entry.cumulativeBytes += len(rawOutput)
		entry.cumulativeTokens += resultTokens
		return s.enterStrategyLocked(entry, budget)
	}

	entry.cumulativeBytes += len(rawOutput)
	entry.cumulativeTokens += resultTokens
	if truncated {
		if hasObservation {
			entry.pendingObserved = append(entry.pendingObserved, observed)
		}
		entry.phase = incompleteReadAutoResultPage
		entry.toolCallID = plan.call.ID
		entry.resultRef = resultRef
		entry.nextByteOffset = recoveryOffset
		entry.totalBytes = len(rawOutput)
		entry.sha256 = digest
		s.configureAutoSourceLocked(entry, args, trailer)
		s.addEntryLocked(entry)
		s.roundProgress = true
		return incompleteReadTransition{detected: true, localSafetyPaged: trailer.localSafety, readID: entry.readID, path: entry.path}
	}

	if trailer.localSafety || (fullRead && trailer.hasMore) {
		if hasObservation {
			entry.pendingObserved = append(entry.pendingObserved, observed)
		}
		entry.phase = incompleteReadAutoSourcePage
		s.configureAutoSourceLocked(entry, args, trailer)
		s.addEntryLocked(entry)
		s.roundProgress = true
		return incompleteReadTransition{detected: true, localSafetyPaged: trailer.localSafety, readID: entry.readID, path: entry.path}
	}

	transition := incompleteReadTransition{readID: entry.readID, path: entry.path}
	if plan.incompleteReadRoot != "" {
		if hasObservation {
			entry.pendingObserved = append(entry.pendingObserved, observed)
		}
		transition.record = append(transition.record, entry.pendingObserved...)
		s.removeEntryLocked(entry.key)
		s.roundProgress = true
		transition.completed = true
	} else if hasObservation {
		transition.record = append(transition.record, observed)
	}
	return transition
}

// legacyImplicitFullRead reproduces the pre-intent default for rollback only.
func (s *incompleteReadState) legacyImplicitFullRead(args readFileArgs) bool {
	return s.legacyImplicitFullReads && !args.LimitExplicit
}

func (s *incompleteReadState) configureAutoSourceLocked(entry *incompleteRead, args readFileArgs, trailer readFileTrailer) {
	entry.nextSourceOffset = 0
	entry.nextSourceLimit = 0
	entry.sourceWindowEnd = 0
	if !trailer.hasMore {
		return
	}
	entry.nextSourceOffset = trailer.nextOffset
	if trailer.localSafety {
		entry.sourceWindowEnd = trailer.requestedEnd
		entry.nextSourceLimit = max(1, trailer.requestedEnd-trailer.nextOffset)
		return
	}
	if entry.fullRead {
		entry.nextSourceLimit = 2000
	}
}

func (s *incompleteReadState) observeStrategyReadLocked(
	entry *incompleteRead,
	plan *toolCallPlan,
	args readFileArgs,
	rawOutput string,
	truncated bool,
	recoveryOffset int,
	resultRef, digest string,
	trailer readFileTrailer,
	observed tool.ModelTextObservation,
	hasObservation bool,
	resultTokens int,
	budget readAutoRecoveryBudget,
) incompleteReadTransition {
	if entry == nil {
		return incompleteReadTransition{}
	}
	current := snapshotIncompleteReadFile(entry.path)
	if !sameIncompleteReadFileVersion(entry.strategyVersion, current) {
		s.resetStrategyEvidenceForVersionLocked(entry, current)
		entry.phase = incompleteReadStrategy
		if plan.incompleteReadAction == incompleteReadActionStrategySource {
			return incompleteReadTransition{strategyRequired: true, readID: entry.readID, path: entry.path, totalBytes: len(rawOutput), totalTokens: resultTokens, limitTokens: budget.maxTokens}
		}
	}
	// A complete provider-visible explicit window is safe even when the model's
	// context size is unknown. Any window that itself needs recovery must fit the
	// live dynamic budget or the model is asked to retry with a smaller limit.
	if (truncated || trailer.localSafety) && (!budget.known || budget.maxTokens <= 0 || resultTokens > budget.maxTokens || resultTokens > budget.headroomTokens) {
		s.roundProgress = false
		return incompleteReadTransition{strategyRequired: true, readID: entry.readID, path: entry.path, totalBytes: len(rawOutput), totalTokens: resultTokens, limitTokens: budget.maxTokens}
	}
	if plan.incompleteReadAction == incompleteReadActionStrategyRead {
		entry.targetReadID = plan.call.ID
		entry.targetObserved = nil
		entry.targetEnd = args.Offset + args.Limit
	} else {
		entry.nextSourceOffset = 0
		entry.nextSourceLimit = 0
	}
	if hasObservation {
		entry.targetObserved = append(entry.targetObserved, observed)
	}
	if truncated {
		entry.phase = incompleteReadStrategyResultPage
		entry.toolCallID = plan.call.ID
		entry.resultRef = resultRef
		entry.nextByteOffset = recoveryOffset
		entry.totalBytes = len(rawOutput)
		entry.sha256 = digest
		if trailer.localSafety {
			entry.nextSourceOffset = trailer.nextOffset
			entry.nextSourceLimit = max(1, trailer.requestedEnd-trailer.nextOffset)
		}
		s.roundProgress = true
		return incompleteReadTransition{strategyProgress: true, localSafetyPaged: trailer.localSafety, readID: entry.readID, path: entry.path}
	}
	if trailer.localSafety {
		entry.phase = incompleteReadStrategySourcePage
		entry.nextSourceOffset = trailer.nextOffset
		entry.nextSourceLimit = max(1, trailer.requestedEnd-trailer.nextOffset)
		s.roundProgress = true
		return incompleteReadTransition{strategyProgress: true, localSafetyPaged: true, readID: entry.readID, path: entry.path}
	}
	if !hasObservation {
		return incompleteReadTransition{readID: entry.readID, path: entry.path}
	}
	s.finishStrategyWindowLocked(entry)
	s.roundProgress = true
	return incompleteReadTransition{strategyProgress: true, readID: entry.readID, path: entry.path}
}

func (s *incompleteReadState) finishStrategyWindowLocked(entry *incompleteRead) {
	if entry == nil || entry.targetReadID == "" || len(entry.targetObserved) == 0 {
		return
	}
	window := incompleteReadWindow{callID: entry.targetReadID, observed: append([]tool.ModelTextObservation(nil), entry.targetObserved...)}
	window.startLine = window.observed[0].StartLine
	last := window.observed[len(window.observed)-1]
	window.endLine = last.StartLine + len(last.LineHashes) - 1
	entry.reads[window.callID] = window
	entry.strategyRevision++
	entry.targetReadID = ""
	entry.targetObserved = nil
	entry.targetEnd = 0
	entry.phase = incompleteReadStrategy
	entry.nextSourceOffset = 0
	entry.nextSourceLimit = 0
}

func (s *incompleteReadState) observeResultPage(plan *toolCallPlan, output string, retainedSnapshotMatches bool) incompleteReadTransition {
	if plan == nil || plan.incompleteReadRoot == "" {
		return incompleteReadTransition{}
	}
	header, body, ok := parseSessionToolResultPage(output)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[plan.incompleteReadRoot]
	if entry == nil || (entry.phase != incompleteReadAutoResultPage && entry.phase != incompleteReadStrategyResultPage) {
		return incompleteReadTransition{}
	}
	if !ok || !retainedSnapshotMatches || header.ResultRef != entry.resultRef || header.Offset != entry.nextByteOffset ||
		header.TotalBytes != entry.totalBytes || header.SHA256 != entry.sha256 ||
		header.NextOffset <= header.Offset || header.NextOffset > header.TotalBytes ||
		len(body) != header.NextOffset-header.Offset ||
		(header.Complete != (header.NextOffset == header.TotalBytes)) {
		s.setFailureLocked(entry, "a retained read page failed its result_ref, SHA-256, or contiguous-offset integrity check")
		return incompleteReadTransition{}
	}
	entry.nextByteOffset = header.NextOffset
	s.roundProgress = true
	transition := incompleteReadTransition{readID: entry.readID, path: entry.path}
	if entry.phase == incompleteReadStrategyResultPage {
		transition.strategyProgress = true
	}
	if !header.Complete {
		return transition
	}
	entry.toolCallID = ""
	entry.resultRef = ""
	entry.nextByteOffset = 0
	entry.totalBytes = 0
	entry.sha256 = ""
	if entry.nextSourceOffset > 0 {
		if entry.phase == incompleteReadStrategyResultPage {
			entry.phase = incompleteReadStrategySourcePage
		} else {
			entry.phase = incompleteReadAutoSourcePage
		}
		return transition
	}
	if entry.phase == incompleteReadStrategyResultPage {
		s.finishStrategyWindowLocked(entry)
		return transition
	}
	transition.record = append(transition.record, entry.pendingObserved...)
	s.removeEntryLocked(entry.key)
	transition.completed = true
	return transition
}

func (a *Agent) retainedReadPageMatches(plan *toolCallPlan, output string) bool {
	if a == nil || plan == nil {
		return false
	}
	header, body, ok := parseSessionToolResultPage(output)
	if !ok {
		return false
	}
	var params toolResultReadParams
	if json.Unmarshal(plan.evidenceArgs, &params) != nil {
		return false
	}
	candidate, err := findToolResultCandidate(a.sess.conversation.Snapshot(), params.ToolCallID, header.ResultRef)
	if err != nil || !candidate.recoverable || header.Offset < 0 || header.NextOffset < header.Offset || header.NextOffset > len(candidate.body) {
		return false
	}
	return candidate.body[header.Offset:header.NextOffset] == body
}

func (s *incompleteReadState) gate(plan *toolCallPlan) (string, bool) {
	if plan == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return "blocked: a prior read_file result could not be completed safely; stop this run and report the incomplete read", true
	}
	if len(s.entries) == 0 {
		return "", false
	}

	input := parseIncompleteReadGateInput(plan)
	for _, key := range s.order {
		entry := s.entries[key]
		if entry == nil {
			continue
		}
		message, matched, _ := matchIncompleteReadGateEntry(plan, input, key, entry)
		if matched {
			if message != "" {
				s.roundViolation = true
			}
			return message, message != ""
		}
	}
	// Truncation is advisory. Exact continuation mismatches above still warn,
	// but unpaid read pages do not freeze mutations or finals.
	return "", false
}

func (s *incompleteReadState) blockFinal() (instruction string, pause *IncompleteReadError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		copy := *s.failure
		return "", &copy
	}
	return "", nil
}

func (s *incompleteReadState) pauseForEntryLocked(entry *incompleteRead, reason string) *IncompleteReadError {
	return &IncompleteReadError{
		Reason: reason, Path: entry.path, ToolCallID: entry.toolCallID,
		ResultRef: entry.resultRef, NextOffset: entry.nextByteOffset,
		ConsumedBytes: entry.cumulativeBytes, TotalBytes: entry.totalBytes,
	}
}

func (s *incompleteReadState) finishToolRound() incompleteReadRoundResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := incompleteReadRoundResult{}
	if s.failure != nil {
		copy := *s.failure
		result.pause = &copy
		return result
	}
	if s.roundViolation {
		s.consecutiveViolations++
	} else if s.roundProgress {
		s.consecutiveViolations = 0
	}
	for _, key := range append([]string(nil), s.order...) {
		entry := s.entries[key]
		if entry == nil || entry.pendingReceipt == nil {
			continue
		}
		for _, id := range entry.pendingReceipt.readIDs {
			window := entry.reads[id]
			result.record = append(result.record, window.observed...)
		}
		result.resolvedIDs = append(result.resolvedIDs, entry.readID)
		s.removeEntryLocked(key)
	}
	entry := s.firstLocked()
	if entry != nil && s.consecutiveViolations >= 2 {
		result.pause = s.pauseForEntryLocked(entry, "the model violated the restricted read strategy in two consecutive rounds")
	}
	s.roundProgress = false
	s.roundViolation = false
	return result
}
