//go:build unix

package claude

import (
	"os"
	"os/exec"
	"syscall"
)

// prepareProcess 为每个 Claude CLI 创建独立进程组，取消时回收其子进程。
func prepareProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}

// killProcessTree 终止 CLI 所在的整个进程组。
func killProcessTree(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Cancel()
	}
}
