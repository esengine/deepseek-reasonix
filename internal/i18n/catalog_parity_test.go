package i18n

import (
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// TestCatalogsAgreeOnCodeTokens guards against a specific drift class: a
// translated message dropping (or inventing) a "code token" that its English
// counterpart carries. Code tokens are the machine-readable bits a user must
// type or press, so they must survive translation verbatim:
//
//   - `backtick-quoted spans` — commands the user is told to run
//   - leading-slash command tokens — /compact, /language, /init, ...
//   - key hints — PgUp/PgDn/Ctrl+Home/End, Shift+Tab, Ctrl+C/Y/D, Esc, arrows
//
// Example regression this test exists for: zh-TW ChatStatusPlanApproval read
// "PgUp/PgDn 捲動" while en/zh also carry Ctrl+Home/End. Because the check is
// a pure set comparison per key, re-introducing that drift deterministically
// fails the test (en has {Ctrl+Home, End} tokens the translation lacks).
//
// The test enumerates fields of the baseline catalogue (English) — the same
// Messages type all three catalogues use — exactly like TestCatalogsComplete,
// so newly added keys are covered automatically. Fields that are deliberately
// out of scope are skipped via the explicit list below.
func TestCatalogsAgreeOnCodeTokens(t *testing.T) {
	// UsageBody and ReportUsageBody are multi-line free-form translated prose
	// (help text), not code tokens. All other fields are checked automatically.
	excluded := map[string]bool{
		"UsageBody":       true,
		"ReportUsageBody": true,
	}

	en := reflect.ValueOf(English)
	typ := en.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if excluded[name] {
			continue
		}
		want := extractCodeTokens(en.Field(i).String())
		for _, cat := range []struct {
			tag string
			v   reflect.Value
		}{
			{"zh", reflect.ValueOf(Chinese)},
			{"zh-TW", reflect.ValueOf(ChineseTraditional)},
			{"es", reflect.ValueOf(Spanish)},
		} {
			got := extractCodeTokens(cat.v.Field(i).String())
			if !equalTokenSets(want, got) {
				t.Errorf("%s (%s): code tokens differ from en\n  en:    %v\n  %-5s: %v",
					name, cat.tag, sortedTokens(want), cat.tag, sortedTokens(got))
			}
		}
	}
}

// token extraction

var (
	reBacktick = regexp.MustCompile("`([^`]+)`")
	// A slash token is a leading-slash word (/init, /resume <n>). Requiring a
	// non-word boundary before it keeps enumerations such as "y/a/p/n",
	// "drag-select/scrollbar" or "auth/quota" from being misread as commands.
	reSlashCmd = regexp.MustCompile("(?:^|[ \\t\\n\\r(（)·\\[：“\"'`,，;；:：、])(/[A-Za-z][A-Za-z0-9_-]*)")
	reKeyToken = regexp.MustCompile(`\b(?:PgUp|PgDn|Home|End|Esc|Shift\+Tab|Ctrl[-+][A-Za-z]+)\b`)
	reArrow    = regexp.MustCompile(`[↑↓←→]`)
	// Localizable filler inside a backtick span is normalized away before
	// comparison so translations may localize examples:
	//   `reasonix run "your task"`   vs   `reasonix run "你的任務"`
	//   `reasonix remote add <name>` vs   `reasonix remote add <名稱>`
	reSpanQuoted = regexp.MustCompile(`"[^"]*"`)
	reSpanAngle  = regexp.MustCompile(`<[^>]*>`)
)

func extractCodeTokens(s string) map[string]bool {
	toks := make(map[string]bool)
	for _, m := range reBacktick.FindAllStringSubmatch(s, -1) {
		span := reSpanQuoted.ReplaceAllString(m[1], `"x"`)
		span = reSpanAngle.ReplaceAllString(span, "<x>")
		toks["`"+span+"`"] = true
	}
	for _, m := range reSlashCmd.FindAllStringSubmatch(s, -1) {
		toks[m[1]] = true
	}
	for _, m := range reKeyToken.FindAllString(s, -1) {
		toks[m] = true
	}
	for _, m := range reArrow.FindAllString(s, -1) {
		toks[m] = true
	}
	return toks
}

func equalTokenSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortedTokens(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
