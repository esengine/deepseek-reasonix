package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeCacheContext(t *testing.T) {
	got := sanitizeCacheContext("alice:/home/alice/My Project!")
	want := "alice--home-alice-My-Project-"
	if got != want {
		t.Fatalf("sanitize = %q, want %q", got, want)
	}
}

func TestHashCacheContextFitsExactly(t *testing.T) {
	rest := strings.Repeat("abc-def-", 200) // 1600 chars
	out := hashCacheContext("alice", "alice-"+rest, 512)
	if len(out) != 512 {
		t.Fatalf("len = %d, want 512", len(out))
	}
	if !strings.HasPrefix(out, "alice-") {
		t.Fatalf("output missing user prefix: %q", out)
	}
	if !strings.HasSuffix(out, rest[len(rest)-32:]) {
		t.Fatalf("output missing path tail: %q", out)
	}
}

func TestDefaultCacheContextForRootOverLongHashes(t *testing.T) {
	if systemUsername() == "" {
		t.Skip("no unix user")
	}
	longRoot := filepath.Join(t.TempDir(), strings.Repeat("segment/", 80))
	got := DefaultCacheContextForRoot(longRoot)
	if len(got) != 512 {
		t.Fatalf("len = %d, want 512", len(got))
	}
	if cacheContextIDRegexp.MatchString(got) {
		t.Fatalf("output %q contains characters DeepSeek rejects", got)
	}
}

func TestCacheContextUserPriority(t *testing.T) {
	t.Setenv("LOGNAME", "log-env")
	if got := (&Config{LogName: "log-config"}).cacheContextUser(); got != "log-config" {
		t.Fatalf("config logname should win over $LOGNAME, got %q", got)
	}
	if got := (&Config{LogName: "log-config", User: "user-config"}).cacheContextUser(); got != "log-config" {
		t.Fatalf("logname should prevail over user, got %q", got)
	}
	if got := (&Config{User: "user-config"}).cacheContextUser(); got != "user-config" {
		t.Fatalf("config user should win over $LOGNAME, got %q", got)
	}
	if got := (&Config{}).cacheContextUser(); got != "log-env" {
		t.Fatalf("$LOGNAME should win over the system account, got %q", got)
	}
}

func TestCacheContextUserFallsBackToSystem(t *testing.T) {
	if systemUsername() == "" {
		t.Skip("no unix user")
	}
	t.Setenv("LOGNAME", "")
	if got := (&Config{}).cacheContextUser(); got != systemUsername() {
		t.Fatalf("system account should be the final fallback, got %q", got)
	}
}

func TestEffectiveCacheContextWithoutRootOnlyExplicit(t *testing.T) {
	if got := (&Config{CacheContext: "explicit"}).EffectiveCacheContext(""); got != "explicit" {
		t.Fatalf("explicit value lost without root, got %q", got)
	}
	if got := (&Config{}).EffectiveCacheContext(""); got != "" {
		t.Fatalf("no root + no explicit should be empty, got %q", got)
	}
}

// TestBuildSessionContextLen256 guards OpenRouter's session_id ceiling: the
// derived id must never exceed 256 characters and must stay sanitized to
// ^[a-zA-Z0-9_-]+$.
func TestBuildSessionContextLen256(t *testing.T) {
	longRoot := filepath.Join(t.TempDir(), strings.Repeat("segment/", 80))
	got := BuildSessionContext("alice", longRoot)
	if len(got) > maxSessionContextLen {
		t.Fatalf("len = %d, want <= %d", len(got), maxSessionContextLen)
	}
	if cacheContextIDRegexp.MatchString(got) {
		t.Fatalf("output %q contains characters OpenRouter rejects", got)
	}
}

func TestBuildSessionContextEqualsCacheContextPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	want := BuildCacheContext("alice", root)
	got := BuildSessionContext("alice", root)
	if got != want {
		t.Fatalf("session id %q != cachecontext %q for the same root", got, want)
	}
}

func TestEffectiveSessionContextSharesUserChain(t *testing.T) {
	t.Setenv("LOGNAME", "log-env")
	got := (&Config{}).EffectiveSessionContext(filepath.Join(t.TempDir(), "proj"))
	if got == "" || len(got) > maxSessionContextLen {
		t.Fatalf("EffectiveSessionContext = %q, want non-empty <= %d", got, maxSessionContextLen)
	}
	if !strings.HasPrefix(got, "log-env") {
		t.Fatalf("effective session id should start with the resolved username, got %q", got)
	}
	if got := (&Config{}).EffectiveSessionContext(""); got != "" {
		t.Fatalf("no root should give empty session id, got %q", got)
	}
}
