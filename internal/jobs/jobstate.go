package jobs

// jobClock is a job's liveness timeline: unix-millisecond stamps advanced as the
// job runs, plus the latch the stalled warning sets once activityAt goes quiet.
// Grouping the latch with the stamp it derives from stops the two from
// describing different jobs, and keeps Job from listing every scalar flat.
// Guarded by Job.mu; this type takes no lock of its own.
type jobClock struct {
	startedAt  int64
	finishedAt int64
	activityAt int64
	stalled    bool
}

// jobOutcome is what the run function left behind: its text, whether the run
// returned at all, and whether Output already surfaced the text. All three are
// written once as the job terminates and read together afterwards. Guarded by
// Job.mu.
type jobOutcome struct {
	text     string
	returned bool
	read     bool // text already surfaced by Output (task jobs stream nothing to the tail)
}
