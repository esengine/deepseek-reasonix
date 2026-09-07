package cli

import (
	"strings"
	"testing"
)

func TestParseShowBalanceArg(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"all", "all", false},
		{"full", "all", false},
		{"ALL", "all", false},
		{"part", "part", false},
		{"partial", "part", false},
		{"mask", "part", false},
		{"no", "no", false},
		{"none", "no", false},
		{"off", "no", false},
		{"hide", "no", false},
		{"bogus", "", true},
		{"", "", true},
	}
	for _, tc := range tests {
		got, err := parseShowBalanceArg(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseShowBalanceArg(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseShowBalanceArg(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestDescribeShowBalanceModesMarksCurrent(t *testing.T) {
	out := describeShowBalanceModes("part")
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("describeShowBalanceModes lines = %d, want 3", len(lines))
	}
	if !strings.HasPrefix(lines[1], "• ") || !strings.Contains(lines[1], "part") {
		t.Fatalf("current mode not marked: %q", lines[1])
	}
	for i, item := range []string{"all", "part", "no"} {
		if !strings.Contains(lines[i], item) {
			t.Fatalf("line %d = %q, want %q", i, lines[i], item)
		}
	}
}
