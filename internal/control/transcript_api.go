package control

import (
	"errors"

	"reasonix/internal/transcript"
	"reasonix/internal/turnevent"
)

var ErrTranscriptProjectionUnavailable = errors.New("transcript projection is unavailable")

type TranscriptReplayRequest struct {
	Identity transcript.Identity `json:"identity"`
	After    uint64              `json:"after"`
}

type TranscriptReplay struct {
	transcript.Boundary
	turnevent.ReplayView
}

type TranscriptProjectionAPI interface {
	TranscriptSnapshot(transcript.PageRequest) (transcript.Snapshot, error)
	TranscriptContent(transcript.ContentRequest) (transcript.ContentChunk, error)
	TranscriptReplay(TranscriptReplayRequest) (TranscriptReplay, error)
}

var _ TranscriptProjectionAPI = (*Controller)(nil)

// SetTurnSubmissionID is called under the transport's admission boundary.
func (c *Controller) SetTurnSubmissionID(submissionID string) {
	if ledger := c.turnEventLedger(); ledger != nil {
		ledger.SetSubmissionID(submissionID)
	}
}

// BindTranscriptRuntimeEpoch runs at the surface's idle runtime publication
// boundary. It takes only display/ledger leaf locks and invokes no callbacks.
func (c *Controller) BindTranscriptRuntimeEpoch(epoch string) {
	c.turnEvents.commitMu.Lock()
	defer c.turnEvents.commitMu.Unlock()
	ledger := c.turnEventLedger()
	if ledger == nil || ledger.ActiveTurnID() != "" {
		return
	}
	ledger.SetRuntimeEpoch(epoch)
	c.turnEvents.mu.RLock()
	p := c.turnEvents.projection
	c.turnEvents.mu.RUnlock()
	if p != nil {
		p.SetRuntimeEpoch(epoch)
	}
}

func (c *Controller) transcriptProjection() (*transcript.Projection, error) {
	c.turnEvents.mu.RLock()
	defer c.turnEvents.mu.RUnlock()
	if c.turnEvents.err != nil {
		return nil, errors.Join(ErrTranscriptProjectionUnavailable, c.turnEvents.err)
	}
	if c.turnEvents.projectionErr != nil {
		return nil, errors.Join(ErrTranscriptProjectionUnavailable, c.turnEvents.projectionErr)
	}
	if c.turnEvents.projection == nil {
		return nil, ErrTranscriptProjectionUnavailable
	}
	return c.turnEvents.projection, nil
}

func (c *Controller) TranscriptSnapshot(req transcript.PageRequest) (transcript.Snapshot, error) {
	p, err := c.transcriptProjection()
	if err != nil {
		return transcript.Snapshot{}, err
	}
	return p.Snapshot(req)
}

func (c *Controller) TranscriptContent(req transcript.ContentRequest) (transcript.ContentChunk, error) {
	p, err := c.transcriptProjection()
	if err != nil {
		return transcript.ContentChunk{}, err
	}
	return p.Content(req)
}

func (c *Controller) TranscriptReplay(req TranscriptReplayRequest) (TranscriptReplay, error) {
	c.turnEvents.commitMu.Lock()
	defer c.turnEvents.commitMu.Unlock()
	p, err := c.transcriptProjection()
	if err != nil {
		return TranscriptReplay{}, err
	}
	boundary := p.Boundary()
	if boundary.Identity != req.Identity {
		if boundary.Identity.SessionID != req.Identity.SessionID {
			return TranscriptReplay{}, errors.New("transcript replay session mismatch")
		}
		return TranscriptReplay{Boundary: boundary, ReplayView: turnevent.ReplayView{Events: []turnevent.Envelope{}, ResetRequired: true, LatestSequence: boundary.CoveredThroughSeq}}, nil
	}
	view, err := c.TurnEventReplay(req.After)
	return TranscriptReplay{Boundary: boundary, ReplayView: view}, err
}
