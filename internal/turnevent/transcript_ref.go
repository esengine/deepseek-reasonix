package turnevent

// transcriptSnapshot is the transcript identity stamped on every envelope: the
// schema-1 revision/digest pair, and for schema-2 sessions the head and leaf
// message the turn ended on, which is known before the save lands.
type transcriptSnapshot struct {
	revision     int64
	digest       string
	headID       string
	leafID       string
	rewriteEpoch uint64
}

func (l *Ledger) SetTranscriptRewriteEpoch(epoch uint64) {
	l.mu.Lock()
	l.transcript.rewriteEpoch = epoch
	l.mu.Unlock()
}

func (l *Ledger) SetTranscriptSnapshot(revision int64, digest string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transcript.revision, l.transcript.digest = revision, digest
	l.mu.Unlock()
}

// SetTranscriptHead records the schema-2 head position the next envelopes
// describe. An empty head id clears it (schema-1 session).
func (l *Ledger) SetTranscriptHead(headID, leafID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transcript.headID, l.transcript.leafID = headID, leafID
	l.mu.Unlock()
}

// terminalSummaryFor folds a terminal envelope into the summary a checkpoint
// keeps once its events are compacted away.
func terminalSummaryFor(rec Envelope, outcome string, started int64) TerminalSummary {
	summary := TerminalSummary{
		TurnID: rec.TurnID, TerminalSequence: rec.Sequence, Status: rec.Status, Outcome: outcome,
		RuntimeEpoch: rec.RuntimeEpoch, SubmissionID: rec.SubmissionID,
		StartedAt: started, FinishedAt: rec.CreatedAt,
		TranscriptRevision: rec.TranscriptRevision, TranscriptDigest: rec.TranscriptDigest,
		HeadID: rec.HeadID, LeafMessageID: rec.LeafMessageID,
	}
	if started > 0 && rec.CreatedAt >= started {
		summary.DurationMs = rec.CreatedAt - started
	}
	return summary
}
