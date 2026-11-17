//go:build darwin || linux

package agentclient

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type processTree struct {
	// observer detects the leader's exit without consuming its wait status.
	observer processExitObserver
}

func platformExecutable(path string, _ []string) string {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && unix.Access(path, unix.X_OK) == nil {
		return path
	}
	return ""
}

func (tree *processTree) prepare(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return tree.observer.prepare()
}

func (*processTree) afterStart(*exec.Cmd) error { return nil }

func (tree *processTree) observeExit(pid int) error { return tree.observer.wait(pid) }

func (*processTree) usesLeaderIdentity() bool { return true }

func (*processTree) terminate(child *os.Process) (bool, error) {
	err := unix.Kill(-child.Pid, unix.SIGTERM)
	if errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	return true, groupSignalError(child.Pid, err)
}

func (*processTree) stop(child *os.Process) error {
	err := unix.Kill(-child.Pid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err := groupSignalError(child.Pid, err); err != nil {
		return err
	}
	// The leader remains unreaped, so the group identity cannot be recycled.
	// A delivered signal is not an exit; retain ownership until every live member is gone.
	return waitProcessGroupExit(child.Pid, processGroupAlive)
}

// waitProcessGroupExit requires a successful empty observation; errors retain ownership.
func waitProcessGroupExit(pid int, observe func(int) (bool, error)) error {
	for {
		alive, err := observe(pid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (tree *processTree) release() error { return tree.observer.release() }

func platformExitSignal(state *os.ProcessState) string {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return status.Signal().String()
	}
	return ""
}

func platformProcessPipe() (*os.File, *os.File, error) { return os.Pipe() }
