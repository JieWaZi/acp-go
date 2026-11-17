//go:build darwin || linux

package agentclient

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// observeProcProcessGroup separates the Linux observation algorithm from kernel I/O.
// The owner keeps the original leader unreaped throughout this observation.
func observeProcProcessGroup(pid int, entries []os.DirEntry, getGroup func(int) (int, error), readStat func(string) ([]byte, error)) (bool, error) {
	for _, entry := range entries {
		member, err := strconv.Atoi(entry.Name())
		if err != nil || member <= 0 {
			continue
		}
		group, err := getGroup(member)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		if group != pid {
			continue
		}
		// A restricted proc mount can deny stat for unrelated processes. Only
		// inspect a member after the kernel has identified the owned group.
		raw, err := readStat(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			// ENOENT can also mean a proc visibility change. Confirm that the
			// process actually disappeared or left this group before ignoring it.
			currentGroup, groupErr := getGroup(member)
			if errors.Is(groupErr, syscall.ESRCH) || (groupErr == nil && currentGroup != pid) {
				continue
			}
			if groupErr != nil {
				return false, groupErr
			}
		}
		if err != nil {
			return false, err
		}
		// comm may contain whitespace and parentheses; stat's remaining fields begin after the final ')'.
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			return false, errors.New("invalid process group observation")
		}
		fields := strings.Fields(string(raw[end+1:]))
		if len(fields) < 3 {
			return false, errors.New("incomplete process group observation")
		}
		group, err = strconv.Atoi(fields[2])
		if err != nil {
			return false, err
		}
		if group == pid && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
