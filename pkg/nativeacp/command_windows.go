//go:build windows

package nativeacp

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func platformCommand(ctx context.Context, executable string, args, environment []string) (*exec.Cmd, error) {
	ext := strings.ToLower(filepath.Ext(executable))
	if ext != ".cmd" && ext != ".bat" {
		return exec.CommandContext(ctx, executable, args...), nil
	}
	shell := EnvironmentValue(environment, "ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	shell, err := ResolveCommand(shell, environment)
	if err != nil {
		return nil, err
	}
	line, err := batchCommandLine(executable, args)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, shell)
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(shell) + " /d /v:off /s /c " + line, HideWindow: true}
	return command, nil
}
