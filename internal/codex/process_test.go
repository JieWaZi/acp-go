package codex

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// writeExecutableScript 创建仅用于当前测试的 fake Codex 可执行文件。
func writeExecutableScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake app-server 仅在 Unix 测试；Windows 由交叉构建覆盖")
	}
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("写 fake Codex 失败: %v", err)
	}
	return path
}

// TestStartAppServerUsesSingleProcessAndAppServerArgument 验证进程只启动一次且固定使用 app-server 子命令。
func TestStartAppServerUsesSingleProcessAndAppServerArgument(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
if [ "$1" != "app-server" ]; then
  echo "unexpected:$1" >&2
  exit 9
fi
echo '{"method":"warning","params":{"message":"ready"}}'
cat >/dev/null
`)

	process, err := startAppServer(context.Background(), path, processOptions{})
	if err != nil {
		t.Fatalf("启动 app-server 失败: %v", err)
	}
	line, err := bufio.NewReader(process.Stdout()).ReadString('\n')
	if err != nil {
		t.Fatalf("读取 fake app-server 输出失败: %v", err)
	}
	if !strings.Contains(line, "ready") {
		t.Fatalf("fake app-server 输出为 %q", line)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = process.Close(closeCtx); err != nil {
		t.Fatalf("关闭 app-server 失败: %v", err)
	}
	if err = process.Close(closeCtx); err != nil {
		t.Fatalf("重复关闭 app-server 失败: %v", err)
	}
}

// TestAppServerProcessPreservesExitAndBoundedStderr 验证唯一 Wait owner 保存退出码与有界 stderr 尾部。
func TestAppServerProcessPreservesExitAndBoundedStderr(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
printf 'prefix-'
printf 'very-important-diagnostic' >&2
exit 7
`)

	process, err := startAppServer(context.Background(), path, processOptions{MaxStderrBytes: 12})
	if err != nil {
		t.Fatalf("启动 app-server 失败: %v", err)
	}
	<-process.Done()
	err = process.Err()
	if !errors.Is(err, ErrAppServerExited) {
		t.Fatalf("退出错误为 %v", err)
	}
	if !strings.Contains(err.Error(), "7") || !strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("退出错误未保留 exit/stderr 尾部: %v", err)
	}
	if strings.Contains(err.Error(), "very-important") {
		t.Fatalf("stderr 未受 12 字节上限约束: %v", err)
	}
}

// TestAppServerProcessCloseKillsHungProcess 验证 stdin EOF 后不退出的进程会在关闭期限内被终止。
func TestAppServerProcessCloseKillsHungProcess(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
trap '' TERM
while :; do sleep 1; done
`)

	process, err := startAppServer(context.Background(), path, processOptions{})
	if err != nil {
		t.Fatalf("启动 app-server 失败: %v", err)
	}
	closeCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = process.Close(closeCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("强制关闭错误为 %v", err)
	}
	select {
	case <-process.Done():
	case <-time.After(time.Second):
		t.Fatal("强制关闭未回收进程")
	}
}

// TestAgentSurfacesProcessExitToPendingInitialize 验证真实进程退出会携带 exit/stderr 解除 typed request。
func TestAgentSurfacesProcessExitToPendingInitialize(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.148.0"
  exit 0
fi
IFS= read -r line
echo "fatal app-server fixture" >&2
exit 7
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent, err := NewAgent(ctx, Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CodexPath: path,
	})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	_, err = agent.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	if !errors.Is(err, ErrAppServerUnavailable) {
		t.Fatalf("initialize 错误为 %v", err)
	}
	if !errors.Is(err, ErrAppServerExited) {
		t.Fatalf("initialize 错误丢失进程退出根因: %v", err)
	}
	if !strings.Contains(err.Error(), "code 7") || !strings.Contains(err.Error(), "fatal app-server fixture") {
		t.Fatalf("process fatal 未保留退出诊断: %v", err)
	}
	_ = agent.Close(context.Background())
}

// TestAgentOwnsRuntimeBeyondConstructionContext 验证 Serve context 结束后仍由 Agent.Close 负责有序终止子进程。
func TestAgentOwnsRuntimeBeyondConstructionContext(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.148.0"
  exit 0
fi
cat >/dev/null
`)
	constructionCtx, cancelConstruction := context.WithCancel(context.Background())
	agent, err := NewAgent(constructionCtx, Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CodexPath: path,
	})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	cancelConstruction()
	if err = agent.runtimeCtx.Err(); err != nil {
		t.Fatalf("construction context 抢先终止 Agent runtime: %v", err)
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err = agent.Close(closeCtx); err != nil {
		t.Fatalf("显式关闭 Agent 失败: %v", err)
	}
}
