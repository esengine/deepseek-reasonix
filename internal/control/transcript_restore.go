package control

import (
	"crypto/rand"
	"errors"
	"path/filepath"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/transcript"
	"reasonix/internal/turnevent"
)

func (c *Controller) restoreTranscriptProjection(sessionPath string, ledger *turnevent.Ledger) (*transcript.Projection, error) {
	identity := transcript.Identity{SessionID: agent.BranchID(sessionPath), RuntimeEpoch: rand.Text()}
	ledger.SetRuntimeEpoch(identity.RuntimeEpoch)
	var messages []provider.Message
	if c.executor != nil && c.executor.Session() != nil {
		messages, identity.HeadID, identity.RewriteEpoch = c.executor.Session().DisplayBaseline()
	}
	latest, _ := ledger.ProjectionCursor()
	p, restored, err := restoreFromTranscriptCheckpoint(sessionPath, ledger, identity, messages, latest)
	if err != nil || restored {
		return p, err
	}
	dir := c.sessionDir
	if dir == "" && sessionPath != "" {
		dir = filepath.Dir(sessionPath)
	}
	legacy, err := transcript.LoadLegacyDisplays(dir, sessionPath)
	if err != nil {
		return nil, err
	}
	pending := ledger.PendingProjections()
	users := make([]provider.Message, 0)
	for _, m := range messages {
		if agent.IsUserAuthoredTurnMessage(m) {
			users = append(users, m)
		}
	}
	installedTurns := make(map[string]bool)
	for _, turn := range legacy.Turns {
		if turn.TurnID != "" {
			installedTurns[turn.TurnID] = true
		}
	}
	covered, prefixEnd := latest, len(messages)
	for i, group := range pending {
		userID := ""
		for _, envelope := range group.Events {
			if envelope.Kind == "user_message" && (envelope.Source == "" || envelope.Source == "executor") {
				userID = envelope.Event.MessageID
				break
			}
		}
		if userID != "" {
			// The identified user event is the start of the not-yet-projected
			// suffix. Do not seed its already-autosaved assistant deltas twice.
			for index, m := range messages {
				if m.ID == userID {
					prefixEnd = min(prefixEnd, index)
					break
				}
			}
			if len(group.Events) > 0 {
				covered = min(covered, group.Events[0].Sequence-1)
			}
			continue
		}
		if installedTurns[group.TurnID] {
			continue
		}
		rows := transcript.PendingDisplayMessages(group, transcript.Formatter{})
		if len(rows) == 0 {
			continue
		}
		userIndex := len(users) - len(pending) + i
		if userIndex < 0 || userIndex >= len(users) {
			return nil, errors.New("legacy display recovery has no matching user boundary")
		}
		legacy.Turns = append(legacy.Turns, transcript.LegacyDisplayTurn{TurnID: group.TurnID, UserMessageID: users[userIndex].ID, Messages: rows})
	}
	rows := transcript.History(messages[:prefixEnd], transcript.HistoryOptions{LegacyTurns: legacy.Turns, CheckpointTurns: c.CheckpointTurnsByMessageIndex(), SubmitContent: func(m provider.Message) string {
		return StripReferencedContextPrefix(StripComposePrefixes(m.Content))
	}, UserContent: func(m provider.Message) string {
		if display := legacy.Users[transcript.LegacyDisplayKey(m.Content)]; display != "" {
			return display
		}
		if m.RawContent != "" {
			return m.RawContent
		}
		return StripReferencedContextPrefix(StripComposePrefixes(m.Content))
	}})
	p, err = transcript.NewProjection(identity, rows, covered)
	if err != nil {
		return nil, err
	}
	if err := replayTranscriptSuffix(p, ledger, covered, latest, identity.RuntimeEpoch); err != nil {
		return nil, err
	}
	return p, nil
}

func restoreFromTranscriptCheckpoint(sessionPath string, ledger *turnevent.Ledger, identity transcript.Identity, messages []provider.Message, latest uint64) (*transcript.Projection, bool, error) {
	checkpoint, exists, err := transcript.LoadCheckpoint(sessionPath)
	if err != nil {
		return nil, false, err
	}
	if !exists || checkpoint.CoveredThroughSeq > latest || checkpoint.ProviderCount < 0 || checkpoint.ProviderCount > len(messages) {
		return nil, false, nil
	}
	prefixDigest, err := agent.ContentDigestForMessages(messages[:checkpoint.ProviderCount])
	if err != nil {
		return nil, false, err
	}
	if prefixDigest != checkpoint.TranscriptDigest || checkpoint.Identity.HeadID != identity.HeadID || checkpoint.Identity.RewriteEpoch != identity.RewriteEpoch {
		return nil, false, nil
	}
	p, err := transcript.RestoreCheckpoint(checkpoint, identity)
	if err != nil {
		return nil, false, err
	}
	if err := replayTranscriptSuffix(p, ledger, checkpoint.CoveredThroughSeq, latest, identity.RuntimeEpoch); err != nil {
		return nil, false, err
	}
	return p, true, nil
}

func replayTranscriptSuffix(p *transcript.Projection, ledger *turnevent.Ledger, after, latest uint64, runtimeEpoch string) error {
	if after == latest {
		return nil
	}
	events, err := ledger.EventsAfter(after)
	if err != nil {
		return err
	}
	for _, envelope := range events {
		// Recovered turns are terminal. Adopt their display data into this
		// runtime, without replaying any provider/tool/prompt side effect.
		envelope.RuntimeEpoch = runtimeEpoch
		if err := p.Apply(envelope); err != nil {
			return err
		}
	}
	if p.Boundary().CoveredThroughSeq != latest {
		return errors.New("transcript recovery suffix is no longer retained")
	}
	return nil
}
