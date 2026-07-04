package shared

import (
	"context"

	"github.com/deepteams/gage"
)

// Send delivers ev on out unless ctx is cancelled first. It reports whether
// the event was sent. Pump goroutines use it so every channel send also
// selects on ctx.Done().
func Send(ctx context.Context, out chan<- gage.Event, ev gage.Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
