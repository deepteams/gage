//go:build !unix

package tools

import (
	"os/exec"
	"time"
)

// configureProcessGroup is a no-op beyond WaitDelay on non-unix platforms:
// there is no process group to kill, but Wait must still return after
// cancellation even if a descendant holds the output pipes open.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
}
