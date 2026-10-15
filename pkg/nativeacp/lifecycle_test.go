package nativeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// lifecycleHost 记录真实外层连接收到的原生会话事件。
type lifecycleHost struct {
	// Client 保留测试未调用的标准接口。
	acp.Client
	// notifications 保存完整通知 payload。
	notifications chan callbackObservation
	// updates 保存真实协议事件。
	updates chan string
	// callbacks 保存宿主收到的原生反向请求。
	callbacks chan callbackObservation
}

// SessionUpdate 把事件交给测试同步器，不阻塞协议通知队列。
func (host *lifecycleHost) SessionUpdate(_ context.Context, request acp.SessionNotification) error {
	host.captureNotification("session/update", request)
	host.updates <- string(request.SessionId) + ":" + request.Update.AgentMessageChunk.Content.Text.Text
	return nil
}

// startLifecycleBridge 启动测试二进制作为 CLI，并绑定真实的外层 ACP 连接。
func startLifecycleBridge(t *testing.T, fifo, strict bool) (*Agent, *acp.ClientSideConnection, *lifecycleHost) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(context.Background(), Config{
		Command: binary, Args: []string{"-test.run=^TestLifecycleProcess$", "--", "native-prefix"},
		VersionArgs: []string{"-test.run=^TestLifecycleProcess$", "--", "--version"},
		Environment: []string{"NATIVE_LIFECYCLE_PROCESS=1"},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)), PromptFIFO: fifo, StrictCloseSession: strict,
	})
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	outer := acp.NewAgentSideConnection(agent, outW, inR)
	agent.SetAgentConnection(outer)
	host := &lifecycleHost{updates: make(chan string, 32), callbacks: make(chan callbackObservation, 32), notifications: make(chan callbackObservation, 32)}
	peer := acp.NewClientSideConnection(host, inW, outR)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := agent.Close(ctx); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Error(err)
			}
		}
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := peer.LoadSession(ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(id), Cwd: "/fixture", McpServers: []acp.McpServer{}}); err != nil {
			t.Fatal(err)
		}
	}
	return agent, peer, host
}

// promptAsync 从真实外层发送请求，结果通道允许观察阻塞与释放。
func promptAsync(peer *acp.ClientSideConnection, ctx context.Context, id, text string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: acp.SessionId(id), Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
		done <- err
	}()
	return done
}

// promptAgentAsync 直接验证 Go API 的 FIFO，外层 SDK 对重叠请求会先取消旧上下文。
func promptAgentAsync(agent *Agent, ctx context.Context, id, text string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: acp.SessionId(id), Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
		done <- err
	}()
	return done
}

