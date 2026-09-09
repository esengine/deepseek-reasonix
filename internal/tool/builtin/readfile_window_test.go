package builtin

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestScanWindowedReadDoesNotConsumeWholeFile guards the regression where scan()
// drained the entire file just to count the remaining lines for the pagination
// trailer. A small windowed read of a large file reads only a bounded prefix and
// reports the total as a lower bound.
func TestScanWindowedReadDoesNotConsumeWholeFile(t *testing.T) {
	var buf bytes.Buffer
	for i := 1; i <= 100_000; i++ {
		fmt.Fprintf(&buf, "line %d\n", i)
	}
	total := buf.Len()
	if total < 500*1024 {
		t.Fatalf("test fixture too small (%d bytes) to be meaningful", total)
	}

	cr := &countingReader{r: bytes.NewReader(buf.Bytes())}
	out, err := readFile{}.scan(cr, 0, 3)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if !strings.Contains(out, "1→line 1") || !strings.Contains(out, "3→line 3") {
		t.Fatalf("window content wrong:\n%s", out)
	}
	if strings.Contains(out, "line 4") {
		t.Fatalf("window leaked line 4:\n%s", out)
	}
	if !strings.Contains(out, "PARTIAL view: showing lines 1-3 of ") || !strings.Contains(out, "+. The file continues") {
		t.Fatalf("pagination trailer must report a lower-bound total:\n%s", out)
	}
	if strings.Contains(out, "of 100000.") {
		t.Fatalf("total must not be exact when the look-ahead budget is exceeded:\n%s", out)
	}
	if !strings.Contains(out, "pass offset=3") {
		t.Fatalf("continuation offset missing:\n%s", out)
	}
	if cr.n > 100*1024 {
		t.Fatalf("read %d of %d bytes for a 3-line window; should read only a small prefix", cr.n, total)
	}
}

// TestScanWindowedReadReportsExactTotalForSmallFiles pins the exact total when
// the remainder fits inside the look-ahead budget.
func TestScanWindowedReadReportsExactTotalForSmallFiles(t *testing.T) {
	var buf bytes.Buffer
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&buf, "line %d\n", i)
	}
	out, err := readFile{}.scan(bytes.NewReader(buf.Bytes()), 10, 5)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.Contains(out, "PARTIAL view: showing lines 11-15 of 50.") {
		t.Fatalf("exact total missing:\n%s", out)
	}
}
