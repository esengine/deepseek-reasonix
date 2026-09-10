package bot

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSavedSessionPathForMessageRestoresLastSession verifies that after a
// gateway restart (no in-memory override), the session file path this remote
// chat last used is restored from the persisted session_mappings, including
// stripping the path: prefix.
func TestSavedSessionPathForMessageRestoresLastSession(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "20260802-000000.000000000-deepseek-v4-flash.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	cfg := GatewayConfig{
		ConnectionChannels: map[string]ChannelConfig{
			"weixin-weixin": {
				SessionMappings: []SessionMapping{{
					RemoteID:  "user@im.wechat",
					SessionID: "path:" + sessionPath,
				}},
			},
		},
	}
	gw := NewGateway(cfg, nil, nil)
	msg := InboundMessage{
		Platform:     PlatformWeixin,
		ConnectionID: "weixin-weixin",
		ChatType:     ChatDM,
		ChatID:       "user@im.wechat",
	}
	got := gw.savedSessionPathForMessage(msg)
	want := filepath.Clean(sessionPath)
	if got != want {
		t.Fatalf("savedSessionPathForMessage = %q, want %q", got, want)
	}
}

// TestSavedSessionPathForMessageMissingFile verifies that a missing session
// file yields an empty string so the bot starts a fresh session instead of
// failing Resume and becoming unresponsive.
func TestSavedSessionPathForMessageMissingFile(t *testing.T) {
	cfg := GatewayConfig{
		ConnectionChannels: map[string]ChannelConfig{
			"weixin-weixin": {
				SessionMappings: []SessionMapping{{
					RemoteID:  "user@im.wechat",
					SessionID: "path:" + filepath.Join(t.TempDir(), "gone.jsonl"),
				}},
			},
		},
	}
	gw := NewGateway(cfg, nil, nil)
	msg := InboundMessage{
		Platform:     PlatformWeixin,
		ConnectionID: "weixin-weixin",
		ChatType:     ChatDM,
		ChatID:       "user@im.wechat",
	}
	if got := gw.savedSessionPathForMessage(msg); got != "" {
		t.Fatalf("savedSessionPathForMessage = %q, want empty for missing session file", got)
	}
}

// TestSavedSessionPathForMessageNoMapping verifies that no matching mapping
// yields an empty string (new user / new chat starts a fresh session).
func TestSavedSessionPathForMessageNoMapping(t *testing.T) {
	cfg := GatewayConfig{
		ConnectionChannels: map[string]ChannelConfig{
			"weixin-weixin": {
				SessionMappings: []SessionMapping{{
					RemoteID:  "other@im.wechat",
					SessionID: "path:" + filepath.Join(t.TempDir(), "x.jsonl"),
				}},
			},
		},
	}
	gw := NewGateway(cfg, nil, nil)
	msg := InboundMessage{
		Platform:     PlatformWeixin,
		ConnectionID: "weixin-weixin",
		ChatType:     ChatDM,
		ChatID:       "user@im.wechat",
	}
	if got := gw.savedSessionPathForMessage(msg); got != "" {
		t.Fatalf("savedSessionPathForMessage = %q, want empty for no matching mapping", got)
	}
}

// TestSavedSessionPathForMessagePlatformFallback verifies that mappings are
// read from the platform-level Channels when no per-connection mapping exists.
func TestSavedSessionPathForMessagePlatformFallback(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "20260802-000000.000000000-deepseek-v4-flash.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	cfg := GatewayConfig{
		Channels: map[Platform]ChannelConfig{
			PlatformWeixin: {
				SessionMappings: []SessionMapping{{
					RemoteID:  "user@im.wechat",
					SessionID: "path:" + sessionPath,
				}},
			},
		},
	}
	gw := NewGateway(cfg, nil, nil)
	msg := InboundMessage{
		Platform: PlatformWeixin,
		ChatType: ChatDM,
		ChatID:   "user@im.wechat",
	}
	got := gw.savedSessionPathForMessage(msg)
	want := filepath.Clean(sessionPath)
	if got != want {
		t.Fatalf("savedSessionPathForMessage = %q, want %q", got, want)
	}
}
