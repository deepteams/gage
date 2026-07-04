package gage

import (
	"errors"
	"fmt"
)

// Sentinel errors returned across the library. Callers can test them with
// errors.Is.
var (
	// ErrAuth indicates missing, invalid or expired credentials.
	ErrAuth = errors.New("gage: authentication failed")
	// ErrRateLimited indicates the provider throttled the request (HTTP 429).
	ErrRateLimited = errors.New("gage: rate limited")
	// ErrToolNotFound indicates a tool call referenced an unregistered tool.
	ErrToolNotFound = errors.New("gage: tool not found")
	// ErrMaxTurns indicates the agent loop hit its turn budget without a final answer.
	ErrMaxTurns = errors.New("gage: max turns exceeded")
	// ErrNoProvider indicates an agent was configured without a Provider.
	ErrNoProvider = errors.New("gage: no provider configured")
)

// APIError wraps a non-2xx HTTP response from a provider or search backend.
type APIError struct {
	// Provider names the adapter that produced the error.
	Provider string
	// Status is the HTTP status code.
	Status int
	// Body is the (possibly truncated) response body for diagnostics.
	Body string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gage: %s API error: status %d: %s", e.Provider, e.Status, e.Body)
}

// Is lets APIError match the sentinel errors for common status codes so callers
// can write errors.Is(err, ErrAuth) / ErrRateLimited regardless of provider.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrAuth:
		return e.Status == 401 || e.Status == 403
	case ErrRateLimited:
		return e.Status == 429
	}
	return false
}
