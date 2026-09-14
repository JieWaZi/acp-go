package cursor

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	contextCommand            = "/context"
	contextCommandDescription = "Show context usage breakdown"
	contextUsageDescription   = "Current context usage by category."
	contextUsageUnavailable   = "No context usage breakdown to show yet."
	contextTokenPattern       = `[0-9]+(?:\.[0-9]+)?[KkMm]?`
)

var contextUsageHeader = regexp.MustCompile(
	`(?m)^Context[^\r\n]*?\s(` + contextTokenPattern + `)\s*/\s*(` +
		contextTokenPattern + `)\s+([0-9]+(?:\.[0-9]+)?)%\s*$`,
)

// refreshContextUsage 从官方本地命令读取当前窗口占用；不可用时不影响已经完成的生成。
func (a *Agent) refreshContextUsage(ctx context.Context, session *interactiveSession) {
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	usage, err := session.terminal.contextUsage(query)
	if err == nil && usage != nil {
		err = a.notify(ctx, session, acp.SessionUpdate{UsageUpdate: usage})
	}
	if err != nil && a.config.Logger != nil {
		a.config.Logger.Debug("Cursor context usage unavailable", "error", err)
	}
}

// contextUsage 只在终端确认原生命令后打开上下文页，避免未知命令成为用户提示。
func (t *cursorTerminal) contextUsage(ctx context.Context) (*acp.SessionUsageUpdate, error) {
	if err := t.ready(ctx); err != nil {
		return nil, err
	}
	before := t.text()
	if err := t.write(contextCommand); err != nil {
		return nil, err
	}
	confirm, cancel := context.WithTimeout(ctx, time.Second)
	err := t.wait(confirm, func(screen string) bool {
		return screen != before && strings.Contains(screen, contextCommandDescription)
	})
	cancel()
	if err != nil {
		return nil, errors.Join(
			errors.New("Cursor /context command unavailable"),
			t.restoreContextInput(ctx),
		)
	}
	if err = t.write("\r"); err != nil {
		return nil, err
	}
	err = t.wait(ctx, func(screen string) bool {
		return strings.Contains(screen, contextUsageDescription) ||
			strings.Contains(screen, contextUsageUnavailable)
	})
	if err != nil {
		return nil, errors.Join(err, t.restoreContextPager(ctx))
	}
	screen := t.text()
	if strings.Contains(screen, contextUsageUnavailable) {
		return nil, t.restoreContextPager(ctx)
	}
	usage, parseErr := parseContextUsage(screen)
	return usage, errors.Join(parseErr, t.restoreContextPager(ctx))
}

// restoreContextInput 清除未被确认的命令，保证旧版 CLI 不会在下一轮误提交。
func (t *cursorTerminal) restoreContextInput(ctx context.Context) error {
	if err := t.write("\x1b\x15"); err != nil {
		return err
	}
	return t.waitForContextRestore(ctx)
}

// restoreContextPager 关闭用量页并等待原生输入框恢复。
func (t *cursorTerminal) restoreContextPager(ctx context.Context) error {
	if err := t.write("\x1b"); err != nil {
		return err
	}
	return t.waitForContextRestore(ctx)
}

// waitForContextRestore 为终端清理保留独立短时限，不受本轮恰好取消影响。
func (t *cursorTerminal) waitForContextRestore(ctx context.Context) error {
	restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return t.wait(restore, func(screen string) bool {
		return terminalReady(screen) &&
			!strings.Contains(screen, contextCommand) &&
			!strings.Contains(screen, contextCommandDescription) &&
			!strings.Contains(screen, contextUsageDescription)
	})
}

// parseContextUsage 读取 Cursor 页头中的已用量和窗口容量，不累计计费用量。
func parseContextUsage(screen string) (*acp.SessionUsageUpdate, error) {
	match := contextUsageHeader.FindStringSubmatch(screen)
	if len(match) != 4 {
		return nil, errors.New("invalid Cursor /context output")
	}
	used, err := parseContextTokenCount(match[1])
	if err != nil {
		return nil, err
	}
	size, err := parseContextTokenCount(match[2])
	if err != nil {
		return nil, err
	}
	if used < 0 || size <= 0 || used > size {
		return nil, errors.New("invalid Cursor context usage values")
	}
	return &acp.SessionUsageUpdate{Used: used, Size: size}, nil
}

// parseContextTokenCount 转换官方界面使用的整数、K 和 M Token 缩写。
func parseContextTokenCount(label string) (int, error) {
	multiplier := 1.0
	number := label
	if len(label) > 0 {
		switch label[len(label)-1] {
		case 'K', 'k':
			multiplier = 1_000
			number = label[:len(label)-1]
		case 'M', 'm':
			multiplier = 1_000_000
			number = label[:len(label)-1]
		}
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value < 0 || value*multiplier > 10_000_000_000 {
		return 0, errors.New("invalid Cursor context token count")
	}
	return int(math.Round(value * multiplier)), nil
}
