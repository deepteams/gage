// Package httpx exposes a gage agent's event stream over HTTP using Server-Sent
// Events. It provides http.Handler values only; it never starts a server —
// mounting and serving is the consumer's responsibility.
package httpx

import (
	"encoding/json"
	"io"

	"github.com/deepteams/gage"
)

// WriteSSE writes a single gage.Event as one SSE frame:
//
//	event: <type>
//	data: <json>
//
// followed by a blank line. The JSON omits the non-serializable Err field
// (ErrorString carries the message). It returns any write error.
func WriteSSE(w io.Writer, e gage.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "event: "+string(e.Type)+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}
