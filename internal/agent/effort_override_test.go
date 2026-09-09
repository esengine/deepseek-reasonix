package agent

import (
	"testing"

	"reasonix/internal/event"
)

// varyingProvider extends the shared fakeProvider with a request-scoped
// effort vocabulary, mirroring the openai adapter's PerRequestEfforts probe.
type varyingProvider struct {
	fakeProvider
	efforts []string
}

func (p *varyingProvider) PerRequestEfforts() []string { return p.efforts }

func TestSetSessionEffortOverrideVocabularyGate(t *testing.T) {
	newAgent := func(efforts []string) *Agent {
		return New(&varyingProvider{efforts: efforts}, nil, NewSession("s"), Options{}, event.Discard)
	}

	a := newAgent([]string{"low", "max"})
	if !a.SetSessionEffortOverride("max") {
		t.Fatal("vocabulary-listed level rejected")
	}
	if got := a.effortOverrideForRequest(); got != "max" {
		t.Fatalf("override = %q, want max", got)
	}
	if a.SetSessionEffortOverride("high") {
		t.Fatal("level outside the vocabulary accepted")
	}
	if got := a.effortOverrideForRequest(); got != "max" {
		t.Fatalf("override after rejected level = %q, want max", got)
	}
	if !a.SetSessionEffortOverride("") {
		t.Fatal("clearing the override must always succeed")
	}
	if got := a.effortOverrideForRequest(); got != "" {
		t.Fatalf("override after clear = %q, want empty", got)
	}
}

func TestSetSessionEffortOverrideNonVaryingProvider(t *testing.T) {
	a := New(&fakeProvider{}, nil, NewSession("s"), Options{}, event.Discard)
	if a.SetSessionEffortOverride("max") {
		t.Fatal("non-varying provider accepted a per-request override")
	}
	if got := a.effortOverrideForRequest(); got != "" {
		t.Fatalf("override = %q, want empty", got)
	}
}

func TestEffortOverrideForRequestGovernorPriority(t *testing.T) {
	a := New(&varyingProvider{efforts: []string{"low", "max"}}, nil, NewSession("s"), Options{}, event.Discard)
	if !a.SetSessionEffortOverride("max") {
		t.Fatal("set session override")
	}

	prevGovernorEnabled := governorEnabled
	governorEnabled = true
	t.Cleanup(func() { governorEnabled = prevGovernorEnabled })

	// Governor disengaged: the session override stands.
	if got := a.effortOverrideForRequest(); got != "max" {
		t.Fatalf("override = %q, want max", got)
	}
	// Governor engaged: the running guard must not be outbid by a session
	// depth bump.
	a.task.governor.engaged = true
	if got := a.effortOverrideForRequest(); got != governorEffort {
		t.Fatalf("override = %q, want governor effort %q", got, governorEffort)
	}
}
