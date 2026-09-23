package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// defaultClaudeStderrBytes 限制单个进程保留的诊断尾部。
	defaultClaudeStderrBytes = 256 << 10
	// processFinalErrorGrace 给进程退出状态一个短暂收敛窗口，防止已关闭 stdout 的存活进程卡住 reader。
	processFinalErrorGrace = time.Second
)

var (
	// ErrClaudeProcessExited 表示某个 Session 拥有的 Claude CLI 异常退出。
	ErrClaudeProcessExited = errors.New("claude process exited")
)

// processOptions 保存 Session CLI 启动和诊断选项。
type processOptions struct {
	// CWD 是 CLI 进程的工作目录。
	CWD string
	// Args 是传给 CLI 的启动参数。
	Args []string
	// Env 是 CLI 进程的完整环境变量列表。
	Env []string
	// Logger 接收 CLI 原始诊断日志。
	Logger *slog.Logger
	// MaxStderrBytes 是失败诊断保留的 stderr 尾部上限。
	MaxStderrBytes int
}

// claudeProcess 拥有单个 Session 的 Claude CLI 及三条标准流。
type claudeProcess struct {
	// command 是当前 Session 独占的 CLI 命令。
	command *exec.Cmd
	// stdin 是发送 JSONL 消息的输入流。
	stdin io.WriteCloser
	// stdout 是接收 JSONL 消息的输出流。
	stdout io.ReadCloser
	// stderr 保存原始诊断的有限尾部。
	stderr *tailBuffer
	// done 在唯一等待协程完成进程回收后关闭。
	done chan struct{}
	// waitMu 保护最终进程错误。
	waitMu sync.Mutex
	// waitErr 是进程退出后的稳定错误。
	waitErr error
	// closeOnce 保证关闭和强制终止流程只执行一次。
	closeOnce sync.Once
	// closeErr 保存第一次关闭操作的结果。
	closeErr error
}

// startClaudeProcess 启动已探测的 Claude CLI；一个调用只对应一个 Session。
func startClaudeProcess(ctx context.Context, path string, options processOptions) (*claudeProcess, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("starting Claude CLI: %w", ErrInvalidClaudePath)
	}
	if options.MaxStderrBytes <= 0 {
		options.MaxStderrBytes = defaultClaudeStderrBytes
	}
	command := exec.CommandContext(ctx, path, options.Args...)
	prepareProcess(command)
	command.Dir = options.CWD
	command.Env = options.Env
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating Claude stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("creating Claude stdout: %w", err)
	}
	stderr := &tailBuffer{limit: options.MaxStderrBytes}
	command.Stderr = &stderrWriter{tail: stderr, logger: options.Logger}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("starting Claude CLI %q: %w", path, err)
	}
	process := &claudeProcess{
		command: command,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		done:    make(chan struct{}),
	}
	go process.wait()
	return process, nil
}

// Stdin 返回只应由 transport 写入的 CLI stdin。
func (p *claudeProcess) Stdin() io.WriteCloser { return p.stdin }

// Stdout 返回只应由 transport 读取的 CLI stdout。
func (p *claudeProcess) Stdout() io.ReadCloser { return p.stdout }

// Done 在唯一 Wait owner 回收进程后关闭。
func (p *claudeProcess) Done() <-chan struct{} { return p.done }

// Err 返回最终进程结果；运行中为 nil。
func (p *claudeProcess) Err() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

// FinalError 等待进程退出并返回包含退出状态与原始 stderr 尾部的错误。
func (p *claudeProcess) FinalError() error {
	timer := time.NewTimer(processFinalErrorGrace)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.Err()
	case <-timer.C:
		return nil
	}
}

// Close 先用 stdin EOF 请求自然结束，超过调用方期限后强制终止并等待回收。
func (p *claudeProcess) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.done:
			p.closeErr = p.Err()
		case <-ctx.Done():
			killProcessTree(p.command)
			<-p.done
			p.closeErr = ctx.Err()
		}
	})
	return p.closeErr
}

