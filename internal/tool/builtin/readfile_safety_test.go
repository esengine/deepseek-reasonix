package builtin

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func numberedReadLines(t *testing.T, output string) map[int]string {
	t.Helper()
	lines := make(map[int]string)
	for line := range strings.SplitSeq(output, "\n") {
		before, after, ok := strings.Cut(line, "→")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(before))
		if err != nil {
			t.Fatalf("parse numbered line %q: %v", line, err)
		}
		lines[n] = after
	}
	return lines
}

func TestReadFileLocalSafetyPagesExplicitWindowWithoutGaps(t *testing.T) {
	const totalLines = 2000
	var source strings.Builder
	for line := 1; line <= totalLines; line++ {
		fmt.Fprintf(&source, "line-%04d-%s\r\n", line, strings.Repeat(string(rune('a'+line%26)), 5000))
	}
	first, err := (readFile{}).scan(strings.NewReader(source.String()), 0, totalLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) > readFileMaxFormattedBytes {
		t.Fatalf("first page bytes=%d, limit=%d", len(first), readFileMaxFormattedBytes)
	}
	const prefix = "[read_file local safety page; next_offset="
	start := strings.LastIndex(first, prefix)
	if start < 0 {
		t.Fatal("first page did not include the local safety trailer")
	}
	rest := strings.TrimSuffix(first[start+len(prefix):], "]\n")
	fields := strings.Fields(rest)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "requested_end=") {
		t.Fatalf("safety trailer fields=%q", fields)
	}
	next, err := strconv.Atoi(fields[0])
	if err != nil || next <= 0 || next >= totalLines {
		t.Fatalf("next_offset=%d err=%v", next, err)
	}
	requestedEnd, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(fields[1], "requested_end="), ";"))
	if err != nil || requestedEnd != totalLines {
		t.Fatalf("requested_end=%d err=%v", requestedEnd, err)
	}
	if !strings.Contains(rest, "output capped at 8 MiB") {
		t.Fatalf("safety trailer missing 8 MiB notice: %q", rest)
	}
	second, err := (readFile{}).scan(strings.NewReader(source.String()), next, requestedEnd-next)
	if err != nil {
		t.Fatal(err)
	}
	combined := numberedReadLines(t, first)
	for line, text := range numberedReadLines(t, second) {
		if _, duplicate := combined[line]; duplicate {
			t.Fatalf("duplicate line %d", line)
		}
		combined[line] = text
	}
	if len(combined) != totalLines {
		t.Fatalf("reconstructed lines=%d, want %d", len(combined), totalLines)
	}
	for line := 1; line <= totalLines; line++ {
		if !strings.HasPrefix(combined[line], fmt.Sprintf("line-%04d-", line)) || strings.Contains(combined[line], "\r") {
			t.Fatalf("line %d was missing, reordered, or retained CR: %q", line, combined[line])
		}
	}
}

func TestReadFileRejectsLineAboveLocalSafetyLimit(t *testing.T) {
	_, err := (readFile{}).scan(strings.NewReader(strings.Repeat("x", readFileMaxLineBytes+1)+"\n"), 0, 1)
	if err == nil || !strings.Contains(err.Error(), "1 MiB local safety limit") {
		t.Fatalf("oversized line error=%v", err)
	}
}
