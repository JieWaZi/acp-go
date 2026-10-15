package nativeacp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// TestNativeCallPreservesExtensionPayloadAndErrors 验证原生调用入口不改写扩展参数和 JSON-RPC 错误。
func TestNativeCallPreservesExtensionPayloadAndErrors(t *testing.T) {
	agent, peer, _ := startLifecycleBridge(t, true, true)
	params := json.RawMessage(`{"_meta":{"trace":"literal"},"content":[{"type":"text","text":"hello"}]}`)
	for _, call := range []func(context.Context, string, any) (json.RawMessage, error){agent.CallNative, peer.CallExtension} {
		raw, err := call(context.Background(), "_echo", params)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(params, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("extension changed payload: %s", raw)
		}
		_, err = call(context.Background(), "_error", nil)
		var requestErr *acp.RequestError
		if !errors.As(err, &requestErr) || requestErr.Code != -32042 || requestErr.Message != "fixture error" || !reflect.DeepEqual(requestErr.Data, map[string]any{"_meta": map[string]any{"trace": "opaque"}, "retry": false}) {
			t.Fatalf("extension lost native error: %v", err)
		}
	}
}

// TestNativeConfigSnapshotAndCompleteEnvironment 捕获调用者修改环境或版本参数影响未来版本探测的回归。
func TestNativeConfigSnapshotAndCompleteEnvironment(t *testing.T) {
	t.Setenv("NATIVE_PARENT_ONLY", "must-not-inherit")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestLifecycleProcess$", "--", "prefix-value"}
	versionArgs := []string{"-test.run=^TestLifecycleProcess$", "--", "--version"}
	environment := []string{"NATIVE_LIFECYCLE_PROCESS=1", "NATIVE_INSTANCE=isolated"}
	agent, err := NewAgent(context.Background(), Config{Command: binary, Args: args, VersionArgs: versionArgs, Environment: environment, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	args[2] = "changed-prefix"
	versionArgs[2] = "changed-version"
	environment[0] = "NATIVE_LIFECYCLE_PROCESS="
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if response.AgentInfo.Version != "native-version" || acpmeta.RuntimeVersion(response.AgentInfo.Meta) != "fixture-cli 7.2.1" {
		t.Fatalf("version snapshot: %+v", response.AgentInfo)
	}
	raw, err := agent.CallNative(ctx, "_environment", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		// Parent 是子进程是否继承未授权宿主变量的证据。
		Parent string `json:"parent"`
		// Instance 是子进程所见的实例隔离变量。
		Instance string `json:"instance"`
		// Prefix 是 ACP 进程启动时实际收到的参数。
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Parent != "" || result.Instance != "isolated" || result.Prefix != "prefix-value" {
		t.Fatalf("process configuration: %+v", result)
	}
	if agent.config.Args[2] != "prefix-value" {
		t.Fatal("agent retained caller argument slice")
	}
	repeated, err := agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil || acpmeta.RuntimeVersion(repeated.AgentInfo.Meta) != "fixture-cli 7.2.1" {
		t.Fatalf("repeat initialize: %+v %v", repeated, err)
	}
}

// TestResolveCommandUsesOnlySelectedEnvironment 捕获 PATH 回退或错误使用父环境的回归。
func TestResolveCommandUsesOnlySelectedEnvironment(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "fixture-cli")
	if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	if got, err := ResolveCommand("fixture-cli", []string{"PATH=" + directory}); err != nil || got != path {
		t.Fatalf("selected environment: %s %v", got, err)
	}
	if _, err := ResolveCommand("fixture-cli", []string{}); err == nil {
		t.Fatal("empty environment fell back to parent PATH")
	}
	if _, err := ResolveCommand(filepath.Join(directory, "missing", "fixture-cli"), []string{"PATH=" + directory}); err == nil {
		t.Fatal("explicit missing path fell back to PATH")
	}
}

// TestPromptFailedRestoreKeepsClosedSession 验证恢复失败不会重新注册已关闭的执行队列。
func TestPromptFailedRestoreKeepsClosedSession(t *testing.T) {
	agent, peer, _ := startLifecycleBridge(t, true, true)
	_, _ = peer.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "a"})
	_, err := peer.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: "a", Cwd: "/fixture", McpServers: []acp.McpServer{}, Meta: map[string]any{"reject": true}})
	if err == nil {
		t.Fatal("fixture must reject restore")
	}
	awaitPrompt(t, promptAgentAsync(agent, context.Background(), "a", "closed"), true)
}