// wait 是唯一 exec.Cmd.Wait owner，并在 stderr flush 后构造稳定错误。
func (p *claudeProcess) wait() {
	err := p.command.Wait()
	p.waitMu.Lock()
	if err != nil {
		detail := err.Error()
		if p.command.ProcessState != nil {
			detail = fmt.Sprintf("code %d", p.command.ProcessState.ExitCode())
		}
		stderr := strings.TrimSpace(p.stderr.String())
		if stderr != "" {
			p.waitErr = fmt.Errorf("%w with %s: %s", ErrClaudeProcessExited, detail, stderr)
		} else {
			p.waitErr = fmt.Errorf("%w with %s", ErrClaudeProcessExited, detail)
		}
	}
	p.waitMu.Unlock()
	close(p.done)
}

// stderrWriter 保存并记录 Claude 的原始 stderr。
type stderrWriter struct {
	// tail 接收原始诊断尾部。
	tail *tailBuffer
	// logger 接收原始诊断行。
	logger *slog.Logger
}

// Write 把原始输出写入有界尾部和日志。
func (w *stderrWriter) Write(data []byte) (int, error) {
	_, _ = w.tail.Write(data)
	if w.logger != nil {
		if diagnostic := string(data); diagnostic != "" {
			w.logger.Warn("Claude CLI stderr", "stderr", diagnostic)
		}
	}
	return len(data), nil
}

// tailBuffer 是并发安全的固定容量尾部缓冲。
type tailBuffer struct {
	// mu 保护尾部数据的并发读写。
	mu sync.Mutex
	// data 保存最近写入的有限字节。
	data []byte
	// limit 是最多保留的字节数。
	limit int
}

// Write 只保留最近 limit 字节。
func (b *tailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(data)
	if len(data) >= b.limit {
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
		return original, nil
	}
	if overflow := len(b.data) + len(data) - b.limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
	return original, nil
}

// String 返回当前尾部的副本。
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

// cloneClaudeEnvironment 保留 nil 的继承语义并隔离调用方后续切片修改。
func cloneClaudeEnvironment(environment []string) []string {
	if environment == nil {
		return nil
	}
	return append([]string{}, environment...)
}

// claudeEnvironmentValue 从完整环境读取最后一个同名键；nil 环境继承当前进程。
func claudeEnvironmentValue(environment []string, name string) string {
	if environment == nil {
		return os.Getenv(name)
	}
	normalizedName := normalizeClaudeEnvironmentName(name)
	value := ""
	for _, item := range environment {
		key, candidate, found := strings.Cut(item, "=")
		if found && normalizeClaudeEnvironmentName(key) == normalizedName {
			value = candidate
		}
	}
	return value
}

// claudeProcessEnv 构造 Claude CLI 子进程需要的隔离环境。
func claudeProcessEnv(base []string, extra map[string]string) []string {
	if base == nil {
		base = os.Environ()
	}
	values := make(map[string]string, len(base)+len(extra)+2)
	names := make(map[string]string, len(base)+len(extra)+2)
	for _, item := range base {
		key, value, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		setClaudeEnvironmentValue(values, names, key, value)
	}
	delete(values, normalizeClaudeEnvironmentName("NODE_OPTIONS"))
	delete(names, normalizeClaudeEnvironmentName("NODE_OPTIONS"))
	setClaudeEnvironmentValue(values, names, "CLAUDE_CODE_ENTRYPOINT", "sdk-ts")
	setClaudeEnvironmentValue(values, names, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS", "1")
	for key, value := range extra {
		setClaudeEnvironmentValue(values, names, key, value)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, names[key]+"="+values[key])
	}
	return result
}

// setClaudeEnvironmentValue 按平台键语义覆盖一条环境值并保留最后使用的键名。
func setClaudeEnvironmentValue(
	values map[string]string,
	names map[string]string,
	name string,
	value string,
) {
	normalized := normalizeClaudeEnvironmentName(name)
	values[normalized] = value
	names[normalized] = name
}

// normalizeClaudeEnvironmentName 保持 Windows 环境键大小写不敏感的进程语义。
func normalizeClaudeEnvironmentName(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
