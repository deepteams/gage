package shared

import (
	"strings"
	"testing"
)

func TestScanSSE(t *testing.T) {
	input := ": comment\n" +
		"event: message\n" +
		"data: {\"a\":1}\n" +
		"\n" +
		"data: line1\n" +
		"data: line2\n" +
		"\n" +
		"data: [DONE]\n" +
		"\n"

	var events []SSEEvent
	err := ScanSSE(strings.NewReader(input), func(e SSEEvent) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	if events[0].Event != "message" || events[0].Data != `{"a":1}` {
		t.Fatalf("event0 = %+v", events[0])
	}
	// Per the SSE spec, multiple data: lines are joined with a newline.
	if events[1].Data != "line1\nline2" {
		t.Fatalf("event1 data = %q", events[1].Data)
	}
}

func TestScanSSETrailingNoBlankLine(t *testing.T) {
	// Event not terminated by a blank line should still flush at EOF.
	input := "data: hello\n"
	var got []string
	err := ScanSSE(strings.NewReader(input), func(e SSEEvent) error {
		got = append(got, e.Data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got = %v", got)
	}
}
