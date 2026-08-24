package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
)

// lockedBuffer 是并发安全的 transport 测试 writer。
type lockedBuffer struct {
	// mu 保护 buffer。
	mu sync.Mutex
	// buffer 保存写入字节。
	buffer bytes.Buffer
}

// Write 记录 transport 输出。
func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

// String 返回当前输出快照。
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// TestClaudeTransportCallAndControlRequest 验证 control 关联和反向请求响应共享单写边界。
func TestClaudeTransportCallAndControlRequest(t *testing.T) {
	reader, input := io.Pipe()
	writer := &lockedBuffer{}
	transport := newClaudeTransport(context.Background(), reader, writer, reader, transportOptions{
		ControlHandler: func(_ context.Context, request *protocol.ControlRequestMessage) (any, error) {
			if request.ControlSubtype() != protocol.ControlCanUseTool {
				t.Fatalf("subtype = %q", request.ControlSubtype())
			}
			return protocol.PermissionResult{Behavior: "deny", Message: "test"}, nil
		},
	})
	t.Cleanup(func() { _ = transport.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var response map[string]any
		result <- transport.Call(ctx, protocol.InitializeControlRequest{Subtype: protocol.ControlInitialize}, &response)
	}()

	requestID := waitWrittenRequestID(t, writer)
	_, _ = io.WriteString(input, `{"type":"control_response","response":{"subtype":"success","request_id":"`+requestID+`","response":{"ok":true}}}`+"\n")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, _ = io.WriteString(input, `{"type":"control_request","request_id":"cli-1","request":{"subtype":"can_use_tool","tool_name":"Read","input":{},"tool_use_id":"t1"}}`+"\n")
	waitForSubstring(t, writer, `"request_id":"cli-1"`)
}

// waitWrittenRequestID 等待第一条 control request 并返回请求标识。
func waitWrittenRequestID(t *testing.T, writer *lockedBuffer) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, _ := bufio.NewReader(strings.NewReader(writer.String())).ReadString('\n')
		if line != "" {
			var envelope struct {
				// RequestID 是 transport 生成的控制请求标识。
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal([]byte(line), &envelope) == nil && envelope.RequestID != "" {
				return envelope.RequestID
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待 control request 超时")
	return ""
}

// waitForSubstring 等待 writer 包含目标片段。
func waitForSubstring(t *testing.T, writer *lockedBuffer, target string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(writer.String(), target) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 %q 超时，输出=%q", target, writer.String())
}

// TestClaudeTransportMalformedAndOversized 验证坏 JSON 可跳过而超限帧终止 transport。
func TestClaudeTransportMalformedAndOversized(t *testing.T) {
	reader := strings.NewReader("not-json\n" + strings.Repeat("x", 128) + "\n")
	transport := newClaudeTransport(context.Background(), reader, io.Discard, nil, transportOptions{MaxLineBytes: 32})
	select {
	case <-transport.Done():
		if !errors.Is(transport.Err(), ErrClaudeFrameTooLarge) {
			t.Fatalf("Err() = %v", transport.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transport 未结束")
	}
}

// TestClaudeTransportCancelsIncomingControl 验证 CLI cancel 会终止对应反向控制回调。
func TestClaudeTransportCancelsIncomingControl(t *testing.T) {
	reader, input := io.Pipe()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	transport := newClaudeTransport(context.Background(), reader, io.Discard, reader, transportOptions{
		ControlHandler: func(ctx context.Context, _ *protocol.ControlRequestMessage) (any, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
	})
	t.Cleanup(func() { _ = transport.Close() })
	_, _ = io.WriteString(input, `{"type":"control_request","request_id":"cli-cancel","request":{"subtype":"can_use_tool","tool_name":"Read","input":{},"tool_use_id":"t1"}}`+"\n")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("控制回调未启动")
	}
	_, _ = io.WriteString(input, `{"type":"control_cancel_request","request_id":"cli-cancel"}`+"\n")
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("控制回调未取消")
	}
}
