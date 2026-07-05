package gemini

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

// sseServer serves the given payloads as SSE data lines, capturing the
// request body, headers, and URL of the last request.
func sseServer(t *testing.T, events []string, capture *[]byte, headers *http.Header, reqURL *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		if headers != nil {
			*headers = r.Header.Clone()
		}
		if reqURL != nil {
			*reqURL = r.URL.String()
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

func collect(t *testing.T, ch <-chan gage.Event) []gage.Event {
	t.Helper()
	var events []gage.Event
	for e := range ch {
		events = append(events, e)
	}
	return events
}

func TestRequestMapping(t *testing.T) {
	events := []string{`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`}
	var reqBody []byte
	var headers http.Header
	var reqURL string
	srv := sseServer(t, events, &reqBody, &headers, &reqURL)

	p := New(Config{APIKey: "key-1", BaseURL: srv.URL, Model: "gemini-x"})
	temp, topP := 0.5, 0.9
	schema := gage.JSONSchema(`{"type":"object","properties":{"answer":{"type":"string"}}}`)
	ch, err := p.Stream(context.Background(), gage.Request{
		System: "be nice",
		Messages: []gage.Message{
			gage.UserText("q"),
			{Role: gage.RoleAssistant, Content: []gage.ContentPart{
				gage.SignedReasoningPart("let me think", "sig-1"),
				gage.ReasoningPart("unsigned thought"), // must be skipped
				gage.TextPart("using tool"),
				gage.ToolUsePart(gage.ToolCall{ID: "call_0", Name: "grep", Input: json.RawMessage(`{"q":"x"}`)}),
			}},
			gage.ToolResultMessage(gage.TextResult("call_0", "42 matches")),
			{Role: gage.RoleUser, Content: []gage.ContentPart{
				gage.TextPart("also look at these"),
				{Kind: gage.PartImage, Image: &gage.ImageSource{URL: "https://files/img1", MediaType: "image/png"}},
				gage.DocumentPart(gage.DocumentSource{MediaType: "application/pdf", Data: "cGRmYnl0ZXM="}),
			}},
		},
		Tools: []gage.ToolSchema{
			{Name: "grep", Description: "search", Parameters: gage.JSONSchema(`{"type":"object"}`)},
		},
		Options: gage.GenerateOptions{
			Temperature:     &temp,
			TopP:            &topP,
			MaxTokens:       512,
			StopSequences:   []string{"END"},
			ToolChoice:      &gage.ToolChoice{Mode: gage.ToolChoiceTool, Name: "grep"},
			ReasoningEffort: gage.ReasoningMedium,
			ResponseFormat:  &gage.ResponseFormat{Type: gage.ResponseJSONSchema, Name: "out", Schema: schema},
			PromptCache:     true, // hint: silently ignored
			Extra:           map[string]any{"cachedContent": "cachedContents/abc"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if headers.Get("x-goog-api-key") != "key-1" {
		t.Fatalf("x-goog-api-key = %q", headers.Get("x-goog-api-key"))
	}
	if !strings.HasPrefix(reqURL, "/models/gemini-x:streamGenerateContent") || !strings.Contains(reqURL, "alt=sse") {
		t.Fatalf("url = %q", reqURL)
	}

	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}

	// System prompt.
	si := body["systemInstruction"].(map[string]any)["parts"].([]any)
	if len(si) != 1 || si[0].(map[string]any)["text"] != "be nice" {
		t.Fatalf("systemInstruction = %v", si)
	}

	// Contents: user, model, tool-result-as-user, user with media.
	contents := body["contents"].([]any)
	if len(contents) != 4 {
		t.Fatalf("contents = %v", contents)
	}
	c0 := contents[0].(map[string]any)
	if c0["role"] != "user" || c0["parts"].([]any)[0].(map[string]any)["text"] != "q" {
		t.Fatalf("contents[0] = %v", c0)
	}

	// Assistant turn: signed thought (unsigned skipped), text, functionCall.
	c1 := contents[1].(map[string]any)
	if c1["role"] != "model" {
		t.Fatalf("contents[1] role = %v", c1["role"])
	}
	mp := c1["parts"].([]any)
	if len(mp) != 3 {
		t.Fatalf("model parts = %v", mp)
	}
	if p0 := mp[0].(map[string]any); p0["thought"] != true || p0["text"] != "let me think" || p0["thoughtSignature"] != "sig-1" {
		t.Fatalf("thought part = %v", p0)
	}
	if p1 := mp[1].(map[string]any); p1["text"] != "using tool" {
		t.Fatalf("text part = %v", p1)
	}
	fc := mp[2].(map[string]any)["functionCall"].(map[string]any)
	if fc["name"] != "grep" || fc["args"].(map[string]any)["q"] != "x" {
		t.Fatalf("functionCall = %v", fc)
	}

	// Tool result: user turn with a functionResponse naming the correlated tool.
	c2 := contents[2].(map[string]any)
	if c2["role"] != "user" {
		t.Fatalf("contents[2] role = %v", c2["role"])
	}
	fr := c2["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["name"] != "grep" {
		t.Fatalf("functionResponse name = %v", fr["name"])
	}
	if fr["response"].(map[string]any)["result"] != "42 matches" {
		t.Fatalf("functionResponse response = %v", fr["response"])
	}

	// Media parts: URL image → fileData, inline document → inlineData.
	c3parts := contents[3].(map[string]any)["parts"].([]any)
	fd := c3parts[1].(map[string]any)["fileData"].(map[string]any)
	if fd["fileUri"] != "https://files/img1" || fd["mimeType"] != "image/png" {
		t.Fatalf("fileData = %v", fd)
	}
	id := c3parts[2].(map[string]any)["inlineData"].(map[string]any)
	if id["mimeType"] != "application/pdf" || id["data"] != "cGRmYnl0ZXM=" {
		t.Fatalf("inlineData = %v", id)
	}

	// Tools.
	decls := body["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	d0 := decls[0].(map[string]any)
	if d0["name"] != "grep" || d0["description"] != "search" || d0["parameters"].(map[string]any)["type"] != "object" {
		t.Fatalf("functionDeclarations = %v", decls)
	}

	// Tool choice: specific tool → ANY + allowedFunctionNames.
	fcc := body["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if fcc["mode"] != "ANY" {
		t.Fatalf("functionCallingConfig = %v", fcc)
	}
	if names := fcc["allowedFunctionNames"].([]any); len(names) != 1 || names[0] != "grep" {
		t.Fatalf("allowedFunctionNames = %v", fcc["allowedFunctionNames"])
	}

	// Generation config.
	gc := body["generationConfig"].(map[string]any)
	if gc["temperature"] != 0.5 || gc["topP"] != 0.9 || gc["maxOutputTokens"] != float64(512) {
		t.Fatalf("generationConfig = %v", gc)
	}
	if stops := gc["stopSequences"].([]any); len(stops) != 1 || stops[0] != "END" {
		t.Fatalf("stopSequences = %v", gc["stopSequences"])
	}
	if gc["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType = %v", gc["responseMimeType"])
	}
	rs := gc["responseJsonSchema"].(map[string]any)
	if rs["type"] != "object" || rs["properties"] == nil {
		t.Fatalf("responseJsonSchema = %v", rs)
	}
	tc := gc["thinkingConfig"].(map[string]any)
	if tc["includeThoughts"] != true || tc["thinkingBudget"] != float64(8192) {
		t.Fatalf("thinkingConfig = %v", tc)
	}

	// Extra merged verbatim at the top level; PromptCache adds nothing.
	if body["cachedContent"] != "cachedContents/abc" {
		t.Fatalf("extra not merged: %v", body)
	}
}

func TestResponseJSONMode(t *testing.T) {
	events := []string{`{"candidates":[{"content":{"parts":[{"text":"{}"}]},"finishReason":"STOP"}]}`}
	var reqBody []byte
	srv := sseServer(t, events, &reqBody, nil, nil)
	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	ch, err := p.Stream(context.Background(), gage.Request{
		Messages: []gage.Message{gage.UserText("hi")},
		Options:  gage.GenerateOptions{ResponseFormat: &gage.ResponseFormat{Type: gage.ResponseJSON}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	gc := body["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" {
		t.Fatalf("generationConfig = %v", gc)
	}
	if gc["responseJsonSchema"] != nil {
		t.Fatalf("unexpected responseJsonSchema: %v", gc)
	}
}

func TestEventSequenceWithThinking(t *testing.T) {
	events := []string{
		`{"candidates":[{"content":{"parts":[{"text":"hm","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"mm","thought":true,"thoughtSignature":"sig-abc"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":" there"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":9,"thoughtsTokenCount":4,"cachedContentTokenCount":3}}`,
	}
	srv := sseServer(t, events, nil, nil, nil)
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
	if reasoning.String() != "hmmm" || text.String() != "Hi there" {
		t.Fatalf("reasoning=%q text=%q", reasoning.String(), text.String())
	}
	if len(sigs) != 1 || sigs[0] != "sig-abc" {
		t.Fatalf("signatures = %v", sigs)
	}
	if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 9 ||
		usage.ReasoningTokens != 4 || usage.CacheReadTokens != 3 {
		t.Fatalf("usage = %+v", usage)
	}
	if stop != gage.StopEndTurn {
		t.Fatalf("stop = %q", stop)
	}
	want := []gage.EventType{
		gage.EventMessageStart,
		gage.EventReasoningDelta, gage.EventReasoningDelta, gage.EventReasoningDone,
		gage.EventTextDelta, gage.EventTextDelta,
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
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"grep","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3}}`,
	}
	srv := sseServer(t, events, nil, nil, nil)
	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	events2 := collect(t, ch)

	var types []gage.EventType
	var start, done *gage.ToolCall
	var stop gage.StopReason
	for _, e := range events2 {
		types = append(types, e.Type)
		switch e.Type {
		case gage.EventToolCallStart:
			start = e.ToolCall
		case gage.EventToolCallDone:
			done = e.ToolCall
		case gage.EventMessageDone:
			stop = e.StopReason
		}
	}
	want := []gage.EventType{
		gage.EventMessageStart,
		gage.EventToolCallStart, gage.EventToolCallDelta, gage.EventToolCallDone,
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
	if start == nil || start.ID != "call_0" || start.Name != "grep" {
		t.Fatalf("start = %+v", start)
	}
	if done == nil || done.ID != "call_0" || string(done.Input) != `{"q":"x"}` {
		t.Fatalf("done = %+v", done)
	}
	// Gemini reports STOP on function-calling turns; a tool call means tool_use.
	if stop != gage.StopToolUse {
		t.Fatalf("stop = %q", stop)
	}
}

func TestStopReasons(t *testing.T) {
	cases := []struct {
		finish string
		want   gage.StopReason
	}{
		{"MAX_TOKENS", gage.StopMaxTokens},
		{"SAFETY", gage.StopReason("SAFETY")}, // unknown values pass through
	}
	for _, tc := range cases {
		events := []string{`{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"` + tc.finish + `"}]}`}
		srv := sseServer(t, events, nil, nil, nil)
		p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
		ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
		if err != nil {
			t.Fatal(err)
		}
		var stop gage.StopReason
		for e := range ch {
			if e.Type == gage.EventMessageDone {
				stop = e.StopReason
			}
		}
		if stop != tc.want {
			t.Fatalf("finish %q: stop = %q, want %q", tc.finish, stop, tc.want)
		}
	}
}

func TestStreamErrorChunk(t *testing.T) {
	events := []string{`{"error":{"code":500,"message":"boom"}}`}
	srv := sseServer(t, events, nil, nil, nil)
	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	last := evs[len(evs)-1]
	if last.Type != gage.EventError || !strings.Contains(last.ErrorString, "boom") {
		t.Fatalf("last event = %+v", last)
	}
}

func TestUnsupportedFailFast(t *testing.T) {
	// None of these may dial: the base URL is unroutable.
	p := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:0", Model: "m"})
	cases := []struct {
		name string
		opts gage.GenerateOptions
	}{
		{"response_format", gage.GenerateOptions{ResponseFormat: &gage.ResponseFormat{Type: "xml"}}},
		{"reasoning_effort", gage.GenerateOptions{ReasoningEffort: "ultra"}},
		{"tool_choice", gage.GenerateOptions{ToolChoice: &gage.ToolChoice{Mode: "weird"}}},
	}
	for _, tc := range cases {
		_, err := p.Stream(context.Background(), gage.Request{
			Messages: []gage.Message{gage.UserText("hi")},
			Options:  tc.opts,
		})
		if !errors.Is(err, gage.ErrUnsupported) {
			t.Fatalf("%s: err = %v, want ErrUnsupported", tc.name, err)
		}
	}
}

func TestModelRequired(t *testing.T) {
	p := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:0"})
	if _, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}}); err == nil {
		t.Fatal("expected an error for a missing model")
	}
}

func TestAPIErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := New(Config{APIKey: "wrong", BaseURL: srv.URL, Model: "m"})
	_, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	var apiErr *gage.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, gage.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth match", err)
	}
}

func TestMissingAPIKey(t *testing.T) {
	p := New(Config{BaseURL: "http://127.0.0.1:0", Model: "m"})
	_, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if !errors.Is(err, gage.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestCountTokens(t *testing.T) {
	var reqBody []byte
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		reqBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"totalTokens":42}`)
	}))
	t.Cleanup(srv.Close)

	p := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "gemini-x"})
	n, err := p.CountTokens(context.Background(), gage.Request{
		System:   "sys",
		Messages: []gage.Message{gage.UserText("hi")},
		Tools:    []gage.ToolSchema{{Name: "grep", Parameters: gage.JSONSchema(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Fatalf("tokens = %d", n)
	}
	if path != "/models/gemini-x:countTokens" {
		t.Fatalf("path = %q", path)
	}
	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["contents"] == nil || body["systemInstruction"] == nil || body["tools"] == nil {
		t.Fatalf("count body = %v", body)
	}
}

func TestModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","inputTokenLimit":1048576,"outputTokenLimit":65536}],"nextPageToken":"tok"}`)
			return
		}
		io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","inputTokenLimit":1048576,"outputTokenLimit":65536}]}`)
	}))
	t.Cleanup(srv.Close)

	p := New(Config{APIKey: "k", BaseURL: srv.URL})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %v", models)
	}
	if models[0].ID != "gemini-2.5-pro" || models[0].Name != "Gemini 2.5 Pro" ||
		models[0].ContextWindow != 1048576 || models[0].MaxOutputTokens != 65536 {
		t.Fatalf("models[0] = %+v", models[0])
	}
	if models[1].ID != "gemini-2.5-flash" {
		t.Fatalf("models[1] = %+v", models[1])
	}
}

func TestNameAndDefaults(t *testing.T) {
	p := New(Config{APIKey: "k"})
	if p.Name() != "gemini" {
		t.Fatalf("name = %q", p.Name())
	}
	if p.BaseURL != DefaultBaseURL {
		t.Fatalf("base = %q", p.BaseURL)
	}
	p2 := New(Config{APIKey: "k", ProviderName: "custom"})
	if p2.Name() != "custom" {
		t.Fatalf("name = %q", p2.Name())
	}
}

func TestModelPrefixStripped(t *testing.T) {
	events := []string{`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`}
	var reqURL string
	srv := sseServer(t, events, nil, nil, &reqURL)
	p := New(Config{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), gage.Request{
		Model:    "models/gemini-x",
		Messages: []gage.Message{gage.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
	if !strings.HasPrefix(reqURL, "/models/gemini-x:streamGenerateContent") {
		t.Fatalf("url = %q", reqURL)
	}
}
