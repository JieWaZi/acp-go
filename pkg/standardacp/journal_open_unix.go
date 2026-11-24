//go:build unix

package standardacp

import "golang.org/x/sys/unix"

// Reject a swapped symlink or FIFO when opening a checked journal path.
const journalReadOpenFlags = unix.O_NONBLOCK | unix.O_NOFOLLOW
