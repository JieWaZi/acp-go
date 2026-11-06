//go:build unix

package workbuddy

import "golang.org/x/sys/unix"

// Avoid blocking on a named pipe or following a symlink swapped in after lstat.
const workBuddyHistoryOpenFlags = unix.O_NONBLOCK | unix.O_NOFOLLOW
