package nativeacp

import (
	"context"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// completionGateContext 在旧 SDK 请求完成后暂停调用者取消检查，精确控制恢复时序。
type completionGateContext struct {
	// Context 提供真实父级取消，SDK 派生上下文仍按原规则取消。
	context.Context
	// entered 在取消检查到达闸门时关闭。
	entered chan struct{}
	// release 允许检查继续，不向生产实现注入测试钩子。
	release chan struct{}
	// once 使重复 Err 查询安全共享同一闸门。
	once sync.Once
}

// Err 只阻塞已取消的父上下文检查，不影响请求建立和原生响应等待。
func (gate *completionGateContext) Err() error {
	err := gate.Context.Err()
	if err != nil {
		gate.once.Do(func() { close(gate.entered) })
		<-gate.release
	}
	return err
}

// notificationGateContext 暂停 SDK 发送通知前的上下文检查，模拟在途取消通知。
type notificationGateContext struct {
	// Context 保留无取消的基础上下文。
	context.Context
	// entered 表示本地执行已取消但原生通知尚未写出。
	entered chan struct{}
	// release 控制原生通知何时真正写入 SDK 连接。
	release chan struct{}
	// once 保护闸门进入信号。
	once sync.Once
}

// Done 在 SDK SendNotification 的入口暂停，测试无需替换进程传输。
func (gate *notificationGateContext) Done() <-chan struct{} {
	gate.once.Do(func() { close(gate.entered) })
	<-gate.release
	return gate.Context.Done()
}

// awaitGate 以确定性闸门同步竞态步骤，不依赖睡眠猜测旧请求完成位置。
func awaitGate(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation gate was not reached")
	}
}

// restoreGeneration 成功恢复同一持久 ID，为两类竞态注册新的执行代。
func restoreGeneration(t *testing.T, peer *acp.ClientSideConnection, method string) {
	t.Helper()
	var err error
	if method == "load" {
		_, err = peer.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: "a", Cwd: "/fixture", McpServers: []acp.McpServer{}})
	} else {
		_, err = peer.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: "a", Cwd: "/fixture"})
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestOldPromptCancellationCannotCancelRestoredGeneration 捕获旧请求完成后按持久 ID 误取消新执行的回归。
func TestOldPromptCancellationCannotCancelRestoredGeneration(t *testing.T) {
	for _, method := range []string{"load", "resume"} {
		t.Run(method, func(t *testing.T) {
			agent, peer, host := startLifecycleBridge(t, true, true)
			base, cancel := context.WithCancel(context.Background())
			gate := &completionGateContext{Context: base, entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			defer release()
			old := promptAgentAsync(agent, gate, "a", "old")
			expectUpdate(t, host, "a:old")
			cancel()
			awaitGate(t, gate.entered)
			restoreGeneration(t, peer, method)
			fresh := promptAgentAsync(agent, context.Background(), "a", "fresh")
			expectUpdate(t, host, "a:fresh")
			release()
			awaitPrompt(t, old, true)
			agent.mutex.Lock()
			freshCanceled := agent.turnContexts["a"] == nil || agent.turnContexts["a"].Err() != nil
			agent.mutex.Unlock()
			if freshCanceled {
				t.Fatal("old automatic cancellation canceled restored execution")
			}
			raw, err := peer.CallExtension(context.Background(), "_cancels", nil)
			if err != nil || string(raw) != `[]` {
				t.Fatalf("old generation sent native session cancel: %s %v", raw, err)
			}
			releaseNative(t, peer, "a")
			awaitPrompt(t, fresh, false)
		})
	}
}

// TestPendingNativeCancelBlocksRestoredGeneration 捕获旧代取消通知未发送就放行新代的回归。
func TestPendingNativeCancelBlocksRestoredGeneration(t *testing.T) {
	for _, method := range []string{"load", "resume"} {
		t.Run(method, func(t *testing.T) {
			agent, peer, host := startLifecycleBridge(t, true, true)
			old := promptAgentAsync(agent, context.Background(), "a", "old")
			expectUpdate(t, host, "a:old")
			gate := &notificationGateContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			defer release()
			canceled := make(chan error, 1)
			go func() { canceled <- agent.Cancel(gate, acp.CancelNotification{SessionId: "a"}) }()
			awaitGate(t, gate.entered)
			awaitPrompt(t, old, true)
			restoreGeneration(t, peer, method)
			fresh := promptAgentAsync(agent, context.Background(), "a", "fresh")
			// 等待 fresh 已进入新队列；旧实现会直接发原生 prompt，因此永远达不到一个等待者。
			waitQueue(t, agent, "a", 1)
			select {
			case update := <-host.updates:
				t.Fatalf("restored generation started before native cancel delivery: %s", update)
			default:
			}
			release()
			awaitPrompt(t, canceled, false)
			expectUpdate(t, host, "a:fresh")
			raw, err := peer.CallExtension(context.Background(), "_cancels", nil)
			if err != nil || string(raw) != `["a"]` {
				t.Fatalf("missing native cancel: %s %v", raw, err)
			}
			releaseNative(t, peer, "a")
			awaitPrompt(t, fresh, false)
		})
	}
}
