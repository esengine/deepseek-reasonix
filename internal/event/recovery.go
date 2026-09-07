package event

// RetryScope distinguishes connection+header retries, body-phase stream
// retries, and host-classified protocol recovery. Older clients ignore an
// unknown value and still render the generic retry state.
type RetryScope string

const (
	RetryScopeHeaders  RetryScope = "headers"
	RetryScopeStream   RetryScope = "stream"
	RetryScopeProtocol RetryScope = "protocol"
)

// RecoveryStatus is a local UI projection, never provider-visible metadata.
type RecoveryStatus struct {
	Phase         string `json:"phase,omitempty"`
	Reason        string `json:"reason,omitempty"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
	WaitedMs      int64  `json:"waited_ms,omitempty"`
	Waiting       bool   `json:"waiting,omitempty"`
	TurnID        string `json:"turnId,omitempty"`
	AttemptID     string `json:"attemptId,omitempty"`
	RequiresUser  bool   `json:"requiresUserDecision,omitempty"`
	Silent        bool   `json:"silentInterruption,omitempty"`
}
