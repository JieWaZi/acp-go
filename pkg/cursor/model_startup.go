package cursor

import (
	"context"
	"errors"
	"strings"
	"time"
)

// startSelectedTerminal 在用户消息提交前限次重试官方模型目录加载失败，始终使用相同模型及参数。
func startSelectedTerminal(ctx, lifetime context.Context, command string, args, environment []string, cwd, model string) (*cursorTerminal, error) {
	for attempt := 0; attempt < 3; attempt++ {
		terminal, err := startTerminal(lifetime, command, args, environment, cwd)
		if err != nil {
			return nil, err
		}
		if err = terminal.ready(ctx); err == nil {
			return terminal, nil
		}
		// 先回收进程与输出泵，再检查完整启动错误，避免读取尚未投影的最后一帧。
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		closeErr := terminal.close(cleanup)
		cancel()
		if closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !missingParameterizedCatalog(terminal.text(), model) {
			return nil, err
		}
	}
	return nil, errors.New("Cursor could not load the selected model's parameter catalog; retry without changing the selected model")
}

// missingParameterizedCatalog 只识别已确认参数模型被旧目录拒绝的启动错误，不重试配额、认证或用户轮次失败。
func missingParameterizedCatalog(screen, model string) bool {
	if !strings.Contains(model, "[") {
		return false
	}
	normalized := strings.Join(strings.Fields(screen), " ")
	return strings.Contains(normalized, "Cannot use this model: "+model+". Available models:")
}
