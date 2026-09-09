package tool

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseReadWindowRequiresContiguousNumberedLines(t *testing.T) {
	cases := []struct {
		name      string
		output    string
		wantOK    bool
		wantStart int
		wantLines []string
	}{
		{name: "contiguous", output: "   1→a\n   2→b\n", wantOK: true, wantStart: 1, wantLines: []string{"a", "b"}},
		{name: "offset window", output: "  10→x\n  11→y\n", wantOK: true, wantStart: 10, wantLines: []string{"x", "y"}},
		{name: "gap fails closed", output: "   1→a\n   5→b\n"},
		{name: "unnumbered", output: "plain text\n"},
		{name: "empty", output: ""},
		{name: "trailer and blank lines ignored", output: "   1→a\n\n[more lines below; pass offset=1 to continue]\n", wantOK: true, wantStart: 1, wantLines: []string{"a"}},
		{name: "arrow inside content", output: "   1→a→b\n", wantOK: true, wantStart: 1, wantLines: []string{"a→b"}},
		{name: "non-numeric prefix skipped", output: "abc→def\n"},
		{name: "zero line number skipped", output: "   0→a\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseReadWindow(tc.output)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (window %+v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.StartLine != tc.wantStart {
				t.Fatalf("StartLine = %d, want %d", got.StartLine, tc.wantStart)
			}
			if len(got.Lines) != len(tc.wantLines) {
				t.Fatalf("Lines = %q, want %q", got.Lines, tc.wantLines)
			}
			for i := range tc.wantLines {
				if got.Lines[i] != tc.wantLines[i] {
					t.Fatalf("line %d = %q, want %q", i, got.Lines[i], tc.wantLines[i])
				}
			}
		})
	}
}

func TestReadWindowRangeIsZeroBasedHalfOpen(t *testing.T) {
	w := ReadWindow{StartLine: 120, Lines: []string{"a", "b"}}
	if got, want := w.Range(), (ReadRange{Start: 119, End: 121}); got != want {
		t.Fatalf("Range() = %+v, want %+v", got, want)
	}
	if !(ReadRange{Start: 5, End: 5}).Empty() {
		t.Fatal("equal bounds must be empty")
	}
	if got := (ReadRange{Start: 3, End: 8}).Lines(); got != 5 {
		t.Fatalf("Lines() = %d, want 5", got)
	}
}

func TestWindowDigestBindsPathAndContent(t *testing.T) {
	base := ReadWindow{StartLine: 1, Lines: []string{"alpha", "beta"}}
	other := ReadWindow{StartLine: 1, Lines: []string{"alpha", "gamma"}}
	shifted := ReadWindow{StartLine: 2, Lines: []string{"alpha", "beta"}}

	token := WindowDigest("/w/a.go", base)
	if token == "" {
		t.Fatal("token must not be empty")
	}
	if again := WindowDigest("/w/a.go", base); again != token {
		t.Fatalf("same content produced different tokens: %q vs %q", token, again)
	}
	if changed := WindowDigest("/w/a.go", other); changed == token {
		t.Fatal("edited line must change the version token")
	}
	if moved := WindowDigest("/w/a.go", shifted); moved == token {
		t.Fatal("shifted window must change the version token")
	}
	if elsewhere := WindowDigest("/w/b.go", base); elsewhere == token {
		t.Fatal("different path must change the version token")
	}
}

func TestReadCursorRoundTripAndMatching(t *testing.T) {
	env := ReadResultEnvelope{
		ReadID:          "ir-1",
		Source:          ReadResultSource{CanonicalPath: "/w/a.go", Snapshot: "ss2:abc"},
		DeliveredRanges: []ReadRange{{Start: 0, End: 40}},
	}
	token := EncodeReadCursor(ReadCursor{Path: "/w/a.go", ReadID: "ir-1", Snapshot: "ss2:abc", NextStart: 40})
	if token == "" {
		t.Fatal("cursor must encode")
	}
	cursor, ok := DecodeReadCursor(token)
	if !ok {
		t.Fatalf("decode failed for %q", token)
	}
	if !cursor.Matches(env) {
		t.Fatal("cursor must match its own envelope")
	}
	if (ReadCursor{Path: "/w/other.go", ReadID: "ir-1", Snapshot: "ss2:abc", NextStart: 40}).Matches(env) {
		t.Fatal("cursor must not match a different path")
	}
	if (ReadCursor{Path: "/w/a.go", ReadID: "ir-1", Snapshot: "ss2:def", NextStart: 40}).Matches(env) {
		t.Fatal("cursor must not match a different content version")
	}
	if (ReadCursor{Path: "/w/a.go", ReadID: "ir-1", Snapshot: "ss2:abc", NextStart: 41}).Matches(env) {
		t.Fatal("cursor must not match a start outside the delivered range")
	}
	for _, bad := range []string{"", "rc2:", "rc2:!!!", "other:abc", "rc2:" + "eyJwYXRoIjoiIn0"} {
		if _, ok := DecodeReadCursor(bad); ok {
			t.Fatalf("decoded malformed cursor %q", bad)
		}
	}
	if EncodeReadCursor(ReadCursor{Path: "", ReadID: "ir-1", Snapshot: "ss2:abc", NextStart: 0}) != "" {
		t.Fatal("cursor without a path must not encode")
	}
}

