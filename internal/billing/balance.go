// Package billing queries a provider's wallet balance for the status line. The
// only documented shape today is DeepSeek's GET /user/balance, so Fetch speaks
// that schema. Balance is strictly optional: a provider with no balance_url is
// never queried — callers pass "" and get (nil, nil) back, and surfaces simply
// omit the readout. Kept tiny and dependency-free (net/http + encoding/json) so
// every frontend can share one fetch.
package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Balance is a wallet balance normalized for display.
type Balance struct {
	Available bool   // the provider reports the account can still serve API calls
	Infos     []Info // one entry per currency the provider returns
}

// Currencies returns the distinct ISO currency codes reported by the wallet,
// in stable order. It never converts or combines balances.
func (b *Balance) Currencies() []string {
	if b == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, info := range b.Infos {
		cur := strings.ToUpper(strings.TrimSpace(info.Currency))
		if normalized := normalizeCurrency(cur); normalized != "" {
			cur = normalized
		}
		if cur != "" {
			seen[cur] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for cur := range seen {
		result = append(result, cur)
	}
	sort.Strings(result)
	return result
}

// PrimaryCurrency returns the sole usable wallet currency, if there is one.
func (b *Balance) PrimaryCurrency() string {
	currencies := b.Currencies()
	if len(currencies) != 1 {
		return ""
	}
	if currencies[0] != "CNY" && currencies[0] != "USD" {
		return ""
	}
	return currencies[0]
}

// MultiCurrency reports whether more than one wallet currency is present.
func (b *Balance) MultiCurrency() bool { return len(b.Currencies()) > 1 }

// Info is one currency's balance (DeepSeek returns one per currency).
type Info struct {
	Currency        string // "CNY" | "USD"
	TotalBalance    string // total available (granted + topped-up)
	GrantedBalance  string // unexpired promotional credit
	ToppedUpBalance string // paid-in credit
}

// deepseekResp mirrors the GET /user/balance response shape.
type deepseekResp struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

// httpClient bounds the balance query so a slow endpoint can't hang the status
// line; the per-call ctx still cancels it on shutdown.
var httpClient = &http.Client{Timeout: 12 * time.Second}

// Fetch queries url (a DeepSeek-style balance endpoint) with a Bearer apiKey and
// returns the normalized balance. An empty url yields (nil, nil) — "not
// configured", not an error — so callers can treat both the same and just omit
// the readout.
func Fetch(ctx context.Context, url, apiKey string) (*Balance, error) {
	return FetchWithClient(ctx, httpClient, url, apiKey)
}

// FetchWithClient queries the balance endpoint using the caller-provided client.
// A nil client falls back to the package default.
func FetchWithClient(ctx context.Context, client *http.Client, url, apiKey string) (*Balance, error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	if client == nil {
		client = httpClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("balance: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var dr deepseekResp
	if err := json.Unmarshal(body, &dr); err != nil {
		return nil, fmt.Errorf("balance: decode: %w", err)
	}
	b := &Balance{Available: dr.IsAvailable}
	for _, bi := range dr.BalanceInfos {
		b.Infos = append(b.Infos, Info{
			Currency:        bi.Currency,
			TotalBalance:    bi.TotalBalance,
			GrantedBalance:  bi.GrantedBalance,
			ToppedUpBalance: bi.ToppedUpBalance,
		})
	}
	return b, nil
}

// symbol maps an ISO currency code to a compact symbol; an unknown code passes
// through with a trailing space ("XYZ 12.00").
func symbol(currency string) string {
	switch strings.ToUpper(currency) {
	case "CNY", "RMB":
		return "¥"
	case "USD":
		return "$"
	default:
		if currency == "" {
			return ""
		}
		return currency + " "
	}
}

// BalanceDisplayMode selects how a wallet balance is rendered on a status bar.
type BalanceDisplayMode string

const (
	// DisplayAll renders the full amount (e.g. "¥14123.19").
	DisplayAll BalanceDisplayMode = "all"
	// DisplayPart masks digits above the hundreds place (e.g. "¥*123.19").
	DisplayPart BalanceDisplayMode = "part"
	// DisplayNo hides the amount entirely ("***").
	DisplayNo BalanceDisplayMode = "no"
)

// Display renders the primary balance compactly, e.g. "¥110.00". It preserves
// the legacy CNY-first behavior for callers that have no display-currency
// preference.
func (b *Balance) Display() string {
	return b.DisplayForCurrency("")
}

// DisplayMasked renders the balance like Display but hides every digit above
// the hundreds place behind a leading "*" (see MaskAboveHundreds), so a status
// bar never exposes the full wallet amount.
func (b *Balance) DisplayMasked() string {
	return b.DisplayMaskedForCurrency("")
}

// DisplayForMode renders the balance according to the given display mode:
// DisplayAll behaves like DisplayForCurrency, DisplayPart like
// DisplayMaskedForCurrency, and DisplayNo returns "***" whenever a balance is
// present (an absent balance still renders as "").
func (b *Balance) DisplayForMode(currency string, mode BalanceDisplayMode) string {
	if mode == DisplayNo {
		if b == nil || len(b.Infos) == 0 {
			return ""
		}
		return "***"
	}
	return b.displayForCurrency(currency, mode == DisplayPart)
}

// DisplayForCurrency renders the balance matching the requested pricing
// currency. When the provider does not return that currency, it falls back to
// Display's legacy CNY-first selection and prefixes the provider's real ISO
// currency (for example "CNY ¥70.16"); it never performs an implicit
// exchange-rate conversion.
func (b *Balance) DisplayForCurrency(currency string) string {
	return b.displayForCurrency(currency, false)
}

// DisplayMaskedForCurrency is DisplayForCurrency with the high digits masked.
func (b *Balance) DisplayMaskedForCurrency(currency string) string {
	return b.displayForCurrency(currency, true)
}

// displayForCurrency is the shared selection/render path behind Display and
// DisplayMasked variants. masked enables MaskAboveHundreds on the amount.
func (b *Balance) displayForCurrency(currency string, masked bool) string {
	if b == nil || len(b.Infos) == 0 {
		return ""
	}
	render := func(i Info) string {
		amount := strings.TrimSpace(i.TotalBalance)
		if masked {
			amount = MaskAboveHundreds(amount)
		}
		return symbol(i.Currency) + amount
	}
	pick := b.Infos[0]
	preferred := normalizeCurrency(currency)
	if preferred != "" {
		for _, i := range b.Infos {
			if normalizeCurrency(i.Currency) == preferred {
				return render(i)
			}
		}
	}
	for _, i := range b.Infos {
		if normalizeCurrency(i.Currency) == "CNY" {
			pick = i
			break
		}
	}
	display := render(pick)
	actual := strings.ToUpper(strings.TrimSpace(pick.Currency))
	if normalized := normalizeCurrency(actual); normalized != "" {
		actual = normalized
	}
	if preferred != "" && actual != "" && actual != preferred {
		return actual + " " + display
	}
	return display
}

// MaskAboveHundreds hides every digit above the hundreds place behind a single
// leading "*": "14123.19" becomes "*123.19". The hundreds place always keeps
// its full three-digit width, zero-padded ("10000.82" → "*000.82"). Balances
// of at most 999 pass through unchanged ("745.85"), as does anything that is
// not a plain decimal amount, so unparseable provider values stay exactly as
// received. The original decimal width is preserved, and a leading sign is
// kept ("-14123.19" → "-*123.19").
func MaskAboveHundreds(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return raw
	}
	sign := ""
	switch s[0] {
	case '-', '+':
		sign, s = s[:1], s[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if !isDecimalDigits(intPart) || (hasFrac && !isDecimalDigits(fracPart)) {
		return raw
	}
	n, ok := new(big.Int).SetString(intPart, 10)
	if !ok || n.Cmp(big.NewInt(999)) <= 0 {
		return raw
	}
	masked := new(big.Int).Mod(n, big.NewInt(1000))
	out := fmt.Sprintf("*%03d", masked.Int64())
	if hasFrac {
		out += "." + fracPart
	}
	return sign + out
}

// isDecimalDigits reports whether s is non-empty and ASCII digits only.
func isDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func normalizeCurrency(currency string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "CNY", "RMB", "CNH", "¥", "￥":
		return "CNY"
	case "USD", "$", "US$":
		return "USD"
	default:
		return ""
	}
}
