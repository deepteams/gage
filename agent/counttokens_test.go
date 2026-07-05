package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/deepteams/gage"
)

// countingProvider is a mockProvider that also implements gage.TokenCounter
// with a scripted count (or error).
type countingProvider struct {
	mockProvider
	countMu    sync.Mutex
	count      int
	countErr   error
	countCalls int
	lastCount  gage.Request
}

func (c *countingProvider) CountTokens(ctx context.Context, req gage.Request) (int, error) {
	c.countMu.Lock()
	defer c.countMu.Unlock()
	c.countCalls++
	c.lastCount = req
	return c.count, c.countErr
}

func (c *countingProvider) counted() int {
	c.countMu.Lock()
	defer c.countMu.Unlock()
	return c.countCalls
}

func recordingCompactor(compacted *bool, usage *gage.Usage) gage.CompactorFunc {
	return func(ctx context.Context, msgs []gage.Message, u gage.Usage) ([]gage.Message, gage.Usage, error) {
		*compacted = true
		*usage = u
		return msgs, gage.Usage{}, nil
	}
}

func TestCountTokensExactCountTriggersCompaction(t *testing.T) {
	// The heuristic estimate of this tiny conversation is far below the
	// threshold; only the provider's exact count (5000) crosses it.
	input := []gage.Message{gage.UserText("hi")}
	if est := gage.EstimateTokens(input); est >= 1000 {
		t.Fatalf("test premise broken: heuristic estimate %d >= threshold", est)
	}
	cp := &countingProvider{count: 5000}
	cp.turns = [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}
	var compacted bool
	var compactUsage gage.Usage
	ag, _ := New(Config{
		Provider:     cp,
		Compactor:    recordingCompactor(&compacted, &compactUsage),
		CompactAfter: 1000,
		CountTokens:  true,
	})
	if _, err := ag.RunSync(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if cp.counted() == 0 {
		t.Fatal("CountTokens capability never called")
	}
	if !compacted {
		t.Fatal("compactor not invoked although exact count crossed the threshold")
	}
	if compactUsage.InputTokens != 5000 {
		t.Fatalf("compactor saw usage %+v, want InputTokens=5000", compactUsage)
	}
	// The counted request must describe the upcoming provider call.
	cp.countMu.Lock()
	counted := cp.lastCount
	cp.countMu.Unlock()
	if len(counted.Messages) != 1 || counted.Messages[0].Text() != "hi" {
		t.Fatalf("counted request messages = %+v", counted.Messages)
	}
}

func TestCountTokensErrorFallsBackToHeuristic(t *testing.T) {
	// The exact count fails; the heuristic (well above the threshold) must
	// still drive compaction, and the run must not fail.
	big := strings.Repeat("x", 8000) // heuristic ~2000 tokens
	input := []gage.Message{gage.UserText(big)}
	if est := gage.EstimateTokens(input); est < 1000 {
		t.Fatalf("test premise broken: heuristic estimate %d < threshold", est)
	}
	cp := &countingProvider{countErr: errors.New("count endpoint down")}
	cp.turns = [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}
	var compacted bool
	var compactUsage gage.Usage
	ag, _ := New(Config{
		Provider:     cp,
		Compactor:    recordingCompactor(&compacted, &compactUsage),
		CompactAfter: 1000,
		CountTokens:  true,
	})
	if _, err := ag.RunSync(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if cp.counted() == 0 {
		t.Fatal("CountTokens capability never attempted")
	}
	if !compacted {
		t.Fatal("compactor not invoked by heuristic fallback")
	}
	if compactUsage.InputTokens < 1000 {
		t.Fatalf("compactor saw usage %+v, want heuristic estimate >= 1000", compactUsage)
	}
}

func TestCountTokensErrorBelowThresholdNoCompaction(t *testing.T) {
	// Count fails and the heuristic stays below the threshold: no compaction,
	// no run error — the failure is silent.
	cp := &countingProvider{countErr: errors.New("count endpoint down")}
	cp.turns = [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}
	var compacted bool
	var compactUsage gage.Usage
	ag, _ := New(Config{
		Provider:     cp,
		Compactor:    recordingCompactor(&compacted, &compactUsage),
		CompactAfter: 1000,
		CountTokens:  true,
	})
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" {
		t.Fatalf("result text = %q", res.Text)
	}
	if compacted {
		t.Fatal("compactor invoked below threshold")
	}
}

func TestCountTokensDisabledCounterNeverCalled(t *testing.T) {
	cp := &countingProvider{count: 5000}
	cp.turns = [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}
	var compacted bool
	var compactUsage gage.Usage
	ag, _ := New(Config{
		Provider:     cp,
		Compactor:    recordingCompactor(&compacted, &compactUsage),
		CompactAfter: 1000,
		CountTokens:  false,
	})
	if _, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("hi")}); err != nil {
		t.Fatal(err)
	}
	if cp.counted() != 0 {
		t.Fatalf("CountTokens called %d times with the flag off", cp.counted())
	}
	if compacted {
		t.Fatal("compactor invoked although the heuristic is below the threshold")
	}
}
