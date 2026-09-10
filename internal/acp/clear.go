package acp

import (
	"context"
	"encoding/json"
	"strings"

	"reasonix/internal/control"
)

// handleClearPrompt resolves the ACP /clear verb, mirroring the CLI's /clear
// confirmation contract: YOLO mode clears immediately (the mode already opted
// out of confirmations), every other approval mode asks the client through a
// session/request_permission round-trip first. The turn path never reaches
// submitCommandOrTurnReady, so the verb lives here instead of the controller.
func (s *service) handleClearPrompt(ctx context.Context, sess *acpSession, text string) (bool, any, error) {
	if strings.TrimSpace(text) != "/clear" {
		return false, nil, nil
	}
	ctrl := sess.currentCtrl()
	if ctrl == nil {
		return true, nil, &RPCError{Code: ErrInternal, Message: "session/prompt: no active controller for /clear"}
	}
	if ctrl.ToolApprovalMode() != control.ToolApprovalYolo {
		approved, err := s.confirmSessionClear(ctx, sess)
		if err != nil {
			return true, nil, &RPCError{Code: ErrInternal, Message: "session/prompt: clear confirmation failed: " + err.Error()}
		}
		if !approved {
			return true, clearPromptResult(sess), nil
		}
	}
	if err := ctrl.ClearSession(); err != nil {
		return true, nil, &RPCError{Code: ErrInternal, Message: "session/prompt: clear failed: " + err.Error()}
	}
	sess.transcript = ctrl.SessionPath()
	return true, clearPromptResult(sess), nil
}

// clearPromptResult is the successful /clear prompt response, carrying the
// (rotated) transcript path exactly like the ordinary end_turn path does.
func clearPromptResult(sess *acpSession) *SessionPromptResult {
	res := &SessionPromptResult{StopReason: StopEndTurn}
	if sess.transcript != "" {
		res.TranscriptPath = &sess.transcript
	}
	return res
}

// confirmSessionClear asks the client whether the current session may be
// cleared. It reuses the tool-approval round-trip because ACP v1 has no
// dedicated in-place clear; clients already render session/request_permission.
func (s *service) confirmSessionClear(ctx context.Context, sess *acpSession) (bool, error) {
	params := PermissionRequestParams{
		SessionID: sess.id,
		ToolCall: PermissionToolCall{
			ToolCallID: "clear-" + sess.id,
			Title:      "Clear the current session?",
			Kind:       "other",
			Status:     "pending",
		},
		Options: []PermissionOption{
			{OptionID: "reasonix_clear_confirm", Name: "Clear the session", Kind: OptAllowOnce},
			{OptionID: "reasonix_clear_cancel", Name: "Cancel", Kind: OptRejectOnce},
		},
	}
	raw, err := s.conn.Request(ctx, "session/request_permission", params)
	if err != nil {
		return false, err
	}
	var res PermissionRequestResult
	if json.Unmarshal(raw, &res) != nil || res.Outcome.Outcome != "selected" {
		return false, nil
	}
	return res.Outcome.OptionID == "reasonix_clear_confirm", nil
}
