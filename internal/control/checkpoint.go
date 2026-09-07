package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/checkpoint"
	"reasonix/internal/diff"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// checkpointManager owns the snapshot-based rewind bookkeeping: the per-session
// checkpoint store, the monotonic turn counter, and the conversation-rewind
// boundary map. Like approvalManager it holds only the bookkeeping behind its own
// lock, off the controller's c.mu — the Controller keeps the rewind/fork
// orchestration (truncating the session, restoring code, emitting events) that
// needs its other collaborators.
//
// turn is decoupled from the store so it remains monotonic across session work;
// bound[turn] records len(Session.Messages) at that turn's start — the truncation
// boundary for a conversation rewind/fork. Boundaries are persisted in each
// checkpoint and rebuilt from the store on resume (so a reopened session can still
// rewind conversation / fork). Context compression never changes the transcript,
// so it leaves these boundaries intact. Every store call does its disk I/O off mu —
// mu is taken only to read/swap the store pointer and mutate turn/bound.
type checkpointManager struct {
	// mu guards store, turn, and bound; every critical section under it is short
	// and non-blocking (no disk I/O).
	mu    sync.Mutex
	store *checkpoint.Store
	turn  int
	bound map[int]int
}

// rebind points the store at the (possibly new) session, loading any checkpoints
// already on disk, and resets the turn counter and boundaries from them. root is
// the workspace root used to guard restore writes. Called on construction and
// whenever the session path changes (NewSession/Resume/SetSessionPath/fork).
func (m *checkpointManager) rebind(dir, root string) {
	store := checkpoint.New(dir, root)
	next := store.NextTurn() // continue numbering past any checkpoints on disk
	bound := store.Bounds()  // rebuilt from persisted checkpoints so a resumed
	if bound == nil {        // session can still rewind conversation / fork
		bound = map[int]int{}
	}
	m.mu.Lock()
	m.store = store
	m.turn = next
	m.bound = bound
	m.mu.Unlock()
}

// enabled reports whether a checkpoint store is bound.
func (m *checkpointManager) enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store != nil
}

// beginWithObserver opens a checkpoint and updates the mutation observer's
// ownership turn for subsequent captures.
func (m *checkpointManager) beginWithObserver(input string, msgIndex int, obs *checkpoint.MutationObserver) (int, *checkpoint.Store, bool) {
	m.mu.Lock()
	store := m.store
	if store == nil {
		m.mu.Unlock()
		return 0, nil, false
	}
	turn := m.turn
	m.turn++
	m.bound[turn] = msgIndex
	m.mu.Unlock()
	if obs != nil {
		obs.NoteCrossTurnBackgroundWriter(turn)
		obs.SetOwnershipTurn(turn)
	}
	store.Begin(turn, input, msgIndex)
	return turn, store, true
}

type guardedTurnCheckpoint struct {
	session      *agent.Session
	store        *checkpoint.Store
	turn         int
	messageIndex int
	openedAt     int64
}

type guardedTurnCompletion struct {
	checkpoint *guardedTurnCheckpoint
}

type guardedTurnCompletionKey struct{}

func withGuardedTurnCompletion(ctx context.Context) (context.Context, *guardedTurnCompletion) {
	completion := &guardedTurnCompletion{}
	return context.WithValue(ctx, guardedTurnCompletionKey{}, completion), completion
}

// beginCheckpoint opens a rewind checkpoint before the visible user message is
// appended. Guarded turns retain the exact boundary so TurnDone can identify
// the corresponding optimistic frontend item without positional guessing.
func (c *Controller) beginCheckpoint(ctx context.Context, input string) {
	if c.executor == nil || c.executor.Session() == nil {
		return
	}
	session := c.executor.Session()
	messageIndex := session.Len()
	openedAt := time.Now().UnixMilli()
	atomic.AddInt64(&c.sessionRevision, 1)
	turn, store, ok := c.checkpoints.beginWithObserver(input, messageIndex, c.mutationObserver)
	if ok {
		if completion, _ := ctx.Value(guardedTurnCompletionKey{}).(*guardedTurnCompletion); completion != nil {
			completion.checkpoint = &guardedTurnCheckpoint{
				session: session, store: store, turn: turn, messageIndex: messageIndex, openedAt: openedAt,
			}
		}
	}
	// User-visible turn start records an irreversible message-send receipt so
	// recovery never claims a clean rollback of an already-committed prompt.
	// Keep this owner bookkeeping even when checkpoints are disabled.
	gen := c.RuntimeGeneration()
	if gen == 0 {
		gen = c.RuntimeOwner().Gate.Published()
	}
	msgID := fmt.Sprintf("turn-%d-%d", gen, atomic.LoadInt64(&c.sessionRevision))
	// Dedup: a retried turn with the same revision must not double-record.
	owner := c.RuntimeOwner()
	owner.RecordMessageSentOnce(gen, msgID, "control")
	d := owner.DecideResume(gen)
	c.mu.Lock()
	c.lastResumeDecision = d
	c.mu.Unlock()
}

