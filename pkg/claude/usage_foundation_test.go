package claude

import (
	"context"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	"testing"
)

// TestUsageFoundationClaudeMissingUsageUnknown 验证没有用量回执不能制造零消费。
func TestUsageFoundationClaudeMissingUsageUnknown(t *testing.T) {
	message, err := protocol.DecodeMessage([]byte(`{"type":"result","subtype":"success","session_id":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := promptResponseFromResult(message.(*protocol.ResultMessage), "")
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage != nil {
		t.Fatalf("missing usage became known: %+v", response.Usage)
	}
}

// TestUsageFoundationClaudePartialSurvivesErrorAndCancellation 验证失败与取消前的消费可到达宿主。
func TestUsageFoundationClaudePartialSurvivesErrorAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		client := &recordingClaudeClient{}
		turn := &claudeTurn{id: "turn", streamedBlocks: map[string][]streamedContentBlock{}, result: make(chan turnResult, 1), drained: make(chan struct{})}
		session := &claudeSession{id: "s", active: turn, contextWindowSize: 200000, agent: &Agent{updater: client}}
		message, err := protocol.DecodeMessage([]byte(`{"type":"assistant","session_id":"s","message":{"id":"m1","model":"claude-test","content":[],"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":0}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = session.handleAssistant(context.Background(), message.(*protocol.AssistantMessage)); err != nil {
			t.Fatal(err)
		}
		turn.cancelled = cancelled
		result, err := protocol.DecodeMessage([]byte(`{"type":"result","session_id":"s","is_error":true,"subtype":"error_during_execution","result":"failed"}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = session.handleResult(context.Background(), result.(*protocol.ResultMessage)); err != nil {
			t.Fatal(err)
		}
		updates, _ := client.snapshot()
		var got acpmeta.TurnUsage
		var ok bool
		for _, update := range updates {
			if update.Update.UsageUpdate != nil {
				if value, valid := acpmeta.ReadTurnUsage(update.Update.UsageUpdate.Meta); valid {
					got = value
					ok = true
				}
			}
		}
		if !ok || got.InputTokens == nil || *got.InputTokens != 100 || got.OutputTokens == nil || *got.OutputTokens != 10 || got.CacheReadTokens == nil || *got.CacheReadTokens != 0 || got.CacheWriteTokens != nil || got.Complete {
			t.Fatalf("cancelled=%v: partial lost: %+v", cancelled, got)
		}
	}
}

// TestUsageFoundationClaudeExplicitZeroAndPartialFields 验证显式零与缺失分量独立保留。
func TestUsageFoundationClaudeExplicitZeroAndPartialFields(t *testing.T) {
	for _, raw := range []string{`{"input_tokens":0,"output_tokens":0}`, `{"input_tokens":5,"output_tokens":7,"cache_read_input_tokens":0}`} {
		message, err := protocol.DecodeMessage([]byte(`{"type":"result","subtype":"success","usage":` + raw + `}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := promptResponseFromResult(message.(*protocol.ResultMessage), "")
		if err != nil {
			t.Fatal(err)
		}
		got, ok := acpmeta.ReadTurnUsage(response.Meta)
		if !ok || got.Complete || got.Coverage != "main_loop" || got.InputTokens == nil || got.OutputTokens == nil || got.CacheWriteTokens != nil || response.Usage == nil {
			t.Fatalf("presence lost: %+v / %+v", response, got)
		}
	}
}

// TestUsageFoundationClaudeErrorZeroCannotEraseObservedSpend 验证崩溃回执的全零不能清除已到达消费。
func TestUsageFoundationClaudeErrorZeroCannotEraseObservedSpend(t *testing.T) {
	client := &recordingClaudeClient{}
	turn := &claudeTurn{id: "turn", streamedBlocks: map[string][]streamedContentBlock{}, result: make(chan turnResult, 1), drained: make(chan struct{})}
	session := &claudeSession{id: "s", active: turn, agent: &Agent{updater: client}}
	for _, raw := range []string{`{"type":"assistant","session_id":"s","message":{"id":"m","content":[],"usage":{"input_tokens":100,"output_tokens":10}}}`, `{"type":"result","session_id":"s","is_error":true,"subtype":"error_during_execution","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`} {
		message, err := protocol.DecodeMessage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		switch typed := message.(type) {
		case *protocol.AssistantMessage:
			err = session.handleAssistant(context.Background(), typed)
		case *protocol.ResultMessage:
			err = session.handleResult(context.Background(), typed)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	updates, _ := client.snapshot()
	var got acpmeta.TurnUsage
	for _, update := range updates {
		if update.Update.UsageUpdate != nil {
			got, _ = acpmeta.ReadTurnUsage(update.Update.UsageUpdate.Meta)
		}
	}
	if got.InputTokens == nil || *got.InputTokens != 100 || got.OutputTokens == nil || *got.OutputTokens != 10 || got.Complete {
		t.Fatalf("error zero erased spend: %+v", got)
	}
}

// TestUsageFixClaudeSteeringResultBoundary 保证 result 后继续执行的消费不被旧快照遮住或重复累加。
func TestUsageFixClaudeSteeringResultBoundary(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		client := &recordingClaudeClient{}
		turn := &claudeTurn{id: "turn", steered: true, streamedBlocks: map[string][]streamedContentBlock{}, result: make(chan turnResult, 1), drained: make(chan struct{})}
		session := &claudeSession{id: "s", active: turn, agent: &Agent{updater: client}}
		raws := []string{
			`{"type":"assistant","session_id":"s","message":{"id":"first","content":[],"usage":{"input_tokens":100,"output_tokens":10}}}`,
			`{"type":"result","session_id":"s","uuid":"result-first","subtype":"success","usage":{"input_tokens":100,"output_tokens":10}}`,
			`{"type":"assistant","session_id":"s","message":{"id":"first","content":[],"usage":{"input_tokens":100,"output_tokens":10}}}`,
			`{"type":"assistant","session_id":"s","message":{"id":"second","content":[],"usage":{"input_tokens":50,"output_tokens":5}}}`,
			`{"type":"assistant","session_id":"s","message":{"id":"second","content":[],"usage":{"input_tokens":50,"output_tokens":5}}}`,
		}
		for _, raw := range raws {
			message, err := protocol.DecodeMessage([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			switch typed := message.(type) {
			case *protocol.AssistantMessage:
				err = session.handleAssistant(context.Background(), typed)
			case *protocol.ResultMessage:
				err = session.handleResult(context.Background(), typed)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		turn.cancelled = cancelled
		result, _ := protocol.DecodeMessage([]byte(`{"type":"result","session_id":"s","uuid":"result-error","is_error":true,"usage":{"input_tokens":0,"output_tokens":0}}`))
		if err := session.handleResult(context.Background(), result.(*protocol.ResultMessage)); err != nil {
			t.Fatal(err)
		}
		updates, _ := client.snapshot()
		var got acpmeta.TurnUsage
		for _, update := range updates {
			if update.Update.UsageUpdate != nil {
				got, _ = acpmeta.ReadTurnUsage(update.Update.UsageUpdate.Meta)
			}
		}
		if got.InputTokens == nil || *got.InputTokens != 150 || got.OutputTokens == nil || *got.OutputTokens != 15 || got.Complete {
			t.Fatalf("cancelled=%v: later spend hidden: %+v", cancelled, got)
		}
	}
}

// TestUsageFixClaudeSteeringSuccessReceipts 验证新 result 替换对应请求快照，重复回执不再累加。
func TestUsageFixClaudeSteeringSuccessReceipts(t *testing.T) {
	client := &recordingClaudeClient{}
	turn := &claudeTurn{id: "turn", steered: true, streamedBlocks: map[string][]streamedContentBlock{}, result: make(chan turnResult, 1), drained: make(chan struct{})}
	session := &claudeSession{id: "s", active: turn, agent: &Agent{updater: client}}
	for _, raw := range []string{
		`{"type":"assistant","message":{"id":"a","content":[],"usage":{"input_tokens":100,"output_tokens":10}}}`,
		`{"type":"result","uuid":"r1","subtype":"success","usage":{"input_tokens":100,"output_tokens":10}}`,
		`{"type":"assistant","message":{"id":"b","content":[],"usage":{"input_tokens":50,"output_tokens":5}}}`,
		`{"type":"result","uuid":"r2","subtype":"success","usage":{"input_tokens":50,"output_tokens":5}}`,
		`{"type":"result","uuid":"r2","subtype":"success","usage":{"input_tokens":50,"output_tokens":5}}`,
	} {
		message, err := protocol.DecodeMessage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		switch typed := message.(type) {
		case *protocol.AssistantMessage:
			err = session.handleAssistant(context.Background(), typed)
		case *protocol.ResultMessage:
			err = session.handleResult(context.Background(), typed)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := session.handleSystem(context.Background(), &protocol.SystemMessage{Subtype: "session_state_changed", State: "idle"}); err != nil {
		t.Fatal(err)
	}
	result := <-turn.result
	if result.err != nil || result.response.Usage == nil || result.response.Usage.InputTokens != 150 || result.response.Usage.OutputTokens != 15 {
		t.Fatalf("receipt counted twice: %+v", result)
	}
}
