package kimi

import (
	"context"
	"encoding/json"
	"errors"
	acp "github.com/coder/acp-go-sdk"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestKimiNativeErrorContract 验证 Kimi 原生 ACP 的认证、限流和未知错误完整穿透用量兼容层。
func TestKimiNativeErrorContract(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cwd := t.TempDir()
	agent, err := NewAgent(ctx, Config{KimiPath: executable,
		PrefixArgs:       []string{"-test.run=^TestKimiErrorACPProcess$", "--"},
		Environment:      append(os.Environ(), "ACP_GO_KIMI_ERROR_FIXTURE=1", "KIMI_CODE_HOME="+t.TempDir()),
		WorkingDirectory: cwd, StateDirectory: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := agent.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"authentication_failed", "rate_limit", "future_error"} {
		t.Run(kind, func(t *testing.T) {
			_, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(kind)}})
			var failure *acp.RequestError
			if !errors.As(err, &failure) {
				t.Fatalf("原生错误被吞掉：%v", err)
			}
			data, _ := failure.Data.(map[string]any)
			expectedCode := -32603
			if kind == "authentication_failed" {
				expectedCode = -32000
			}
			if failure.Code != expectedCode || data["errorKind"] != kind || data["message"] != "provider diagnostic" {
				t.Fatalf("原生错误被改写：%+v", failure)
			}
		})
	}
	response, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("explain error handling")}})
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("普通 error 文本被误判：%+v %v", response, err)
	}
}

// TestKimiErrorACPProcess 是仅运行本地 SDK 的确定性原生协议替身。
func TestKimiErrorACPProcess(t *testing.T) {
	if os.Getenv("ACP_GO_KIMI_ERROR_FIXTURE") != "1" {
		return
	}
	connection := acp.NewConnection(func(_ context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "Kimi Code CLI", "version": "fixture"}}, nil
		case "session/new":
			return map[string]any{"sessionId": "error-session"}, nil
		case "session/prompt":
			var request acp.PromptRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, acp.NewInvalidParams(nil)
			}
			kind := request.Prompt[0].Text.Text
			if kind == "explain error handling" {
				return map[string]any{"stopReason": "end_turn"}, nil
			}
			data := map[string]any{"errorKind": kind, "message": "provider diagnostic"}
			if kind == "authentication_failed" {
				return nil, acp.NewAuthRequired(data)
			}
			return nil, acp.NewInternalError(data)
		default:
			return nil, acp.NewMethodNotFound(method)
		}
	}, os.Stdout, os.Stdin)
	<-connection.Done()
	os.Exit(0)
}
