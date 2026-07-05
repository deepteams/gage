package gage

import (
	"strings"
	"testing"
)

func TestEstimateTokensText(t *testing.T) {
	msgs := []Message{UserText(strings.Repeat("a", 400))}
	got := EstimateTokens(msgs)
	// 400 chars ≈ 100 tokens plus per-message overhead.
	if got < 100 || got > 110 {
		t.Fatalf("estimate = %d", got)
	}
}

func TestEstimateTokensCoversAllParts(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, Content: []ContentPart{
			ReasoningPart(strings.Repeat("r", 40)),
			TextPart(strings.Repeat("t", 40)),
			ToolUsePart(ToolCall{ID: "c1", Name: "bash", Input: []byte(strings.Repeat("i", 40))}),
		}},
		ToolResultMessage(TextResult("c1", strings.Repeat("o", 40))),
		{Role: RoleUser, Content: []ContentPart{{Kind: PartImage, Image: &ImageSource{URL: "https://x/y.png"}}}},
	}
	got := EstimateTokens(msgs)
	// 4×10 text tokens + bash + overheads + flat image cost.
	if got < estimatedImageTokens+40 {
		t.Fatalf("estimate = %d, image or parts undercounted", got)
	}
}

func TestEstimateTextTokens(t *testing.T) {
	if EstimateTextTokens("") != 0 {
		t.Fatal("empty text must estimate 0")
	}
	if EstimateTextTokens(strings.Repeat("x", 800)) != 200 {
		t.Fatal("estimate off for plain text")
	}
}
