package turnevent

import "time"

type MetricsSnapshot struct {
	RawEvents             uint64
	StreamRecords         uint64
	BytesWritten          uint64
	ReplayEvents          uint64
	ReplayBytes           uint64
	ReplayResets          uint64
	Compactions           uint64
	CompactionFailures    uint64
	BytesBeforeCompact    uint64
	BytesAfterCompact     uint64
	TornTails             uint64
	WriteFailures         uint64
	ProjectionRetries     uint64
	OpenCount             uint64
	SyncCount             uint64
	CloseCount            uint64
	AppendLatencyBuckets  [5]uint64
	ReplayLatencyBuckets  [5]uint64
	CompactLatencyBuckets [5]uint64
	FileSizeBytes         int64
	UnconfirmedTurns      int
}

func (l *Ledger) MetricsSnapshot() MetricsSnapshot {
	if l == nil {
		return MetricsSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.metrics
	out.FileSizeBytes = l.fileSize
	out.UnconfirmedTurns = len(l.pendingProjectionsLocked())
	return out
}

func (l *Ledger) DrainMetrics() MetricsSnapshot {
	if l == nil {
		return MetricsSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.metrics
	out.FileSizeBytes = l.fileSize
	out.UnconfirmedTurns = len(l.pendingProjectionsLocked())
	l.metrics = MetricsSnapshot{}
	return out
}

func latencyBucket(elapsed time.Duration) int {
	switch {
	case elapsed < time.Millisecond:
		return 0
	case elapsed < 5*time.Millisecond:
		return 1
	case elapsed < 20*time.Millisecond:
		return 2
	case elapsed < 100*time.Millisecond:
		return 3
	default:
		return 4
	}
}
