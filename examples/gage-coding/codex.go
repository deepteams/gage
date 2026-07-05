package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/codex"
	"github.com/deepteams/gage/providers/shared/oauth"
)

// This file shows the second way of connecting a provider: an OAuth
// "subscription" flow instead of an API key. `go run . -login codex` runs the
// interactive PKCE login against the ChatGPT account once; afterwards the
// codex provider picks up the stored tokens and refreshes them transparently.
// providers/claudecode works the same way for Claude subscriptions.

// codexTokenPath stores the ChatGPT OAuth tokens outside the workspace, under
// the user config dir, so they never end up in a repo. gage never hard-codes
// token paths in providers: where credentials live is the consumer's choice,
// expressed through the gage.TokenStore port.
func codexTokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gage-coding", "codex.json"), nil
}

// codexStore returns the file-backed TokenStore for the Codex credentials.
// For production use, prefer oauth.NewEncryptedFileStore or an OS keychain.
func codexStore() (gage.TokenStore, error) {
	path, err := codexTokenPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return oauth.NewFileStore(path), nil
}

// codexLogin runs the interactive OAuth (PKCE) flow: it briefly binds the
// localhost callback the Codex client id expects, opens the browser on the
// authorization URL, and persists the tokens in the store.
func codexLogin(ctx context.Context) error {
	store, err := codexStore()
	if err != nil {
		return err
	}
	if _, err := codex.Login(ctx, store, func(url string) {
		fmt.Println("open this URL to authorize gage-coding with your ChatGPT account:")
		fmt.Println("  " + url)
		openBrowser(url)
	}); err != nil {
		return err
	}
	path, _ := codexTokenPath()
	fmt.Println("logged in; credentials saved to", path)
	return nil
}

// codexProvider returns a Codex provider when stored credentials exist. The
// provider refreshes expired tokens on its own and retries once on a 401.
func codexProvider(ctx context.Context, model string) (gage.Provider, string, bool) {
	store, err := codexStore()
	if err != nil {
		return nil, "", false
	}
	if _, err := store.Load(ctx); err != nil {
		return nil, "", false // no stored credentials: not logged in
	}
	if model == "" {
		model = codex.DefaultModel
	}
	return codex.New(store, codex.WithDefaultModel(model)), "codex/" + model, true
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "linux":
		_ = exec.Command("xdg-open", url).Start()
	}
}
