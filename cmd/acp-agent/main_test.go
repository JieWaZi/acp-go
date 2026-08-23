package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestRunStartsDefaultAndExplicitCodex 验证默认选择与显式 codex 都进入真实 SDK stdio 服务。
// 若默认值改变、显式选择走不同实现或 composition root 未启动 SDK，本测试应失败。
func TestRunStartsDefaultAndExplicitCodex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// name 描述 Adapter 参数形式。
		name string
		// args 是传给组合根的完整命令行参数。
		args []string
	}{
		{name: "默认 Codex", args: []string{}},
		{name: "显式 Codex", args: []string{"--adapter", "codex"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			response := runInitializeExchange(t, tt.args)
			if response.ProtocolVersion != acp.ProtocolVersionNumber {
				t.Fatalf("协议版本为 %d，期望 %d", response.ProtocolVersion, acp.ProtocolVersionNumber)
			}
			if response.AgentInfo == nil || response.AgentInfo.Name != "codex" {
				t.Fatalf("Agent 信息为 %#v，期望 codex", response.AgentInfo)
			}
		})
	}
}

// TestRunRejectsUnknownAdapterBeforeProtocolOutput 验证未知 Adapter 只产生 stderr 诊断。
// 若选择器静默回退或 SDK 已向 stdout 写出协议帧，本测试应失败。
func TestRunRejectsUnknownAdapterBeforeProtocolOutput(t *testing.T) {
	t.Parallel()

	var protocolOutput bytes.Buffer
	var diagnostics bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--adapter", "unknown"},
		processIO{
			input:       bytes.NewReader(nil),
			output:      &protocolOutput,
			diagnostics: &diagnostics,
		},
	)

	if exitCode == 0 {
		t.Fatal("未知 Adapter 返回了成功退出码")
	}
	if protocolOutput.Len() != 0 {
		t.Fatalf("未知 Adapter 向 stdout 写入了 %q", protocolOutput.String())
	}
	if !strings.Contains(diagnostics.String(), `adapter "unknown"`) ||
		!strings.Contains(diagnostics.String(), "adapter not found") {
		t.Fatalf("诊断信息 %q 缺少 Adapter 名称或根因", diagnostics.String())
	}
}

// TestRunRejectsUnexpectedArguments 验证多余位置参数在协议启动前被拒绝。
// 若拼写错误的启动参数被静默忽略，本测试应失败。
func TestRunRejectsUnexpectedArguments(t *testing.T) {
	t.Parallel()

	var protocolOutput bytes.Buffer
	var diagnostics bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"unexpected"},
		processIO{
			input:       bytes.NewReader(nil),
			output:      &protocolOutput,
			diagnostics: &diagnostics,
		},
	)

	if exitCode == 0 {
		t.Fatal("多余参数返回了成功退出码")
	}
	if protocolOutput.Len() != 0 {
		t.Fatalf("多余参数向 stdout 写入了 %q", protocolOutput.String())
	}
	if !strings.Contains(diagnostics.String(), "unexpected arguments") {
		t.Fatalf("诊断信息 %q 缺少多余参数根因", diagnostics.String())
	}
}

// runInitializeExchange 启动命令、发送 initialize，并在收到响应后关闭客户端输入。
func runInitializeExchange(t *testing.T, args []string) acp.InitializeResponse {
	t.Helper()

	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
		closeTestPipe(t, clientInput)
		closeTestPipe(t, serverOutput)
	})

	var diagnostics bytes.Buffer
	exitResult := make(chan int, 1)
	go func() {
		exitResult <- run(
			context.Background(),
			args,
			processIO{
				input:       serverInput,
				output:      serverOutput,
				diagnostics: &diagnostics,
			},
		)
	}()

	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n"
	if _, err := io.WriteString(clientOutput, request); err != nil {
		t.Fatalf("写入 initialize 请求失败: %v", err)
	}

	responseLine, err := bufio.NewReader(clientInput).ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取 initialize 响应失败: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(responseLine, &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(envelope["error"]) != 0 {
		t.Fatalf("initialize 返回协议错误: %s", envelope["error"])
	}

	var response acp.InitializeResponse
	if err = json.Unmarshal(envelope["result"], &response); err != nil {
		t.Fatalf("解析 initialize result 失败: %v", err)
	}

	if err = clientOutput.Close(); err != nil {
		t.Fatalf("关闭客户端输出失败: %v", err)
	}
	select {
	case exitCode := <-exitResult:
		if exitCode != 0 {
			t.Fatalf("命令退出码为 %d，诊断: %s", exitCode, diagnostics.String())
		}
	case <-time.After(time.Second):
		t.Fatal("等待命令退出超时")
	}

	return response
}

// closeTestPipe 关闭测试管道，并把真实清理失败记录到当前测试。
func closeTestPipe(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("关闭测试管道失败: %v", err)
	}
}
