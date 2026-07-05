package main

import (
	"fmt"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"github.com/deepteams/gage/tools"
)

// newExplorerTool builds a read-only sub-agent and exposes it to the main
// agent as an "explore" tool (agent.AsTool). Delegation keeps the parent's
// context small: the sub-agent runs its own loop — reading files, grepping —
// and only its final summary comes back as the tool result.
func newExplorerTool(provider gage.Provider, root string) (gage.Tool, error) {
	fsCfg := tools.FSConfig{Root: root}

	// Only the read-only tools, selected by their own metadata. With no way
	// to write, shell out, or reach the network, the sub-agent needs no
	// Approver of its own; the parent's Approver still gates each "explore"
	// delegation (the sub-agent tool is marked RequiresApproval).
	reg := tools.NewRegistry()
	for _, t := range append(tools.NewFSTools(fsCfg), tools.NewSearchTools(fsCfg)...) {
		if mp, ok := t.(gage.ToolMetadataProvider); ok && mp.Metadata().ReadOnly {
			reg.MustRegister(tools.LimitResultSize(t, 48<<10))
		}
	}

	explorer, err := agent.New(agent.Config{
		Name:     "explorer",
		Provider: provider,
		System: fmt.Sprintf(`You are explorer, a read-only code scout working inside %s.
All paths are relative to that root. Use glob/grep to locate code and
read_file to inspect it. You cannot modify anything.

Answer the delegated question with a concise, structured summary: relevant
file paths, key symbols, and how the pieces fit together. No preamble.`, root),
		Registry:       reg,
		MaxTurns:       12,
		MaxToolRepeats: 3,
		ToolTimeout:    time.Minute,
	})
	if err != nil {
		return nil, err
	}
	return explorer.AsTool("explore",
		"Delegate a codebase exploration question to a read-only sub-agent. "+
			"It searches and reads the workspace on its own and returns a summary, "+
			"keeping large intermediate results out of your context. "+
			"Use it for questions like 'where is X handled?' or 'how does Y work?'."), nil
}