// TestPromptNewSessionAndSupportedClose 覆盖创建的会话注册与上游支持 close 的正常路径。
func TestPromptNewSessionAndSupportedClose(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	response, err := peer.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/fixture", McpServers: []acp.McpServer{}})
	if err != nil || response.SessionId != "new" {
		t.Fatalf("new: %+v %v", response, err)
	}
	running := promptAsync(peer, context.Background(), "new", "created")
	expectUpdate(t, host, "new:created")
	if _, err := peer.CallExtension(context.Background(), "_support_close", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "new"}); err != nil {
		t.Fatal(err)
	}
	awaitPrompt(t, running, true)
	awaitPrompt(t, promptAgentAsync(agent, context.Background(), "new", "closed"), true)
}

// TestNativeUnboundCallbackWaitingIsCancelable 验证尚未绑定的回调可由请求上下文或 Close 解除。
func TestNativeUnboundCallbackWaitingIsCancelable(t *testing.T) {
	for _, action := range []string{"context", "close"} {
		t.Run(action, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			agent, err := NewAgent(context.Background(), Config{Command: binary, Args: []string{"-test.run=^TestLifecycleProcess$"}, Environment: []string{"NATIVE_LIFECYCLE_PROCESS=1"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := agent.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := agent.CallNative(ctx, "_callback", map[string]any{"method": "fs/read_text_file", "params": map[string]any{"sessionId": "a", "path": "/fixture"}})
				done <- err
			}()
			if action == "close" {
				if err := agent.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			awaitPrompt(t, done, true)
		})
	}
}

// TestPromptRestoreReplacesLiveQueue 验证显式恢复废弃旧执行代且新执行不会被旧清理删除。
func TestPromptRestoreReplacesLiveQueue(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	old := promptAgentAsync(agent, context.Background(), "a", "old")
	expectUpdate(t, host, "a:old")
	queued := promptAgentAsync(agent, context.Background(), "a", "old-queued")
	waitQueue(t, agent, "a", 1)
	if _, err := peer.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: "a", Cwd: "/fixture"}); err != nil {
		t.Fatal(err)
	}
	fresh := promptAgentAsync(agent, context.Background(), "a", "fresh")
	expectUpdate(t, host, "a:fresh")
	awaitPrompt(t, old, true)
	awaitPrompt(t, queued, true)
	agent.mutex.Lock()
	active := agent.active["a"]
	agent.mutex.Unlock()
	if !active {
		t.Fatal("old execution removed restored active state")
	}
	releaseNative(t, peer, "a")
	awaitPrompt(t, fresh, false)
}

// TestPromptActiveContextCancellationNotifiesNative 验证正在运行的上下文取消保留原生 session/cancel。
func TestPromptActiveContextCancellationNotifiesNative(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	ctx, cancel := context.WithCancel(context.Background())
	running := promptAgentAsync(agent, ctx, "a", "cancel-context")
	expectUpdate(t, host, "a:cancel-context")
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	waitQueue(t, agent, "a", 1)
	cancel()
	awaitPrompt(t, running, true)
	expectUpdate(t, host, "a:next")
	raw, err := peer.CallExtension(context.Background(), "_cancels", nil)
	if err != nil || string(raw) != `["a"]` {
		t.Fatalf("native cancel: %s %v", raw, err)
	}
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
}