func TestClipToNarrowsDeliveredRangeToVisibleBytes(t *testing.T) {
	env := ReadResultEnvelope{
		Source:          ReadResultSource{CanonicalPath: "/w/a.go", Snapshot: "ss2:abc"},
		ReadID:          "ir-1",
		DeliveredRanges: []ReadRange{{Start: 0, End: 100}},
		HasMore:         false,
		EOF:             true,
	}
	var visible strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&visible, "  %d→line\n", i)
	}

	clipped := env.ClipTo(visible.String())
	if got, want := clipped.DeliveredRanges, []ReadRange{{Start: 0, End: 40}}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("DeliveredRanges = %+v, want %+v", got, want)
	}
	if clipped.TransportCut != ReadCutToolOutput {
		t.Fatalf("TransportCut = %q, want %q", clipped.TransportCut, ReadCutToolOutput)
	}
	if !clipped.HasMore || clipped.EOF {
		t.Fatalf("a truncated delivery must be resumable: has_more=%v eof=%v", clipped.HasMore, clipped.EOF)
	}
	cursor, ok := DecodeReadCursor(clipped.NextCursor)
	if !ok || cursor.NextStart != 40 {
		t.Fatalf("NextCursor = %q (cursor %+v, ok=%v), want next_start 40", clipped.NextCursor, cursor, ok)
	}
	if !cursor.Matches(clipped) {
		t.Fatal("clipped envelope must accept its own cursor")
	}
	if env.TransportCut != ReadCutNone || !env.EOF {
		t.Fatal("ClipTo must not mutate the receiver")
	}
}

func TestClipToKeepsCompleteDelivery(t *testing.T) {
	env := ReadResultEnvelope{
		Source:          ReadResultSource{CanonicalPath: "/w/a.go", Snapshot: "ss2:abc"},
		ReadID:          "ir-1",
		DeliveredRanges: []ReadRange{{Start: 0, End: 2}},
		EOF:             true,
	}
	clipped := env.ClipTo("  1→a\n  2→b\n")
	if clipped.TransportCut != ReadCutNone || clipped.HasMore {
		t.Fatalf("complete delivery must stay complete: cut=%q has_more=%v", clipped.TransportCut, clipped.HasMore)
	}
	if len(clipped.DeliveredRanges) != 1 || clipped.DeliveredRanges[0] != (ReadRange{Start: 0, End: 2}) {
		t.Fatalf("DeliveredRanges = %+v", clipped.DeliveredRanges)
	}
}

func TestParseReadTrailerRecognizesBothPagingForms(t *testing.T) {
	more := ParseReadTrailer("   1→a\n\n[more lines below; pass offset=7 to continue]\n")
	if !more.HasMore || more.NextOffset != 7 || more.LocalSafety {
		t.Fatalf("more-lines trailer = %+v", more)
	}
	partial := ParseReadTrailer("   1→a\n\n[PARTIAL view: showing lines 1-3 of 50. The file continues; pass offset=3 to read on, or grep for the section you need. A partial read is fine when the visible range is sufficient.]\n")
	if !partial.HasMore || partial.NextOffset != 3 || partial.LocalSafety {
		t.Fatalf("partial-view trailer = %+v", partial)
	}
	safety := ParseReadTrailer("   1→a\n\n[read_file local safety page; next_offset=9 requested_end=20; output capped at 8 MiB for this call]\n")
	if !safety.HasMore || !safety.LocalSafety || safety.NextOffset != 9 || safety.RequestedEnd != 20 {
		t.Fatalf("safety trailer = %+v", safety)
	}
	if absent := ParseReadTrailer("   1→a\n"); absent.HasMore || absent.LocalSafety {
		t.Fatalf("absent trailer = %+v", absent)
	}
}