// validatedCheckpointTurn returns the checkpoint only while its original
// boundary still names the real user message committed by this guarded turn.
// Stale or synthetic candidates fail closed rather than being relocated.
func (c *Controller) validatedCheckpointTurn(completion *guardedTurnCompletion) *int {
	if completion == nil || completion.checkpoint == nil || c.executor == nil {
		return nil
	}
	candidate := completion.checkpoint
	if c.executor.Session() != candidate.session {
		return nil
	}
	if !c.checkpoints.matchesBoundary(candidate.store, candidate.turn, candidate.messageIndex) {
		return nil
	}
	messages := candidate.session.Snapshot()
	if candidate.messageIndex < 0 || candidate.messageIndex >= len(messages) {
		return nil
	}
	message := messages[candidate.messageIndex]
	if message.Role != provider.RoleUser || message.LocalOnly ||
		!agent.IsUserAuthoredTurnMessage(message) ||
		(message.CreatedAt > 0 && candidate.openedAt > 0 && message.CreatedAt < candidate.openedAt) {
		return nil
	}
	turn := candidate.turn
	return &turn
}

func (m *checkpointManager) matchesBoundary(store *checkpoint.Store, turn, messageIndex int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	boundary, ok := m.bound[turn]
	return ok && m.store == store && boundary == messageIndex
}

// turnsByMessageIndex returns message-log index -> checkpoint turn over live
// boundaries. The desktop transcript uses this authoritative map instead of
// recounting visible user bubbles, which can diverge when synthetic user-role
// messages are hidden from the UI.
func (m *checkpointManager) turnsByMessageIndex() map[int]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int]int, len(m.bound))
	for turn, index := range m.bound {
		if existing, ok := out[index]; ok && existing < turn {
			continue
		}
		out[index] = turn
	}
	return out
}

// boundary returns the recorded turn-start message index, if any.
func (m *checkpointManager) boundary(turn int) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bound[turn]
	return b, ok
}

// list returns the checkpoint metadata (nil when disabled).
func (m *checkpointManager) list() []checkpoint.Meta {
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.List()
}

func (m *checkpointManager) fileState(path string) (checkpoint.FileState, bool) {
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()
	if store == nil {
		return checkpoint.FileState{}, false
	}
	return store.FileState(path)
}

// snapshot records a pre-edit file change into the open checkpoint — the
// executor's pre-edit hook. No-op when disabled.
func (m *checkpointManager) snapshot(ch diff.Change) {
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()
	if store != nil {
		store.Snapshot(ch)
	}
}

// truncateFrom renumbers future turns from `turn` and drops every boundary at or
// after it — the conversation-rewind renumber after the message log is cut back.
func (m *checkpointManager) truncateFrom(turn int) error {
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()
	if store != nil {
		if err := store.TruncateFrom(turn); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.turn = turn
	for k := range m.bound {
		if k >= turn {
			delete(m.bound, k)
		}
	}
	m.mu.Unlock()
	return nil
}

// storeRef returns the live store pointer without holding mu across caller work.
func (m *checkpointManager) storeRef() *checkpoint.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store
}

// stampCheckpointIdentity binds the open checkpoint to the turn's first
// side-effecting call once its start barrier is durable, so a later restore
// names the attempt it protects instead of only the user turn.
func (c *Controller) stampCheckpointIdentity(e event.Event, turnID string) {
	store := c.checkpoints.storeRef()
	if store == nil || e.Tool.ID == "" {
		return
	}
	sum := sha256.Sum256([]byte(e.Tool.Args))
	store.StampRecoveryIdentity(turnID, e.Tool.ID, hex.EncodeToString(sum[:]), func() string {
		if c.executor == nil || c.executor.Session() == nil {
			return ""
		}
		digest, err := c.executor.Session().ContentDigest()
		if err != nil {
			return ""
		}
		return digest
	})
}
