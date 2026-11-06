//go:build !darwin && !linux && !windows

package agentclient

import (
	"os"
	"os/exec"
)

type processTree struct{}

func platformExecutable(path string, _ []string) string {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}

func (*processTree) prepare(*exec.Cmd) error {
	return errProcessUnsupported
}

func (*processTree) afterStart(*exec.Cmd) error { return nil }

func (*processTree) observeExit(int) error { return nil }

func (*processTree) usesLeaderIdentity() bool { return false }

func (*processTree) terminate(*os.Process) (bool, error) { return false, nil }

func (*processTree) stop(*os.Process) error { return nil }

func (*processTree) release() error { return nil }

func platformExitSignal(*os.ProcessState) string { return "" }

func platformProcessPipe() (*os.File, *os.File, error) { return os.Pipe() }
