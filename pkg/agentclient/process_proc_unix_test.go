//go:build darwin || linux

package agentclient

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

func procObservationEntries(t *testing.T) []os.DirEntry {
	t.Helper()
	entries, err := fs.ReadDir(fstest.MapFS{"100/stat": {}, "200/stat": {}, "self/stat": {}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestProcGroupObservationSkipsUnrelatedPermissionFailure(t *testing.T) {
	var reads []string
	alive, err := observeProcProcessGroup(100, procObservationEntries(t), func(pid int) (int, error) {
		return pid, nil
	}, func(path string) ([]byte, error) {
		reads = append(reads, path)
		if strings.Contains(path, "/200/") {
			return nil, os.ErrPermission
		}
		return []byte("100 (leader) Z 1 100 100"), nil
	})
	if alive || err != nil || len(reads) != 1 {
		t.Fatalf("unrelated process must not obstruct empty owned group: alive=%v err=%v reads=%v", alive, err, reads)
	}
}

func TestProcGroupObservationKeepsUnknownOwnership(t *testing.T) {
	for _, tc := range []struct {
		// name identifies the kernel observation boundary under test.
		name string
		// groupError prevents determining whether a PID belongs to the owned group.
		groupError error
		// readError prevents observing a known group member's current state.
		readError error
	}{
		{name: "group-denied", groupError: os.ErrPermission},
		{name: "member-denied", readError: os.ErrPermission},
		{name: "member-hidden", readError: os.ErrNotExist},
		{name: "member-stat-disappeared-but-group-still-exists", readError: syscall.ESRCH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alive, err := observeProcProcessGroup(100, procObservationEntries(t), func(int) (int, error) {
				return 100, tc.groupError
			}, func(string) ([]byte, error) { return nil, tc.readError })
			if alive || err == nil {
				t.Fatalf("unknown group membership/state cannot prove release: alive=%v err=%v", alive, err)
			}
		})
	}
}

func TestProcGroupObservationHandlesExitBetweenQueries(t *testing.T) {
	for _, vanished := range []error{os.ErrNotExist, syscall.ESRCH} {
		for _, movedGroup := range []int{0, 200} {
			calls := 0
			alive, err := observeProcProcessGroup(100, procObservationEntries(t), func(pid int) (int, error) {
				if pid == 200 {
					return 200, nil
				}
				calls++
				if calls == 1 {
					return 100, nil
				}
				if movedGroup == 0 {
					return 0, syscall.ESRCH
				}
				return movedGroup, nil
			}, func(string) ([]byte, error) { return nil, vanished })
			if alive || err != nil || calls != 2 {
				t.Fatalf("confirmed exit/move should finish observation: alive=%v err=%v calls=%d", alive, err, calls)
			}
		}
	}
}

func TestProcGroupObservationRequiresLiveMemberState(t *testing.T) {
	for _, state := range []string{"S", "D", "T", "R", "Z", "X"} {
		t.Run(state, func(t *testing.T) {
			alive, err := observeProcProcessGroup(100, procObservationEntries(t), func(pid int) (int, error) {
				if pid == 200 {
					return 0, syscall.ESRCH
				}
				return 100, nil
			}, func(string) ([]byte, error) {
				return []byte("100 (name with ) and spaces) " + state + " 1 100 100"), nil
			})
			if err != nil || alive != (state != "Z" && state != "X") {
				t.Fatalf("state=%s alive=%v err=%v", state, alive, err)
			}
		})
	}
	_, err := observeProcProcessGroup(100, procObservationEntries(t), func(int) (int, error) {
		return 100, nil
	}, func(string) ([]byte, error) { return nil, syscall.EIO })
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("observation failure lost: %v", err)
	}
}
