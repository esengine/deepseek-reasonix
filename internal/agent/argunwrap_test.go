package agent

import (
	"encoding/json"
	"testing"
)

func TestJSONStringOfObject(t *testing.T) {
	cases := []struct {
		raw  string
		want string // "" means not unwrapped
	}{
		{`"{\"name\":\"explore\"}"`, `{"name":"explore"}`},
		{`" {\"task\":\"x\"} "`, `{"task":"x"}`},
		{`"plain text"`, ""},
		{`""`, ""},
		{`{"name":"explore"}`, ""},
		{`42`, ""},
		{`null`, ""},
	}
	for _, tc := range cases {
		got, ok := jsonStringOfObject(json.RawMessage(tc.raw))
		if tc.want == "" {
			if ok {
				t.Fatalf("jsonStringOfObject(%q) unwrapped to %q, want no unwrap", tc.raw, got)
			}
			continue
		}
		if !ok || string(got) != tc.want {
			t.Fatalf("jsonStringOfObject(%q) = (%q, %v), want %q", tc.raw, got, ok, tc.want)
		}
	}
}
