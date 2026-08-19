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
