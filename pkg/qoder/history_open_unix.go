//go:build unix

package qoder

import "golang.org/x/sys/unix"

// Refuse symlink replacement and avoid blocking on a substituted named pipe.
const qoderHistoryOpenFlags = unix.O_NONBLOCK | unix.O_NOFOLLOW
