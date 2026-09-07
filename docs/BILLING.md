# Billing, display currency, and cost quotes

Reasonix keeps three facts separate:

1. `original`: an estimate from the selected public/custom rate card in its
   pricing-table currency. It is not an invoice or a provider debit.
2. `valuations`: occurrence-time `identity` and, when available, an
   `official_table` estimate for the same model in the other official region.
3. Wallet balances: the exact original-currency values returned by a provider.

Reasonix has no runtime FX download, cache, refresh goroutine, or wallet
conversion. Old `fx`/`rateSnapshot` fields remain readable for history only;
new quotes never generate them.

```toml
[billing]
display_currency = "auto"   # auto | CNY | USD
show_balance = "all"        # all | part | no — status-bar wallet balance display

[[providers]]
billing_currency = "USD"    # pricing-table basis, not settlement currency
billing_mode = "payg"       # payg | subscription_equivalent
```

Legacy `[desktop].currency` remains readable and migrates to
`[billing].display_currency`. `auto` is intentionally unresolved in config:
one valid wallet currency may become a tab/session hint; otherwise a single
original currency is selected or mixed currencies are shown as buckets. A
language, browser locale, or host region never changes a rate card.

`[billing].show_balance` selects how the CLI and desktop status bars render
the wallet balance: `all` (default) shows the full amount, `part` masks digits
above the hundreds place behind a leading `*`, and `no` replaces the amount
with `***`. The desktop hover detail and the serve API always keep the full
amount.

## CostQuote

`usage.costQuote` is the canonical host-side usage payload:

| Field | Meaning |
| --- | --- |
| `original` | Original-currency rate-card estimate |
| `originalTotals[]` | ISO-sorted original buckets for mixed aggregates |
| `valuations.*.basis` | `identity` or `official_table` for new quotes |
| `selected` | A single amount only when a display total exists |
| `costComplete` | Usage and pricing facts are complete |
| `displayComplete` | A requested single-currency total exists |
| `complete` | Compatibility alias mirroring `displayComplete` |
| `displayStatus` | `matched`, `fallback_original`, `bucketed`, or `unavailable` |
| `aggregateMode` | `single_currency`, `common_valuation`, or `currency_buckets` |
| `rateBand` | DeepSeek occurrence-time band: `peak`, `off_peak`, or aggregate `mixed` |
| `ratedAt` | UTC request-completion time used to select a scheduled rate |

If a requested currency is unavailable but every original is the same, the
original amount is shown with `fallback_original`. Mixed originals produce
`originalTotals` and no scalar zero. `—` is reserved for missing usage or
pricing (`unavailable`). Legacy scalar aliases (`cost`, `costUsd`,
`total_cost`) are written only when `selected` exists.

## DeepSeek scheduled rates

For official DeepSeek OpenAI, Responses, and Anthropic endpoints, V4 Flash,
`deepseek-v4-flash-vision-exp` (same list price as Flash), and V4 Pro use
occurrence-time pricing from 2026-08-17 00:00 Beijing time. Peak windows are
09:00–12:00 and 14:00–18:00 Beijing time; boundaries are left-closed/right-open
and all other times are off-peak. The request-completion timestamp is used
because the provider does not report per-token billing time. Images sent to the
vision SKU are billed as input tokens from provider usage.

The stored provider price remains the peak anchor. Dynamic resolution is
enabled only for PAYG configurations whose complete rate card exactly matches
that official anchor. Custom endpoints, edited prices, and unrecognized models
remain static. Persisted session, ledger, and stats quotes are never repriced.

## Wallets and diagnostics

Wallets are never converted or cross-added. An explicit target uses the exact
matching wallet; if it is absent, the real currency is shown with an ISO
prefix. Automatic mode uses a single valid wallet currency only as a runtime
hint. Multiple/unknown/error responses do not affect the cost facts.

```sh
reasonix doctor billing
reasonix doctor billing --json
```

The compatible `fx` report is always `enabled=false` and has no cache. The
report also lists the automatic selection policy, pricing-table currencies,
and official-catalog matches.
