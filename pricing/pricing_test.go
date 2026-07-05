package pricing

import (
	"math"
	"testing"

	"github.com/deepteams/gage"
)

func approx(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindExactBeatsPrefix(t *testing.T) {
	tbl := Table{
		"claude-opus-4":   {InputPerMTok: 15, OutputPerMTok: 75},
		"claude-opus-4-5": {InputPerMTok: 5, OutputPerMTok: 25},
	}
	p, ok := tbl.Find("claude-opus-4-5")
	if !ok {
		t.Fatal("expected a match")
	}
	approx(t, p.InputPerMTok, 5) // exact match, not the shorter prefix

	p, ok = tbl.Find("claude-opus-4")
	if !ok {
		t.Fatal("expected a match")
	}
	approx(t, p.InputPerMTok, 15)
}

func TestFindLongestPrefixWins(t *testing.T) {
	tbl := Table{
		"claude-opus-4":   {InputPerMTok: 15, OutputPerMTok: 75},
		"claude-opus-4-5": {InputPerMTok: 5, OutputPerMTok: 25},
	}
	// Dated variant: no exact key, longest prefix must win.
	p, ok := tbl.Find("claude-opus-4-5-20251101")
	if !ok {
		t.Fatal("expected a match")
	}
	approx(t, p.InputPerMTok, 5)

	// Dated variant of the shorter family still matches the shorter key.
	p, ok = tbl.Find("claude-opus-4-20250514")
	if !ok {
		t.Fatal("expected a match")
	}
	approx(t, p.InputPerMTok, 15)
}

func TestFindStripsProviderPrefix(t *testing.T) {
	tbl := Table{"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15}}

	// Exact after stripping the provider segment.
	p, ok := tbl.Find("anthropic/claude-sonnet-4-5")
	if !ok {
		t.Fatal("expected a match after provider-prefix strip")
	}
	approx(t, p.InputPerMTok, 3)

	// Prefix match on the stripped form (dated variant behind a provider).
	p, ok = tbl.Find("anthropic/claude-sonnet-4-5-20250929")
	if !ok {
		t.Fatal("expected a prefix match after provider-prefix strip")
	}
	approx(t, p.OutputPerMTok, 15)
}

func TestFindCaseInsensitive(t *testing.T) {
	tbl := Table{"GPT-4o": {InputPerMTok: 2.5, OutputPerMTok: 10}}
	if _, ok := tbl.Find("gpt-4O"); !ok {
		t.Fatal("expected case-insensitive exact match")
	}
	if _, ok := tbl.Find("OpenAI/GPT-4O-2024-08-06"); !ok {
		t.Fatal("expected case-insensitive provider-strip + prefix match")
	}
}

func TestFindUnknown(t *testing.T) {
	if _, ok := Default.Find("some-model-nobody-priced"); ok {
		t.Fatal("unknown model must not match")
	}
	if _, ok := Default.Find(""); ok {
		t.Fatal("empty model must not match")
	}
	if _, ok := (Table{}).Find("gpt-4o"); ok {
		t.Fatal("empty table must not match")
	}
}

func TestCostArithmetic(t *testing.T) {
	// gpt-4o: $2.50 in, $10 out, $1.25 cache read, no cache write.
	u := gage.Usage{
		InputTokens:     1_000_000,
		OutputTokens:    500_000,
		CacheReadTokens: 2_000_000,
	}
	got, ok := Cost("gpt-4o", u)
	if !ok {
		t.Fatal("expected gpt-4o in Default")
	}
	// 1M*2.50/1M + 0.5M*10/1M + 2M*1.25/1M = 2.50 + 5.00 + 2.50
	approx(t, got, 10.0)

	// claude-sonnet-4-5 (dated id via prefix): $3 in, $15 out,
	// cache read 0.1x = $0.30, cache write 1.25x = $3.75.
	u = gage.Usage{
		InputTokens:      200_000,
		OutputTokens:     100_000,
		CacheReadTokens:  1_000_000,
		CacheWriteTokens: 400_000,
	}
	got, ok = Cost("claude-sonnet-4-5-20250929", u)
	if !ok {
		t.Fatal("expected claude-sonnet-4-5 in Default")
	}
	// 0.2*3 + 0.1*15 + 1.0*0.30 + 0.4*3.75 = 0.6 + 1.5 + 0.3 + 1.5
	approx(t, got, 3.9)

	// Zero usage costs zero but still reports found.
	got, ok = Cost("claude-haiku-4-5", gage.Usage{})
	if !ok {
		t.Fatal("expected claude-haiku-4-5 in Default")
	}
	approx(t, got, 0)
}

func TestCostUnknownModel(t *testing.T) {
	got, ok := Cost("unknown-model", gage.Usage{InputTokens: 1000})
	if ok {
		t.Fatal("unknown model must report ok=false")
	}
	approx(t, got, 0)
}

func TestDefaultAnthropicCacheRates(t *testing.T) {
	// Anthropic entries must carry cache read = 0.1x input and
	// cache write = 1.25x input.
	for _, key := range []string{
		"claude-opus-4", "claude-opus-4-5", "claude-sonnet-4",
		"claude-sonnet-4-5", "claude-haiku-3-5", "claude-haiku-4-5",
	} {
		p, ok := Default.Find(key)
		if !ok {
			t.Fatalf("missing Default entry %q", key)
		}
		approx(t, p.CacheReadPerMTok, p.InputPerMTok*0.10)
		approx(t, p.CacheWritePerMTok, p.InputPerMTok*1.25)
	}
}

func TestDefaultVariantKeysShadowFamilies(t *testing.T) {
	// Newer Opus generations are cheaper than the claude-opus-4 family;
	// the explicit keys must win over the shorter prefix.
	p, ok := Default.Find("claude-opus-4-6")
	if !ok {
		t.Fatal("expected claude-opus-4-6 in Default")
	}
	approx(t, p.InputPerMTok, 5)

	// o3-mini must not price as o3.
	p, ok = Default.Find("o3-mini-2025-01-31")
	if !ok {
		t.Fatal("expected o3-mini in Default")
	}
	approx(t, p.InputPerMTok, 1.10)
}
