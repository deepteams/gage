package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared/oauth"
)

func testStore() gage.TokenStore {
	return oauth.NewMemoryStoreWith(gage.Credentials{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)})
}

func TestProviderStreamAndHeaders(t *testing.T) {
	var gotAuth, gotBeta, gotVersion string
	var reqBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotVersion = r.Header.Get("anthropic-version")
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":2}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			io.WriteString(w, e+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	p := New(testStore(), false, WithMessagesURL(srv.URL))
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
	if text.String() != "Hi" {
		t.Fatalf("text = %q", text.String())
	}
	// message_delta usage is cumulative: output_tokens 5 replaces the
	// message_start figure (2), it is not added to it.
	if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", usage)
	}
	if stop != gage.StopEndTurn {
		t.Fatalf("stop = %q", stop)
	}
	if gotAuth != "Bearer tok" || !strings.Contains(gotBeta, "oauth-2025-04-20") || gotVersion != AnthropicVersion {
		t.Fatalf("headers auth=%q beta=%q version=%q", gotAuth, gotBeta, gotVersion)
	}

	// Verify the spoof block was sent first, followed by the caller's system.
	var body map[string]any
	json.Unmarshal(reqBody, &body)
	sys := body["system"].([]any)
	if len(sys) != 2 || sys[0].(map[string]any)["text"] != SystemSpoof || sys[1].(map[string]any)["text"] != "sys" {
		t.Fatalf("system blocks = %v", sys)
	}
}

func TestProviderToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"bash"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
		}
		for _, e := range events {
			io.WriteString(w, e+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)

	p := New(testStore(), false, WithMessagesURL(srv.URL))
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("run ls")}})
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
	if done == nil || done.Name != "bash" || string(done.Input) != `{"cmd":"ls"}` {
		t.Fatalf("done = %+v", done)
	}
	if stop != gage.StopToolUse {
		t.Fatalf("stop = %q", stop)
	}
}

func TestProviderResponseFormatUnsupported(t *testing.T) {
	// The spoofed backend must not be poked with structured-output betas:
	// Stream fails fast, before dialing.
	p := New(testStore(), false, WithMessagesURL("http://127.0.0.1:0"))
	_, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options: gage.GenerateOptions{
			ResponseFormat: &gage.ResponseFormat{Type: gage.ResponseJSONSchema, Name: "out"},
		},
	})
	if !errors.Is(err, gage.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if err == nil || !strings.Contains(err.Error(), "claudecode") {
		t.Fatalf("err should name the provider: %v", err)
	}
}
