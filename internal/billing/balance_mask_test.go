package billing

import "testing"

func TestMaskAboveHundreds(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Issue examples.
		{"14123.19", "*123.19"},
		{"745.85", "745.85"},
		// Boundary at the hundreds place.
		{"999", "999"},
		{"999.99", "999.99"},
		{"1000", "*000"},
		{"1000.00", "*000.00"},
		{"1001", "*001"},
		// Hundreds place keeps its full three-digit width, zero-padded.
		{"10000.82", "*000.82"},
		{"14100.00", "*100.00"},
		{"1234567.89", "*567.89"},
		// Small and zero balances pass through.
		{"0", "0"},
		{"0.00", "0.00"},
		{"12.34", "12.34"},
		// Sign is kept ahead of the mask.
		{"-14123.19", "-*123.19"},
		{"+14123.19", "+*123.19"},
		{"-745.85", "-745.85"},
		// Unparseable amounts are never altered.
		{"", ""},
		{"abc", "abc"},
		{"1,234.56", "1,234.56"},
		{"12.3.4", "12.3.4"},
		{"  14123.19 ", "*123.19"}, // surrounding space trimmed
		// Integers larger than int64 still mask correctly.
		{"1234567890123456789012345678901234567890.50", "*890.50"},
	}
	for _, tc := range tests {
		if got := MaskAboveHundreds(tc.in); got != tc.want {
			t.Errorf("MaskAboveHundreds(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBalanceDisplayForMode(t *testing.T) {
	b := &Balance{Infos: []Info{{Currency: "CNY", TotalBalance: "14123.19"}}}
	cases := []struct {
		mode BalanceDisplayMode
		want string
	}{
		{DisplayAll, "¥14123.19"},
		{DisplayPart, "¥*123.19"},
		{DisplayNo, "***"},
		{BalanceDisplayMode("bogus"), "¥14123.19"}, // unknown falls back to all
	}
	for _, tc := range cases {
		if got := b.DisplayForMode("", tc.mode); got != tc.want {
			t.Errorf("DisplayForMode(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
	if got := (*Balance)(nil).DisplayForMode("", DisplayNo); got != "" {
		t.Errorf("nil DisplayForMode(no) = %q, want empty", got)
	}
	if got := (&Balance{}).DisplayForMode("", DisplayNo); got != "" {
		t.Errorf("empty DisplayForMode(no) = %q, want empty", got)
	}
}

func TestBalanceDisplayMasked(t *testing.T) {
	b := &Balance{Available: true, Infos: []Info{
		{Currency: "CNY", TotalBalance: "14123.19"},
		{Currency: "USD", TotalBalance: "9.82"},
	}}
	if got := b.Display(); got != "¥14123.19" {
		t.Fatalf("Display() = %q, want unmasked ¥14123.19", got)
	}
	if got := b.DisplayMasked(); got != "¥*123.19" {
		t.Fatalf("DisplayMasked() = %q, want ¥*123.19", got)
	}
	if got := b.DisplayMaskedForCurrency("USD"); got != "$9.82" {
		t.Fatalf("DisplayMaskedForCurrency(USD) = %q, want $9.82 (≤999 stays visible)", got)
	}
}

func TestBalanceDisplayMaskedFallbackKeepsCurrencyPrefix(t *testing.T) {
	b := &Balance{Infos: []Info{{Currency: "CNY", TotalBalance: "14123.19"}}}
	if got := b.DisplayMaskedForCurrency("USD"); got != "CNY ¥*123.19" {
		t.Fatalf("fallback masked display = %q, want CNY ¥*123.19", got)
	}
	if got := (*Balance)(nil).DisplayMasked(); got != "" {
		t.Fatalf("nil DisplayMasked() = %q, want empty", got)
	}
	if got := (&Balance{}).DisplayMasked(); got != "" {
		t.Fatalf("empty DisplayMasked() = %q, want empty", got)
	}
}
