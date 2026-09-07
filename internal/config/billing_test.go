package config

import (
	"testing"

	"reasonix/internal/billing"
	"reasonix/internal/provider"
)

func TestDisplayCurrencyIndependentOfListPrices(t *testing.T) {
	c := Default()
	flash, _ := c.Provider("deepseek-flash")
	before := flash.Price.Output
	if err := c.SetDisplayCurrency("CNY"); err != nil {
		t.Fatal(err)
	}
	if flash.Price.Output != before {
		t.Fatalf("list price mutated: %v -> %v", before, flash.Price.Output)
	}
	if got := c.ExplicitDisplayCurrency(); got != "CNY" {
		t.Fatalf("display = %q", got)
	}
	if got := flash.ProviderBillingCurrency(); got != "USD" {
		t.Fatalf("billing currency = %q, want USD", got)
	}
}

func TestCustomPriceProtectedFromCatalog(t *testing.T) {
	custom := billing.RateCard{CacheHit: 9, Input: 9, Output: 9, Currency: "USD"}
	if _, ok := billing.MatchesCatalog("deepseek", "deepseek-v4-flash", custom); ok {
		t.Fatal("custom price must not match official catalog")
	}
}

func TestQuoteForUsageUsesSelectedDisplay(t *testing.T) {
	price := deepSeekV4FlashPriceUSD()
	q := QuoteForUsage(price, nil, "USD", "m", "executor", billing.BillingModePAYG, "")
	if q.Complete {
		t.Fatal("nil usage should be incomplete")
	}
	u := &provider.Usage{PromptTokens: 1_000_000, CompletionTokens: 0}
	q = QuoteForUsage(price, u, "USD", "m", "executor", billing.BillingModePAYG, "")
	if q.Original.Currency != "USD" || q.Selected == nil {
		t.Fatalf("quote = %+v", q)
	}
}

func TestShowBalanceDefaultsToFullAmount(t *testing.T) {
	if got := (*Config)(nil).ShowBalanceMode(); got != billing.DisplayAll {
		t.Fatalf("nil config mode = %q, want all", got)
	}
	c := Default()
	if got := c.ShowBalanceMode(); got != billing.DisplayAll {
		t.Fatalf("unset show_balance mode = %q, want all", got)
	}
	if c.Billing.ShowBalance != "" {
		t.Fatalf("default config must not pin show_balance, got %q", c.Billing.ShowBalance)
	}
}

func TestShowBalanceModeRoundTrip(t *testing.T) {
	c := Default()
	for _, tc := range []struct {
		in   string
		want billing.BalanceDisplayMode
	}{
		{"all", billing.DisplayAll},
		{"part", billing.DisplayPart},
		{"no", billing.DisplayNo},
		{"", billing.DisplayAll},
	} {
		if err := c.SetShowBalanceMode(tc.in); err != nil {
			t.Fatalf("SetShowBalanceMode(%q): %v", tc.in, err)
		}
		if got := c.ShowBalanceMode(); got != tc.want {
			t.Fatalf("mode after %q = %q, want %q", tc.in, got, tc.want)
		}
	}
	if err := c.SetShowBalanceMode("bogus"); err == nil {
		t.Fatal("SetShowBalanceMode(bogus) must fail")
	}
}

func TestShowBalanceModeAliases(t *testing.T) {
	c := Default()
	if err := c.SetShowBalanceMode("masked"); err != nil {
		t.Fatal(err)
	}
	if c.Billing.ShowBalance != "part" {
		t.Fatalf("alias masked must pin %q, got %q", "part", c.Billing.ShowBalance)
	}
	if got := c.ShowBalanceMode(); got != billing.DisplayPart {
		t.Fatalf("mode = %q, want part", got)
	}
	if err := c.SetShowBalanceMode("hide"); err != nil {
		t.Fatal(err)
	}
	if got := c.ShowBalanceMode(); got != billing.DisplayNo {
		t.Fatalf("mode = %q, want no", got)
	}
}
