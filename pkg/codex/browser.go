package codex

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// systemBrowserOpener 使用系统默认浏览器打开认证地址。
// 它只负责把 URL 交给操作系统，不解析、不记录也不持有认证信息。
type systemBrowserOpener struct{}

// Open 使用当前平台的标准 URL launcher 启动浏览器。
func (systemBrowserOpener) Open(ctx context.Context, url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", url)
	case "linux":
		command = exec.CommandContext(ctx, "xdg-open", url)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("opening browser on unsupported platform %q", runtime.GOOS)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("starting browser launcher: %w", err)
	}
	// launcher 生命周期属于操作系统；释放句柄避免 Agent 为短命辅助进程保留 waiter。
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("releasing browser launcher: %w", err)
	}
	return nil
}
