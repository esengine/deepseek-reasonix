package agent

import (
	"sort"
	"sync"

	"reasonix/internal/tool"
)

// turnLoopState groups per-turn loop-guard maps so parallel tool goroutines
// share one lock instead of unsynchronized maps on turnRuntime.
type turnLoopState struct {
	mu                      sync.Mutex
	dispatchClasses         map[string]tool.CallClass
	resultFingerprints      map[string]string
	acceptedDecisions       map[string]acceptedDecision
	previousErrorCategories map[string]struct{}
	softBudgetNudged        bool
	softBudgetNudgeRound    int
	// softBudgetExtensions counts read-only budget doublings granted this turn
	// via extend_research_budget (0..3 -> 10/20/40/80 rounds).
	softBudgetExtensions int
}

func (s *turnLoopState) setDispatchClasses(classes map[string]tool.CallClass) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatchClasses = classes
}

func (s *turnLoopState) dispatchClass(id string) (tool.CallClass, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	class, ok := s.dispatchClasses[id]
	return class, ok
}

func (s *turnLoopState) rememberFingerprint(fp, callID string) (prev string, seen bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resultFingerprints == nil {
		s.resultFingerprints = map[string]string{}
	}
	prev, seen = s.resultFingerprints[fp]
	if !seen {
		s.resultFingerprints[fp] = callID
	}
	return prev, seen
}

func (s *turnLoopState) rememberDecision(id, question, answer string) {
	s.rememberDecisionAmbiguity(id, question, answer, decisionAmbiguity{})
}

func (s *turnLoopState) rememberDecisionAmbiguity(id, question, answer string, ambiguity decisionAmbiguity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acceptedDecisions == nil {
		s.acceptedDecisions = map[string]acceptedDecision{}
	}
	s.acceptedDecisions[id] = acceptedDecision{ID: id, Question: question, Answer: answer, Ambiguity: ambiguity}
}

func (s *turnLoopState) decision(id string) (acceptedDecision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dec, ok := s.acceptedDecisions[id]
	return dec, ok
}

func (s *turnLoopState) snapshotDecisions() []acceptedDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]acceptedDecision, 0, len(s.acceptedDecisions))
	for _, decision := range s.acceptedDecisions {
		out = append(out, decision)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *turnLoopState) advanceErrorCategories(current map[string]int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	hit := false
	next := make(map[string]struct{}, len(current))
	for category, count := range current {
		if count >= 2 {
			hit = true
		}
		if _, repeated := s.previousErrorCategories[category]; repeated {
			hit = true
		}
		next[category] = struct{}{}
	}
	s.previousErrorCategories = next
	return hit
}

// softBudgetExtensionCount returns how many read-only budget doublings were
// granted this turn.
func (s *turnLoopState) softBudgetExtensionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.softBudgetExtensions
}

// extendSoftBudget grants one doubling (at most max per turn) and re-arms the
// nudge so the extended budget is measured from the current round.
func (s *turnLoopState) extendSoftBudget(max int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.softBudgetExtensions >= max {
		return s.softBudgetExtensions, false
	}
	s.softBudgetExtensions++
	s.softBudgetNudged = false
	s.softBudgetNudgeRound = 0
	return s.softBudgetExtensions, true
}

func (s *turnLoopState) markSoftBudgetNudged(round int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.softBudgetNudged {
		return false
	}
	s.softBudgetNudged = true
	s.softBudgetNudgeRound = round
	return true
}

func (s *turnLoopState) softBudgetNudgedAt() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.softBudgetNudgeRound
}
