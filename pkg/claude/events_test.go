package claude

import (
	"context"
	"encoding/json"
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
	if err == nil {
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

// TestClaudeExplicitErrorsNeverBecomeAssistantText 验证结构化错误不泄漏为正常回复，正文关键词不参与错误判断。
func TestClaudeExplicitErrorsNeverBecomeAssistantText(t *testing.T) {
	for _, kind := range []string{"billing_error", ""} {
		t.Run(kind, func(t *testing.T) {
			client := &recordingClaudeClient{}
			turn := &claudeTurn{streamedBlocks: map[string][]streamedContentBlock{}, result: make(chan turnResult, 1), drained: make(chan struct{})}
			session := &claudeSession{id: "session", active: turn, agent: &Agent{updater: client}}
			message := &protocol.AssistantMessage{SessionID: "session", Error: kind,
				Message: protocol.AnthropicMessage{Content: []protocol.ContentBlock{{Type: "text", Text: "error is a normal programming term"}}}}
			if err := session.handleAssistant(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			updates, _ := client.snapshot()
			if kind != "" && len(updates) != 0 {
				t.Fatalf("错误交付为正常消息：%+v", updates)
			}
			if kind != "" {
				if err := session.handleResult(context.Background(), &protocol.ResultMessage{Subtype: "success"}); err != nil {
					t.Fatal(err)
				}
				result := <-turn.result
				var failure *acp.RequestError
				if !errors.As(result.err, &failure) || failure.Message != message.Message.Content[0].Text {
					t.Fatalf("错误详情丢失：%v", result.err)
				}
			}
			if kind == "" && len(updates) != 1 {
				t.Fatalf("普通文本被误判：%+v", updates)
			}
		})
	}
}

// TestClaudeAssistantErrorOverridesSuccessfulResult 验证消息级错误和结果错误均高于停止原因。
func TestClaudeAssistantErrorOverridesSuccessfulResult(t *testing.T) {
	for _, reason := range []string{"end_turn", "max_tokens", "refusal"} {
		for _, explicitResultError := range []bool{false, true} {
			message := &protocol.ResultMessage{Subtype: "success", StopReason: &reason, IsError: explicitResultError, Result: "request rejected"}
			kind := ""
			if !explicitResultError {
				kind = "authentication_failed"
			}
			if _, err := promptResponseFromResult(message, kind); err == nil {
				t.Fatalf("错误被 %s 掩盖", reason)
			}
		}
	}
	for reason, expected := range map[string]acp.StopReason{"end_turn": acp.StopReasonEndTurn, "max_tokens": acp.StopReasonMaxTokens, "refusal": acp.StopReasonRefusal} {
		response, err := promptResponseFromResult(&protocol.ResultMessage{Subtype: "success", StopReason: &reason, Result: "explain error handling"}, "")
		if err != nil || response.StopReason != expected {
			t.Fatalf("正常停止语义改变：%+v %v", response, err)
		}
	}
}

// TestClaudeIdleKeepsProviderErrorWithoutResult 验证缺少 result 的 idle 仍保留已知供应商失败。
func TestClaudeIdleKeepsProviderErrorWithoutResult(t *testing.T) {
	turn := &claudeTurn{result: make(chan turnResult, 1), drained: make(chan struct{}), assistantError: "rate_limit", assistantErrorMessage: "provider limit reached"}
	session := &claudeSession{active: turn}
	if err := session.handleSystem(context.Background(), &protocol.SystemMessage{Subtype: "session_state_changed", State: "idle"}); err != nil {
		t.Fatal(err)
	}
	result := <-turn.result
	var failure *acp.RequestError
	if !errors.As(result.err, &failure) {
		t.Fatalf("结构化错误丢失：%v", result.err)
	}
	data, _ := failure.Data.(map[string]any)
	if data["errorKind"] != "rate_limit" || failure.Message != "provider limit reached" {
		t.Fatalf("错误诊断丢失：%+v", failure)
	}
}

// TestClaudeLateErrorKeepsStreamedPartial 锁定已交付正文无法撤回，晚到错误只终结失败且不补发错误正文。
func TestClaudeLateErrorKeepsStreamedPartial(t *testing.T) {
	client := &recordingClaudeClient{}
	turn := &claudeTurn{
		streamedBlocks: map[string][]streamedContentBlock{},
		result:         make(chan turnResult, 1),
		drained:        make(chan struct{}),
	}
	session := &claudeSession{id: "session", active: turn, agent: &Agent{updater: client}}
	ctx := context.Background()
	if err := session.handleStreamEvent(ctx, &protocol.StreamEventMessage{
		SessionID: "session",
		Event: protocol.StreamEvent{
			Type:  "content_block_delta",
			Delta: &protocol.ContentBlock{Type: "text_delta", Text: "partial response"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	updates, _ := client.snapshot()
	if len(updates) != 1 || updates[0].Update.AgentMessageChunk == nil {
		t.Fatalf("普通流式正文未立即交付：%+v", updates)
	}
	if err := session.handleAssistant(ctx, &protocol.AssistantMessage{
		SessionID: "session",
		Error:     "rate_limit",
		Message: protocol.AnthropicMessage{Content: []protocol.ContentBlock{
			{Type: "text", Text: "provider limit reached"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.handleResult(ctx, &protocol.ResultMessage{Subtype: "success"}); err != nil {
		t.Fatal(err)
	}
	result := <-turn.result
	var failure *acp.RequestError
	if !errors.As(result.err, &failure) || failure.Message != "provider limit reached" {
		t.Fatalf("晚到错误未形成失败终态：%v", result.err)
	}
	updates, _ = client.snapshot()
	if len(updates) != 1 || updates[0].Update.AgentMessageChunk.Content.Text.Text != "partial response" {
		t.Fatalf("已交付正文被改写或补发了错误正文：%+v", updates)
	}
}

// TestClaudeSubagentErrorKeepsParentTurnSuccessful 验证子代理错误等待工具结果结算，不覆盖主代理成功终态。
func TestClaudeSubagentErrorKeepsParentTurnSuccessful(t *testing.T) {
	client := &recordingClaudeClient{}
	turn := &claudeTurn{
		streamedBlocks: map[string][]streamedContentBlock{},
		result:         make(chan turnResult, 1), drained: make(chan struct{}),
	}
	session := &claudeSession{
		id: "session", active: turn, agent: &Agent{updater: client},
		tools: make(map[string]*toolState),
	}
	ctx := context.Background()
	parentID := "subagent-tool"
	if err := session.startTool(ctx, protocol.ContentBlock{Type: "tool_use", ID: parentID, Name: "Agent"}); err != nil {
		t.Fatal(err)
	}
	if err := session.handleAssistant(ctx, &protocol.AssistantMessage{
		SessionID: "session", ParentToolUseID: &parentID, Error: "rate_limit",
		Message: protocol.AnthropicMessage{Content: []protocol.ContentBlock{
			{Type: "text", Text: "subagent provider failed"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	updates, _ := client.snapshot()
	if len(updates) != 1 || turn.assistantError != "" || session.tools[parentID].Completed {
		t.Fatalf("子代理错误变成正文、提前结算工具或污染顶层错误：%+v %q", updates, turn.assistantError)
	}
	select {
	case result := <-turn.result:
		t.Fatalf("子代理错误提前终结整轮：%+v", result)
	default:
	}
	if err := session.handleUser(ctx, &protocol.UserMessage{
		SessionID: "session",
		Message: protocol.AnthropicMessage{Content: []protocol.ContentBlock{{
			Type: "tool_result", ToolUseID: parentID, IsError: true,
			Content: json.RawMessage(`"subagent provider failed"`),
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.handleAssistant(ctx, &protocol.AssistantMessage{
		SessionID: "session",
		Message: protocol.AnthropicMessage{Content: []protocol.ContentBlock{{
			Type: "text", Text: "I handled the subagent failure.",
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.handleResult(ctx, &protocol.ResultMessage{Subtype: "success"}); err != nil {
		t.Fatal(err)
	}
	result := <-turn.result
	if result.err != nil || result.response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("子代理错误覆盖主代理成功终态：%+v", result)
	}
	updates, _ = client.snapshot()
	if len(updates) != 3 || updates[1].Update.ToolCallUpdate == nil ||
		updates[1].Update.ToolCallUpdate.Status == nil || *updates[1].Update.ToolCallUpdate.Status != acp.ToolCallStatusFailed ||
		updates[2].Update.AgentMessageChunk == nil || updates[2].Update.AgentMessageChunk.Content.Text.Text != "I handled the subagent failure." {
		t.Fatalf("工具失败和主代理正文未独立交付：%+v", updates)
	}
}
