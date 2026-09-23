//go:build !unix

package claude

import "os/exec"

// prepareProcess 在非 Unix 平台保留默认进程配置。
func prepareProcess(command *exec.Cmd) {}

// killProcessTree 在非 Unix 平台终止直接子进程。
func killProcessTree(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
