package jobs

import (
	"context"
	"fmt"
	"io"
	"reasonix/internal/event"
	"reasonix/internal/nilutil"
	"strings"
)

// StartForSession launches a job owned by parentSession. Session-scoped readers
// only see jobs whose owner matches the active session.
func (m *Manager) StartForSession(parentSession, kind, label string, run func(ctx context.Context, out io.Writer) (string, error)) *Job {
	parentSession = strings.TrimSpace(parentSession)
	kind = strings.TrimSpace(kind)
	if err := validatePathSegment(parentSession, "parentSession"); err != nil {
		return m.startInvalid(parentSession, kind, label, err)
	}
	if err := validatePathSegment(kind, "kind"); err != nil {
		return m.startInvalid(parentSession, kind, label, err)
	}
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("%s-%d", kind, m.seq)
	ctx, cancel := context.WithCancel(m.root)
	startedAt := nowMs()
	logPath, metaPath, file, artifactErr := m.openArtifactLocked(parentSession, id)
	j := &Job{
		ID:               id,
		Kind:             kind,
		Label:            label,
		SessionID:        parentSession,
		status:           Running,
		clock:            jobClock{startedAt: startedAt, activityAt: startedAt},
		cancel:           cancel,
		done:             make(chan struct{}),
		artifactPath:     logPath,
		artifactMetaPath: metaPath,
		artifactFile:     file,
		artifactComplete: artifactErr == "",
		artifactErr:      artifactErr,
	}
	ctx = WithSession(ctx, parentSession)
	ctx = context.WithValue(ctx, jobCtxKey{}, j)
	key := jobKey(parentSession, id)
	m.jobs[key] = j
	m.order = append(m.order, key)
	m.mu.Unlock()
	j.mu.Lock()
	if err := m.writeJobMetaLocked(j, Running); err != nil {
		j.artifactComplete = false
		j.artifactErr = err.Error()
	}
	j.mu.Unlock()
	if m.onJobStart != nil {
		m.onJobStart(j.done)
	}

	m.emitIfActive(parentSession, event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: startedText(kind, id, label)})
	m.notifyRuntime(parentSession, id)

	if !nilutil.IsNil(m.taskRecorder) {
		m.taskRecorder.RecordStart(id, kind, label)
	}

	m.wg.Add(1)
	if m.stalledWarning > 0 {
		m.wg.Add(1)
		go m.monitorStalled(parentSession, j)
	}
	go m.runJob(ctx, j, run)
	return j
}

func (m *Manager) runJob(ctx context.Context, j *Job, run func(context.Context, io.Writer) (string, error)) {
	defer m.wg.Done()
	result, err := runRecovered(ctx, jobWriter{j}, run)
	j.mu.Lock()
	j.outcome.returned = true
	j.mu.Unlock()

	var st Status
	switch {
	case ctx.Err() != nil:
		st = Killed
	case err != nil:
		st = Failed
		if result == "" {
			result = err.Error()
		}
	default:
		st = Done
	}
	finishedAt := nowMs()
	if result != "" {
		j.mu.Lock()
		if j.artifactFile != nil {
			if _, writeErr := j.artifactFile.WriteString(result); writeErr != nil {
				j.artifactErr = writeErr.Error()
			}
		} else {
			j.outcome.text = result
		}
		j.tail = appendTail(j.tail, []byte(result), defaultTailBytes)
		j.mu.Unlock()
	}
	targetDir := m.artifactTargetDirForJob(j)
	j.mu.Lock()
	if j.artifactFile != nil {
		if closeErr := j.artifactFile.Close(); closeErr != nil && j.artifactErr == "" {
			j.artifactErr = closeErr.Error()
		}
		j.artifactFile = nil
	}
	if j.artifactErr != "" {
		j.artifactComplete = false
	}
	j.clock.finishedAt = finishedAt
	if targetDir != "" {
		if moveErr := j.moveArtifactToDirLocked(targetDir); moveErr != nil {
			j.noteArtifactErr("migration: " + moveErr.Error())
		}
	}
	metaErr := m.writeJobMetaLocked(j, st)
	if metaErr != nil {
		j.noteArtifactErr("metadata: " + metaErr.Error())
	}
	j.mu.Unlock()
	// Queue the drain note and closing Notice before terminal status so Wait
	// cannot observe completion before DrainCompletedNote sees its bookkeeping.
	// The structured runtime notification follows the actual done boundary.
	parentSession := m.recordCompletion(j, st, err)

	j.mu.Lock()
	if j.status != Killed { // a concurrent Kill already published Killed — keep it
		j.status = st
	}
	if j.artifactPath != "" && j.artifactComplete {
		j.outcome.text = ""
		j.tail = nil
	}
	j.mu.Unlock()
	close(j.done)
	m.notifyRuntime(parentSession, j.ID)
}
