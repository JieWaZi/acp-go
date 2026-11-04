package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const defaultMaxStderrBytes = 256 << 10

var (
	// ErrAppServerExited 表示唯一 Codex app-server 子进程异常退出。
	ErrAppServerExited = errors.New("codex app-server exited")
	// ErrInvalidCodexPath 表示进程构造期缺少已探测的可执行文件路径。
	ErrInvalidCodexPath = errors.New("invalid codex executable path")
)

// processOptions 保存 app-server 的启动配置和有界诊断策略。
type processOptions struct {
	// PrefixArgs 是放在 app-server 子命令之前的调用方启动参数。
	PrefixArgs []string
	// Environment 是 app-server 使用的完整环境；nil 表示继承当前进程。
	Environment []string
	// MaxStderrBytes 是只保留最近 stderr 尾部的字节上限。
	MaxStderrBytes int
	// Logger 实时接收 app-server stderr 诊断；为空时仅保存崩溃尾部。
	Logger *slog.Logger
}

// appServerProcess 拥有唯一 `codex app-server` 子进程及其三条标准流。
// Wait 和关闭由该结构统一持有，避免多个 goroutine 竞争进程回收。
type appServerProcess struct {
	// command 是已成功 Start 的子进程命令。
	command *exec.Cmd
	// stdin 只交给内层 NDJSON transport 写入。
	stdin io.WriteCloser
	// stdout 只交给内层 NDJSON transport 读取。
	stdout io.ReadCloser
	// stderr 保存有界诊断尾部，不进入外层 ACP stdout。
	stderr *tailBuffer
	// done 在唯一 Wait goroutine 回收进程后关闭。
	done chan struct{}
	// waitMu 保护 waitErr。
	waitMu sync.Mutex
	// waitErr 保存包含退出码和 stderr 的稳定进程结果。
	waitErr error
	// closeOnce 保证 stdin/kill/等待序列只执行一次。
	closeOnce sync.Once
	// closeErr 保存第一次关闭的结果供重复调用返回。
	closeErr error
}

// startAppServer 使用已探测路径启动唯一 stdin/stdout 模式的 app-server。
func startAppServer(ctx context.Context, path string, options processOptions) (*appServerProcess, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("starting codex app-server: %w", ErrInvalidCodexPath)
	}
	if options.MaxStderrBytes <= 0 {
		options.MaxStderrBytes = defaultMaxStderrBytes
	}

	arguments := append([]string{}, options.PrefixArgs...)
	arguments = append(arguments, "app-server")
	command := exec.CommandContext(ctx, path, arguments...)
	prepareProcess(command)
	command.Env = options.Environment
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating codex app-server stdin: %w", err)
	}
	// The parent owns the read end. Cmd.Wait must not close it before the scanner
	// consumes the child's final frames.
	stdout, childOutput, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("creating codex app-server stdout: %w", err)
	}
	defer childOutput.Close()
	command.Stdout = childOutput
	command.WaitDelay = time.Second
	stderr := &tailBuffer{limit: options.MaxStderrBytes, logger: options.Logger}
	command.Stderr = stderr
	if err = command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("starting codex app-server %q: %w", path, err)
	}

	process := &appServerProcess{
		command: command,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		done:    make(chan struct{}),
	}
	go process.wait()
	return process, nil
}

// Stdin 返回 app-server stdin；只有 transport 应写入。
func (p *appServerProcess) Stdin() io.WriteCloser {
	return p.stdin
}

// Stdout 返回 app-server stdout；只有 transport 应读取。
func (p *appServerProcess) Stdout() io.ReadCloser {
	return p.stdout
}

// Done 返回唯一 Wait owner 完成回收的通知通道。
func (p *appServerProcess) Done() <-chan struct{} {
	return p.done
}

// Err 返回进程最终结果；调用方应先等待 Done，运行中返回 nil。
func (p *appServerProcess) Err() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

// FinalError joins the sole process waiter after transport drains stdout.
func (p *appServerProcess) FinalError() error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.Err()
	case <-timer.C:
		return nil
	}
}

// Close 先关闭 stdin 请求自然退出，超出调用方期限时强制 Kill，并始终等待唯一 Wait owner。
func (p *appServerProcess) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		defer p.stdout.Close()
		_ = p.stdin.Close()
		select {
		case <-p.done:
			p.closeErr = p.Err()
			return
		case <-ctx.Done():
			killProcessTree(p.command)
			<-p.done
			p.closeErr = ctx.Err()
		}
	})
	return p.closeErr
}

// wait 是进程唯一的 Wait 调用点，并在 stderr 已刷新后构造稳定错误。
func (p *appServerProcess) wait() {
	err := p.command.Wait()
	if stdout, ok := p.stdout.(*os.File); ok {
		// A child can inherit stdout and delay EOF after the owned process exited.
		_ = stdout.SetReadDeadline(time.Now().Add(2 * time.Second))
	}
	p.waitMu.Lock()
	if err != nil {
		exitDetail := err.Error()
		if p.command.ProcessState != nil {
			exitDetail = fmt.Sprintf("code %d", p.command.ProcessState.ExitCode())
		}
		present, truncated := p.stderr.status()
		if present {
			p.waitErr = fmt.Errorf("%w with %s: stderr=redacted truncated=%t", ErrAppServerExited, exitDetail, truncated)
		} else {
			p.waitErr = fmt.Errorf("%w with %s: stderr=absent truncated=false", ErrAppServerExited, exitDetail)
		}
	}
	p.waitMu.Unlock()
	close(p.done)
}

// tailBuffer 是并发安全的固定容量尾部缓冲，只保留最近诊断。
type tailBuffer struct {
	// mu 保护 data。
	mu sync.Mutex
	// data 保存不超过 limit 的 stderr 尾部。
	data []byte
	// limit 是最大保留字节数。
	limit int
	// truncated records whether any bytes were discarded.
	truncated bool
	// logger receives a single fixed marker without stderr content.
	logger *slog.Logger
	// logged prevents repeated diagnostics and unbounded log traffic.
	logged sync.Once
}

// Write 实现 io.Writer；新数据超过上限时丢弃最旧前缀。
func (b *tailBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	if originalLength == 0 {
		return 0, nil
	}
	b.mu.Lock()
	if len(b.data)+len(data) > b.limit {
		b.truncated = true
	}
	if len(data) >= b.limit {
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
	} else {
		overflow := len(b.data) + len(data) - b.limit
		if overflow > 0 {
			copy(b.data, b.data[overflow:])
			b.data = b.data[:len(b.data)-overflow]
		}
		b.data = append(b.data, data...)
	}
	b.mu.Unlock()
	if b.logger != nil {
		b.logged.Do(func() { b.logger.Warn("Codex app-server stderr", "stderr", "redacted") })
	}
	return originalLength, nil
}

// status returns only presence and truncation facts, never buffered content.
func (b *tailBuffer) status() (bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data) > 0, b.truncated
}
