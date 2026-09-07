package control

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/event"
)

func TestTerminalTurnStatusEscalatesUnprovenOutcomes(t *testing.T) {
	cases := []struct {
		name string
		e    event.Event
		want event.TurnStatus
	}{
		{"clean completion", event.Event{}, event.TurnCompleted},
		{"cancel with only not-started calls", event.Event{Cancelled: true, Recovery: &event.RecoveryStatus{Phase: "turn_recovery_required"}}, event.TurnInterrupted},
		{"cancel with unknown side effect", event.Event{Cancelled: true, Recovery: &event.RecoveryStatus{RequiresUser: true}}, event.TurnRecoveryRequired},
		{"silent interruption", event.Event{Err: context.Canceled, Recovery: &event.RecoveryStatus{Silent: true}}, event.TurnRecoveryRequired},
		{"provider failure with partial display", event.Event{Err: errors.New("boom"), Recovery: &event.RecoveryStatus{}}, event.TurnFailed},
		{"provider failure with unknown side effect", event.Event{Err: errors.New("boom"), Recovery: &event.RecoveryStatus{RequiresUser: true}}, event.TurnRecoveryRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalTurnStatus(tc.e); got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
			if !terminalTurnStatus(tc.e).Terminal() {
				t.Fatal("every turn must end in a terminal status")
			}
		})
	}
}
