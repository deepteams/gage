package structured

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/tools"
)

type person struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type scriptedReply struct {
	text  string
	usage gage.Usage
}

// stubProvider replays one scripted reply per Stream call, following the
// provider event contract: message_start → text_delta* → usage →
// message_done → close. With rejectFormat set it fails any request carrying a
// ResponseFormat with gage.ErrUnsupported before streaming begins.
type stubProvider struct {
	mu           sync.Mutex
	replies      []scriptedReply
	call         int
	reqs         []gage.Request
	rejectFormat bool
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	if s.rejectFormat && req.Options.ResponseFormat != nil {
		s.mu.Unlock()
		return nil, gage.Unsupported("stub", "ResponseFormat")
	}
	i := s.call
	s.call++
	s.mu.Unlock()

	rep := scriptedReply{text: "{}"}
	if i < len(s.replies) {
		rep = s.replies[i]
	}
	ch := make(chan gage.Event)
	go func() {
		defer close(ch)
		for _, e := range []gage.Event{
			gage.MessageStart(),
			gage.TextDelta(rep.text),
			gage.UsageEvent(rep.usage),
			gage.MessageDone(gage.StopEndTurn),
		} {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (s *stubProvider) requests() []gage.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gage.Request(nil), s.reqs...)
}

func TestGenerateHappyPath(t *testing.T) {
	sp := &stubProvider{replies: []scriptedReply{
		{text: `{"name":"Ada","age":36}`, usage: gage.Usage{InputTokens: 10, OutputTokens: 5}},
	}}
	got, usage, err := Generate[person](context.Background(), sp, gage.Request{
		Messages: []gage.Message{gage.UserText("who wrote the first program?")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada" || got.Age != 36 {
		t.Fatalf("decoded = %+v", got)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", usage)
	}
	reqs := sp.requests()
	if len(reqs) != 1 {
		t.Fatalf("provider called %d times, want 1", len(reqs))
	}
	rf := reqs[0].Options.ResponseFormat
	if rf == nil {
		t.Fatal("ResponseFormat not forced onto the request")
	}
	if rf.Type != gage.ResponseJSONSchema || rf.Name != "person" || !rf.Strict {
		t.Fatalf("response format = %+v", rf)
	}
	if string(rf.Schema) != string(tools.SchemaOf[person]()) {
		t.Fatalf("schema = %s", rf.Schema)
	}
}

func TestGenerateStripsFences(t *testing.T) {
	sp := &stubProvider{replies: []scriptedReply{
		{text: "```json\n{\"name\":\"Ada\",\"age\":36}\n```"},
	}}
	got, _, err := Generate[person](context.Background(), sp, gage.Request{
		Messages: []gage.Message{gage.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada" || got.Age != 36 {
		t.Fatalf("decoded = %+v", got)
	}
	if len(sp.requests()) != 1 {
		t.Fatal("fenced but valid JSON must not trigger a repair round-trip")
	}
}

func TestGenerateRepairLoop(t *testing.T) {
	sp := &stubProvider{replies: []scriptedReply{
		{text: "Sure! Here it is: name=Ada", usage: gage.Usage{InputTokens: 1, OutputTokens: 2}},
		{text: `{"name":"Ada","age":`, usage: gage.Usage{InputTokens: 3, OutputTokens: 4}},
		{text: `{"name":"Ada","age":36}`, usage: gage.Usage{InputTokens: 5, OutputTokens: 6}},
	}}
	got, usage, err := Generate[person](context.Background(), sp, gage.Request{
		System:   "be terse",
		Messages: []gage.Message{gage.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada" || got.Age != 36 {
		t.Fatalf("decoded = %+v", got)
	}
	// Usage accumulates across all three attempts.
	if usage.InputTokens != 9 || usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v", usage)
	}
	reqs := sp.requests()
	if len(reqs) != 3 {
		t.Fatalf("provider called %d times, want 3", len(reqs))
	}
	// The final request carries the growing repair conversation: original user
	// message plus assistant reply + repair demand per failed attempt.
	msgs := reqs[2].Messages
	if len(msgs) != 5 {
		t.Fatalf("final request has %d messages, want 5: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != gage.RoleAssistant || msgs[1].Text() != "Sure! Here it is: name=Ada" {
		t.Fatalf("assistant reply not replayed: %+v", msgs[1])
	}
	if msgs[2].Role != gage.RoleUser || !strings.Contains(msgs[2].Text(), "could not be decoded") ||
		!strings.Contains(msgs[2].Text(), "only valid JSON") {
		t.Fatalf("repair message = %q", msgs[2].Text())
	}
	// The repair message quotes the decode error.
	if !strings.Contains(msgs[2].Text(), "decode into") {
		t.Fatalf("repair message does not quote the decode error: %q", msgs[2].Text())
	}
}

func TestGenerateRepairExhaustion(t *testing.T) {
	sp := &stubProvider{replies: []scriptedReply{
		{text: "nope", usage: gage.Usage{OutputTokens: 1}},
		{text: "still nope", usage: gage.Usage{OutputTokens: 1}},
		{text: "never", usage: gage.Usage{OutputTokens: 1}},
	}}
	_, usage, err := Generate[person](context.Background(), sp, gage.Request{
		Messages: []gage.Message{gage.UserText("go")},
	})
	if err == nil || !strings.Contains(err.Error(), "repair attempts") {
		t.Fatalf("err = %v", err)
	}
	if n := len(sp.requests()); n != 3 {
		t.Fatalf("provider called %d times, want 3 (initial + 2 repairs)", n)
	}
	if usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want accumulation across failed attempts", usage)
	}
}

func TestGenerateUnsupportedFallback(t *testing.T) {
	sp := &stubProvider{
		rejectFormat: true,
		replies: []scriptedReply{
			{text: `{"name":"Ada","age":36}`, usage: gage.Usage{InputTokens: 7, OutputTokens: 3}},
		},
	}
	got, usage, err := Generate[person](context.Background(), sp, gage.Request{
		System:   "base prompt",
		Messages: []gage.Message{gage.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada" || got.Age != 36 {
		t.Fatalf("decoded = %+v", got)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", usage)
	}
	reqs := sp.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (rejected + fallback)", len(reqs))
	}
	if reqs[0].Options.ResponseFormat == nil {
		t.Fatal("first attempt must carry ResponseFormat")
	}
	if reqs[1].Options.ResponseFormat != nil {
		t.Fatal("fallback attempt must drop ResponseFormat")
	}
	sys := reqs[1].System
	if !strings.HasPrefix(sys, "base prompt") {
		t.Fatalf("fallback lost the base system prompt: %q", sys)
	}
	if !strings.Contains(sys, "JSON Schema") || !strings.Contains(sys, string(tools.SchemaOf[person]())) {
		t.Fatalf("fallback system prompt lacks the schema: %q", sys)
	}
}

func TestGenerateNonUnsupportedErrorSurfaces(t *testing.T) {
	// Only ErrUnsupported triggers the schema-in-system-prompt fallback; any
	// other pre-stream failure is returned to the caller as-is.
	_, _, err := Generate[person](context.Background(), failingProvider{err: gage.ErrAuth}, gage.Request{
		Messages: []gage.Message{gage.UserText("go")},
	})
	if !errors.Is(err, gage.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

type failingProvider struct{ err error }

func (f failingProvider) Name() string { return "failing" }
func (f failingProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	return nil, f.err
}

func TestDecode(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"bare", `{"name":"Ada","age":36}`},
		{"whitespace", "\n\t  {\"name\":\"Ada\",\"age\":36}  \n"},
		{"fenced", "```json\n{\"name\":\"Ada\",\"age\":36}\n```"},
		{"fenced no lang", "```\n{\"name\":\"Ada\",\"age\":36}\n```"},
		{"fenced padded", "  ```json\n{\"name\":\"Ada\",\"age\":36}\n```  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode[person](tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "Ada" || got.Age != 36 {
				t.Fatalf("decoded = %+v", got)
			}
		})
	}
	if _, err := Decode[person]("not json"); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestFromResult(t *testing.T) {
	res := &gage.Result{Text: "```json\n{\"name\":\"Ada\",\"age\":36}\n```"}
	got, err := FromResult[person](res)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada" || got.Age != 36 {
		t.Fatalf("decoded = %+v", got)
	}
	if _, err := FromResult[person](nil); err == nil {
		t.Fatal("expected error for nil result")
	}
}
