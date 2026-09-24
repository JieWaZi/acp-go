package kimi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestReadWireFailure 使用真实 Kimi 2.0.2 的终态结构，防止重试耗尽后误报成功。
func TestReadWireFailure(t *testing.T) {
	rate := `{"type":"turn.ended","agentId":"main","turnId":0,"reason":"failed","error":{"code":"provider.rate_limit","message":"429 Rate limit exceeded (local fault fixture)","name":"APIProviderRateLimitError","details":{"statusCode":429,"requestId":null,"traceId":null},"retryable":true}}` + "\n"
	unavailable := `{"type":"turn.ended","agentId":"main","turnId":0,"reason":"failed","error":{"code":"provider.api_error","message":"503 Service unavailable (local fault fixture)","name":"APIStatusError","details":{"statusCode":503},"retryable":false}}` + "\n"
	for _, test := range []struct {
		// name 标识需要区分的终态场景。
		name string
		// wire 是本轮官方日志。
		wire string
		// offset 排除旧轮次失败。
		offset int64
		// kind 是需要对宿主提供的结构化错误类别。
		kind string
	}{
		{name: "rate", wire: rate, kind: "rate_limit"},
		{name: "server", wire: unavailable, kind: "server_error"},
		{name: "old_turn", wire: rate, offset: int64(len(rate))},
		{name: "recovered", wire: rate + `{"type":"turn.ended","agentId":"main","reason":"completed"}` + "\n"},
		{name: "child_failed", wire: `{"type":"turn.ended","agentId":"child","reason":"failed"}` + "\n"},
		{name: "transient_failure", wire: `{"type":"turn.step.interrupted","agentId":"main","reason":"error","message":"429 Rate limit"}` + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wire.jsonl")
			if err := os.WriteFile(path, []byte(test.wire), 0600); err != nil {
				t.Fatal(err)
			}
			err, _, _ := scanWireFailure(path, test.offset)
			if test.kind == "" {
				if err != nil {
					t.Fatalf("unexpected failure: %v", err)
				}
				return
			}
			var request *acp.RequestError
			if !errors.As(err, &request) {
				t.Fatalf("missing request error: %v", err)
			}
			if request.Data.(map[string]any)["errorKind"] != test.kind {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestReadWireFailureWaitsForAppend 覆盖真实 CLI 先回复 ACP、再异步补齐半条 Wire 记录的顺序。
func TestReadWireFailureWaitsForAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	start := `{"type":"turn.prompt","agentId":"main"}` + "\n"
	if err := os.WriteFile(path, []byte(start+`{"type":"turn.ended",`), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- readWireFailure(context.Background(), path, 0) }()
	select {
	case err := <-done:
		t.Fatalf("未等待终态落盘：%v", err)
	case <-time.After(30 * time.Millisecond):
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`"agentId":"main","reason":"failed","error":{"code":"provider.rate_limit","message":"429 Rate limit exceeded"}}` + "\n")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("异步失败被当作成功")
	}
}

// TestReadWireFailureHonorsCancellation 保证取消不会等待 Wire 超时。
func TestReadWireFailureHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"turn.prompt","agentId":"main"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := readWireFailure(ctx, path, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未保留：%v", err)
	}
}
