package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/internal/jsonschema"
)

// BashConfig configures the bash tool.
type BashConfig struct {
	// Dir is the working directory for commands (default: process cwd).
	Dir string
	// Shell is the shell binary (default: "/bin/bash").
	Shell string
	// DefaultTimeout applies when the model does not specify one (default 60s).
	DefaultTimeout time.Duration
	// MaxTimeout caps any requested timeout (default 600s).
	MaxTimeout time.Duration
	// MaxOutputBytes caps combined stdout+stderr returned (default 256 KiB).
	MaxOutputBytes int
}

func (c BashConfig) shell() string {
	if c.Shell != "" {
		return c.Shell
	}
	return "/bin/bash"
}

func (c BashConfig) defaultTimeout() time.Duration {
	if c.DefaultTimeout > 0 {
		return c.DefaultTimeout
	}
	return 60 * time.Second
}

func (c BashConfig) maxTimeout() time.Duration {
	if c.MaxTimeout > 0 {
		return c.MaxTimeout
	}
	return 600 * time.Second
}

func (c BashConfig) maxOutput() int {
	if c.MaxOutputBytes > 0 {
		return c.MaxOutputBytes
	}
	return 256 << 10
}

// NewBashTool returns the bash tool.
func NewBashTool(cfg BashConfig) gage.Tool { return &bashTool{cfg} }

type bashTool struct{ cfg BashConfig }

func (t *bashTool) Name() string { return "bash" }
func (t *bashTool) Description() string {
	return "Run a shell command and return its combined stdout and stderr. Use for builds, tests, git and other CLI tasks."
}
func (t *bashTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"command":         jsonschema.Str("The shell command to execute."),
		"timeout_seconds": jsonschema.Int("Optional timeout in seconds."),
	}, "command")
}

func (t *bashTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	if args.Command == "" {
		return errResult(fmt.Errorf("command is required")), nil
	}

	timeout := t.cfg.defaultTimeout()
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	if timeout > t.cfg.maxTimeout() {
		timeout = t.cfg.maxTimeout()
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, t.cfg.shell(), "-c", args.Command)
	cmd.Dir = t.cfg.Dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.Bytes()
	if max := t.cfg.maxOutput(); len(out) > max {
		out = append(out[:max], []byte("\n... (output truncated)")...)
	}

	text := string(out)
	if cctx.Err() == context.DeadlineExceeded {
		return gage.ErrorResult("", fmt.Sprintf("command timed out after %s\n%s", timeout, text)), nil
	}
	if err != nil {
		return gage.ErrorResult("", fmt.Sprintf("%s\nexit error: %v", text, err)), nil
	}
	if text == "" {
		text = "(no output)"
	}
	return gage.TextResult("", text), nil
}
