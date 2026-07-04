package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

// namedSSEServer serves events with explicit "event:" names.
func namedSSEServer(t *testing.T, events [][2]string, capture *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			io.WriteString(w, "event: "+e[0]+"\ndata: "+e[1]+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResponsesStreamTextAndUsage(t *testing.T) {
	events := [][2]string{
		{"response.output_text.delta", `{"delta":"Hel"}`},
		{"response.output_text.delta", `{"delta":"lo"}`},
		{"response.reasoning_text.delta", `{"delta":"hmm"}`},
		{"response.completed", `{"response":{"usage":{"input_tokens":4,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1}}}}`},
	}
	var reqBody []byte
	srv := namedSSEServer(t, events, &reqBody)

	c := &ResponsesClient{ProviderName: "codex", URL: srv.URL, DefaultModel: "gpt", Store: false}
	ch, err := c.Stream(context.Background(), gage.Request{
		System:   "be brief",
		Messages: []gage.Message{gage.UserText("hi")},
		Options:  gage.GenerateOptions{ReasoningEffort: gage.ReasoningHigh},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text, reasoning strings.Builder
	var usage *gage.Usage
	for e := range ch {
		switch e.Type {
		case gage.EventTextDelta:
			text.WriteString(e.Text)
		case gage.EventReasoningDelta:
			reasoning.WriteString(e.Text)
		case gage.EventUsage:
			usage = e.Usage
		}
	}
	if text.String() != "Hello" || reasoning.String() != "hmm" {
		t.Fatalf("text=%q reasoning=%q", text.String(), reasoning.String())
	}
	if usage == nil || usage.InputTokens != 4 || usage.ReasoningTokens != 1 {
		t.Fatalf("usage = %+v", usage)
	}

	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["instructions"] != "be brief" || body["store"] != false {
		t.Fatalf("body = %v", body)
	}
	if r, ok := body["reasoning"].(map[string]any); !ok || r["effort"] != "high" {
		t.Fatalf("reasoning = %v", body["reasoning"])
	}
}

func TestResponsesStreamToolCall(t *testing.T) {
	events := [][2]string{
		{"response.output_item.added", `{"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"grep"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"item_1","delta":"{\"pattern\":"}`},
		{"response.function_call_arguments.delta", `{"item_id":"item_1","delta":"\"foo\"}"}`},
		{"response.function_call_arguments.done", `{"item_id":"item_1","arguments":"{\"pattern\":\"foo\"}"}`},
		{"response.completed", `{"response":{"usage":{"input_tokens":1,"output_tokens":1}}}`},
	}
	srv := namedSSEServer(t, events, nil)
	c := &ResponsesClient{ProviderName: "codex", URL: srv.URL, DefaultModel: "gpt"}
	ch, err := c.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("search foo")}})
	if err != nil {
		t.Fatal(err)
	}
	var start, done *gage.ToolCall
	var stop string
	for e := range ch {
		switch e.Type {
		case gage.EventToolCallStart:
			start = e.ToolCall
		case gage.EventToolCallDone:
			done = e.ToolCall
		case gage.EventMessageDone:
			stop = e.StopReason
		}
	}
	if start == nil || start.Name != "grep" || start.ID != "call_1" {
		t.Fatalf("start = %+v", start)
	}
	if done == nil || string(done.Input) != `{"pattern":"foo"}` {
		t.Fatalf("done = %+v", done)
	}
	if stop != "tool_use" {
		t.Fatalf("stop = %q", stop)
	}
}

func TestResponsesAuthorizerInvoked(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"response\":{}}\n\n")
	}))
	t.Cleanup(srv.Close)

	c := &ResponsesClient{
		ProviderName: "codex",
		URL:          srv.URL,
		Authorize: func(ctx context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer tok123")
			return nil
		},
	}
	ch, err := c.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if gotAuth != "Bearer tok123" {
		t.Fatalf("auth header = %q", gotAuth)
	}
}
