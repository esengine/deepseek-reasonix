package cli

import (
	"net/url"
	"strings"
)

// mirrorAssetURL rewrites one release-asset URL onto the configured download
// mirror. Only host-hopped paths are rewritten: the mirror base keeps an
// optional path prefix, and the asset keeps its exact owner/repo/tag/file
// tail so a mirror that serves the same release layout works unmodified.
// API URLs (api.github.com) are never mirrored — the releases metadata still
// comes from GitHub so asset identity and sizes stay authoritative.
func mirrorAssetURL(raw, mirror string) string {
	raw = strings.TrimSpace(raw)
	mirror = strings.TrimSpace(mirror)
	if raw == "" || mirror == "" || !strings.HasPrefix(raw, "https://github.com/") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.Contains(mirror, "://") {
		mirror = "https://" + mirror
	}
	m, err := url.Parse(mirror)
	if err != nil || m.Scheme == "" || m.Host == "" {
		return raw
	}
	tail := strings.TrimPrefix(u.Path, "/")
	if tail == "" {
		return raw
	}
	m.Path = strings.TrimRight(m.Path, "/") + "/" + tail
	return m.String()
}
