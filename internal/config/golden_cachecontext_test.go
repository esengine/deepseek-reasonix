package config

import (
	"strings"
	"testing"
)

// TestHashCacheContextGolden pins the exact bytes of the hash-shortened
// cachecontext so a change to the hashing scheme (algorithm or hash length)
// fails loudly instead of silently altering the emitted user_id.
func TestHashCacheContextGolden(t *testing.T) {
	rest := strings.Repeat("abc-def-", 200)
	out := hashCacheContext("alice", "alice-"+rest, 512)
	const want = "alice-80f7243f4423a6a6f-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-abc-def-"
	if out != want {
		t.Fatalf("hashCacheContext = %q, want %q", out, want)
	}
	if len(want) != 512 {
		t.Fatalf("golden length = %d, want 512", len(want))
	}
}
