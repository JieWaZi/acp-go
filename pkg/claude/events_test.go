package claude

import (
	"context"
	"errors"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// TestTrailingIdleDebtDoesNotSettleNextTurn 锁定 upstream 的无身份尾随 idle 隔离语义。
func TestTrailingIdleDebtDoesNotSettleNextTurn(t *testing.T) {
	t.Parallel()
	turn := &claudeTurn{
		result:  make(chan turnResult, 1),
		drained: make(chan struct{}),
	}
	session := &claudeSession{active: turn, owedTrailingIdles: 1}
	idle := &protocol.SystemMessage{Subtype: "session_state_changed", State: "idle"}
	if err := session.handleSystem(context.Background(), idle); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-turn.result:
		t.Fatalf("旧 idle 结算了新 Turn：%#v", result)
	default:
	}
	select {
	case <-turn.drained:
		t.Fatal("旧 idle 解除了新 Turn 的 drain")
	default:
	}
	if session.owedTrailingIdles != 0 {
		t.Fatalf("owedTrailingIdles = %d", session.owedTrailingIdles)
	}

	if err := session.handleSystem(context.Background(), idle); err != nil {
		t.Fatal(err)
	}
	result := <-turn.result
	if !errors.Is(result.err, ErrClaudeTurnEndedWithoutResult) {
		t.Fatalf("新 Turn idle result = %#v", result)
	}
}

// TestPromptResponseFromResultMatchesUpstreamErrorSemantics 锁定 Provider 错误与停止原因优先级。
func TestPromptResponseFromResultMatchesUpstreamErrorSemantics(t *testing.T) {
	t.Parallel()

	base := protocol.ResultMessage{
		Subtype: "success",
		IsError: true,
		Result:  "API Error: 402 Insufficient Balance",
	}
	_, err := promptResponseFromResult(&base, "billing_error")
	var requestError *acp.RequestError
	if !errors.As(err, &requestError) || requestError.Code != -32603 ||
		requestError.Message != base.Result {
		t.Fatalf("provider error = %#v", err)
	}
	data, ok := requestError.Data.(map[string]any)
	if !ok || data["errorKind"] != "billing_error" {
		t.Fatalf("provider error data = %#v", requestError.Data)
	}

	maxTokens := "max_tokens"
	base.StopReason = &maxTokens
	response, err := promptResponseFromResult(&base, "billing_error")
	if err != nil || response.StopReason != acp.StopReasonMaxTokens {
		t.Fatalf("max tokens response = %#v, err = %v", response, err)
	}

	base.StopReason = nil
	base.IsError = false
	base.Subtype = "error_during_execution"
	response, err = promptResponseFromResult(&base, "")
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("non-error execution result = %#v, err = %v", response, err)
	}

	base.Subtype = "success"
	base.Result = "Not logged in. Please run /login"
	_, err = promptResponseFromResult(&base, "")
	if !errors.As(err, &requestError) || requestError.Code != -32000 {
		t.Fatalf("login result = %#v", err)
	}
}

// TestStreamedBlockRemainderIgnoresRebasedAssistantIndex 锁定 upstream 的文档顺序去重语义。
func TestStreamedBlockRemainderIgnoresRebasedAssistantIndex(t *testing.T) {
	t.Parallel()
	blocks := make(map[string][]streamedContentBlock)
	recordStreamedBlock(blocks, "", 1, "text", "ACP_")
	recordStreamedBlock(blocks, "", 1, "text", "OK")
	remainder, consumed := streamedBlockRemainder("ACP_OK", "text", blocks[""], 0)
	if !consumed || remainder != "" {
		t.Fatalf("remainder = %q, consumed = %v", remainder, consumed)
	}
}

// TestStreamedBlockRemainderKeepsUnstreamedTail 验证流中断时只补发聚合帧尾部。
func TestStreamedBlockRemainderKeepsUnstreamedTail(t *testing.T) {
	t.Parallel()
	blocks := []streamedContentBlock{{index: 0, kind: "thinking", text: "part"}}
	remainder, consumed := streamedBlockRemainder("partial", "thinking", blocks, 0)
	if !consumed || remainder != "ial" {
		t.Fatalf("remainder = %q, consumed = %v", remainder, consumed)
	}
}
