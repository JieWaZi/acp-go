//go:build darwin || linux

package pi

import (
	"os"
	"strconv"
	"syscall"
)

// snapshotWorkspaceRoot takes both identity representations from one stat.
// libuv copies POSIX st_dev/st_ino into its unsigned 64-bit stat fields.
func snapshotWorkspaceRoot(path string) (os.FileInfo, string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info, "", "", nil
	}
	return info, strconv.FormatUint(uint64(stat.Dev), 10), strconv.FormatUint(uint64(stat.Ino), 10), nil
}
