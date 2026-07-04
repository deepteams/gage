package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared/oauth"
)

func TestSystemSpoofFirstBlock(t *testing.T) {
	blocks := systemBlocks("real system prompt")
	if len(blocks) != 2 {
		t.Fatalf("blocks = %v", blocks)
	}
	if blocks[0]["text"] != SystemSpoof {
		t.Fatalf("first block = %v", blocks[0])
	}
	if blocks[1]["text"] != "real system prompt" {
		t.Fatalf("second block = %v", blocks[1])
	}
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
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}`,
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

	store := oauth.NewMemoryStoreWith(gage.Credentials{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)})
	p := New(store, false, WithMessagesURL(srv.URL))
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
	if text.String() != "Hi" {
		t.Fatalf("text = %q", text.String())
	}
	if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", usage)
	}
	if stop != "end_turn" {
		t.Fatalf("stop = %q", stop)
	}
	if gotAuth != "Bearer tok" || !strings.Contains(gotBeta, "oauth-2025-04-20") || gotVersion != AnthropicVersion {
		t.Fatalf("headers auth=%q beta=%q version=%q", gotAuth, gotBeta, gotVersion)
	}

	// Verify the spoof block was sent first.
	var body map[string]any
	json.Unmarshal(reqBody, &body)
	sys := body["system"].([]any)
	if sys[0].(map[string]any)["text"] != SystemSpoof {
		t.Fatalf("system spoof missing: %v", sys)
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

	store := oauth.NewMemoryStoreWith(gage.Credentials{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)})
	p := New(store, false, WithMessagesURL(srv.URL))
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("run ls")}})
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
	if done == nil || done.Name != "bash" || string(done.Input) != `{"cmd":"ls"}` {
		t.Fatalf("done = %+v", done)
	}
	if stop != "tool_use" {
		t.Fatalf("stop = %q", stop)
	}
}
