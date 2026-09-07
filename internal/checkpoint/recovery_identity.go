package checkpoint

// RecoveryIdentity names the interrupted attempt a checkpoint protects. It is
// stamped once per turn, at the first side-effecting call, so a later restore
// can tell whether it targets the state that call started from.
type RecoveryIdentity struct {
	TurnID              string
	AttemptID           string
	ToolCallID          string
	ToolArgumentsDigest string
	ProviderDigest      string
	WorkspaceReference  string
}

// StampRecoveryIdentity binds the open checkpoint to the first writer of its
// turn. Later writers keep the first binding; providerDigest runs only when a
// stamp actually lands because it hashes the whole transcript.
func (s *Store) StampRecoveryIdentity(turnID, toolCallID, argsDigest string, providerDigest func() string) {
	if s == nil || toolCallID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cur
	if c == nil || c.ToolCallID != "" {
		return
	}
	c.TurnID, c.ToolCallID, c.ToolArgumentsDigest = turnID, toolCallID, argsDigest
	if providerDigest != nil {
		c.ProviderDigest = providerDigest()
	}
	s.persistBestEffort(c)
}

// RecoveryIdentity reports the stamp on one turn's checkpoint, if any.
func (s *Store) RecoveryIdentity(turn int) (RecoveryIdentity, bool) {
	if s == nil {
		return RecoveryIdentity{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range append(append([]*Checkpoint(nil), s.done...), s.cur) {
		if c == nil || c.Turn != turn || c.ToolCallID == "" {
			continue
		}
		return RecoveryIdentity{
			TurnID: c.TurnID, AttemptID: c.AttemptID, ToolCallID: c.ToolCallID,
			ToolArgumentsDigest: c.ToolArgumentsDigest, ProviderDigest: c.ProviderDigest,
			WorkspaceReference: c.WorkspaceReference,
		}, true
	}
	return RecoveryIdentity{}, false
}
