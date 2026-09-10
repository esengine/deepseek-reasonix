package control

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"reasonix/internal/hook"
)

// A question blocks the run on the user exactly like an approval prompt, so it
// must fire the same Notification hook; without it an external channel hears
// about pending approvals while a blocking ask looks like the agent working.
func TestAskFiresNotificationHook(t *testing.T) {
	var mu sync.Mutex
	var stdins []string
	hooks := hook.NewRunner([]hook.ResolvedHook{{
		HookConfig: hook.HookConfig{Command: "record-notification"},
		Event:      hook.Notification,
		Scope:      hook.ScopeProject,
	}}, "", func(ctx context.Context, in hook.SpawnInput) hook.SpawnResult {
		mu.Lock()
		stdins = append(stdins, in.Stdin)
		mu.Unlock()
		return hook.SpawnResult{ExitCode: 0}
	}, nil)

	sink := &askProbeSink{}
	c := New(Options{Sink: sink, SessionDir: t.TempDir(), Hooks: hooks})

	go func() { _, _ = c.Ask(t.Context(), askProbeQuestions()) }()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(stdins)
		mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("a blocking question never fired the Notification hook")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	var payload struct {
		Event            string `json:"event"`
		Message          string `json:"message"`
		NotificationType string `json:"notificationType"`
	}
	mu.Lock()
	stdin := stdins[0]
	mu.Unlock()
	if err := json.Unmarshal([]byte(stdin), &payload); err != nil {
		t.Fatalf("hook stdin is not JSON: %v", err)
	}
	if payload.Event != "Notification" {
		t.Fatalf("event = %q, want Notification", payload.Event)
	}
	if payload.NotificationType != "question_prompt" {
		t.Fatalf("notificationType = %q, want question_prompt", payload.NotificationType)
	}
	if payload.Message != "answer needed: Which fix?" {
		t.Fatalf("message = %q, want the first question's prompt", payload.Message)
	}
}
