package cursor

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// cursorTerminal 拥有一条交互终端及其屏幕状态，不创建 tmux 或 Python 包装进程。
type cursorTerminal struct {
	// screen 使用成熟 VT 模拟器解释终端重绘与输入协议。
	screen *vt.SafeEmulator
	// process 是唯一 Cursor 交互进程。
	process *exec.Cmd
	// tty 是宿主拥有的伪终端。
	tty *os.File
	// cancel 结束整个子进程组。
	cancel context.CancelFunc
	// done 表示子进程已经回收。
	done chan struct{}
	// workers 汇合终端双向数据泵。
	workers sync.WaitGroup
	// err 是进程退出错误，在 done 关闭后读取。
	err error
	// closeOnce 保证清理幂等。
	closeOnce sync.Once
}

// startTerminal 启动本连接拥有的伪终端，并汇合双向 VT 数据泵。
func startTerminal(ctx context.Context, command string, args, env []string, cwd string) (*cursorTerminal, error) {
	lifetime, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(lifetime, command, args...)
	cmd.Env = env
	cmd.Dir = cwd
	prepareTerminalProcess(cmd)
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 70, Cols: 180})
	if err != nil {
		cancel()
		return nil, err
	}
	t := &cursorTerminal{screen: vt.NewSafeEmulator(180, 70), process: cmd, tty: tty, cancel: cancel, done: make(chan struct{})}
	t.workers.Add(2)
	go func() { defer t.workers.Done(); _, _ = io.Copy(t.screen, tty) }()
	go func() { defer t.workers.Done(); _, _ = io.Copy(tty, t.screen) }()
	go func() { t.err = cmd.Wait(); close(t.done) }()
	return t, nil
}

// text 读取去除终端控制序列的当前屏幕快照。
func (t *cursorTerminal) text() string { return ansi.Strip(t.screen.Render()) }

// wait 等待指定屏幕状态，同时响应取消与子进程退出。
func (t *cursorTerminal) wait(ctx context.Context, match func(string) bool) error {
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		if match(t.text()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.done:
			if t.err != nil {
				return t.err
			}
			return io.EOF
		case <-ticker.C:
		}
	}
}

// ready 等待终端进入可接受下一轮输入的状态。
func (t *cursorTerminal) ready(ctx context.Context) error {
	return t.wait(ctx, terminalReady)
}

// terminalReady 识别空闲输入框并排除仍在生成中的终端。
func terminalReady(text string) bool {
	return (strings.Contains(text, "Plan, search, build anything") || strings.Contains(text, "Add a follow-up") || strings.Contains(text, "Plan, build, fix anything")) && !strings.Contains(text, "ctrl+c to stop")
}

// write 仅向尚未退出的受管终端发送按键。
func (t *cursorTerminal) write(value string) error {
	select {
	case <-t.done:
		return errors.New("Cursor terminal exited")
	default:
	}
	_, err := io.WriteString(t.tty, value)
	return err
}

// submit 使用 bracketed paste 保留多行文本，并等待 composer 呈现后单独提交。
func (t *cursorTerminal) submit(ctx context.Context, value string) error {
	if strings.IndexFunc(value, func(r rune) bool { return r < 32 && r != '\n' && r != '\t' || r == 127 }) >= 0 {
		return errors.New("prompt contains unsupported terminal control characters")
	}
	if err := t.ready(ctx); err != nil {
		return err
	}
	t.screen.Paste(value)
	needle := strings.TrimSpace(strings.SplitN(strings.TrimSpace(value), "\n", 2)[0])
	if len([]rune(needle)) > 32 {
		needle = string([]rune(needle)[:32])
	}
	if err := t.wait(ctx, func(text string) bool { return strings.Contains(text, needle) }); err != nil {
		return err
	}
	return t.write("\r")
}

// close 幂等关闭终端、子进程和双向数据泵。
func (t *cursorTerminal) close(ctx context.Context) error {
	t.closeOnce.Do(func() {
		t.cancel()
		_ = t.tty.Close()
		// VT 的 Close 会无锁改写 Read 共用的标记；关闭公开输入管道即可安全解除双向泵。
		if pipe, ok := t.screen.InputPipe().(io.Closer); ok {
			_ = pipe.Close()
		}
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
	}
	t.workers.Wait()
	return nil
}
