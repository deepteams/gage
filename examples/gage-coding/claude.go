package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/claudecode"
)

// claudeStore returns the file-backed TokenStore for the Claude credentials.
// It shares oauthFileStore with the Codex flow (see codex.go).
func claudeStore() (gage.TokenStore, string, error) {
	return oauthFileStore("claudecode.json")
}

func claudeLogin(ctx context.Context, console bool) error {
	store, path, err := claudeStore()
	if err != nil {
		return err
	}
	authURL, complete, err := claudecode.Login(console)
	if err != nil {
		return err
	}
	fmt.Println("open this URL to authorize gage-coding with your Claude account:")
	fmt.Println("  " + authURL)
	fmt.Print("paste the returned code#state value: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	if _, err := complete(ctx, store, strings.TrimSpace(line)); err != nil {
		return err
	}
	fmt.Println("logged in; credentials saved to", path)
	return nil
}

func claudeProvider(ctx context.Context, model string) (gage.Provider, string, bool) {
	store, _, err := claudeStore()
	if err != nil {
		return nil, "", false
	}
	if _, err := store.Load(ctx); err != nil {
		return nil, "", false
	}
	if model == "" {
		model = claudecode.DefaultModel
	}
	return claudecode.New(store, false, claudecode.WithDefaultModel(model)), "claudecode/" + model, true
}
