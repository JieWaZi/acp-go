package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	// claudeVersionProbeTimeout 限制异常 CLI 的版本探测时间。
	claudeVersionProbeTimeout = 5 * time.Second
	// maxClaudeVersionOutput 防止版本命令无界写入内存。
	maxClaudeVersionOutput = 64 << 10
)

var (
	// ErrClaudeNotFound 表示显式路径或 PATH 中不存在可执行 Claude CLI。
	ErrClaudeNotFound = errors.New("claude executable not found")
	// ErrInvalidClaudePath 表示路径存在但不是可执行普通文件。
	ErrInvalidClaudePath = errors.New("invalid claude executable path")
	// claudeVersionPattern 只用于诊断，不作为脆弱的运行时版本门槛。
	claudeVersionPattern = regexp.MustCompile(`\b([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)\b`)
)

// executable 保存已验证可启动的 Claude CLI 路径与诊断版本。
type executable struct {
	// Path 是 Session 进程使用的绝对或 PATH 已解析路径。
	Path string
	// Version 是可识别版本；探测失败时为 unknown，运行仍可继续。
	Version string
}

// pathLookup 隔离 PATH 查询以验证显式路径失败不回退。
type pathLookup func(file string) (string, error)

// versionCommandOptions 保存版本探测的完整参数和环境。
type versionCommandOptions struct {
	// Args 是传给 Claude 的完整版本探测参数。
	Args []string
	// Environment 是版本探测使用的完整环境；nil 表示继承当前进程。
	Environment []string
}

// versionRunner 隔离短生命周期版本命令。
type versionRunner func(ctx context.Context, path string, options versionCommandOptions) ([]byte, error)

// prepareExecutable 按 CLAUDE_CODE_EXECUTABLE→PATH 的单向规则解析 Claude CLI。
func prepareExecutable(
	ctx context.Context,
	config Config,
	lookPath pathLookup,
	runVersion versionRunner,
) (executable, error) {
	if config.Logger == nil {
		return executable{}, errors.New("preparing claude executable: logger is nil")
	}
	path, err := resolveClaudePath(config.ClaudePath, lookPath)
	if err != nil {
		return executable{}, err
	}
	if err := validateExecutable(path); err != nil {
		return executable{}, err
	}

	version := "unknown"
	versionArgs := append([]string{}, config.PrefixArgs...)
	versionArgs = append(versionArgs, "--version")
	output, probeErr := runVersion(ctx, path, versionCommandOptions{
		Args:        versionArgs,
		Environment: config.Environment,
	})
	if probeErr != nil {
		config.Logger.Warn("Claude CLI version probe failed; startup will continue", "error", probeErr)
	} else if match := claudeVersionPattern.FindSubmatch(output); len(match) == 2 {
		version = string(match[1])
	} else {
		config.Logger.Warn("Claude CLI version output was not recognized; startup will continue")
	}
	config.Logger.Debug("Resolved Claude CLI", "path", path, "version", version)
	return executable{Path: path, Version: version}, nil
}

// resolveClaudePath 保证显式配置错误时不会静默改用另一份 PATH 可执行文件。
func resolveClaudePath(explicitPath string, lookPath pathLookup) (string, error) {
	if strings.TrimSpace(explicitPath) != "" {
		path, err := filepath.Abs(explicitPath)
		if err != nil {
			return "", fmt.Errorf("resolving CLAUDE_CODE_EXECUTABLE %q: %w", explicitPath, err)
		}
		return path, nil
	}
	path, err := lookPath("claude")
	if err != nil {
		return "", fmt.Errorf("resolving claude from PATH: %w: %w", ErrClaudeNotFound, err)
	}
	return path, nil
}

// validateExecutable 在启动 Session 前给出稳定的路径诊断。
func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("checking claude executable %q: %w: %w", path, ErrInvalidClaudePath, err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("checking claude executable %q: %w", path, ErrInvalidClaudePath)
	}
	return nil
}

// runClaudeVersion 使用超时和有界输出执行版本探测。
func runClaudeVersion(ctx context.Context, path string, options versionCommandOptions) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, claudeVersionProbeTimeout)
	defer cancel()
	buffer := &boundedBuffer{limit: maxClaudeVersionOutput}
	command := exec.CommandContext(probeCtx, path, options.Args...)
	command.Env = options.Environment
	command.Stdout = buffer
	command.Stderr = buffer
	if err := command.Run(); err != nil {
		return append([]byte(nil), buffer.Bytes()...), err
	}
	return append([]byte(nil), buffer.Bytes()...), nil
}

// boundedBuffer 丢弃超过上限的后续字节，适合短命令输出。
type boundedBuffer struct {
	// buffer 保存上限以内的命令输出。
	buffer bytes.Buffer
	// limit 是允许保留的最大字节数。
	limit int
}

// Write 实现 io.Writer，并始终报告完整消费以免阻塞子进程。
func (b *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return original, nil
}

// Bytes 返回当前保留的版本输出。
func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }
