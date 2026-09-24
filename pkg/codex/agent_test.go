package codex

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// TestAgentInitializeAdvertisesRuntimeCapabilities 验证握手成功后声明 V1 已接线的真实能力。
func TestAgentInitializeAdvertisesRuntimeCapabilities(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t)
	agent.executable.Version = "0.149.1"
	response := initializeTestAgent(t, agent)
	if response.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Fatalf("协议版本为 %d，期望 %d", response.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	if response.AgentInfo == nil || response.AgentInfo.Name != "codex" {
		t.Fatalf("Agent 信息为 %#v，期望 codex", response.AgentInfo)
	}
	runtimeMeta, ok := response.AgentInfo.Meta["runtime"].(map[string]any)
	if !ok || runtimeMeta["version"] != "0.149.1" {
		t.Fatalf("Runtime 元数据为 %#v，期望 CLI 版本 0.149.1", response.AgentInfo.Meta)
	}
	if !response.AgentCapabilities.LoadSession || response.AgentCapabilities.SessionCapabilities.Close == nil ||
		response.AgentCapabilities.SessionCapabilities.Resume == nil ||
		response.AgentCapabilities.SessionCapabilities.AdditionalDirectories == nil {
		t.Fatalf("runtime session 能力为 %#v", response.AgentCapabilities)
	}
	if response.AgentCapabilities.Auth.Logout == nil {
		t.Fatal("initialize 未声明已实现的 logout 能力")
	}
	if !response.AgentCapabilities.McpCapabilities.Http || response.AgentCapabilities.McpCapabilities.Sse ||
		response.AgentCapabilities.McpCapabilities.Acp {
		t.Fatalf("MCP transport 能力为 %#v，期望 stdio 隐式支持且仅声明 HTTP", response.AgentCapabilities.McpCapabilities)
	}
	if len(response.AuthMethods) != 2 || response.AuthMethods[0].Agent == nil ||
		response.AuthMethods[0].Agent.Id != "api-key" || response.AuthMethods[1].Agent == nil ||
		response.AuthMethods[1].Agent.Id != "chat-gpt" {
		t.Fatalf("认证方法为 %#v，期望仅 API Key 与 ChatGPT", response.AuthMethods)
	}
	wantMeta := map[string]any{
		"steering": map[string]any{"supported": true},
		"fork":     map[string]any{"mode": "unsupported"},
	}
	if !reflect.DeepEqual(response.Meta, wantMeta) {
		t.Fatalf("initialize meta 为 %#v，期望 %#v", response.Meta, wantMeta)
	}
}

// TestAgentInitializeHidesChatGPTWhenBrowserIsDisabled 验证 NO_BROWSER 非空时隐藏浏览器登录。
// 若 Initialize 绕过 authenticator 的环境边界并继续宣告浏览器登录，本测试应失败。
func TestAgentInitializeHidesChatGPTWhenBrowserIsDisabled(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t)
	agent.auth.getenv = func(name string) string {
		if name == "NO_BROWSER" {
			return "1"
		}
		return ""
	}
	response := initializeTestAgent(t, agent)
	if len(response.AuthMethods) != 1 || response.AuthMethods[0].Agent == nil ||
		response.AuthMethods[0].Agent.Id != "api-key" {
		t.Fatalf("NO_BROWSER 下认证方法为 %#v，期望仅 API Key", response.AuthMethods)
	}
}

// TestAgentReturnsMethodNotFoundForUnadvertisedOperations 验证未声明的可选能力返回 SDK 标准错误。
// 若占位 Agent 静默接受未支持请求，本测试应失败。
func TestAgentReturnsMethodNotFoundForUnadvertisedOperations(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t)
	tests := []struct {
		// name 描述当前未声明的可选操作。
		name string
		// call 执行操作并只返回待检查的 SDK 错误。
		call func() error
	}{
		{
			name: "session list",
			call: func() error {
				_, err := agent.ListSessions(context.Background(), acp.ListSessionsRequest{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.call()
			var requestErr *acp.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != -32601 {
				t.Fatalf("操作错误为 %v，期望 SDK MethodNotFound", err)
			}
		})
	}
}

// TestAgentCancelIsIdempotentWithoutRuntime 验证没有活动 runtime 时取消仍可安全重复调用。
// 若空状态取消产生错误或 panic，本测试应失败。
func TestAgentCancelIsIdempotentWithoutRuntime(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t)
	for range 2 {
		if err := agent.Cancel(context.Background(), acp.CancelNotification{}); err != nil {
			t.Fatalf("空 runtime 取消返回错误: %v", err)
		}
	}
}

// TestAgentReturnsResourceNotFoundForMissingSession 锁住 Session 缺失的 ACP 标准错误码。
func TestAgentReturnsResourceNotFoundForMissingSession(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t)
	initializeTestAgent(t, agent)
	_, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: "missing-session",
		Prompt:    []acp.ContentBlock{acp.TextBlock("continue")},
	})
	var requestErr *acp.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != acpResourceNotFoundCode {
		t.Fatalf("missing Session error = %v", err)
	}
}

// TestNewAgentRejectsNilLogger 验证 Adapter 在构造期拒绝缺失诊断依赖。
// 若 nil logger 延迟到 runtime 事件路径才 panic，本测试应失败。
func TestNewAgentRejectsNilLogger(t *testing.T) {
	t.Parallel()

	if _, err := NewAgent(context.Background(), Config{}); !errors.Is(err, ErrInvalidLogger) {
		t.Fatalf("构造错误为 %v，期望匹配 %v", err, ErrInvalidLogger)
	}
}

// newTestAgent 创建使用丢弃 logger 的测试 Codex Agent。
func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(context.Context, protocol.ClientRequest, any) error { return nil }
	runtimeCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newAgentWithClient(logger, runtimeCtx, cancel, newAppServerClient(runtimeCtx, rpc))
}

// initializeTestAgent 完成测试 Agent 的 app-server/ACP initialize。
func initializeTestAgent(t *testing.T, agent *Agent) acp.InitializeResponse {
	t.Helper()
	response, err := agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	if err != nil {
		t.Fatalf("initialize 返回错误: %v", err)
	}
	return response
}
