package gage

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMessageHelpers(t *testing.T) {
	m := Message{Role: RoleAssistant, Content: []ContentPart{
		TextPart("hello "),
		ReasoningPart("(thinking)"),
		TextPart("world"),
		ToolUsePart(ToolCall{ID: "1", Name: "read_file", Input: json.RawMessage(`{}`)}),
	}}
	if got := m.Text(); got != "hello world" {
		t.Fatalf("Text() = %q, want %q", got, "hello world")
	}
	calls := m.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("ToolCalls() = %+v", calls)
	}
}

func TestToolResultText(t *testing.T) {
	r := TextResult("c1", "ok")
	if r.Text() != "ok" || r.IsError {
		t.Fatalf("unexpected result %+v", r)
	}
	e := ErrorResult("c1", "boom")
	if !e.IsError || e.Text() != "boom" {
		t.Fatalf("unexpected error result %+v", e)
	}
}

func TestUsageAdd(t *testing.T) {
	a := Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2}
	b := Usage{InputTokens: 3, OutputTokens: 7}
	sum := a.Add(b)
	if sum.InputTokens != 13 || sum.OutputTokens != 12 || sum.ReasoningTokens != 2 {
		t.Fatalf("Add = %+v", sum)
	}
	if sum.Total() != 25 {
		t.Fatalf("Total = %d", sum.Total())
	}
}

func TestEventConstructors(t *testing.T) {
	if TextDelta("x").Type != EventTextDelta {
		t.Fatal("TextDelta type")
	}
	e := ErrorEvent(errors.New("bad"))
	if e.Type != EventError || e.ErrorString != "bad" || e.Err == nil {
		t.Fatalf("ErrorEvent = %+v", e)
	}
	tc := ToolCall{ID: "1", Name: "bash"}
	if got := ToolCallStart(tc); got.ToolCall == nil || got.ToolCall.Name != "bash" {
		t.Fatalf("ToolCallStart = %+v", got)
	}
	if EventDone != DoneEvent(nil).Type {
		t.Fatal("DoneEvent type")
	}
	if got := TextDelta("x").WithTurn(3); got.Turn != 3 {
		t.Fatalf("WithTurn = %+v", got)
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	e := UsageEvent(Usage{InputTokens: 1, OutputTokens: 2})
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != EventUsage || back.Usage == nil || back.Usage.OutputTokens != 2 {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestApplyOptions(t *testing.T) {
	o := ApplyOptions(GenerateOptions{}, WithTemperature(0.2), WithMaxTokens(100), WithReasoningEffort(ReasoningHigh), WithExtra("k", "v"))
	if o.Temperature == nil || *o.Temperature != 0.2 {
		t.Fatalf("temperature = %v", o.Temperature)
	}
	if o.MaxTokens != 100 || o.ReasoningEffort != ReasoningHigh || o.Extra["k"] != "v" {
		t.Fatalf("options = %+v", o)
	}
}

func TestCredentialsExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	c := Credentials{ExpiresAt: now.Add(20 * time.Second)}
	if c.Expired(30*time.Second, now) != true {
		t.Fatal("should be expired within skew")
	}
	if c.Expired(5*time.Second, now) != false {
		t.Fatal("should not be expired")
	}
	if (Credentials{}).Expired(time.Second, now) {
		t.Fatal("zero ExpiresAt never expires")
	}
}

func TestAPIErrorIs(t *testing.T) {
	if !errors.Is(&APIError{Status: 401}, ErrAuth) {
		t.Fatal("401 should be ErrAuth")
	}
	if !errors.Is(&APIError{Status: 429}, ErrRateLimited) {
		t.Fatal("429 should be ErrRateLimited")
	}
	if errors.Is(&APIError{Status: 500}, ErrAuth) {
		t.Fatal("500 is not ErrAuth")
	}
}

func TestSchemaOf(t *testing.T) {
	tool := ToolFunc{ToolName: "t", Desc: "d", Params: json.RawMessage(`{"type":"object"}`)}
	s := SchemaOf(tool)
	if s.Name != "t" || s.Description != "d" || string(s.Parameters) != `{"type":"object"}` {
		t.Fatalf("SchemaOf = %+v", s)
	}
}

func TestToolMetadataAndSummary(t *testing.T) {
	tool := ToolFunc{
		ToolName: "fetch",
		Meta:     ToolMetadata{ReadOnly: true, Network: true},
		CallSummary: func(input json.RawMessage) string {
			return "custom summary"
		},
	}
	meta := MetadataOf(tool)
	if !meta.ReadOnly || !meta.Network {
		t.Fatalf("metadata = %+v", meta)
	}
	if got := CallSummaryOf(tool, json.RawMessage(`{"url":"https://example.com"}`)); got != "custom summary" {
		t.Fatalf("summary = %q", got)
	}
	plain := ToolFunc{ToolName: "plain"}
	if got := CallSummaryOf(plain, json.RawMessage(`{"b":2, "a":1}`)); got != `plain {"b":2,"a":1}` {
		t.Fatalf("plain summary = %q", got)
	}
}
