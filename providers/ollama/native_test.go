package ollama

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
	var stop string
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
	if stop != "end_turn" {
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
	var stop string
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
	if stop != "tool_use" {
		t.Fatalf("stop = %q", stop)
	}
}

func TestOpenAICompatMode(t *testing.T) {
	p := New("http://x:11434", WithOpenAICompat())
	if p.Name() != "ollama" {
		t.Fatalf("name = %q", p.Name())
	}
}
