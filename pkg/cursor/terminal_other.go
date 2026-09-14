//go:build !unix

package cursor

import "os/exec"

// prepareTerminalProcess 保留平台默认进程回收；不支持的 PTY 平台由库显式返回错误。
func prepareTerminalProcess(cmd *exec.Cmd) {}
