package openai

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

// collectEvents drains a stream into a slice.
func collectEvents(t *testing.T, ch <-chan gage.Event) []gage.Event {
	t.Helper()
	var out []gage.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func sseServer(t *testing.T, chunks []string, capture *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			io.WriteString(w, "data: "+c+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
		io.WriteString(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatStreamText(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
	}
	var reqBody []byte
	srv := sseServer(t, chunks, &reqBody)

	c := &ChatClient{ProviderName: "test", BaseURL: srv.URL, DefaultModel: "m"}
	ch, err := c.Stream(context.Background(), gage.Request{
		System:   "sys",
		Messages: []gage.Message{gage.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, ch)

	// Verify request body mapping.
	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "m" || body["stream"] != true {
		t.Fatalf("body = %v", body)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected system+user, got %d", len(msgs))
	}

	// Verify events: start, text, text, usage, done.
	var text strings.Builder
	var types []gage.EventType
	var usage *gage.Usage
	var stop string
	for _, e := range events {
		types = append(types, e.Type)
		switch e.Type {
		case gage.EventTextDelta:
			text.WriteString(e.Text)
		case gage.EventUsage:
			usage = e.Usage
		case gage.EventMessageDone:
			stop = e.StopReason
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q", text.String())
	}
	if usage == nil || usage.InputTokens != 5 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	if stop != "end_turn" {
		t.Fatalf("stop = %q", stop)
	}
	if types[0] != gage.EventMessageStart || types[len(types)-1] != gage.EventMessageDone {
		t.Fatalf("event order = %v", types)
	}
}

func TestChatStreamToolCall(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	srv := sseServer(t, chunks, nil)
	c := &ChatClient{ProviderName: "test", BaseURL: srv.URL, DefaultModel: "m"}
	ch, err := c.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("read a.txt")}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, ch)

	var start, done *gage.ToolCall
	var stop string
	for _, e := range events {
		switch e.Type {
		case gage.EventToolCallStart:
			start = e.ToolCall
		case gage.EventToolCallDone:
			done = e.ToolCall
		case gage.EventMessageDone:
			stop = e.StopReason
		}
	}
	if start == nil || start.Name != "read_file" || start.ID != "call_1" {
		t.Fatalf("start = %+v", start)
	}
	if done == nil || string(done.Input) != `{"path":"a.txt"}` {
		t.Fatalf("done input = %s", done.Input)
	}
	if stop != "tool_use" {
		t.Fatalf("stop = %q", stop)
	}
}

func TestChatStreamAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"nope"}`)
	}))
	t.Cleanup(srv.Close)
	c := &ChatClient{ProviderName: "test", BaseURL: srv.URL}
	_, err := c.Stream(context.Background(), gage.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *gage.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("err = %v", err)
	}
}
