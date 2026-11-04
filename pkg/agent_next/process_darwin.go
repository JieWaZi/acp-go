package agentclient

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

type processExitObserver struct {
	// queue is reserved before launch and remains valid until the owner closes it.
	queue int
	// open prevents cleanup from touching an unrelated descriptor.
	open bool
}

func (observer *processExitObserver) prepare() error {
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	fd, err := unix.Kqueue()
	if err != nil {
		return err
	}
	unix.CloseOnExec(fd)
	observer.queue, observer.open = fd, true
	return nil
}

func (observer *processExitObserver) wait(pid int) error {
	var registration unix.Kevent_t
	unix.SetKevent(&registration, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	registration.Fflags = unix.NOTE_EXIT
	for {
		_, err := unix.Kevent(observer.queue, []unix.Kevent_t{registration}, nil, nil)
		if errors.Is(err, unix.ESRCH) {
			// The leader exited before registration, but its PID is still reserved.
			return nil
		}
		if !errors.Is(err, unix.EINTR) {
			if err != nil {
				return err
			}
			break
		}
	}
	var events [1]unix.Kevent_t
	for {
		_, err := unix.Kevent(observer.queue, nil, events[:], nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func (observer *processExitObserver) release() error {
	if !observer.open {
		return nil
	}
	// Do not close a recycled descriptor if close itself reports an error.
	observer.open = false
	return unix.Close(observer.queue)
}

func groupSignalError(pid int, err error) error {
	if !errors.Is(err, unix.EPERM) {
		return err
	}
	// Darwin also returns EPERM for a group containing only zombies. Do not
	// mistake a real permission failure against a live member for success.
	members, lookupErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if lookupErr != nil {
		return errors.Join(err, lookupErr)
	}
	const zombie = 5 // SZOMB in Darwin's sys/proc.h.
	for _, member := range members {
		if member.Proc.P_stat != zombie {
			return err
		}
	}
	return nil
}
