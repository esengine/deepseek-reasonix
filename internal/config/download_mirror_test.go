package config

import "testing"

func TestCLIDownloadMirrorEnvOverridesFile(t *testing.T) {
	t.Setenv("REASONIX_DOWNLOAD_MIRROR", "https://mirror.example.com/gh")
	cfg := Default()
	cfg.CLI.DownloadMirror = "https://file-mirror.example.com"
	if got := cfg.CLIDownloadMirror(); got != "https://mirror.example.com/gh" {
		t.Fatalf("CLIDownloadMirror() = %q, want the env override", got)
	}
}

func TestCLIDownloadMirrorNormalizes(t *testing.T) {
	t.Setenv("REASONIX_DOWNLOAD_MIRROR", "")
	for raw, want := range map[string]string{
		"mirror.example.com":         "https://mirror.example.com",
		"https://mirror.example.com": "https://mirror.example.com",
		"https://m.example.com/gh/":  "https://m.example.com/gh",
	} {
		cfg := Default()
		cfg.CLI.DownloadMirror = raw
		if got := cfg.CLIDownloadMirror(); got != want {
			t.Fatalf("CLIDownloadMirror() for %q = %q, want %q", raw, got, want)
		}
	}
}

func TestCLIDownloadMirrorEmptyWhenUnset(t *testing.T) {
	t.Setenv("REASONIX_DOWNLOAD_MIRROR", "")
	if got := Default().CLIDownloadMirror(); got != "" {
		t.Fatalf("CLIDownloadMirror() = %q, want empty (github.com direct)", got)
	}
}
