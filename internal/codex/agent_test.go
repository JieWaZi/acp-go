package codex

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestAgentInitializeAdvertisesOnlyFoundationCapabilities 验证占位 Agent 只声明稳定协议身份。
// 若 foundation 阶段误报认证、加载或其他尚未实现能力，本测试应失败。
func TestAgentInitializeAdvertisesOnlyFoundationCapabilities(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t)
	response, err := agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	if err != nil {
		t.Fatalf("initialize 返回错误: %v", err)
	}
	if response.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Fatalf("协议版本为 %d，期望 %d", response.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	if response.AgentInfo == nil || response.AgentInfo.Name != "codex" {
		t.Fatalf("Agent 信息为 %#v，期望 codex", response.AgentInfo)
	}
	if response.AgentCapabilities.LoadSession {
		t.Fatal("foundation Agent 不应声明 session/load")
	}
	if len(response.AuthMethods) != 0 {
		t.Fatalf("foundation Agent 声明了 %d 个认证方法，期望 0", len(response.AuthMethods))
	}
}

// TestAgentReportsUnavailableRuntimeForRequiredOperations 验证必选会话操作不会伪装成功。
// 若 app-server 尚未实现时 new 或 prompt 返回零值成功，本测试应失败。
func TestAgentReportsUnavailableRuntimeForRequiredOperations(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t)
	tests := []struct {
		// name 描述当前必选会话操作。
		name string
		// call 执行操作并只返回待检查的错误链。
		call func() error
	}{
		{
			name: "session new",
			call: func() error {
				_, err := agent.NewSession(context.Background(), acp.NewSessionRequest{})
				return err
			},
		},
		{
			name: "session prompt",
			call: func() error {
				_, err := agent.Prompt(context.Background(), acp.PromptRequest{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.call(); !errors.Is(err, ErrRuntimeUnavailable) {
				t.Fatalf("操作错误为 %v，期望匹配 %v", err, ErrRuntimeUnavailable)
			}
		})
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
			name: "authenticate",
			call: func() error {
				_, err := agent.Authenticate(context.Background(), acp.AuthenticateRequest{})
				return err
			},
		},
		{
			name: "logout",
			call: func() error {
				_, err := agent.Logout(context.Background(), acp.LogoutRequest{})
				return err
			},
		},
		{
			name: "session close",
			call: func() error {
				_, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{})
				return err
			},
		},
		{
			name: "session list",
			call: func() error {
				_, err := agent.ListSessions(context.Background(), acp.ListSessionsRequest{})
				return err
			},
		},
		{
			name: "session resume",
			call: func() error {
				_, err := agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{})
				return err
			},
		},
		{
			name: "session config",
			call: func() error {
				_, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{})
				return err
			},
		},
		{
			name: "session mode",
			call: func() error {
				_, err := agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{})
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

// TestNewAgentRejectsNilLogger 验证 Adapter 在构造期拒绝缺失诊断依赖。
// 若 nil logger 延迟到 runtime 事件路径才 panic，本测试应失败。
func TestNewAgentRejectsNilLogger(t *testing.T) {
	t.Parallel()

	if _, err := NewAgent(nil); !errors.Is(err, ErrInvalidLogger) {
		t.Fatalf("构造错误为 %v，期望匹配 %v", err, ErrInvalidLogger)
	}
}

// newTestAgent 创建使用丢弃 logger 的测试 Codex Agent。
func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent, err := NewAgent(logger)
	if err != nil {
		t.Fatalf("创建测试 Agent 失败: %v", err)
	}
	return agent
}
