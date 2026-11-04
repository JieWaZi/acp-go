//go:build !windows

package nativeacp

import (
	"context"
	"os/exec"
)

func platformCommand(ctx context.Context, executable string, args, environment []string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, executable, args...), nil
}
