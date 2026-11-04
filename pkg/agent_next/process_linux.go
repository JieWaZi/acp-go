package agentclient

import (
	"errors"

	"golang.org/x/sys/unix"
)

type processExitObserver struct{}

func (*processExitObserver) prepare() error { return nil }

func (*processExitObserver) release() error { return nil }

func (*processExitObserver) wait(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func groupSignalError(_ int, err error) error { return err }
