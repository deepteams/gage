package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/deepteams/gage"
)

// terminalApprover gates tool execution from the terminal. Read-only local
// tools (read_file, list_dir, grep, glob) run without asking; anything that
// writes, shells out, or touches the network prompts. Answering "a" sets
// Approval.Remember, which the gage.RememberingPerInput wrapper in main caches
// by tool + exact input — approving one command never green-lights another.
type terminalApprover struct {
	in  *bufio.Reader
	out io.Writer
}

func (a *terminalApprover) Approve(_ context.Context, req gage.PermissionRequest) (gage.Approval, error) {
	m := req.Metadata
	if m.ReadOnly && !m.Destructive && !m.Network {
		return gage.Allowed(), nil
	}

	// Summary is filled by the agent from the tool's ToolCallDescriber; fall
	// back to the raw input for tools that do not describe their calls.
	summary := req.Summary
	if summary == "" {
		summary = req.Tool + " " + string(req.Input)
	}
	fmt.Fprintf(a.out, "\n\x1b[33m⚠ %s\x1b[0m\n  allow? [y]es / [a]lways for this exact call / [N]o: ", summary)

	line, err := a.in.ReadString('\n')
	if err != nil {
		return gage.Denied("approval prompt unavailable"), nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return gage.Allowed(), nil
	case "a", "always":
		return gage.Approval{Allow: true, Remember: true}, nil
	default:
		return gage.Denied("the user denied this call; explain what you wanted to do or try another approach"), nil
	}
}
