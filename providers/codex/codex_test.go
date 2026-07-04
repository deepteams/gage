package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared/oauth"
)

func TestAccountIDFromClaims(t *testing.T) {
	if got := accountIDFromClaims(map[string]any{"chatgpt_account_id": "a1"}); got != "a1" {
		t.Fatalf("direct = %q", got)
	}
	nested := map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "a2"},
	}
	if got := accountIDFromClaims(nested); got != "a2" {
		t.Fatalf("nested = %q", got)
	}
	orgs := map[string]any{
		"https://api.openai.com/auth": map[string]any{"organizations": []any{map[string]any{"id": "org9"}}},
	}
	if got := accountIDFromClaims(orgs); got != "org9" {
		t.Fatalf("orgs = %q", got)
	}
}

func TestDecorateAccountIDFromJWT(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"chatgpt_account_id": "acct-x"})
	tok := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	cr := &gage.Credentials{AccessToken: tok, Extra: map[string]string{}}
	if err := decorateAccountID(cr); err != nil {
		t.Fatal(err)
	}
	if cr.AccountID != "acct-x" {
		t.Fatalf("account id = %q", cr.AccountID)
	}
}

func TestProviderSendsAuthHeaders(t *testing.T) {
	var gotAuth, gotAcct, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAcct = r.Header.Get("ChatGPT-Account-Id")
		gotBeta = r.Header.Get("OpenAI-Beta")
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	t.Cleanup(srv.Close)

	store := oauth.NewMemoryStoreWith(gage.Credentials{
		AccessToken: "tok-abc",
		ExpiresAt:   time.Now().Add(time.Hour),
		AccountID:   "acct-777",
	})
	p := New(store, WithResponsesURL(srv.URL))
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotAcct != "acct-777" {
		t.Fatalf("account = %q", gotAcct)
	}
	if gotBeta != "responses=experimental" {
		t.Fatalf("beta = %q", gotBeta)
	}
}
