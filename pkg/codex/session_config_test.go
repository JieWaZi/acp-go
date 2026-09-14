package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// TestAgentDefaultModeUserInputOption 验证公开构造选项传入真实 Agent，零值不改变其他调用方。
func TestAgentDefaultModeUserInputOption(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		agent, err := newProcessTestAgent(context.Background(), Config{
			Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
			CodexPath:                   writeExecutableScript(t, "cat >/dev/null\n"),
			DefaultModeRequestUserInput: enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		if agent.defaultModeRequestUserInput != enabled {
			t.Errorf("构造选项未传递：want=%t", enabled)
		}
		if err := agent.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSessionDefaultModeUserInput 验证新建与恢复会话仅在未声明 feature 时补默认值。
func TestSessionDefaultModeUserInput(t *testing.T) {
	tests := []struct {
		// name 标识有效配置场景。
		name string
		// effective 模拟上游合并用户、项目、profile 与启动参数后的 ConfigToml。
		effective string
		// enabled 表示调用方是否启用默认提问策略。
		enabled bool
		// wantDefault 表示是否应向 thread 请求补 feature。
		wantDefault bool
	}{
		{name: "unset", effective: `{}`, enabled: true, wantDefault: true},
		{name: "null_features", effective: `{"features":null}`, enabled: true, wantDefault: true},
		{name: "other_features", effective: `{"features":{"multi_agent":false}}`, enabled: true, wantDefault: true},
		{name: "explicit_true", effective: `{"features":{"default_mode_request_user_input":true}}`, enabled: true},
		{name: "explicit_false", effective: `{"features":{"default_mode_request_user_input":false}}`, enabled: true},
		{name: "caller_opt_out", effective: `{}`},
	}
	for _, tt := range tests {
		for _, resume := range []bool{false, true} {
			operation := "new"
			if resume {
				operation = "resume"
			}
			t.Run(tt.name+"/"+operation, func(t *testing.T) {
				rpc := newFakeAppServerRPC()
				cwd := t.TempDir()
				reads := 0
				var got map[string]json.RawMessage
				rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
					switch request.Method() {
					case protocol.MethodInitialize:
						return nil
					case protocol.MethodConfigRead:
						reads++
						params := request.(protocol.ConfigReadRequest).Params
						if params.Cwd == nil || *params.Cwd != cwd {
							t.Fatalf("未按当前 session cwd 读取配置: %#v", params)
						}
						result.(*protocol.ConfigReadResponse).Config = json.RawMessage(tt.effective)
					case protocol.MethodThreadStart:
						got = request.(protocol.ThreadStartRequest).Params.Config
						result.(*protocol.ThreadStartResponse).Thread.ID = "thread-default-input"
					case protocol.MethodThreadResume:
						got = request.(protocol.ThreadResumeRequest).Params.Config
						result.(*protocol.ThreadResumeResponse).Thread.ID = "thread-default-input"
					default:
						return errors.New("unexpected call: " + request.Method())
					}
					return nil
				}
				agent := newRuntimeTestAgent(t, rpc)
				agent.defaultModeRequestUserInput = tt.enabled
				// 即使关闭 MCP 过滤或未启用 MCP，提问默认策略也必须独立读取用户配置。
				agent.setEnvironment([]string{disableMCPConfigFilteringEnv + "=true"})
				var err error
				if resume {
					_, err = agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{
						Cwd: cwd, SessionId: "thread-default-input",
					})
				} else {
					_, err = agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd})
				}
				if err != nil {
					t.Fatal(err)
				}
				value, exists := got["features."+defaultModeRequestUserInputFeature]
				if exists != tt.wantDefault || (exists && string(value) != "true") {
					t.Fatalf("提问默认配置为 %s，wantDefault=%t", value, tt.wantDefault)
				}
				if _, replaced := got["features"]; replaced {
					t.Fatal("不应覆盖完整 features 表")
				}
				if (reads == 1) != tt.enabled {
					t.Fatalf("config/read 次数=%d，调用方启用=%t", reads, tt.enabled)
				}
			})
		}
	}
}

// TestSessionDefaultModeUserInputReadFailure 验证读取失败或损坏配置不会被当作未设置。
func TestSessionDefaultModeUserInputReadFailure(t *testing.T) {
	for _, raw := range []string{"rpc_error", "", "null", `{"features":false}`} {
		t.Run(raw, func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
				switch request.Method() {
				case protocol.MethodInitialize:
					return nil
				case protocol.MethodConfigRead:
					if raw == "rpc_error" {
						return errors.New("config read failed")
					}
					result.(*protocol.ConfigReadResponse).Config = json.RawMessage(raw)
					return nil
				default:
					t.Fatalf("读取失败后仍发出请求: %s", request.Method())
					return nil
				}
			}
			agent := newRuntimeTestAgent(t, rpc)
			agent.defaultModeRequestUserInput = true
			if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err == nil {
				t.Fatal("应返回配置读取错误")
			}
		})
	}
}
