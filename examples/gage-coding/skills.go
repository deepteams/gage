package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/deepteams/gage/skills"
)

// Skills are Claude Code-style SKILL.md folders: the frontmatter's
// name+description are advertised in the agent's system prompt (via
// agent.Config.Skills), and the model loads a skill's full instructions on
// demand with the "skill" tool (skills.NewTool). Two demo skills ship in
// .agents/skills — the cross-tool convention for per-project skills — and
// -skills can point anywhere else.

// loadSkills loads every SKILL.md folder under dir. A missing directory just
// means "no skills" — only parse failures inside an existing dir warn.
func loadSkills(dir string) (*skills.Set, error) {
	if dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	set, err := skills.LoadDir(dir)
	if err != nil {
		// LoadDir returns the valid skills alongside an aggregated error for
		// the ones that failed to parse; keep what loaded.
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	if set.Len() == 0 {
		return nil, nil
	}
	return set, nil
}
