package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

func sseServer(t *testing.T, events []string, capture *[]byte, headers *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		if headers != nil {
			*headers = r.Header.Clone()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			io.WriteString(w, "data: "+e+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRequestMapping(t *testing.T) {
	events := []string{`{"type":"message_stop"}`}
	var reqBody []byte
	var headers http.Header
	srv := sseServer(t, events, &reqBody, &headers)

	p := New(Config{APIKey: "key-1", BaseURL: srv.URL, Model: "claude-x"})
	schema := gage.JSONSchema(`{"type":"object","properties":{"answer":{"type":"string"}}}`)
	ch, err := p.Stream(context.Background(), gage.Request{
		System: "be nice",
		Messages: []gage.Message{
			gage.UserText("q"),
			{Role: gage.RoleAssistant, Content: []gage.ContentPart{
				gage.TextPart("using tool"),
				gage.SignedReasoningPart("let me think", "sig-1"),
				{Kind: gage.PartReasoning, Signature: RedactedSignaturePrefix + "opaque-bytes"},
				gage.ToolUsePart(gage.ToolCall{ID: "tu_1", Name: "bash", Input: gage.JSONSchema(`{"cmd":"ls"}`)}),
			}},
			gage.ToolResultMessage(gage.TextResult("tu_1", "ok")),
			gage.UserText("final"),
		},
		Tools: []gage.ToolSchema{
			{Name: "bash", Description: "run", Parameters: gage.JSONSchema(`{"type":"object"}`)},
			{Name: "grep", Description: "search", Parameters: gage.JSONSchema(`{"type":"object"}`)},
		},
		Options: gage.GenerateOptions{
			PromptCache:    true,
			ResponseFormat: &gage.ResponseFormat{Type: gage.ResponseJSONSchema, Name: "out", Schema: schema, Strict: true},
			Extra:          map[string]any{"metadata": map[string]any{"user_id": "u1"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	if headers.Get("x-api-key") != "key-1" {
		t.Fatalf("x-api-key = %q", headers.Get("x-api-key"))
	}
	if headers.Get("anthropic-version") != Version {
		t.Fatalf("anthropic-version = %q", headers.Get("anthropic-version"))
	}
	if !strings.Contains(headers.Get("anthropic-beta"), BetaStructuredOutputs) {
		t.Fatalf("anthropic-beta = %q", headers.Get("anthropic-beta"))
	}

	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "claude-x" || body["stream"] != true {
		t.Fatalf("body = %v", body)
	}

	// System: no prefix for the API-key provider, cache_control on last block.
	sys := body["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != "be nice" {
		t.Fatalf("system = %v", sys)
	}
	if cc := sys[0].(map[string]any)["cache_control"]; cc == nil {
		t.Fatalf("system cache_control missing: %v", sys)
	}

	// Tools: cache_control only on the last schema.
	tools := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %v", tools)
	}
	if cc := tools[0].(map[string]any)["cache_control"]; cc != nil {
		t.Fatalf("first tool should not carry cache_control: %v", tools[0])
	}
	if cc := tools[1].(map[string]any)["cache_control"]; cc == nil {
		t.Fatalf("last tool cache_control missing: %v", tools[1])
	}

	// Messages: assistant thinking blocks precede text/tool_use; the final
	// message's last block carries cache_control (converted from string form).
	msgs := body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %v", msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 4 {
		t.Fatalf("assistant content = %v", asst)
	}
	if b0 := asst[0].(map[string]any); b0["type"] != "thinking" || b0["thinking"] != "let me think" || b0["signature"] != "sig-1" {
		t.Fatalf("thinking block = %v", b0)
	}
	if b1 := asst[1].(map[string]any); b1["type"] != "redacted_thinking" || b1["data"] != "opaque-bytes" {
		t.Fatalf("redacted block = %v", b1)
	}
	if b2 := asst[2].(map[string]any); b2["type"] != "text" {
		t.Fatalf("text block = %v", b2)
	}
	if b3 := asst[3].(map[string]any); b3["type"] != "tool_use" || b3["name"] != "bash" {
		t.Fatalf("tool_use block = %v", b3)
	}
	final := msgs[3].(map[string]any)["content"].([]any)
	lastBlock := final[len(final)-1].(map[string]any)
	if lastBlock["cache_control"] == nil || lastBlock["text"] != "final" {
		t.Fatalf("final message block = %v", lastBlock)
	}
	// Only the final message carries a breakpoint.
	if txt, ok := msgs[0].(map[string]any)["content"].(string); !ok || txt != "q" {
		t.Fatalf("first user message = %v", msgs[0])
	}

	// output_format from ResponseJSONSchema.
	of := body["output_format"].(map[string]any)
	if of["type"] != "json_schema" || of["schema"] == nil {
		t.Fatalf("output_format = %v", of)
	}

	// Extra merged at top level.
	if body["metadata"] == nil {
		t.Fatalf("extra not merged: %v", body)
	}
}

func TestEventSequenceWithThinking(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"mm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque=="}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Hi"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	}
	srv := sseServer(t, events, nil, nil)
	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}

	var types []gage.EventType
	var sigs []string
	var reasoning, text strings.Builder
	var usage *gage.Usage
	var stop gage.StopReason
	for e := range ch {
		types = append(types, e.Type)
		switch e.Type {
		case gage.EventReasoningDelta:
			reasoning.WriteString(e.Text)
		case gage.EventReasoningDone:
			sigs = append(sigs, e.Signature)
		case gage.EventTextDelta:
			text.WriteString(e.Text)
		case gage.EventUsage:
			usage = e.Usage
		case gage.EventMessageDone:
			stop = e.StopReason
		}
	}
	if reasoning.String() != "hmmm" || text.String() != "Hi" {
		t.Fatalf("reasoning=%q text=%q", reasoning.String(), text.String())
	}
	// Accumulated signature_deltas, then the redacted block's marked payload.
	if len(sigs) != 2 || sigs[0] != "sig-abc" || sigs[1] != RedactedSignaturePrefix+"opaque==" {
		t.Fatalf("signatures = %v", sigs)
	}
	// message_delta output_tokens (9) replaces the message_start value (1).
	if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v", usage)
	}
	if stop != gage.StopEndTurn {
		t.Fatalf("stop = %q", stop)
	}
	want := []gage.EventType{
		gage.EventMessageStart,
		gage.EventReasoningDelta, gage.EventReasoningDelta, gage.EventReasoningDone,
		gage.EventReasoningDone, // redacted block
		gage.EventTextDelta,
		gage.EventUsage,
		gage.EventMessageDone,
	}
	if len(types) != len(want) {
		t.Fatalf("event types = %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event[%d] = %v, want %v (all: %v)", i, types[i], want[i], types)
		}
	}
}

func TestToolCallStream(t *testing.T) {
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_9","name":"grep"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
	}
	srv := sseServer(t, events, nil, nil)
	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var done *gage.ToolCall
	var stop gage.StopReason
	for e := range ch {
		switch e.Type {
		case gage.EventToolCallDone:
			done = e.ToolCall
		case gage.EventMessageDone:
			stop = e.StopReason
		}
	}
	if done == nil || done.ID != "tu_9" || done.Name != "grep" || string(done.Input) != `{"q":"x"}` {
		t.Fatalf("done = %+v", done)
	}
	if stop != gage.StopToolUse {
		t.Fatalf("stop = %q", stop)
	}
}

func TestResponseJSONWithoutSchemaUnsupported(t *testing.T) {
	// The Messages API has no schemaless JSON mode; the explicit request must
	// fail fast, before dialing.
	p := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:0", Model: "m"})
	_, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options:  gage.GenerateOptions{ResponseFormat: &gage.ResponseFormat{Type: gage.ResponseJSON}},
	})
	if !errors.Is(err, gage.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestNoStructuredBetaWithoutFormat(t *testing.T) {
	events := []string{`{"type":"message_stop"}`}
	var headers http.Header
	srv := sseServer(t, events, nil, &headers)
	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if strings.Contains(headers.Get("anthropic-beta"), BetaStructuredOutputs) {
		t.Fatalf("unexpected structured-outputs beta: %q", headers.Get("anthropic-beta"))
	}
}

func TestModelRequired(t *testing.T) {
	p := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:0"})
	if _, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}}); err == nil {
		t.Fatal("expected an error for a missing model")
	}
}
