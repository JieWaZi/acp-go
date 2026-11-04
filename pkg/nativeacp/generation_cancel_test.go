package nativeacp

import (
	"context"
	"sync"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

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

// TestOldPromptCancellationCannotCancelRestoredGeneration checks exact owner identity.
func TestOldPromptCancellationCannotCancelRestoredGeneration(t *testing.T) {
	for _, method := range []string{"load", "resume"} {
		t.Run(method, func(t *testing.T) {
			agent, peer, host := startLifecycleBridge(t, true, true)
			old := promptAgentAsync(agent, context.Background(), "a", "old")
			expectUpdate(t, host, "a:old")
			agent.mutex.Lock()
			owner := agent.turnOwners["a"]
			agent.mutex.Unlock()
			releaseNative(t, peer, "a")
			awaitPrompt(t, old, false)
			restoreGeneration(t, peer, method)
			fresh := promptAgentAsync(agent, context.Background(), "a", "fresh")
			expectUpdate(t, host, "a:fresh")
			if err := agent.cancelPromptOwner(context.Background(), acp.CancelNotification{SessionId: "a"}, owner); err != nil {
				t.Fatal(err)
			}
			raw, err := peer.CallExtension(context.Background(), "_cancels", nil)
			if err != nil || string(raw) != `[]` {
				t.Fatalf("stale cancellation reached native generation: %s %v", raw, err)
			}
			releaseNative(t, peer, "a")
			awaitPrompt(t, fresh, false)
		})
	}
}

// TestPendingNativeCancelBlocksRestoredGeneration covers terminal receipt before the
// cancellation notification has finished writing. Neither restore nor a successor
// can enter while that old native cancellation could still affect it.
func TestPendingNativeCancelBlocksRestoredGeneration(t *testing.T) {
	for _, method := range []string{"load", "resume"} {
		t.Run(method, func(t *testing.T) {
			agent, peer, host := startLifecycleBridge(t, true, true)
			old := promptAgentAsync(agent, context.Background(), "a", "old")
			expectUpdate(t, host, "a:old")
			gate := &notificationGateContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer release()
			canceled := make(chan error, 1)
			go func() { canceled <- agent.Cancel(gate, acp.CancelNotification{SessionId: "a"}) }()
			awaitGate(t, gate.entered)
			releaseNative(t, peer, "a")
			var err error
			if method == "load" {
				_, err = peer.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: "a", Cwd: "/fixture", McpServers: []acp.McpServer{}})
			} else {
				_, err = peer.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: "a", Cwd: "/fixture"})
			}
			if err == nil {
				t.Fatal("restore crossed an unfinished cancellation")
			}
			select {
			case <-old:
				t.Fatal("owner released before cancel write completed")
			default:
			}
			release()
			awaitPrompt(t, canceled, false)
			awaitPrompt(t, old, false)
			restoreGeneration(t, peer, method)
			fresh := promptAgentAsync(agent, context.Background(), "a", "fresh")
			expectUpdate(t, host, "a:fresh")
			releaseNative(t, peer, "a")
			awaitPrompt(t, fresh, false)
		})
	}
}
