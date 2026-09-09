package transcript

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/turnevent"
)

const ProtocolVersion = 1

type Identity struct {
	SessionID    string `json:"sessionId"`
	HeadID       string `json:"headId"`
	RewriteEpoch uint64 `json:"rewriteEpoch"`
	RuntimeEpoch string `json:"runtimeEpoch"`
}

type ActiveAttempt struct {
	ID        string `json:"id"`
	MessageID string `json:"messageId"`
}

// Runtime is reduced from the same ordered events as the visible records.
// In particular it is never sampled independently after a history read.
type Runtime struct {
	TurnID            string                       `json:"turnId,omitempty"`
	SubmissionID      string                       `json:"submissionId,omitempty"`
	Status            event.TurnStatus             `json:"status,omitempty"`
	Phase             string                       `json:"phase,omitempty"`
	StartedAt         int64                        `json:"startedAt,omitempty"`
	PendingEvents     []eventwire.Event            `json:"pendingEvents"`
	CompletionSummary *eventwire.CompletionSummary `json:"completionSummary,omitempty"`
}

type Boundary struct {
	ProtocolVersion    int      `json:"protocolVersion"`
	SnapshotID         string   `json:"snapshotId"`
	Identity           Identity `json:"identity"`
	ProjectionRevision uint64   `json:"projectionRevision"`
	CoveredThroughSeq  uint64   `json:"coveredThroughSeq"`
}

// Projection has one commit boundary for rows, runtime and coverage. All
// mutations happen after durable append and before publishing the event.
// It performs no callbacks or I/O under its mutex.
type Projection struct {
	mu            sync.Mutex
	incarnation   string
	identity      Identity
	revision      uint64
	covered       uint64
	buffer        Buffer
	runtime       Runtime
	attempts      map[string]ActiveAttempt
	prompts       map[string]eventwire.Event
	snapshots     map[string]frozenSnapshot
	snapshotOrder []string
	snapshotBytes int
}

func NewProjection(identity Identity, baseline []Message, covered uint64) (*Projection, error) {
	p := &Projection{incarnation: rand.Text(), identity: identity, covered: covered, revision: 1,
		attempts: make(map[string]ActiveAttempt), prompts: make(map[string]eventwire.Event)}
	// Take ownership of nested metadata as well as the slice. Callers may
	// reuse their conversion buffers immediately after construction.
	encoded, err := json.Marshal(baseline)
	if err != nil {
		return nil, err
	}
	var owned []Message
	if err = json.Unmarshal(encoded, &owned); err != nil {
		return nil, err
	}
	p.buffer.byMessageID = make(map[string]*bufferedMessage)
	seen := make(map[string]bool)
	for _, m := range owned {
		if m.Role == "user" {
			p.buffer.userTurns++
			if m.HistoryTurn == 0 {
				m.HistoryTurn = p.buffer.userTurns
			}
		}
		if m.RecordID == "" {
			switch {
			case m.Role == "tool" && m.ToolCallID != "":
				m.RecordID = "tool:" + m.ToolCallID
			case m.MessageID != "":
				m.RecordID = "m:" + m.MessageID
			default:
				return nil, errors.New("transcript baseline has a record without identity")
			}
		}
		if seen[m.RecordID] {
			return nil, fmt.Errorf("duplicate transcript record %q", m.RecordID)
		}
		seen[m.RecordID] = true
		row := &bufferedMessage{message: m}
		if m.Role == "assistant" {
			row.content.replace(m.Content)
			row.reasoning.replace(m.Reasoning)
			row.message.Content, row.message.Reasoning = "", ""
		}
		p.buffer.messages = append(p.buffer.messages, row)
		if m.MessageID != "" && (m.Role == "assistant" || m.Role == "user") {
			p.buffer.byMessageID[m.MessageID] = row
		}
	}
	return p, nil
}

