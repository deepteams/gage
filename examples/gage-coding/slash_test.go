package main

import (
	"context"
	"strings"
	"testing"
)

func TestHandleSlashInfoCommands(t *testing.T) {
	app := &appRuntime{
		root:      "/tmp/work",
		modelID:   "test-model",
		sessionID: "demo",
		cfg: appConfig{
			Path:        "/tmp/work/.gage-coding.jsonc",
			Model:       "configured-model",
			DefaultMode: "build",
			Permission:  []byte(`{"read":"allow"}`),
		},
		commands:  &commandSet{commands: map[string]customCommand{"plan": {Name: "plan"}}},
		snapshots: newSnapshotManager(t.TempDir()),
	}

	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "status", line: "/status", want: []string{"mode: build", "model: test-model", "session: demo", "custom commands: 1"}},
		{name: "root", line: "/root", want: []string{"/tmp/work"}},
		{name: "model", line: "/model", want: []string{"test-model"}},
		{name: "config", line: "/config", want: []string{"path: /tmp/work/.gage-coding.jsonc", "model: configured-model", "default_mode: build"}},
		{name: "permissions", line: "/permissions", want: []string{"\"read\": \"allow\""}},
		{name: "help", line: "/help", want: []string{"/status", "/permissions", "/undo [list]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := app.HandleSlash(context.Background(), tt.line, modeBuild)
			if err != nil {
				t.Fatalf("HandleSlash(%q) error = %v", tt.line, err)
			}
			for _, want := range tt.want {
				if !strings.Contains(got.Output, want) {
					t.Errorf("HandleSlash(%q) output missing %q:\n%s", tt.line, want, got.Output)
				}
			}
		})
	}
}

func TestReadOnlyFiltersMutatingTools(t *testing.T) {
	app := &appRuntime{
		root:      t.TempDir(),
		snapshots: newSnapshotManager(t.TempDir()),
		commands:  &commandSet{},
		readOnly:  true,
	}

	for _, tool := range app.toolsForMode(modeBuild) {
		switch tool.Name() {
		case "bash", "write_file", "edit":
			t.Fatalf("readonly build mode exposed mutating tool %q", tool.Name())
		}
	}
}
