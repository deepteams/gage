package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/claudecode"
	"github.com/deepteams/gage/providers/shared/oauth"
)

func claudeTokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gage-coding", "claudecode.json"), nil
}

func claudeStore() (gage.TokenStore, error) {
	path, err := claudeTokenPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return oauth.NewFileStore(path), nil
}

func claudeLogin(ctx context.Context, console bool) error {
	store, err := claudeStore()
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
	path, _ := claudeTokenPath()
	fmt.Println("logged in; credentials saved to", path)
	return nil
}

func claudeProvider(ctx context.Context, model string) (gage.Provider, string, bool) {
	store, err := claudeStore()
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
