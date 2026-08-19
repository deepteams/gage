package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestDocumentPartsEncoding(t *testing.T) {
	events := []string{`{"type":"message_stop"}`}
	var reqBody []byte
	srv := sseServer(t, events, &reqBody, nil)
	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	ch, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{
			{Role: gage.RoleUser, Content: []gage.ContentPart{
				gage.TextPart("summarize"),
				gage.DocumentPart(gage.DocumentSource{Data: "cGRmLWJ5dGVz", Filename: "report.pdf"}),
				gage.DocumentPart(gage.DocumentSource{URL: "https://example.com/spec.pdf"}),
				gage.DocumentPart(gage.DocumentSource{Data: "dHh0", MediaType: "text/plain"}),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 4 {
		t.Fatalf("content = %v", content)
	}

	// Inline base64 document: media_type defaults to application/pdf and
	// Filename maps to title.
	d0 := content[1].(map[string]any)
	if d0["type"] != "document" || d0["title"] != "report.pdf" {
		t.Fatalf("base64 document block = %v", d0)
	}
	src0 := d0["source"].(map[string]any)
	if src0["type"] != "base64" || src0["media_type"] != "application/pdf" || src0["data"] != "cGRmLWJ5dGVz" {
		t.Fatalf("base64 document source = %v", src0)
	}

	// URL document: url source, no title.
	d1 := content[2].(map[string]any)
	if d1["type"] != "document" {
		t.Fatalf("url document block = %v", d1)
	}
	if _, ok := d1["title"]; ok {
		t.Fatalf("unexpected title on url document: %v", d1)
	}
	src1 := d1["source"].(map[string]any)
	if src1["type"] != "url" || src1["url"] != "https://example.com/spec.pdf" {
		t.Fatalf("url document source = %v", src1)
	}

	// Explicit media type is preserved.
	src2 := content[3].(map[string]any)["source"].(map[string]any)
	if src2["media_type"] != "text/plain" {
		t.Fatalf("explicit media_type = %v", src2)
	}
}

func TestDocumentPartEmptyFails(t *testing.T) {
	// A document with neither URL nor Data must fail before dialing.
	p := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:0", Model: "m"})
	_, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{
			{Role: gage.RoleUser, Content: []gage.ContentPart{
				gage.DocumentPart(gage.DocumentSource{Filename: "empty.pdf"}),
			}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "document") {
		t.Fatalf("err = %v, want document encoding error", err)
	}
}

func TestCountTokens(t *testing.T) {
	var reqBody []byte
	var gotPath, gotMethod string
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		headers = r.Header.Clone()
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":123}`)
	}))
	t.Cleanup(srv.Close)

	p := New(Config{APIKey: "key-9", BaseURL: srv.URL, Model: "claude-x"})
	tc, ok := p.(gage.TokenCounter)
	if !ok {
		t.Fatal("anthropic provider does not implement gage.TokenCounter")
	}
	n, err := tc.CountTokens(context.Background(), gage.Request{
		System:   "be nice",
		Messages: []gage.Message{gage.UserText("count me")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 123 {
		t.Fatalf("count = %d, want 123", n)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/messages/count_tokens" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if headers.Get("x-api-key") != "key-9" || headers.Get("anthropic-version") != Version {
		t.Fatalf("headers = %v", headers)
	}

	// Same body shape as Stream, minus stream and max_tokens.
	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["stream"]; ok {
		t.Fatalf("stream must be omitted: %v", body)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("max_tokens must be omitted: %v", body)
	}
	if body["model"] != "claude-x" {
		t.Fatalf("model = %v", body["model"])
	}
	sys := body["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != "be nice" {
		t.Fatalf("system = %v", sys)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "count me" {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestCountTokensAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"bad"}}`)
	}))
	t.Cleanup(srv.Close)

	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	_, err := p.(gage.TokenCounter).CountTokens(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
	})
	var apiErr *gage.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want *gage.APIError with status 400", err)
	}
}

func TestThinkingMapping(t *testing.T) {
	c := &Client{ProviderName: "anthropic", DefaultModel: "claude-x"}
	cases := []struct {
		name      string
		effort    gage.ReasoningEffort
		maxTokens int
		want      map[string]any // nil = no thinking block
		wantMax   int            // expected max_tokens, 0 = unchanged
		wantErr   bool
	}{
		{name: "unset"},
		{name: "off", effort: gage.ReasoningOff, want: map[string]any{"type": "disabled"}},
		{name: "minimal", effort: gage.ReasoningMinimal, want: map[string]any{"type": "enabled", "budget_tokens": 1024}},
		// max_tokens left to the default: it is raised to fit the budget.
		{name: "high", effort: gage.ReasoningHigh, want: map[string]any{"type": "enabled", "budget_tokens": 16384}, wantMax: 16384 + DefaultMaxTokens},
		// Aliases fold onto the portable scale.
		{name: "alias ultra", effort: "Ultra", want: map[string]any{"type": "enabled", "budget_tokens": 32768}, wantMax: 32768 + DefaultMaxTokens},
		// budget_tokens must stay strictly below max_tokens.
		{name: "clamped", effort: gage.ReasoningMax, maxTokens: 4096, want: map[string]any{"type": "enabled", "budget_tokens": 4095}},
		// ... and above the API minimum, otherwise the request is rejected.
		{name: "max_tokens too small", effort: gage.ReasoningLow, maxTokens: 512, wantErr: true},
		// A gateway's own label cannot be turned into a budget.
		{name: "unknown", effort: "turbo", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _, err := c.buildBodyMap(gage.Request{
				Messages: []gage.Message{gage.UserText("hi")},
				Options:  gage.GenerateOptions{ReasoningEffort: tc.effort, MaxTokens: tc.maxTokens},
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("thinking = %v, want error", b["thinking"])
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := b["thinking"].(map[string]any)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("thinking = %v, want none", got)
				}
				return
			}
			if got["type"] != tc.want["type"] || fmt.Sprint(got["budget_tokens"]) != fmt.Sprint(tc.want["budget_tokens"]) {
				t.Fatalf("thinking = %v, want %v", got, tc.want)
			}
			if tc.wantMax > 0 && fmt.Sprint(b["max_tokens"]) != fmt.Sprint(tc.wantMax) {
				t.Fatalf("max_tokens = %v, want %d", b["max_tokens"], tc.wantMax)
			}
		})
	}
}
