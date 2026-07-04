//go:build unix

package tools

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup puts the command in its own process group and installs
// a Cancel that kills the whole group, so grandchildren spawned by `sh -c` die
// with the shell on timeout/cancellation. WaitDelay bounds Wait even if a
// surviving descendant keeps the output pipes open.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
}
