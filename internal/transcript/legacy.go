package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"reasonix/internal/store"
)

// LegacyDisplayTurn is read-only compatibility for pre-projection desktops.
// New events and snapshots use MessageID and TurnID, never text matching.
type LegacyDisplayTurn struct {
	TurnID        string    `json:"turnId,omitempty"`
	UserMessageID string    `json:"userMessageId,omitempty"`
	UserHash      string    `json:"userHash"`
	Messages      []Message `json:"messages"`
}

type LegacyDisplays struct {
	Users map[string]string
	Turns []LegacyDisplayTurn
}

func LegacyDisplayKey(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func LoadLegacyDisplays(dir, sessionPath string) (LegacyDisplays, error) {
	var users map[string]map[string]string
	var turns map[string][]LegacyDisplayTurn
	if err := readLegacyJSON(store.SessionLegacyDisplays(dir), &users); err != nil {
		return LegacyDisplays{}, err
	}
	if err := readLegacyJSON(store.SessionLegacyPlannerDisplays(dir), &turns); err != nil {
		return LegacyDisplays{}, err
	}
	key := filepath.Base(sessionPath)
	return LegacyDisplays{Users: users[key], Turns: turns[key]}, nil
}

func readLegacyJSON(path string, dst any) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	const maxLegacyBytes = 64 << 20
	b, err := io.ReadAll(io.LimitReader(f, maxLegacyBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxLegacyBytes {
		return errors.New("legacy transcript display exceeds import limit")
	}
	return json.Unmarshal(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}), dst)
}
