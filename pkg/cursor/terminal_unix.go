//go:build unix

package cursor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepareTerminalProcess 配合 creack/pty 的独立会话，在取消时终止完整进程组。
func prepareTerminalProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
