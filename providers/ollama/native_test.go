package ollama

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

func ndjsonServer(t *testing.T, lines []string, capture *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl, _ := w.(http.Flusher)
		for _, l := range lines {
			io.WriteString(w, l+"\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNativeStreamText(t *testing.T) {
	lines := []string{
		`{"message":{"content":"Hi"}}`,
		`{"message":{"content":" there"}}`,
		`{"message":{"content":""},"done":true,"prompt_eval_count":7,"eval_count":3}`,
	}
	var reqBody []byte
	srv := ndjsonServer(t, lines, &reqBody)

	p := New(srv.URL, WithDefaultModel("llama3"))
	ch, err := p.Stream(context.Background(), gage.Request{
		System:   "sys",
		Messages: []gage.Message{gage.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var usage *gage.Usage
	var stop gage.StopReason
	for e := range ch {
		switch e.Type {
		case gage.EventTextDelta:
			text.WriteString(e.Text)
		case gage.EventUsage:
			usage = e.Usage
		case gage.EventMessageDone:
			stop = e.StopReason
		}
	}
	if text.String() != "Hi there" {
		t.Fatalf("text = %q", text.String())
	}
	if usage == nil || usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", usage)
	}
	if stop != gage.StopEndTurn {
		t.Fatalf("stop = %q", stop)
	}

	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "llama3" || body["stream"] != true {
		t.Fatalf("body = %v", body)
	}
}

func TestNativeStreamToolCall(t *testing.T) {
	lines := []string{
		`{"message":{"content":"","tool_calls":[{"function":{"name":"list_dir","arguments":{"path":"."}}}]}}`,
		`{"message":{"content":""},"done":true,"prompt_eval_count":5,"eval_count":2}`,
	}
	srv := ndjsonServer(t, lines, nil)
	p := New(srv.URL, WithDefaultModel("llama3"))
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("ls")}})
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
	if done == nil || done.Name != "list_dir" || string(done.Input) != `{"path":"."}` {
		t.Fatalf("done = %+v", done)
	}
	if stop != gage.StopToolUse {
		t.Fatalf("stop = %q", stop)
	}
}

func TestOpenAICompatMode(t *testing.T) {
	p := New("http://x:11434", WithOpenAICompat())
	if p.Name() != "ollama" {
		t.Fatalf("name = %q", p.Name())
	}
}

func TestNativeRequestOptionsMapping(t *testing.T) {
	lines := []string{`{"message":{"content":"{}"},"done":true,"done_reason":"stop"}`}
	var reqBody []byte
	srv := ndjsonServer(t, lines, &reqBody)
	p := New(srv.URL, WithDefaultModel("llama3"))
	ch, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options: gage.GenerateOptions{
			ReasoningEffort: gage.ReasoningHigh,
			ResponseFormat: &gage.ResponseFormat{
				Type:   gage.ResponseJSONSchema,
				Schema: gage.JSONSchema(`{"type":"object","properties":{"x":{"type":"integer"}}}`),
			},
			Extra: map[string]any{"keep_alive": "5m"},
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
	if body["think"] != true {
		t.Fatalf("think = %v", body["think"])
	}
	format, _ := body["format"].(map[string]any)
	if format == nil || format["type"] != "object" {
		t.Fatalf("format = %v", body["format"])
	}
	if body["keep_alive"] != "5m" {
		t.Fatalf("extra not merged: %v", body["keep_alive"])
	}
}

func TestNativeJSONFormat(t *testing.T) {
	lines := []string{`{"message":{"content":"{}"},"done":true}`}
	var reqBody []byte
	srv := ndjsonServer(t, lines, &reqBody)
	p := New(srv.URL, WithDefaultModel("llama3"))
	ch, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options:  gage.GenerateOptions{ResponseFormat: &gage.ResponseFormat{Type: gage.ResponseJSON}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	var body map[string]any
	json.Unmarshal(reqBody, &body)
	if body["format"] != "json" {
		t.Fatalf("format = %v", body["format"])
	}
}

func TestNativeToolChoiceUnsupported(t *testing.T) {
	p := New("http://127.0.0.1:0", WithDefaultModel("llama3"))
	tc := gage.ToolChoice{Mode: gage.ToolChoiceRequired}
	_, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options:  gage.GenerateOptions{ToolChoice: &tc},
	})
	if !errors.Is(err, gage.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestNativeToolResultsCarryToolName(t *testing.T) {
	msgs := toNativeMessages("", []gage.Message{
		gage.UserText("q"),
		{Role: gage.RoleAssistant, Content: []gage.ContentPart{
			gage.ToolUsePart(gage.ToolCall{ID: "call_0", Name: "list_dir"}),
			gage.ToolUsePart(gage.ToolCall{ID: "call_1", Name: "read_file"}),
		}},
		gage.ToolResultMessage(gage.TextResult("call_0", ".git")),
		gage.ToolResultMessage(gage.TextResult("call_1", "hello")),
	})
	if len(msgs) != 4 {
		t.Fatalf("msgs = %v", msgs)
	}
	if msgs[2]["tool_name"] != "list_dir" || msgs[3]["tool_name"] != "read_file" {
		t.Fatalf("tool results not correlated: %v / %v", msgs[2], msgs[3])
	}
}
