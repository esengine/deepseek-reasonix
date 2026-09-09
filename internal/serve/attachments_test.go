package serve

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
)

// minimalPNG returns a valid 1x1 white PNG.
func minimalPNG() []byte {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestUploadAttachmentJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)

	projRoot := filepath.Join(home, "project")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc, WorkspaceRoot: projRoot})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer srv.Close()

	pngData := minimalPNG()
	b64 := base64.StdEncoding.EncodeToString(pngData)
	body, _ := json.Marshal(map[string]string{
		"data": b64,
		"mime": "image/png",
	})

	resp, err := http.Post(srv.URL+"/attachments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	ref := result["ref"]
	if ref == "" {
		t.Fatal("ref is empty")
	}
	if !strings.Contains(ref, ".reasonix/attachments/") {
		t.Fatalf("ref %q does not contain .reasonix/attachments/", ref)
	}
	if !strings.HasSuffix(ref, ".png") {
		t.Fatalf("ref %q does not end with .png", ref)
	}
	// Verify the file was actually written (relative to project root).
	if _, err := os.Stat(filepath.Join(projRoot, ref)); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestUploadAttachmentNoData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)

	projRoot := filepath.Join(home, "project")
	os.MkdirAll(projRoot, 0o755)

	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc, WorkspaceRoot: projRoot})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"mime": "image/png"})
	resp, err := http.Post(srv.URL+"/attachments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadAttachmentUnpaddedBase64(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)

	projRoot := filepath.Join(home, "project")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc, WorkspaceRoot: projRoot})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer srv.Close()

	pngData := minimalPNG()
	b64 := base64.StdEncoding.EncodeToString(pngData)
	b64 = strings.TrimRight(b64, "=") // strip padding, as many clients do

	body, _ := json.Marshal(map[string]string{
		"data": b64,
		"mime": "image/png",
	})
	resp, err := http.Post(srv.URL+"/attachments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := readAll(resp)
		t.Fatalf("status = %d, want 200 (unpadded base64 accepted); body=%s", resp.StatusCode, b)
	}
}

func TestUploadAttachmentOversize(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)

	projRoot := filepath.Join(home, "project")
	os.MkdirAll(projRoot, 0o755)

	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc, WorkspaceRoot: projRoot})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer srv.Close()

	// Raw body over the MaxBytesReader cap (well beyond maxUploadBytes*2).
	huge := strings.Repeat("A", maxUploadBytes*2+1)
	body, _ := json.Marshal(map[string]string{"data": huge, "mime": "image/png"})
	resp, err := http.Post(srv.URL+"/attachments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}
