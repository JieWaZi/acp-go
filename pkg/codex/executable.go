package codex

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
	// verifiedCodexVersion 是本 Adapter 完成兼容性验证的 Codex CLI 版本。
	verifiedCodexVersion = "0.148.0"
	// versionProbeTimeout 限制版本探测，避免异常可执行文件阻塞 Adapter 启动。
	versionProbeTimeout = 5 * time.Second
	// maxVersionOutput 限制版本命令写入诊断内存的字节数。
	maxVersionOutput = 64 << 10
)

var (
	// ErrCodexNotFound 表示显式路径或 PATH 中没有可启动的 Codex。
	ErrCodexNotFound = errors.New("codex executable not found")
	// ErrInvalidCodexVersion 表示 `codex --version` 未返回可识别版本。
	ErrInvalidCodexVersion = errors.New("invalid codex version output")
	// codexVersionPattern 兼容 codex-cli 与发布包常见的 codex 前缀。
	codexVersionPattern = regexp.MustCompile(`(?m)\bcodex(?:-cli)?\s+v?([0-9]+\.[0-9]+\.[0-9]+)\b`)
)

// executable 保存经过启动前探测的 Codex 可执行文件信息。
type executable struct {
	// Path 是只用于当前 Adapter 子进程的已解析路径。
	Path string
	// Version 是 `codex --version` 解析出的语义版本。
	Version string
}

// lookPathFunc 隔离 PATH 查询，测试可证明显式 CODEX_PATH 不会回退。
type lookPathFunc func(file string) (string, error)

// commandOptions 保存短生命周期命令的参数和完整环境。
type commandOptions struct {
	// Args 是传给 Codex 的完整参数列表。
	Args []string
	// Environment 是命令使用的完整环境；nil 表示继承当前进程。
	Environment []string
}

// commandRunner 隔离短生命周期命令，进程 runtime 不依赖该测试缝。
type commandRunner func(ctx context.Context, path string, options commandOptions) ([]byte, error)

// prepareExecutable 按 CODEX_PATH→PATH 的单向规则解析并探测 Codex。
func prepareExecutable(
	ctx context.Context,
	config Config,
	lookPath lookPathFunc,
	run commandRunner,
) (executable, error) {
	if config.Logger == nil {
		return executable{}, fmt.Errorf("preparing codex executable: %w", ErrInvalidLogger)
	}

	path, err := resolveCodexPath(config.CodexPath, lookPath)
	if err != nil {
		return executable{}, err
	}
	versionArgs := append([]string{}, config.PrefixArgs...)
	versionArgs = append(versionArgs, "--version")
	output, err := run(ctx, path, commandOptions{
		Args:        versionArgs,
		Environment: config.Environment,
	})
	if err != nil {
		return executable{}, fmt.Errorf("probing codex executable %q: %w", path, err)
	}
	version, err := parseCodexVersion(output)
	if err != nil {
		return executable{}, fmt.Errorf("probing codex executable %q: %w", path, err)
	}
	if version != verifiedCodexVersion {
		config.Logger.Warn(
			"Codex CLI version has not been verified against the current adapter baseline; startup will continue",
			"version", version,
			"verified_version", verifiedCodexVersion,
		)
	}
	return executable{Path: path, Version: version}, nil
}

// resolveCodexPath 实现显式路径优先且失败不回退的启动约束。
func resolveCodexPath(explicitPath string, lookPath lookPathFunc) (string, error) {
	if strings.TrimSpace(explicitPath) != "" {
		path, err := filepath.Abs(explicitPath)
		if err != nil {
			return "", fmt.Errorf("resolving explicit CODEX_PATH %q: %w", explicitPath, err)
		}
		return path, nil
	}
	path, err := lookPath("codex")
	if err != nil {
		return "", fmt.Errorf("resolving codex from PATH: %w: %w", ErrCodexNotFound, err)
	}
	return path, nil
}

// parseCodexVersion 从有界版本命令输出中提取三段式版本。
func parseCodexVersion(output []byte) (string, error) {
	matches := codexVersionPattern.FindSubmatch(output)
	if len(matches) != 2 {
		return "", fmt.Errorf("%w: %q", ErrInvalidCodexVersion, strings.TrimSpace(string(output)))
	}
	return string(matches[1]), nil
}

// runVersionCommand 使用有界输出和超时执行一次版本探测。
func runVersionCommand(ctx context.Context, path string, options commandOptions) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()

	var output limitedBuffer
	output.limit = maxVersionOutput
	command := exec.CommandContext(probeCtx, path, options.Args...)
	command.Env = options.Environment
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return output.Bytes(), err
	}
	return output.Bytes(), nil
}

// environmentLookupFromList 把 exec.Cmd 形式的完整环境转换为 Adapter 读取接口。
func environmentLookupFromList(environment []string) environmentLookup {
	if environment == nil {
		return os.Getenv
	}
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		values[normalizeEnvironmentName(name)] = value
	}
	return func(name string) string {
		return values[normalizeEnvironmentName(name)]
	}
}

// normalizeEnvironmentName 保持 Windows 环境键大小写不敏感的进程语义。
func normalizeEnvironmentName(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// limitedBuffer 只保留固定上限字节，避免子进程异常输出导致无界内存增长。
type limitedBuffer struct {
	// buffer 保存尚未达到上限的输出。
	buffer bytes.Buffer
	// limit 是最多保留的字节数。
	limit int
}

// Write 实现 io.Writer；超过上限的数据被丢弃但仍报告已消费，避免阻塞子进程。
func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLength := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return originalLength, nil
}

// Bytes 返回已保留输出的副本视图；调用方在当前缓冲生命周期内使用。
func (b *limitedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}
