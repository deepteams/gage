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
	"github.com/deepteams/gage/providers/shared"
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
	var stop gage.StopReason
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
	if stop != gage.StopEndTurn {
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
	var stop gage.StopReason
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
	if stop != gage.StopToolUse {
		t.Fatalf("stop = %q", stop)
	}
}

func TestChatMidStreamError(t *testing.T) {
	// OpenRouter/vLLM report failures as an {"error":{...}} SSE payload.
	chunks := []string{
		`{"choices":[{"delta":{"content":"partial"}}]}`,
		`{"error":{"message":"rate limited","code":429}}`,
	}
	srv := sseServer(t, chunks, nil)
	c := &ChatClient{ProviderName: "test", BaseURL: srv.URL, DefaultModel: "m"}
	ch, err := c.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, ch)
	last := events[len(events)-1]
	if last.Type != gage.EventError {
		t.Fatalf("last event = %+v, want error", last)
	}
	var apiErr *gage.APIError
	if !errors.As(last.Err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("err = %v", last.Err)
	}
	if !errors.Is(last.Err, gage.ErrRateLimited) {
		t.Fatalf("err should match ErrRateLimited: %v", last.Err)
	}
	for _, e := range events {
		if e.Type == gage.EventMessageDone {
			t.Fatalf("stream must not complete after an error: %v", events)
		}
	}
}

func TestChatTruncatedToolCallNotFlushed(t *testing.T) {
	// The stream dies mid tool-call: no ToolCallDone may be emitted for the
	// incomplete call, only the error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Announce more bytes than are written so the client sees an
		// unexpected EOF mid-stream.
		w.Header().Set("Content-Length", "4096")
		io.WriteString(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"write_file","arguments":"{\"path\":\"a.txt\",\"content\":\"trunc"}}]}}]}`+"\n\n")
	}))
	t.Cleanup(srv.Close)

	c := &ChatClient{ProviderName: "test", BaseURL: srv.URL, DefaultModel: "m", HTTP: noRetryClient()}
	ch, err := c.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, ch)
	var sawErr bool
	for _, e := range events {
		switch e.Type {
		case gage.EventToolCallDone:
			t.Fatalf("truncated call must not be completed: %+v", e.ToolCall)
		case gage.EventMessageDone:
			t.Fatalf("stream must not complete: %v", events)
		case gage.EventError:
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("expected an error event, got %v", events)
	}
}

func noRetryClient() *shared.Client {
	c := shared.NewClient("test")
	c.MaxRetries = 0
	return c
}

func TestChatResponseFormatMapping(t *testing.T) {
	chunks := []string{`{"choices":[{"delta":{"content":"{}"},"finish_reason":"stop"}]}`}

	t.Run("json_object", func(t *testing.T) {
		var reqBody []byte
		srv := sseServer(t, chunks, &reqBody)
		c := &ChatClient{ProviderName: "test", BaseURL: srv.URL, DefaultModel: "m"}
		ch, err := c.Stream(context.Background(), gage.Request{
			Messages: []gage.Message{gage.UserText("hi")},
			Options:  gage.GenerateOptions{ResponseFormat: &gage.ResponseFormat{Type: gage.ResponseJSON}},
		})
		if err != nil {
			t.Fatal(err)
		}
		collectEvents(t, ch)
		var body map[string]any
		json.Unmarshal(reqBody, &body)
		rf, _ := body["response_format"].(map[string]any)
		if rf == nil || rf["type"] != "json_object" {
			t.Fatalf("response_format = %v", body["response_format"])
		}
	})

	t.Run("json_schema", func(t *testing.T) {
		var reqBody []byte
		srv := sseServer(t, chunks, &reqBody)
		c := &ChatClient{ProviderName: "test", BaseURL: srv.URL, DefaultModel: "m"}
		schema := gage.JSONSchema(`{"type":"object","properties":{"x":{"type":"integer"}}}`)
		ch, err := c.Stream(context.Background(), gage.Request{
			Messages: []gage.Message{gage.UserText("hi")},
			Options: gage.GenerateOptions{ResponseFormat: &gage.ResponseFormat{
				Type: gage.ResponseJSONSchema, Name: "out", Schema: schema, Strict: true,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		collectEvents(t, ch)
		var body map[string]any
		json.Unmarshal(reqBody, &body)
		rf, _ := body["response_format"].(map[string]any)
		if rf == nil || rf["type"] != "json_schema" {
			t.Fatalf("response_format = %v", body["response_format"])
		}
		js, _ := rf["json_schema"].(map[string]any)
		if js == nil || js["name"] != "out" || js["strict"] != true || js["schema"] == nil {
			t.Fatalf("json_schema = %v", js)
		}
	})
}

func TestChatAssistantMessageNeverEmpty(t *testing.T) {
	// An assistant history message with only reasoning parts must not encode
	// as a bare {"role":"assistant"}; it falls back to empty string content.
	msgs, err := toChatMessages("", []gage.Message{
		{Role: gage.RoleAssistant, Content: []gage.ContentPart{gage.ReasoningPart("thinking...")}},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	content, ok := msgs[0]["content"]
	if !ok || content != "" {
		t.Fatalf("content = %v (present=%v), want empty string", content, ok)
	}
	if _, ok := msgs[0]["tool_calls"]; ok {
		t.Fatalf("unexpected tool_calls: %v", msgs[0])
	}
}

func TestChatModelRequired(t *testing.T) {
	c := &ChatClient{ProviderName: "test", BaseURL: "http://127.0.0.1:0"}
	_, err := c.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err == nil || !strings.Contains(err.Error(), "no model specified") {
		t.Fatalf("err = %v, want no model specified", err)
	}
}

func TestChatStreamAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"nope"}`)
	}))
	t.Cleanup(srv.Close)
	c := &ChatClient{ProviderName: "test", BaseURL: srv.URL, DefaultModel: "m"}
	_, err := c.Stream(context.Background(), gage.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *gage.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("err = %v", err)
	}
}

func TestChatDocumentInline(t *testing.T) {
	// An inline document becomes a {"type":"file"} content part carrying a
	// data: URL; Filename defaults to document.pdf when unset.
	msgs, err := toChatMessages("", []gage.Message{
		{Role: gage.RoleUser, Content: []gage.ContentPart{
			gage.TextPart("read this"),
			gage.DocumentPart(gage.DocumentSource{Data: "cGRm", MediaType: "application/pdf", Filename: "spec.pdf"}),
			gage.DocumentPart(gage.DocumentSource{Data: "dHh0"}),
		}},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	content, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(content) != 3 {
		t.Fatalf("content = %#v", msgs[0]["content"])
	}
	f0, _ := content[1]["file"].(map[string]any)
	if content[1]["type"] != "file" || f0 == nil {
		t.Fatalf("file part = %v", content[1])
	}
	if f0["filename"] != "spec.pdf" || f0["file_data"] != "data:application/pdf;base64,cGRm" {
		t.Fatalf("file = %v", f0)
	}
	// Filename and media type defaults.
	f1 := content[2]["file"].(map[string]any)
	if f1["filename"] != "document.pdf" || f1["file_data"] != "data:application/pdf;base64,dHh0" {
		t.Fatalf("defaulted file = %v", f1)
	}
}

func TestChatDocumentURLUnsupported(t *testing.T) {
	// Chat Completions has no file-URL input; the request must fail fast,
	// before dialing.
	c := &ChatClient{ProviderName: "test", BaseURL: "http://127.0.0.1:0", DefaultModel: "m"}
	_, err := c.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{
			{Role: gage.RoleUser, Content: []gage.ContentPart{
				gage.DocumentPart(gage.DocumentSource{URL: "https://example.com/a.pdf"}),
			}},
		},
	})
	if !errors.Is(err, gage.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestChatDocumentEmptyFails(t *testing.T) {
	c := &ChatClient{ProviderName: "test", BaseURL: "http://127.0.0.1:0", DefaultModel: "m"}
	_, err := c.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{
			{Role: gage.RoleUser, Content: []gage.ContentPart{
				gage.DocumentPart(gage.DocumentSource{}),
			}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "document") {
		t.Fatalf("err = %v, want document encoding error", err)
	}
}
