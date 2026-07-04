package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/deepteams/gage"
)

// LocalLogin runs the interactive authorization-code flow using a local HTTP
// server as the OAuth redirect target. It:
//
//  1. generates a PKCE pair,
//  2. calls open with the authorization URL (typically to launch a browser),
//  3. serves listenAddr until the provider redirects back with a code,
//  4. exchanges the code for credentials and stores them.
//
// listenAddr must match the host:port of Config.RedirectURI (e.g.
// "localhost:1455"). This is a CLI/login helper: the temporary server is bound
// only for the duration of the flow. It is never used to serve application
// traffic.
func LocalLogin(ctx context.Context, cfg *Config, store gage.TokenStore, listenAddr string, open func(url string)) (gage.Credentials, error) {
	p, err := GeneratePKCE()
	if err != nil {
		return gage.Credentials{}, err
	}
	redirectPath := "/"
	if u, err := url.Parse(cfg.RedirectURI); err == nil && u.Path != "" {
		redirectPath = u.Path
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return gage.Credentials{}, fmt.Errorf("oauth: listen %s: %w", listenAddr, err)
	}
	defer ln.Close()

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(redirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errStr := q.Get("error"); errStr != "" {
			http.Error(w, "authorization failed: "+errStr, http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("oauth: authorization error: %s", errStr)}
			return
		}
		if q.Get("state") != p.State {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("oauth: state mismatch")}
			return
		}
		code := q.Get("code")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Login complete. You can close this window.</body></html>"))
		resCh <- result{code: code}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	if open != nil {
		open(cfg.AuthCodeURL(p))
	}

	select {
	case <-ctx.Done():
		return gage.Credentials{}, ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			return gage.Credentials{}, res.err
		}
		return completeLogin(ctx, cfg, store, res.code, p.Verifier)
	}
}

// ManualLogin returns the authorization URL and a completion function, for
// providers whose redirect is a copy-paste flow (e.g. Claude Code). The user
// visits the URL, authorizes, then pastes the returned value — which may be in
// the form "code#state" — into complete.
func ManualLogin(cfg *Config) (authURL string, complete func(ctx context.Context, store gage.TokenStore, pasted string) (gage.Credentials, error), err error) {
	p, err := GeneratePKCE()
	if err != nil {
		return "", nil, err
	}
	authURL = cfg.AuthCodeURL(p)
	complete = func(ctx context.Context, store gage.TokenStore, pasted string) (gage.Credentials, error) {
		code, state := splitCodeState(pasted)
		if state != "" && state != p.State {
			return gage.Credentials{}, fmt.Errorf("oauth: state mismatch")
		}
		return completeLogin(ctx, cfg, store, code, p.Verifier)
	}
	return authURL, complete, nil
}

func splitCodeState(pasted string) (code, state string) {
	pasted = strings.TrimSpace(pasted)
	if i := strings.IndexByte(pasted, '#'); i >= 0 {
		return pasted[:i], pasted[i+1:]
	}
	return pasted, ""
}

func completeLogin(ctx context.Context, cfg *Config, store gage.TokenStore, code, verifier string) (gage.Credentials, error) {
	cr, err := cfg.Exchange(ctx, code, verifier)
	if err != nil {
		return gage.Credentials{}, err
	}
	if err := store.Save(ctx, cr); err != nil {
		return gage.Credentials{}, err
	}
	return cr, nil
}