func (p *Projection) Apply(envelope turnevent.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if envelope.SessionID != p.identity.SessionID || (envelope.RuntimeEpoch != "" && envelope.RuntimeEpoch != p.identity.RuntimeEpoch) {
		return errors.New("transcript event identity mismatch")
	}
	if envelope.Sequence <= p.covered {
		return nil
	}
	if envelope.Sequence != p.covered+1 {
		return errors.New("transcript projection sequence gap")
	}
	// Detach pointer payloads before retaining them. Event publication cannot
	// mutate a previously committed snapshot through an aliased tool slice.
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	var owned turnevent.Envelope
	if err = json.Unmarshal(encoded, &owned); err != nil {
		return err
	}
	if e, ok := EventFromEnvelope(owned); ok {
		p.buffer.Apply(e)
		if m := p.buffer.byMessageID[e.MessageID]; m != nil {
			if m.message.CreatedAt == 0 {
				m.message.CreatedAt = owned.CreatedAt
			}
			if owned.SubmissionID != "" {
				m.message.SubmissionID = owned.SubmissionID
			}
		}
	}
	w := owned.Event
	previousTurn := p.runtime.TurnID
	p.runtime.TurnID, p.runtime.Status = owned.TurnID, owned.Status
	p.runtime.SubmissionID = owned.SubmissionID
	switch owned.Kind {
	case "turn_started":
		if previousTurn != owned.TurnID || p.runtime.StartedAt == 0 {
			p.runtime.StartedAt = owned.CreatedAt
		}
		p.runtime.Phase = ""
		p.runtime.CompletionSummary = nil
		p.buffer.completion = nil
	case "turn_phase":
		p.runtime.Phase = w.Phase
	case "completion_summary":
		p.runtime.CompletionSummary = w.Completion
	case "stream_attempt":
		if w.StreamAttempt != nil {
			if w.StreamAttempt.Action == "begin" {
				p.attempts[w.StreamAttempt.ID] = ActiveAttempt{ID: w.StreamAttempt.ID, MessageID: w.MessageID}
			} else {
				delete(p.attempts, w.StreamAttempt.ID)
			}
		}
	case "ask_request", "approval_request", "mcp_interaction":
		id := w.PromptID
		if id == "" {
			id = owned.ItemID
		}
		if id != "" {
			p.prompts[id] = w
		}
	case "prompt_answered":
		delete(p.prompts, owned.ItemID)
	case "turn_done":
		clear(p.prompts)
		clear(p.attempts)
		if owned.TranscriptDigest != "" {
			p.identity.HeadID = owned.HeadID
			p.identity.RewriteEpoch = owned.RewriteEpoch
		}
	}
	p.covered = owned.Sequence
	p.revision++
	return nil
}

// SetRuntimeEpoch is called only by the controller's idle routing boundary.
// Existing rows survive a runtime rebind; outstanding snapshot leases do not.
func (p *Projection) SetRuntimeEpoch(epoch string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.identity.RuntimeEpoch != epoch {
		p.identity.RuntimeEpoch = epoch
		p.incarnation = rand.Text()
		p.revision++
	}
}

func (p *Projection) boundaryLocked() Boundary {
	return Boundary{ProtocolVersion: ProtocolVersion,
		SnapshotID: fmt.Sprintf("%s:%d", p.incarnation, p.revision),
		Identity:   p.identity, ProjectionRevision: p.revision, CoveredThroughSeq: p.covered}
}

func (p *Projection) Boundary() Boundary {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.boundaryLocked()
}

func (p *Projection) runtimeLocked() (Runtime, []ActiveAttempt) {
	runtime := p.runtime
	runtime.PendingEvents = make([]eventwire.Event, 0, len(p.prompts))
	keys := make([]string, 0, len(p.prompts))
	for key := range p.prompts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		runtime.PendingEvents = append(runtime.PendingEvents, p.prompts[key])
	}
	attempts := make([]ActiveAttempt, 0, len(p.attempts))
	for _, attempt := range p.attempts {
		attempts = append(attempts, attempt)
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].ID < attempts[j].ID })
	return runtime, attempts
}
