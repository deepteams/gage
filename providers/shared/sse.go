package shared

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// SSEEvent is a decoded Server-Sent Event: an optional event name and the
// concatenated data payload.
type SSEEvent struct {
	Event string
	Data  string
}

// ScanSSE reads an SSE stream from r and calls fn for each complete event
// (events are separated by a blank line). Lines beginning with "data:" are
// accumulated; a "event:" line sets the event name. Comment lines (":") and
// other fields are ignored. Scanning stops when fn returns a non-nil error,
// when the "[DONE]" sentinel data is seen, or at EOF. The "[DONE]" sentinel is
// not delivered to fn.
func ScanSSE(r io.Reader, fn func(SSEEvent) error) error {
	sc := bufio.NewScanner(r)
	// Allow large tokens (some events carry sizable JSON payloads).
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var (
		eventName string
		data      strings.Builder
	)
	flush := func() error {
		if data.Len() == 0 && eventName == "" {
			return nil
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		ev := SSEEvent{Event: eventName, Data: payload}
		eventName = ""
		data.Reset()
		if strings.TrimSpace(payload) == "[DONE]" {
			return errDone
		}
		return fn(ev)
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			// Blank line terminates the current event.
			if err := flush(); err != nil {
				if err == errDone {
					return nil
				}
				return err
			}
			continue
		}
		if line[0] == ':' {
			continue // comment / heartbeat
		}
		field, value := splitField(line)
		switch field {
		case "event":
			eventName = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		default:
			// id, retry, unknown fields: ignored.
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	// Flush any trailing event without a terminating blank line.
	if err := flush(); err != nil && err != errDone {
		return err
	}
	return nil
}

// errDone is an internal sentinel signalling the "[DONE]" marker.
var errDone = &doneError{}

type doneError struct{}

func (*doneError) Error() string { return "sse: done" }

func splitField(line []byte) (field, value string) {
	idx := bytes.IndexByte(line, ':')
	if idx < 0 {
		return string(line), ""
	}
	field = string(line[:idx])
	v := line[idx+1:]
	// A single leading space after the colon is stripped per the SSE spec.
	if len(v) > 0 && v[0] == ' ' {
		v = v[1:]
	}
	return field, string(v)
}