// expectUpdate 等待原生通知，失败时给出有界诊断。
func expectUpdate(t *testing.T, host *lifecycleHost, want string) {
	t.Helper()
	select {
	case got := <-host.updates:
		if got != want {
			t.Fatalf("update %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("missing update %q", want)
	}
}

// awaitPrompt 验证请求在生命周期操作后确实解除阻塞。
func awaitPrompt(t *testing.T, done <-chan error, wantError bool) {
	t.Helper()
	select {
	case err := <-done:
		if (err != nil) != wantError {
			t.Fatalf("prompt err=%v, want error=%v", err, wantError)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt remained blocked")
	}
}

// waitQueue 用内部状态只同步请求已经入队，断言仍针对外层协议结果。
func waitQueue(t *testing.T, agent *Agent, id acp.SessionId, count int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		agent.mutex.Lock()
		state := agent.sessions[id]
		queued := 0
		if state != nil && state.queue != nil {
			queued = len(state.queue.waiting)
		}
		agent.mutex.Unlock()
		if queued == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not reach %d", count)
}

// releaseNative 用扩展释放指定原生执行，避免测试依赖任意睡眠排序。
func releaseNative(t *testing.T, peer *acp.ClientSideConnection, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := peer.CallExtension(ctx, "_release", map[string]string{"sessionId": id}); err != nil {
		t.Fatal(err)
	}
}

// TestPromptFIFOPreservesOrderAndSessionConcurrency 捕获拒绝并发或全局串行化的回归。
func TestPromptFIFOPreservesOrderAndSessionConcurrency(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	first := promptAgentAsync(agent, context.Background(), "a", "first")
	expectUpdate(t, host, "a:first")
	second := promptAgentAsync(agent, context.Background(), "a", "second")
	waitQueue(t, agent, "a", 1)
	third := promptAgentAsync(agent, context.Background(), "a", "third")
	waitQueue(t, agent, "a", 2)
	other := promptAgentAsync(agent, context.Background(), "b", "other")
	expectUpdate(t, host, "b:other")
	releaseNative(t, peer, "b")
	awaitPrompt(t, other, false)
	releaseNative(t, peer, "a")
	awaitPrompt(t, first, false)
	expectUpdate(t, host, "a:second")
	releaseNative(t, peer, "a")
	awaitPrompt(t, second, false)
	expectUpdate(t, host, "a:third")
	releaseNative(t, peer, "a")
	awaitPrompt(t, third, false)
}

// TestPromptQueueContextCancellation 捕获已取消的排队请求仍进入原生进程的回归。
func TestPromptQueueContextCancellation(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	first := promptAgentAsync(agent, context.Background(), "a", "first")
	expectUpdate(t, host, "a:first")
	ctx, cancel := context.WithCancel(context.Background())
	queued := promptAgentAsync(agent, ctx, "a", "removed")
	waitQueue(t, agent, "a", 1)
	cancel()
	awaitPrompt(t, queued, true)
	waitQueue(t, agent, "a", 0)
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	waitQueue(t, agent, "a", 1)
	releaseNative(t, peer, "a")
	awaitPrompt(t, first, false)
	expectUpdate(t, host, "a:next")
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
}

// TestPromptCancelReleasesOnlyCurrentSession 捕获原生忽略 cancel 时本地等待无法结束的回归。
func TestPromptCancelReleasesOnlyCurrentSession(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	first := promptAgentAsync(agent, context.Background(), "a", "first")
	expectUpdate(t, host, "a:first")
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	waitQueue(t, agent, "a", 1)
	other := promptAgentAsync(agent, context.Background(), "b", "other")
	expectUpdate(t, host, "b:other")
	if err := peer.Cancel(context.Background(), acp.CancelNotification{SessionId: "a"}); err != nil {
		t.Fatal(err)
	}
	awaitPrompt(t, first, true)
	expectUpdate(t, host, "a:next")
	select {
	case err := <-other:
		t.Fatalf("other session canceled: %v", err)
	default:
	}
	releaseNative(t, peer, "b")
	awaitPrompt(t, other, false)
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := peer.CallExtension(ctx, "_cancels", nil)
	if err != nil || string(raw) != `["a"]` {
		t.Fatalf("native cancellation %s: %v", raw, err)
	}
}

// TestPromptCloseSessionDiscardsOldQueueAndAllowsRestore 验证恢复使用新队列而非复用旧等待者。
func TestPromptCloseSessionDiscardsOldQueueAndAllowsRestore(t *testing.T) {
	for _, method := range []string{"load", "resume"} {
		t.Run(method, func(t *testing.T) {
			agent, peer, host := startLifecycleBridge(t, true, true)
			current := promptAgentAsync(agent, context.Background(), "a", "old")
			expectUpdate(t, host, "a:old")
			old := promptAgentAsync(agent, context.Background(), "a", "old-queued")
			waitQueue(t, agent, "a", 1)
			_, err := peer.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "a"})
			var requestErr *acp.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != -32601 {
				t.Fatalf("close must retain native error: %v", err)
			}
			awaitPrompt(t, current, true)
			awaitPrompt(t, old, true)
			awaitPrompt(t, promptAgentAsync(agent, context.Background(), "a", "closed"), true)
			if method == "load" {
				_, err = peer.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: "a", Cwd: "/fixture", McpServers: []acp.McpServer{}})
			} else {
				_, err = peer.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: "a", Cwd: "/fixture"})
			}
			if err != nil {
				t.Fatal(err)
			}
			fresh := promptAgentAsync(agent, context.Background(), "a", "fresh")
			expectUpdate(t, host, "a:fresh")
			releaseNative(t, peer, "a")
			awaitPrompt(t, fresh, false)
		})
	}
}

// TestPromptCloseAndCrashWakeWaiters 捕获关闭或崩溃后排队请求永久挂起的回归。
func TestPromptCloseAndCrashWakeWaiters(t *testing.T) {
	for _, action := range []string{"close", "crash"} {
		t.Run(action, func(t *testing.T) {
			agent, peer, host := startLifecycleBridge(t, true, true)
			current := promptAgentAsync(agent, context.Background(), "a", "current")
			expectUpdate(t, host, "a:current")
			queued := promptAgentAsync(agent, context.Background(), "a", "queued")
			waitQueue(t, agent, "a", 1)
			if action == "crash" {
				_, _ = peer.CallExtension(context.Background(), "_crash", nil)
			} else {
				if err := agent.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			awaitPrompt(t, current, true)
			awaitPrompt(t, queued, true)
		})
	}
}

// TestLegacyPromptConcurrencyRemainsRejected 验证 opt-in 默认值不会改变现有适配器行为。
func TestLegacyPromptConcurrencyRemainsRejected(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, false, false)
	first := promptAgentAsync(agent, context.Background(), "a", "first")
	expectUpdate(t, host, "a:first")
	awaitPrompt(t, promptAgentAsync(agent, context.Background(), "a", "overlap"), true)
	releaseNative(t, peer, "a")
	awaitPrompt(t, first, false)
	if _, err := peer.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "a"}); err != nil {
		t.Fatal(err)
	}
}

