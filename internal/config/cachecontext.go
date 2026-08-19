package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
)

// maxCacheContextLen is DeepSeek's user_id length ceiling (512).
const maxCacheContextLen = 512

// cacheContextIDRegexp matches the characters DeepSeek rejects in a user_id.
var cacheContextIDRegexp = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// DefaultCacheContextForRoot builds the auto cachecontext for a workspace that
// configures none: derived from "<unix-user>:<abs-repo-path>", sanitized to
// DeepSeek's ^[a-zA-Z0-9_-]+$ rule. Over-long values keep the "<user>-" prefix
// and as much of the path tail as fits, hashing the overflowing middle so the
// result is exactly maxCacheContextLen characters.
func DefaultCacheContextForRoot(root string) string {
	return BuildCacheContext(systemUsername(), root)
}

// BuildCacheContext builds a DeepSeek-compliant cachecontext from a username
// and repo root, sanitized to ^[a-zA-Z0-9_-]+$ (≤512). Over-long values keep
// the "<user>-" prefix and the path tail, hashing the overflowing middle so
// the result is exactly maxCacheContextLen characters.
func BuildCacheContext(user, root string) string {
	if user == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil || abs == "" {
		return ""
	}
	sanUser := sanitizeCacheContext(user)
	s := sanitizeCacheContext(user + ":" + abs)
	if len(s) <= maxCacheContextLen {
		return s
	}
	return hashCacheContext(sanUser, s, maxCacheContextLen)
}

// EffectiveCacheContext returns the explicit cachecontext, or the auto
// "<user>:<repo>" default (with the config-resolved logname) when unset. With
// no workspace root it returns only an explicit value; there is no repo path to
// derive an auto id from.
func (c *Config) EffectiveCacheContext(root string) string {
	if c.CacheContext != "" {
		return c.CacheContext
	}
	if root == "" {
		return ""
	}
	return BuildCacheContext(c.cacheContextUser(), root)
}

// cacheContextUser resolves the username for the auto cachecontext, preferring
// (highest first) a config logname, a config user, $LOGNAME, then the system
// account. logname prevails over user when both are set.
func (c *Config) cacheContextUser() string {
	if c != nil && c.LogName != "" {
		return c.LogName
	}
	if c != nil && c.User != "" {
		return c.User
	}
	if env := os.Getenv("LOGNAME"); env != "" {
		return env
	}
	return systemUsername()
}

// sanitizeCacheContext rewrites characters DeepSeek would reject as "-".
func sanitizeCacheContext(s string) string {
	return cacheContextIDRegexp.ReplaceAllString(s, "-")
}

// hashCacheContext packs an over-long cachecontext into exactly maxLen
// characters: "<user>-" + 16-hex hash of the overflowing middle + the path tail.
// BuildCacheContext only calls it for strings over maxCacheContextLen, so rest
// always exceeds tailLen.
func hashCacheContext(sanUser, s string, maxLen int) string {
	const hashLen = 16 // hex chars, half a sha256 digest
	prefix := sanUser + "-"
	if len(prefix) >= maxLen {
		return s[:maxLen]
	}
	if maxPrefix := maxLen - hashLen; len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	tailLen := maxLen - len(prefix) - hashLen
	rest := s[len(sanUser)+1:] // strip the sanitized "<user>-" prefix
	sum := sha256.Sum256([]byte(rest[:len(rest)-tailLen]))
	return prefix + hex.EncodeToString(sum[:hashLen/2]) + rest[len(rest)-tailLen:]
}

// systemUsername returns the Unix account name, falling back to $USER.
func systemUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
