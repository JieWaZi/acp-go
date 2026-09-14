//go:build !unix

package nativeacp

import "os/exec"

// prepareProcess 在不支持 Unix 进程组的平台沿用 Go 的直接子进程回收。
func prepareProcess(command *exec.Cmd) {}
