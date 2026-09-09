package control

import (
	"errors"
	"reasonix/internal/agent"

	"reasonix/internal/transcript"
	"reasonix/internal/turnevent"
)

func (c *Controller) captureTranscriptCheckpoint(ledger *turnevent.Ledger, digest string) {
	if digest == "" {
		digest, _ = agent.ContentDigestForMessages(c.History())
	}
	p, err := c.transcriptProjection()
	if err != nil {
		return
	}
	state, err := p.Checkpoint(digest)
	state.ProviderCount = c.HistoryLen()
	c.turnEvents.mu.Lock()
	defer c.turnEvents.mu.Unlock()
	if c.turnEvents.ledger != ledger {
		return
	}
	if err != nil {
		c.turnEvents.projectionWriteErr = err
		return
	}
	c.turnEvents.pendingCheckpoint = &state
}

// Serializes retries and terminal writes, then reads the latest pending cut.
// A delayed retry cannot overwrite a newer successful checkpoint with an old
// one. No controller or projection lock is held during disk I/O.
func (c *Controller) persistTranscriptCheckpoint(ledger *turnevent.Ledger) error {
	c.turnEvents.persistMu.Lock()
	defer c.turnEvents.persistMu.Unlock()
	c.turnEvents.mu.RLock()
	if c.turnEvents.ledger != ledger {
		c.turnEvents.mu.RUnlock()
		return errors.New("transcript runtime changed before checkpoint persistence")
	}
	state := c.turnEvents.pendingCheckpoint
	path := c.turnEvents.projectionPath
	priorErr := c.turnEvents.projectionWriteErr
	projectionErr := c.turnEvents.projectionErr
	c.turnEvents.mu.RUnlock()
	if projectionErr != nil {
		return projectionErr
	}
	if state == nil {
		return priorErr
	}
	err := transcript.SaveCheckpoint(path, *state)
	c.turnEvents.mu.Lock()
	defer c.turnEvents.mu.Unlock()
	if c.turnEvents.ledger == ledger {
		c.turnEvents.projectionWriteErr = err
		if err == nil {
			c.turnEvents.projectionPersistedThrough = state.CoveredThroughSeq
			if c.turnEvents.pendingCheckpoint == state {
				c.turnEvents.pendingCheckpoint = nil
			}
		}
	}
	return err
}
