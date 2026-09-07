package event

// Notice codes are stable machine-readable identifiers for known notices.
// Frontends localize a notice's main copy by Code and fall back to matching
// the English Text (or showing it raw) when Code is empty or unknown, so
// wording edits in Go no longer silently break localization. Values are
// wire-stable: never rename or reuse one once shipped.
const (
	NoticeCodeFinalReadiness                                    = "final_readiness"
	NoticeCodeEmptyFinal                                        = "empty_final"
	NoticeCodeExecutorHandoff                                   = "executor_handoff"
	NoticeCodeToolBudget                                        = "tool_budget"
	NoticeCodePromptQueued                                      = "prompt_queued"
	NoticeCodeLoopGuard                                         = "loop_guard"
	NoticeCodeProgressGuard                                     = "progress_guard"
	NoticeCodeEvidenceNudge                                     = "evidence_nudge"
	NoticeCodeReasoningGovernor                                 = "reasoning_governor"
	NoticeCodeWorkspaceLease                                    = "workspace_lease"
	NoticeCodeBackgroundJobFinished                             = "background_job_finished"
	NoticeCodeCancelledTurn                                     = "cancelled_turn_display"
	NoticeCodeRecoveredToolConfirmed                            = "recovered_tool_confirmed"
	NoticeCodeStreamInterruptedIdleTimeout                      = "stream_interrupted_idle_timeout"
	NoticeCodeStreamInterruptedPrematureEOF                     = "stream_interrupted_premature_eof"
	NoticeCodeStreamInterruptedConnectionReset                  = "stream_interrupted_connection_reset"
	NoticeCodeUnappliedSteer                                    = "unapplied_steer"
	NoticeCodeMCPToolsList                                      = "mcp_tools_list"
	NoticeCodeSessionRecoveryForked                             = "session_recovery_forked"
	NoticeCodeSessionRecoveryAdopted                            = "session_recovery_adopted"
	NoticeCodeSessionRecoveryAdoptedCovered                     = "session_recovery_adopted_covered"
	NoticeCodeSessionRecoveryDepthCap                           = "session_recovery_depth_cap"
	NoticeCodeSessionShutdownRecoveryForked                     = "session_shutdown_recovery_forked"
	NoticeCodeCompletionUncertain                               = "completion_uncertain"
	NoticeCodeIncompleteReadDetected                            = "incomplete_read_detected"
	NoticeCodeReadContinuationRequired                          = "continuation_required"
	NoticeCodeReadCompleted                                     = "read_completed"
	NoticeCodeReadOversizeRejected                              = "oversize_rejected"
	NoticeCodeReadStrategyRequired                              = "read_strategy_required"
	NoticeCodeReadStrategyProgress                              = "read_strategy_progress"
	NoticeCodeReadStrategyResolved                              = "read_strategy_resolved"
	NoticeCodeReadLocalSafetyPaged                              = "read_local_safety_paged"
	NoticeCodeDecisionReceipt, NoticeCodeContextEditingFallback = "decision_receipt", "context_editing_fallback"
	NoticeCodeSessionTakenOver                                  = "session_taken_over"
	NoticeCodeSessionReclaimRequested                           = "session_reclaim_requested"
	NoticeCodeSessionReclaimed                                  = "session_reclaimed"
	NoticeCodeReasoningReplayRepair                             = "reasoning_replay_repair"
)
