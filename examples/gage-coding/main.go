// Command gage-coding is an opencode-like coding agent demo built on gage.
// It wires providers, guarded coding tools, modes, skills, project rules,
// custom commands, MCP tools, undo/redo snapshots, format-on-edit, sessions,
// and a Bubble Tea TUI.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/anthropic"
	"github.com/deepteams/gage/providers/ollama"
	"github.com/deepteams/gage/providers/openrouter"
)

func main() {
	root := flag.String("root", ".", "workspace root the agent may read and modify")
	model := flag.String("model", "", "model id (default can also come from config)")
	sessionID := flag.String("session", "", "session name to persist and resume the conversation")
	auto := flag.Bool("auto", false, "auto-approve permission requests that are not denied by config")
	yolo := flag.Bool("yolo", false, "alias for -auto")
	login := flag.String("login", "", `run an OAuth login flow ("codex", "claude", "claude-console") and exit`)
	skillsDir := flag.String("skills", ".agents/skills", "directory of SKILL.md skill folders, resolved against -root")
	configPath := flag.String("config", "", "path to .gage-coding.json/.jsonc config (default: search in -root)")
	tui := flag.Bool("tui", true, "run the Bubble Tea TUI (set false for the plain REPL)")
	flag.Parse()

	var err error
	if *login != "" {
		err = runLogin(*login)
	} else {
		err = run(*root, *model, *sessionID, *skillsDir, *configPath, *auto || *yolo, *tui)
	}
	if err != nil && !isTerminalAbort(err) {
		fmt.Fprintln(os.Stderr, "gage-coding:", err)
		os.Exit(1)
	}
}

func runLogin(provider string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch provider {
	case "codex":
		return codexLogin(ctx)
	case "claude":
		return claudeLogin(ctx, false)
	case "claude-console":
		return claudeLogin(ctx, true)
	default:
		return fmt.Errorf("unknown login provider %q (supported: codex, claude, claude-console)", provider)
	}
}

func run(root, model, sessionID, skillsDir, configPath string, auto, useTUI bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	app, err := newRuntime(ctx, root, model, sessionID, skillsDir, configPath)
	if err != nil {
		return err
	}
	defer app.Close()

	mode := mustMode(app.cfg.DefaultMode)
	if useTUI {
		return runTUI(ctx, app, mode, auto)
	}
	return runREPL(ctx, app, mode, auto)
}

func runREPL(ctx context.Context, app *appRuntime, mode agentMode, auto bool) error {
	stdin := bufio.NewReader(os.Stdin)
	term := &terminalInteractor{in: stdin, out: os.Stdout}
	app.SetInteractors(term, term, auto)
	fmt.Printf("gage-coding · %s · root %s · mode %s (/help to list commands)\n", app.modelID, app.root, mode)
	r := &renderer{out: os.Stdout}
	for {
		fmt.Print("\n> ")
		line, err := stdin.ReadString('\n')
		if err != nil {
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			res, err := app.HandleSlash(ctx, line, mode)
			if err != nil {
				fmt.Println("error:", err)
				continue
			}
			if res.Quit {
				return nil
			}
			if res.SetMode != nil {
				mode = *res.SetMode
			}
			if res.Output != "" {
				fmt.Println(res.Output)
			}
			if res.Prompt == "" {
				continue
			}
			line = res.Prompt
			mode = res.Mode
			fmt.Printf("expanded command in %s mode:\n%s\n", mode, line)
		}
		_, summary, err := app.RunPrompt(ctx, line, mode, r.handle)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(os.Stderr, "\nrun failed: %v\n", err)
			continue
		}
		if summary != "" {
			fmt.Printf("\x1b[2m%s\x1b[0m\n", summary)
		}
	}
}

// pickProvider selects a backend. Everything downstream only sees
// gage.Provider — swapping providers never touches the agent code.
func pickProvider(ctx context.Context, model string) (gage.Provider, string, error) {
	if p, id, ok := codexProvider(ctx, model); ok {
		return p, id, nil
	}
	if p, id, ok := claudeProvider(ctx, model); ok {
		return p, id, nil
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		if model == "" {
			model = "claude-sonnet-4-5"
		}
		return anthropic.New(anthropic.Config{APIKey: key, Model: model}), model, nil
	}
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		if model == "" {
			model = "anthropic/claude-sonnet-4.5"
		}
		return openrouter.New(key, openrouter.WithDefaultModel(model)), model, nil
	}
	if model == "" {
		model = "qwen3.6:35b-a3b-coding-mxfp8"
	}
	base := os.Getenv("OLLAMA_HOST")
	if base == "" {
		base = "http://localhost:11434"
	}
	return ollama.New(base, ollama.WithDefaultModel(model)), model, nil
}
