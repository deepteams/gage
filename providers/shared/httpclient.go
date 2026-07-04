// Package shared holds infrastructure reused by the concrete providers: an HTTP
// client with retry, and an SSE stream parser.
package shared

import (
	"context"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

// Client is an HTTP client tuned for streaming provider APIs: sane timeouts, a
// configurable User-Agent, and bounded retries on 429/5xx for the initial
// (non-streaming) response. The response body is streamed, so its read is not
// subject to the client Timeout.
type Client struct {
	HTTP       *http.Client
	UserAgent  string
	MaxRetries int
	// BaseDelay is the first backoff delay; it doubles each retry.
	BaseDelay time.Duration
	// now and sleep are injectable for tests.
	sleep func(context.Context, time.Duration) error
}

// NewClient builds a Client with defaults. userAgent may be empty.
func NewClient(userAgent string) *Client {
	return &Client{
		HTTP: &http.Client{
			// No overall timeout: streaming responses stay open. Dial/TLS/response
			// header timeouts guard the connection setup instead.
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 60 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
		},
		UserAgent:  userAgent,
		MaxRetries: 3,
		BaseDelay:  500 * time.Millisecond,
		sleep:      sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do sends the request, retrying idempotently on 429 and 5xx up to MaxRetries.
// The caller owns closing the returned response body. The provided body must be
// re-readable across retries, so callers should pass the bytes and let Do build
// a fresh reader — see DoJSON.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	sleep := c.sleep
	if sleep == nil {
		sleep = sleepCtx
	}

	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		// Reset the body for retries. http.NewRequest sets GetBody for the common
		// body types (bytes.Reader/Buffer, strings.Reader), enabling this.
		if attempt > 0 && req.GetBody != nil {
			body, gerr := req.GetBody()
			if gerr != nil {
				return nil, gerr
			}
			req.Body = body
		}
		resp, err = c.HTTP.Do(req)
		if err != nil {
			// Network error: retry unless out of budget or ctx cancelled.
			if attempt >= c.MaxRetries || req.Context().Err() != nil {
				return nil, err
			}
			if serr := sleep(req.Context(), c.backoff(attempt, "")); serr != nil {
				return nil, serr
			}
			continue
		}
		if !shouldRetry(resp.StatusCode) || attempt >= c.MaxRetries {
			return resp, nil
		}
		retryAfter := resp.Header.Get("Retry-After")
		// Drain and close before retrying so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if serr := sleep(req.Context(), c.backoff(attempt, retryAfter)); serr != nil {
			return nil, serr
		}
	}
}

func shouldRetry(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

func (c *Client) backoff(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	base := c.BaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	return d
}
