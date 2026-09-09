package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestSnapshotLargeSessionAndNestedContent(t *testing.T) {
	body := strings.Repeat("正文", 12<<20) // 72 MiB: larger than the unpinned cut budget.
	baseline := []Message{{RecordID: "m:u", MessageID: "u", Role: "user", Content: "question"},
		{RecordID: "m:a", MessageID: "a", Role: "assistant", Content: "answer", ToolCalls: []ToolCall{{ID: "call", Name: "write_file", Arguments: body}},
			MemoryCitations: []provider.MemoryCitation{{ID: "citation", Note: strings.Repeat("note", 20000)}}}}
	projection, err := NewProjection(Identity{SessionID: "s", RuntimeEpoch: "epoch"}, baseline, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := projection.Snapshot(PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > 32<<10 {
		t.Fatalf("bounded response bytes=%d error=%v", len(encoded), err)
	}
	var arguments *ContentRef
	for _, ref := range snapshot.Records[1].Refs {
		if strings.Join(ref.Path, "/") == "toolCalls/0/arguments" {
			arguments = &ref
		}
		chunk, err := projection.Content(ContentRequest{ContentRef: ref})
		if err != nil || len(chunk.Data) > contentChunkBytes || chunk.NextOffset != len(chunk.Data) {
			t.Fatalf("nested ref %v: %+v %v", ref.Path, chunk, err)
		}
	}
	if arguments == nil || arguments.Bytes != len(body) {
		t.Fatalf("missing exact argument ref: %+v", snapshot.Records[1].Refs)
	}
	end := len(body) - 6
	chunk, err := projection.Content(ContentRequest{ContentRef: *arguments, Offset: end})
	if err != nil || !chunk.Done || chunk.Data != body[end:] || chunk.NextOffset != len(body) {
		t.Fatalf("tail chunk %+v %v", chunk, err)
	}
	if len(projection.snapshots) != 1 {
		t.Fatalf("large current cut must remain available: %d", len(projection.snapshots))
	}
}

func BenchmarkSnapshotLargeContentChunk(b *testing.B) {
	body := strings.Repeat("x", 8<<20)
	projection, err := NewProjection(Identity{SessionID: "s"}, []Message{{RecordID: "m:a", Role: "assistant", MessageID: "a", Content: body}}, 0)
	if err != nil {
		b.Fatal(err)
	}
	snapshot, err := projection.Snapshot(PageRequest{})
	if err != nil {
		b.Fatal(err)
	}
	request := ContentRequest{ContentRef: snapshot.Records[0].Refs[0], Offset: 1234}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		chunk, err := projection.Content(request)
		if err != nil || len(chunk.Data) != contentChunkBytes {
			b.Fatal(err)
		}
	}
}
