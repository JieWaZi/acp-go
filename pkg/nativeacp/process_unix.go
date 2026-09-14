//go:build unix

package nativeacp

import (
	"os"
	"os/exec"
	"syscall"
)

// prepareProcess 用标准进程组保证关闭 Adapter 时同时回收它启动的 CLI 子进程。
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
