package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
	"testing"
)

// TestForkUsesNativeTurnBoundaryAndColdReceipt 禁止 fork 路径使用 thread/start 或 turn/start。
func TestForkUsesNativeTurnBoundaryAndColdReceipt(t *testing.T) {
	rpc := newFakeAppServerRPC()
	calls := 0
	cwd := t.TempDir()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadFork:
			calls++
			raw, _ := json.Marshal(request)
			var wire struct {
				// Params 保留真实生成请求的边界和目标工作目录。
				Params protocol.ThreadForkParams `json:"params"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				return err
			}
			if wire.Params.ThreadID != "parent" || wire.Params.LastTurnID == nil || *wire.Params.LastTurnID != "turn-before" || wire.Params.Cwd == nil || *wire.Params.Cwd != cwd {
				return fmt.Errorf("wrong fork request: %s", raw)
			}
			response := result.(*protocol.ThreadForkResponse)
			response.Thread.ID = "child"
			response.Model = "fast-model"
			return nil
		default:
			return fmt.Errorf("unexpected native method %s", request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	request := acp.UnstableForkSessionRequest{SessionId: "parent", Cwd: cwd, Meta: acpmeta.PositionMetadata("turn-before")}
	request.Meta["forkReceiptDirectory"] = t.TempDir()
	response, err := agent.UnstableForkSession(context.Background(), request)
	if err != nil || response.SessionId != "child" {
		t.Fatalf("%+v %v", response, err)
	}
	request.Meta["reconcileOnly"] = true
	cold := &Agent{}
	reconciled, err := cold.UnstableForkSession(context.Background(), request)
	if err != nil || reconciled.SessionId != response.SessionId || calls != 1 {
		t.Fatalf("cold reconciliation: %+v %v calls=%d", reconciled, err, calls)
	}
}