// TestLifecycleProcess 是独立进程中的 ACP fixture，stdout 只用于 SDK 协议。
func TestLifecycleProcess(t *testing.T) {
	if os.Getenv("NATIVE_LIFECYCLE_PROCESS") == "" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		fmt.Print("fixture-cli 7.2.1")
		os.Exit(0)
	}
	ready := make(chan struct{})
	var connection *acp.Connection
	var mutex sync.Mutex
	gates := map[string]chan struct{}{}
	supportedClose := false
	cancels := []string{}
	connection = acp.NewConnection(func(ctx context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
		<-ready
		var request struct {
			// SessionID 标识原生请求所属会话。
			SessionID string `json:"sessionId"`
			// Prompt 保存原样收到的输入内容。
			Prompt []acp.ContentBlock `json:"prompt"`
			// Meta 用于选择恢复失败测试路径。
			Meta map[string]any `json:"_meta"`
		}
		_ = json.Unmarshal(raw, &request)
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}, "agentInfo": map[string]any{"name": "fixture", "version": "native-version"}}, nil
		case "session/new":
			return map[string]any{"sessionId": "new", "configOptions": []any{}}, nil
		case "session/load", "session/resume":
			if request.Meta["reject"] == true {
				return nil, acp.NewInvalidParams(map[string]any{"reason": "restore rejected"})
			}
			return map[string]any{"configOptions": []any{}}, nil
		case "session/prompt":
			gate := make(chan struct{})
			mutex.Lock()
			gates[request.SessionID] = gate
			mutex.Unlock()
			_ = connection.SendNotification(ctx, "session/update", map[string]any{"sessionId": request.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": acp.TextBlock(request.Prompt[0].Text.Text)}})
			select {
			case <-gate:
				return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
			case <-ctx.Done():
				return nil, acp.NewInternalError(nil)
			}
		case "session/cancel":
			mutex.Lock()
			cancels = append(cancels, request.SessionID)
			mutex.Unlock()
			return nil, nil
		case "_release":
			mutex.Lock()
			gate := gates[request.SessionID]
			delete(gates, request.SessionID)
			if gate != nil {
				close(gate)
			}
			mutex.Unlock()
			return map[string]any{}, nil
		case "_cancels":
			mutex.Lock()
			defer mutex.Unlock()
			return append([]string{}, cancels...), nil
		case "_crash":
			os.Exit(11)
		case "_notify":
			var notification struct {
				// Method 指定真实 CLI 通知名称。
				Method string `json:"method"`
				// Params 指定原样转发的通知字段。
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(raw, &notification); err != nil {
				return nil, acp.NewInvalidParams(nil)
			}
			if err := connection.SendNotification(ctx, notification.Method, notification.Params); err != nil {
				return nil, acp.NewInternalError(nil)
			}
			return map[string]any{}, nil
		case "_callback":
			var callback struct {
				// Method 指定此次测试回调名称。
				Method string `json:"method"`
				// Params 保存 CLI 原样发出的回调参数。
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(raw, &callback); err != nil {
				return nil, acp.NewInvalidParams(nil)
			}
			result, err := acp.SendRequest[json.RawMessage](connection, ctx, callback.Method, callback.Params)
			if err != nil {
				var protocolErr *acp.RequestError
				if errors.As(err, &protocolErr) {
					return nil, protocolErr
				}
				return nil, acp.NewInternalError(nil)
			}
			return result, nil
		case "_echo":
			return json.RawMessage(raw), nil
		case "_error":
			return nil, &acp.RequestError{Code: -32042, Message: "fixture error", Data: map[string]any{"_meta": map[string]any{"trace": "opaque"}, "retry": false}}
		case "_environment":
			return map[string]any{"parent": os.Getenv("NATIVE_PARENT_ONLY"), "instance": os.Getenv("NATIVE_INSTANCE"), "prefix": os.Args[len(os.Args)-1]}, nil
		case "_support_close":
			mutex.Lock()
			supportedClose = true
			mutex.Unlock()
			return map[string]any{}, nil
		case "session/close":
			mutex.Lock()
			supported := supportedClose
			mutex.Unlock()
			if supported {
				return map[string]any{}, nil
			}
		}
		return nil, acp.NewMethodNotFound(method)
	}, os.Stdout, os.Stdin)
	close(ready)
	<-connection.Done()
	os.Exit(0)
}
