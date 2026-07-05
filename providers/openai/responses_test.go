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
	var stop gage.StopReason
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
	if stop != gage.StopToolUse {
		t.Fatalf("stop = %q", stop)
	}
}

func TestResponsesReasoningEncryptedContent(t *testing.T) {
	events := [][2]string{
		{"response.reasoning_text.delta", `{"delta":"think"}`},
		{"response.output_item.done", `{"item":{"type":"reasoning","id":"rs_1","encrypted_content":"enc-token-xyz"}}`},
		{"response.output_text.delta", `{"delta":"done"}`},
		{"response.completed", `{"response":{"usage":{"input_tokens":1,"output_tokens":1}}}`},
	}
	var reqBody []byte
	srv := namedSSEServer(t, events, &reqBody)

	c := &ResponsesClient{ProviderName: "codex", URL: srv.URL, DefaultModel: "gpt", Store: false}
	ch, err := c.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options:  gage.GenerateOptions{ReasoningEffort: gage.ReasoningMedium},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sig string
	for e := range ch {
		if e.Type == gage.EventReasoningDone {
			sig = e.Signature
		}
	}
	if sig != "enc-token-xyz" {
		t.Fatalf("signature = %q", sig)
	}

	// The request must ask for the encrypted content when reasoning is in
	// play and store is false.
	var body map[string]any
	json.Unmarshal(reqBody, &body)
	inc, _ := body["include"].([]any)
	if len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %v", body["include"])
	}
}

func TestResponsesReasoningReplay(t *testing.T) {
	input, err := toResponsesInput([]gage.Message{
		gage.UserText("q"),
		{Role: gage.RoleAssistant, Content: []gage.ContentPart{
			gage.SignedReasoningPart("thought", "enc-abc"),
			gage.ToolUsePart(gage.ToolCall{ID: "call_1", Name: "grep", Input: gage.JSONSchema(`{"p":1}`)}),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 3 {
		t.Fatalf("input = %v", input)
	}
	// The reasoning item must precede the function_call of the same turn.
	if input[1]["type"] != "reasoning" || input[1]["encrypted_content"] != "enc-abc" {
		t.Fatalf("reasoning item = %v", input[1])
	}
	if input[2]["type"] != "function_call" {
		t.Fatalf("function_call item = %v", input[2])
	}
	// Unsigned reasoning parts are skipped.
	input, err = toResponsesInput([]gage.Message{
		{Role: gage.RoleAssistant, Content: []gage.ContentPart{gage.ReasoningPart("no token")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 0 {
		t.Fatalf("unsigned reasoning must be skipped: %v", input)
	}
}

func TestResponsesInputPreservesImages(t *testing.T) {
	input, err := toResponsesInput([]gage.Message{
		{Role: gage.RoleUser, Content: []gage.ContentPart{
			gage.TextPart("look"),
			{Kind: gage.PartImage, Image: &gage.ImageSource{URL: "https://example.com/a.png"}},
			{Kind: gage.PartImage, Image: &gage.ImageSource{MediaType: "image/png", Data: "abc123"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 1 {
		t.Fatalf("input = %v", input)
	}
	content, ok := input[0]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content = %#v", input[0]["content"])
	}
	if len(content) != 3 {
		t.Fatalf("content = %v", content)
	}
	if content[0]["type"] != "input_text" || content[0]["text"] != "look" {
		t.Fatalf("text part = %v", content[0])
	}
	if content[1]["type"] != "input_image" || content[1]["image_url"] != "https://example.com/a.png" {
		t.Fatalf("url image = %v", content[1])
	}
	if content[2]["type"] != "input_image" || content[2]["image_url"] != "data:image/png;base64,abc123" {
		t.Fatalf("inline image = %v", content[2])
	}
}

func TestResponsesToolChoiceAndFormat(t *testing.T) {
	events := [][2]string{{"response.completed", `{"response":{}}`}}
	var reqBody []byte
	srv := namedSSEServer(t, events, &reqBody)
	c := &ResponsesClient{ProviderName: "codex", URL: srv.URL, DefaultModel: "gpt"}
	tc := gage.ToolChoice{Mode: gage.ToolChoiceTool, Name: "grep"}
	ch, err := c.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options: gage.GenerateOptions{
			ToolChoice: &tc,
			ResponseFormat: &gage.ResponseFormat{
				Type: gage.ResponseJSONSchema, Name: "out",
				Schema: gage.JSONSchema(`{"type":"object"}`), Strict: true,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	var body map[string]any
	json.Unmarshal(reqBody, &body)
	choice, _ := body["tool_choice"].(map[string]any)
	if choice == nil || choice["type"] != "function" || choice["name"] != "grep" {
		t.Fatalf("tool_choice = %v", body["tool_choice"])
	}
	text, _ := body["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format == nil || format["type"] != "json_schema" || format["name"] != "out" || format["strict"] != true {
		t.Fatalf("text.format = %v", text)
	}
}

func TestResponsesStopSequencesUnsupported(t *testing.T) {
	c := &ResponsesClient{ProviderName: "codex", URL: "http://127.0.0.1:0", DefaultModel: "gpt"}
	_, err := c.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options:  gage.GenerateOptions{StopSequences: []string{"STOP"}},
	})
	if !errors.Is(err, gage.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestResponsesModelRequired(t *testing.T) {
	c := &ResponsesClient{ProviderName: "codex", URL: "http://127.0.0.1:0"}
	_, err := c.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err == nil || !strings.Contains(err.Error(), "no model specified") {
		t.Fatalf("err = %v, want no model specified", err)
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
		DefaultModel: "gpt",
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

func TestResponsesDocumentParts(t *testing.T) {
	input, err := toResponsesInput([]gage.Message{
		{Role: gage.RoleUser, Content: []gage.ContentPart{
			gage.TextPart("read"),
			gage.DocumentPart(gage.DocumentSource{Data: "cGRm", MediaType: "application/pdf", Filename: "spec.pdf"}),
			gage.DocumentPart(gage.DocumentSource{URL: "https://example.com/a.pdf"}),
			gage.DocumentPart(gage.DocumentSource{Data: "dHh0"}),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	content, ok := input[0]["content"].([]map[string]any)
	if !ok || len(content) != 4 {
		t.Fatalf("content = %#v", input[0]["content"])
	}
	// Inline document: input_file with filename + data: URL.
	if content[1]["type"] != "input_file" || content[1]["filename"] != "spec.pdf" ||
		content[1]["file_data"] != "data:application/pdf;base64,cGRm" {
		t.Fatalf("inline document = %v", content[1])
	}
	// URL document: input_file with file_url.
	if content[2]["type"] != "input_file" || content[2]["file_url"] != "https://example.com/a.pdf" {
		t.Fatalf("url document = %v", content[2])
	}
	if _, ok := content[2]["file_data"]; ok {
		t.Fatalf("unexpected file_data on url document: %v", content[2])
	}
	// Filename and media type defaults on inline documents.
	if content[3]["filename"] != "document.pdf" || content[3]["file_data"] != "data:application/pdf;base64,dHh0" {
		t.Fatalf("defaulted document = %v", content[3])
	}

	// A document with neither URL nor Data fails fast.
	if _, err := toResponsesInput([]gage.Message{
		{Role: gage.RoleUser, Content: []gage.ContentPart{gage.DocumentPart(gage.DocumentSource{})}},
	}); err == nil {
		t.Fatal("expected an error for an empty document part")
	}
}
