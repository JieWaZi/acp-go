package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// processLogHandler 捕获进程层写入结构化日志的记录，并仅启用告警及以上级别。
type processLogHandler struct {
	// records 保存进程层告警日志，供测试在有界通道上做确定性断言。
	records chan slog.Record
}

// newProcessLogHandler 创建带有有界记录通道的测试日志处理器。
func newProcessLogHandler() *processLogHandler {
	return &processLogHandler{records: make(chan slog.Record, 8)}
}

// Enabled 仅允许生产配置会输出的告警及以上日志，避免调试日志干扰断言。
func (h *processLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

// Handle 保存日志记录；通道容量保证 app-server stderr 复制协程不会被测试阻塞。
func (h *processLogHandler) Handle(ctx context.Context, record slog.Record) error {
	select {
	case h.records <- record.Clone():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WithAttrs 返回当前测试处理器；这些测试只断言调用处直接附加的字段。
func (h *processLogHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

// WithGroup 返回当前测试处理器；进程日志未使用日志分组。
func (h *processLogHandler) WithGroup(_ string) slog.Handler {
	return h
}

// waitForProcessLog 用结构化日志通道作为进程夹具的确定性完成屏障。
// 测试进程的硬超时由 go test -timeout 统一提供，避免宿主拥塞误触发局部一秒期限。
func waitForProcessLog(t *testing.T, handler *processLogHandler) slog.Record {
	t.Helper()
	return <-handler.records
}

// processLogStderr 返回结构化进程日志中的 stderr 字段。
func processLogStderr(record slog.Record) string {
	var stderr string
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "stderr" {
			stderr = attr.Value.String()
		}
		return true
	})
	return stderr
}

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

// newProcessTestAgent 跳过与进程生命周期断言无关的真实 --version 子进程，避免并行夹具争用生产探测期限。
func newProcessTestAgent(ctx context.Context, config Config) (*Agent, error) {
	return newAgentWithVersionRunner(ctx, config, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("codex-cli " + verifiedCodexVersion + "\n"), nil
	})
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

// TestAgentLogsAppServerStderrWhileRunning 验证运行中的 stderr 会实时进入结构化 Logger，且不记录 stdout 协议内容。
func TestAgentLogsAppServerStderrWhileRunning(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.148.0"
  exit 0
fi
echo "stdout-secret"
echo "running diagnostic" >&2
cat >/dev/null
`)
	handler := newProcessLogHandler()
	agent, err := newProcessTestAgent(context.Background(), Config{
		Logger: slog.New(handler), CodexPath: path,
	})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = agent.Close(closeCtx)
	})

	record := waitForProcessLog(t, handler)
	stderr := processLogStderr(record)
	if record.Message != "Codex app-server stderr" || stderr != "running diagnostic" {
		t.Fatalf("运行中 stderr 日志为 message=%q stderr=%q", record.Message, stderr)
	}
	if strings.Contains(record.Message+stderr, "stdout-secret") {
		t.Fatal("结构化日志泄露了 stdout 协议内容")
	}
}

// TestAgentLogsAppServerStderrOnCleanExit 验证退出码为零时 stderr 仍会进入实时诊断日志。
func TestAgentLogsAppServerStderrOnCleanExit(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.148.0"
  exit 0
fi
echo "clean-exit diagnostic" >&2
exit 0
`)
	handler := newProcessLogHandler()
	agent, err := newProcessTestAgent(context.Background(), Config{
		Logger: slog.New(handler), CodexPath: path,
	})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close(context.Background()) })

	record := waitForProcessLog(t, handler)
	if stderr := processLogStderr(record); record.Message != "Codex app-server stderr" || stderr != "clean-exit diagnostic" {
		t.Fatalf("干净退出 stderr 日志为 message=%q stderr=%q", record.Message, stderr)
	}
	<-agent.process.Done()
	if err = agent.process.Err(); err != nil {
		t.Fatalf("干净退出不应产生进程错误: %v", err)
	}
}

// TestAgentFailsClosedEarlyApprovalWithoutConnection 验证组合根已安装 server request handler，连接尚未绑定时仍返回拒绝结果。
func TestAgentFailsClosedEarlyApprovalWithoutConnection(t *testing.T) {
	t.Parallel()
	path := writeExecutableScript(t, `
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.148.0"
  exit 0
fi
echo '{"id":"approval-early","method":"item/commandExecution/requestApproval","params":{"command":"pwd","cwd":"/tmp","itemId":"item-1","startedAtMs":1,"threadId":"thread-1","turnId":"turn-1"}}'
IFS= read -r response
echo "$response" >&2
cat >/dev/null
`)
	handler := newProcessLogHandler()
	agent, err := newProcessTestAgent(context.Background(), Config{
		Logger: slog.New(handler), CodexPath: path,
	})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = agent.Close(closeCtx)
	})

	record := waitForProcessLog(t, handler)
	var response struct {
		// ID 是 app-server 原始审批请求标识。
		ID string `json:"id"`
		// Result 是缺少连接/身份时的拒绝安全结果。
		Result protocol.CommandExecutionRequestApprovalResponse `json:"result"`
		// Error 表示 transport 错误响应，正确接线时必须为空。
		Error *rpcError `json:"error"`
	}
	if err = json.Unmarshal([]byte(processLogStderr(record)), &response); err != nil {
		t.Fatalf("解析 approval 响应失败: %v", err)
	}
	if response.ID != "approval-early" || response.Error != nil || response.Result.Decision == nil ||
		response.Result.Decision.Enum == nil || *response.Result.Decision.Enum != protocol.Cancel {
		t.Fatalf("缺少连接时 approval 未 fail-closed: %#v", response)
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
	agent, err := newProcessTestAgent(ctx, Config{
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
	agent, err := newProcessTestAgent(constructionCtx, Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CodexPath: path,
	})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	cancelConstruction()
	if err = agent.runtimeCtx.Err(); err != nil {
		t.Fatalf("construction context 抢先终止 Agent runtime: %v", err)
	}
	// fake shell 的调度/回收可能受并行测试宿主拥塞影响；go test -timeout 统一提供死锁保护。
	if err = agent.Close(context.Background()); err != nil {
		t.Fatalf("显式关闭 Agent 失败: %v", err)
	}
}
