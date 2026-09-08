package cli

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAskSecretNonTTYReadsCompleteValueWithoutEcho(t *testing.T) {
	const secret = "rk_test_ABCDEF1234567890"
	var out bytes.Buffer
	got := askSecretWith(bufio.NewScanner(strings.NewReader(secret+"\n")), &out, "API key", false, nil)
	if got != secret {
		t.Fatalf("secret = %q, want %q", got, secret)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("prompt output leaked secret: %q", out.String())
	}
}

func TestAskSecretTTYUsesHiddenReader(t *testing.T) {
	const secret = "rk_test_ABCDEF1234567890"
	called := false
	var out bytes.Buffer
	got := askSecretWith(bufio.NewScanner(strings.NewReader("wrong\n")), &out, "API key", true, func() ([]byte, error) {
		called = true
		return []byte(secret), nil
	})
	if !called || got != secret {
		t.Fatalf("TTY read called=%v secret=%q", called, got)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("prompt output leaked secret: %q", out.String())
	}
}

func TestAskSecretTTYReadErrorReturnsEmpty(t *testing.T) {
	var out bytes.Buffer
	got := askSecretWith(bufio.NewScanner(strings.NewReader("ignored\n")), &out, "API key", true, func() ([]byte, error) {
		return nil, errors.New("cancelled")
	})
	if got != "" {
		t.Fatalf("secret = %q, want empty on read error", got)
	}
}
