package cli

import (
	"io"
	"strings"
	"testing"
)

func TestWSLClipboardCommandPreservesUTF8Text(t *testing.T) {
	for _, text := range []string{"中文测试", "hello", "你好 🚀"} {
		t.Run(text, func(t *testing.T) {
			cmd := newWSLClipboardCommand(text)
			if got := cmd.Args[0]; got != "powershell.exe" {
				t.Fatalf("command = %q, want powershell.exe", got)
			}
			script := cmd.Args[len(cmd.Args)-1]
			if !strings.Contains(script, "[Text.UTF8Encoding]::new($false)") {
				t.Fatalf("PowerShell script does not explicitly decode UTF-8: %q", script)
			}
			if !strings.Contains(script, "Set-Clipboard") {
				t.Fatalf("PowerShell script does not use Set-Clipboard: %q", script)
			}
			got, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				t.Fatalf("read command stdin: %v", err)
			}
			if string(got) != text {
				t.Fatalf("clipboard bridge stdin = %q, want %q", got, text)
			}
		})
	}
}

func TestIsWSLFor(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		values map[string]string
		want   bool
	}{
		{name: "windows ignores WSL variables", goos: "windows", values: map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, want: false},
		{name: "linux without WSL", goos: "linux", values: map[string]string{}, want: false},
		{name: "WSL distro", goos: "linux", values: map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, want: true},
		{name: "WSL interop", goos: "linux", values: map[string]string{"WSL_INTEROP": "/run/WSL/8_interop"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.values[key] }
			if got := isWSLFor(tt.goos, getenv); got != tt.want {
				t.Fatalf("isWSLFor(%q) = %v, want %v", tt.goos, got, tt.want)
			}
		})
	}
}
