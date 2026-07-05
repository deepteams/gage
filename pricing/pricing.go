// Package pricing provides model-keyed pricing tables for estimating the USD
// cost of gage.Usage values.
//
// A Table maps model identifiers (or model-family prefixes) to gage.Pricing.
// Find resolves openrouter-style provider-prefixed ids
// ("anthropic/claude-sonnet-4-5") and dated variants
// ("claude-sonnet-4-5-20250929") onto family keys, so a table only needs one
// entry per family.
//
// The Default table is a dated snapshot of public list prices. Rates drift;
// override it before relying on it for billing.
package pricing

import (
	"strings"

	"github.com/deepteams/gage"
)

// Table maps a model id or model-family prefix to its pricing. Keys are
// matched case-insensitively by Find.
type Table map[string]gage.Pricing

// Find resolves model to a pricing entry:
//
//  1. exact match on model;
//  2. exact match with any provider prefix up to the first "/" stripped
//     (openrouter-style ids like "anthropic/claude-sonnet-4-5");
//  3. longest table key that is a prefix of either form (so a family key
//     like "claude-sonnet-4-5" matches "claude-sonnet-4-5-20250929").
//
// All comparisons are case-insensitive.
func (t Table) Find(model string) (gage.Pricing, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" || len(t) == 0 {
		return gage.Pricing{}, false
	}
	if p, ok := t.exact(m); ok {
		return p, true
	}
	stripped := m
	if i := strings.IndexByte(m, '/'); i >= 0 {
		stripped = m[i+1:]
		if p, ok := t.exact(stripped); ok {
			return p, true
		}
	}
	best := -1
	var bestPricing gage.Pricing
	for k, p := range t {
		lk := strings.ToLower(k)
		if lk == "" {
			continue
		}
		if !strings.HasPrefix(m, lk) && !strings.HasPrefix(stripped, lk) {
			continue
		}
		if len(lk) > best {
			best = len(lk)
			bestPricing = p
		}
	}
	if best >= 0 {
		return bestPricing, true
	}
	return gage.Pricing{}, false
}

// exact looks up m (already lower-cased) case-insensitively.
func (t Table) exact(m string) (gage.Pricing, bool) {
	if p, ok := t[m]; ok {
		return p, true
	}
	for k, p := range t {
		if strings.EqualFold(k, m) {
			return p, true
		}
	}
	return gage.Pricing{}, false
}

// Cost prices u for model against the Default table. The boolean reports
// whether the model was found; when false the cost is 0.
func Cost(model string, u gage.Usage) (float64, bool) {
	p, ok := Default.Find(model)
	if !ok {
		return 0, false
	}
	return p.Cost(u), true
}

// anthropicRates builds an Anthropic entry: cache reads bill at 0.1x the
// input rate and cache writes (5-minute TTL) at 1.25x.
func anthropicRates(in, out float64) gage.Pricing {
	return gage.Pricing{
		InputPerMTok:      in,
		OutputPerMTok:     out,
		CacheReadPerMTok:  in * 0.10,
		CacheWritePerMTok: in * 1.25,
	}
}

// Default is a snapshot of public list prices.
//
// USD per MTok, snapshot 2026-07; rates drift — override before relying on
// this for billing.
//
// Keys are family prefixes so Find's prefix matching also covers dated
// variants (e.g. "claude-sonnet-4-5" matches "claude-sonnet-4-5-20250929").
// Only models with well-known public list prices are included; anything
// uncertain is omitted rather than guessed. OpenAI and DeepSeek charge no
// cache-write premium (CacheWritePerMTok is 0); Google's context caching
// adds storage-hour fees that do not fit a flat per-MTok rate, so the Gemini
// cache fields are left zero.
var Default = Table{
	// Anthropic — cache read 0.1x input, cache write 1.25x input.
	"claude-opus-4":   anthropicRates(15, 75), // claude-opus-4-0 / claude-opus-4-20250514
	"claude-opus-4-1": anthropicRates(15, 75),
	// Opus 4.5+ dropped to 5/25; explicit keys keep the "claude-opus-4"
	// prefix from pricing them at the older 4/4.1 rate.
	"claude-opus-4-5":   anthropicRates(5, 25),
	"claude-opus-4-6":   anthropicRates(5, 25),
	"claude-opus-4-7":   anthropicRates(5, 25),
	"claude-opus-4-8":   anthropicRates(5, 25),
	"claude-sonnet-4":   anthropicRates(3, 15), // claude-sonnet-4-0 / claude-sonnet-4-20250514
	"claude-sonnet-4-5": anthropicRates(3, 15),
	"claude-haiku-3-5":  anthropicRates(0.80, 4),
	"claude-3-5-haiku":  anthropicRates(0.80, 4), // the API id orders the version first
	"claude-haiku-4-5":  anthropicRates(1, 5),

	// OpenAI — cached input is billed at a discounted read rate; no write premium.
	"gpt-4o":       {InputPerMTok: 2.50, OutputPerMTok: 10, CacheReadPerMTok: 1.25},
	"gpt-4o-mini":  {InputPerMTok: 0.15, OutputPerMTok: 0.60, CacheReadPerMTok: 0.075},
	"gpt-4.1":      {InputPerMTok: 2, OutputPerMTok: 8, CacheReadPerMTok: 0.50},
	"gpt-4.1-mini": {InputPerMTok: 0.40, OutputPerMTok: 1.60, CacheReadPerMTok: 0.10},
	"gpt-4.1-nano": {InputPerMTok: 0.10, OutputPerMTok: 0.40, CacheReadPerMTok: 0.025},
	"o3":           {InputPerMTok: 2, OutputPerMTok: 8, CacheReadPerMTok: 0.50},
	// Explicit o3-variant keys keep the "o3" prefix from mispricing them.
	"o3-mini": {InputPerMTok: 1.10, OutputPerMTok: 4.40, CacheReadPerMTok: 0.55},
	"o3-pro":  {InputPerMTok: 20, OutputPerMTok: 80},
	"o4-mini": {InputPerMTok: 1.10, OutputPerMTok: 4.40, CacheReadPerMTok: 0.275},

	// Google — standard-context (≤200K prompt) list rates; cache fields
	// omitted (storage-based, see the note above).
	"gemini-2.5-pro":   {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gemini-2.5-flash": {InputPerMTok: 0.30, OutputPerMTok: 2.50},

	// DeepSeek — cache-hit input bills at the discounted read rate; no write premium.
	"deepseek-chat": {InputPerMTok: 0.28, OutputPerMTok: 0.42, CacheReadPerMTok: 0.028},
}
