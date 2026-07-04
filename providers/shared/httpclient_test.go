package shared

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRetriesOn503(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Body must survive retries.
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	}))
	t.Cleanup(srv.Close)

	c := NewClient("test")
	c.BaseDelay = time.Millisecond
	// Speed up sleeps.
	c.sleep = func(context.Context, time.Duration) error { return nil }

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "payload" {
		t.Fatalf("body after retries = %q", body)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestClientGivesUpAfterMaxRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := NewClient("test")
	c.MaxRetries = 2
	c.sleep = func(context.Context, time.Duration) error { return nil }

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&calls) != 3 { // initial + 2 retries
		t.Fatalf("calls = %d", calls)
	}
}

func TestBackoffRespectsRetryAfter(t *testing.T) {
	c := NewClient("test")
	if got := c.backoff(0, "2"); got != 2*time.Second {
		t.Fatalf("backoff with Retry-After = %v", got)
	}
	if got := c.backoff(1, ""); got != 2*c.BaseDelay {
		t.Fatalf("backoff exponential = %v", got)
	}
}
