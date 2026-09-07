package cli

import "testing"

func TestMirrorAssetURL(t *testing.T) {
	const asset = "https://github.com/esengine/DeepSeek-Reasonix/releases/download/v1.31.3/reasonix_windows_amd64.zip"

	cases := []struct {
		name   string
		raw    string
		mirror string
		want   string
	}{
		{
			name:   "host mirror keeps the asset tail",
			raw:    asset,
			mirror: "https://mirror.example.com",
			want:   "https://mirror.example.com/esengine/DeepSeek-Reasonix/releases/download/v1.31.3/reasonix_windows_amd64.zip",
		},
		{
			name:   "path-prefixed mirror appends the tail",
			raw:    asset,
			mirror: "https://mirror.example.com/gh",
			want:   "https://mirror.example.com/gh/esengine/DeepSeek-Reasonix/releases/download/v1.31.3/reasonix_windows_amd64.zip",
		},
		{
			name:   "scheme-less mirror defaults to https",
			raw:    asset,
			mirror: "mirror.example.com",
			want:   "https://mirror.example.com/esengine/DeepSeek-Reasonix/releases/download/v1.31.3/reasonix_windows_amd64.zip",
		},
		{
			name:   "trailing slash on the mirror does not double",
			raw:    asset,
			mirror: "https://mirror.example.com/",
			want:   "https://mirror.example.com/esengine/DeepSeek-Reasonix/releases/download/v1.31.3/reasonix_windows_amd64.zip",
		},
		{
			name:   "empty mirror downloads from github unchanged",
			raw:    asset,
			mirror: "",
			want:   asset,
		},
		{
			name:   "non-github URL is never rewritten",
			raw:    "https://api.github.com/repos/esengine/DeepSeek-Reasonix/releases",
			mirror: "https://mirror.example.com",
			want:   "https://api.github.com/repos/esengine/DeepSeek-Reasonix/releases",
		},
		{
			name:   "garbage mirror leaves the URL alone",
			raw:    asset,
			mirror: "::::not-a-url",
			want:   asset,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mirrorAssetURL(tc.raw, tc.mirror); got != tc.want {
				t.Fatalf("mirrorAssetURL(%q, %q) =\n  %q\nwant\n  %q", tc.raw, tc.mirror, got, tc.want)
			}
		})
	}
}
