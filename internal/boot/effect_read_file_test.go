package boot

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// readThenMutateProvider reads a large file, then runs git commit, then lists
// capabilities and finishes. Truncated/windowed reads must not freeze commits
// or finals through the real Build stack.
type readThenMutateProvider struct {
	mu    sync.Mutex
	reqs  []provider.Request
	round int
}

func (p *readThenMutateProvider) Name() string { return "boot-read-then-mutate" }

func (p *readThenMutateProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.round++
	round := p.round
	p.mu.Unlock()
	switch round {
	case 1:
		return streamChunks(
			provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{
				ID: "read-1", Name: "read_file", Arguments: `{"path":"big.txt"}`,
			}},
			provider.Chunk{Type: provider.ChunkDone},
		), nil
	case 2:
		return streamChunks(
			provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{
				ID: "commit-1", Name: "bash", Arguments: `{"command":"git commit -m test-commit"}`,
			}},
			provider.Chunk{Type: provider.ChunkDone},
		), nil
	case 3:
		return streamChunks(
			provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{
				ID: "list-1", Name: "use_capability", Arguments: `{"action":"list"}`,
			}},
			provider.Chunk{Type: provider.ChunkDone},
		), nil
	default:
		return streamChunks(
			provider.Chunk{Type: provider.ChunkText, Text: "committed after the partial read"},
			provider.Chunk{Type: provider.ChunkDone},
		), nil
	}
}

func (p *readThenMutateProvider) requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]provider.Request, len(p.reqs))
	copy(out, p.reqs)
	return out
}

func TestEffectTruncatedReadDoesNotBlockCommitOrFinalThroughRealBuild(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeFile(t, dir, "tracked.txt", "tracked\n")
	cmd := exec.Command("git", "add", "tracked.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	var body strings.Builder
	for i := 1; i <= 4500; i++ {
		fmt.Fprintf(&body, "line-%04d-content-for-pagination-test\n", i)
	}
	writeFile(t, dir, "big.txt", body.String())

	rec := &readThenMutateProvider{}
	provider.Register("boot-read-then-mutate", func(provider.Config) (provider.Provider, error) {
		return rec, nil
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "boot-read-then-mutate"
model = "x"
`)

	ctrl, err := Build(context.Background(), Options{
		Sink:            event.Discard,
		PermissionAllow: []string{"Bash(git *)"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	ctrl.SetToolApprovalMode("yolo")

	if err := ctrl.Run(context.Background(), "read big.txt then commit"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := rec.requests()
	if len(reqs) < 4 {
		t.Fatalf("provider rounds=%d, want at least read + commit + list + final", len(reqs))
	}

	readResult := lastToolContentByID(reqs, "read-1")
	if readResult == "" {
		t.Fatal("read_file result never reached a later provider request")
	}
	if strings.Contains(readResult, "do not answer, modify state") || strings.Contains(readResult, "INCOMPLETE READ") {
		t.Fatalf("read_file result still forces completion:\n%.400s", readResult)
	}
	if !strings.Contains(readResult, "PARTIAL view") && !strings.Contains(readResult, "next_offset=") {
		t.Fatalf("read_file result missing pagination hint:\n%.400s", readResult)
	}

	commitResult := lastToolContentByID(reqs, "commit-1")
	if commitResult == "" {
		t.Fatal("git commit result never reached a later provider request")
	}
	if strings.Contains(commitResult, "unread content retained") ||
		strings.Contains(commitResult, "restricted search/read mode") ||
		strings.Contains(commitResult, "cannot declare which files it changes") {
		t.Fatalf("git commit was blocked by leftover read gate:\n%.400s", commitResult)
	}

	listResult := lastToolContentByID(reqs, "list-1")
	if listResult == "" {
		t.Fatal("use_capability list result never reached a later provider request")
	}
	if !strings.Contains(listResult, "session:tool_result") {
		t.Fatalf("capability list dropped session:tool_result:\n%.400s", listResult)
	}
}

func lastToolContentByID(reqs []provider.Request, callID string) string {
	var found string
	for _, req := range reqs {
		for _, msg := range req.Messages {
			if msg.Role == provider.RoleTool && msg.ToolCallID == callID {
				found = msg.Content
			}
		}
	}
	return found
}
