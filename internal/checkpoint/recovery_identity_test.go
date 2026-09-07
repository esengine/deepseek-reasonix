package checkpoint

import "testing"

func TestStampRecoveryIdentityBindsFirstWriterAndReloads(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, dir)
	s.Begin(3, "edit files", 7)
	calls := 0
	digest := func() string { calls++; return "sha256:transcript" }
	s.StampRecoveryIdentity("turn_a", "call-1", "args-1", digest)
	s.StampRecoveryIdentity("turn_a", "call-2", "args-2", digest)
	if calls != 1 {
		t.Fatalf("provider digest computed %d times, want once", calls)
	}
	got, ok := s.RecoveryIdentity(3)
	if !ok || got.ToolCallID != "call-1" || got.ToolArgumentsDigest != "args-1" || got.TurnID != "turn_a" || got.ProviderDigest != "sha256:transcript" {
		t.Fatalf("identity = %+v ok=%v, want first writer binding", got, ok)
	}

	reloaded := New(dir, dir)
	got, ok = reloaded.RecoveryIdentity(3)
	if !ok || got.ToolCallID != "call-1" || got.ProviderDigest != "sha256:transcript" {
		t.Fatalf("reloaded identity = %+v ok=%v", got, ok)
	}
	if _, ok := reloaded.RecoveryIdentity(4); ok {
		t.Fatal("unknown turn reported an identity")
	}
}

func TestStampRecoveryIdentityIgnoresTurnsWithoutOpenCheckpoint(t *testing.T) {
	s := New("", "")
	s.StampRecoveryIdentity("turn", "call", "args", func() string { t.Fatal("digest computed with no open checkpoint"); return "" })
	if _, ok := s.RecoveryIdentity(0); ok {
		t.Fatal("stamp landed without a checkpoint")
	}
}
