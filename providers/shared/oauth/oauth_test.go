package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/deepteams/gage"
)

func TestGeneratePKCE(t *testing.T) {
	p, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Verifier == "" || p.Challenge == "" || p.State == "" {
		t.Fatalf("empty PKCE: %+v", p)
	}
	if p.Verifier == p.Challenge {
		t.Fatal("challenge should differ from verifier")
	}
}

func TestAuthCodeURL(t *testing.T) {
	cfg := &Config{
		ClientID:        "cid",
		AuthorizeURL:    "https://auth.example/authorize",
		RedirectURI:     "http://localhost:1455/auth/callback",
		Scopes:          []string{"a", "b"},
		ExtraAuthParams: map[string]string{"flow": "simple"},
	}
	u := cfg.AuthCodeURL(PKCE{Challenge: "chal", State: "st"})
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("client_id") != "cid" || q.Get("code_challenge") != "chal" ||
		q.Get("code_challenge_method") != "S256" || q.Get("scope") != "a b" ||
		q.Get("flow") != "simple" || q.Get("state") != "st" {
		t.Fatalf("query = %v", q)
	}
}

func TestRefreshProactiveAndPersist(t *testing.T) {
	var refreshCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %s", r.Form.Get("grant_type"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"expires_in":   3600,
			// no refresh_token: must keep the old one
		})
	}))
	t.Cleanup(srv.Close)

	fixed := time.Unix(1_000_000, 0)
	cfg := &Config{ClientID: "cid", TokenURL: srv.URL, Now: func() time.Time { return fixed }}
	store := NewMemoryStoreWith(gage.Credentials{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    fixed.Add(10 * time.Second), // within skew -> refresh
		AccountID:    "acct-1",
	})
	ts := &TokenSource{Config: cfg, Store: store}

	cr, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.AccessToken != "new-access" {
		t.Fatalf("access = %q", cr.AccessToken)
	}
	if cr.RefreshToken != "refresh-1" {
		t.Fatalf("refresh token not preserved: %q", cr.RefreshToken)
	}
	if cr.AccountID != "acct-1" {
		t.Fatalf("account id not preserved: %q", cr.AccountID)
	}
	// Persisted?
	saved, _ := store.Load(context.Background())
	if saved.AccessToken != "new-access" {
		t.Fatalf("not persisted: %q", saved.AccessToken)
	}

	// Second call: token now valid -> no extra refresh.
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
}

func TestExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "the-code" ||
			r.Form.Get("code_verifier") != "the-verifier" {
			t.Errorf("form = %v", r.Form)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc",
			"refresh_token": "ref",
			"expires_in":    3600,
			"scope":         "s1 s2",
		})
	}))
	t.Cleanup(srv.Close)

	cfg := &Config{ClientID: "cid", TokenURL: srv.URL}
	cr, err := cfg.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if cr.AccessToken != "acc" || cr.RefreshToken != "ref" || cr.Extra["scope"] != "s1 s2" {
		t.Fatalf("cr = %+v", cr)
	}
	if cr.ExpiresAt.IsZero() {
		t.Fatal("expires not set")
	}
}

func TestDecodeJWTClaims(t *testing.T) {
	claims := map[string]any{
		"chatgpt_account_id": "acct-42",
		"https://api.openai.com/auth": map[string]any{
			"organizations": []any{map[string]any{"id": "org-1"}},
		},
	}
	payload, _ := json.Marshal(claims)
	tok := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	got, err := DecodeJWTClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got["chatgpt_account_id"] != "acct-42" {
		t.Fatalf("claims = %v", got)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	path := t.TempDir() + "/nested/auth.json"
	fs := NewFileStore(path)
	if _, err := fs.Load(context.Background()); err == nil {
		t.Fatal("expected error for missing file")
	}
	in := gage.Credentials{AccessToken: "a", RefreshToken: "r", AccountID: "acct"}
	if err := fs.Save(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	out, err := fs.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessToken != "a" || out.AccountID != "acct" {
		t.Fatalf("out = %+v", out)
	}
}
