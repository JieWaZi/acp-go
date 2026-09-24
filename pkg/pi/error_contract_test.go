package pi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestPiErrorsSurviveSuccessfulModelRetry 验证自动重试只能清除模型错误，不能覆盖扩展和交付失败。
func TestPiErrorsSurviveSuccessfulModelRetry(t *testing.T) {
	for _, name := range []string{"extension", "delivery", "model recovered", "model failed", "retry exhausted"} {
		t.Run(name, func(t *testing.T) {
			deliveryError := errors.New("delivery failed")
			events := make(chan map[string]any, 4)
			done := make(chan struct{})
			turn := make(chan acp.StopReason, 1)
			process := &rpcProcess{events: events, done: done}
			session := &session{process: process, turn: turn, eventsDone: make(chan struct{})}
			if name == "delivery" {
				session.failure = deliveryError
			}
			if name == "extension" {
				events <- map[string]any{"type": "extension_error", "error": "extension rejected operation"}
			}
			if name == "model recovered" || name == "model failed" {
				events <- map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "error", "errorMessage": "provider failed"}}
			}
			if name == "retry exhausted" {
				events <- map[string]any{"type": "auto_retry_end", "success": false, "finalError": "529 overloaded_error: Overloaded"}
			} else if name != "model failed" {
				events <- map[string]any{"type": "auto_retry_end", "success": true}
			}
			events <- map[string]any{"type": "agent_settled"}
			agent := &Agent{}
			go agent.events(session)
			t.Cleanup(func() { close(events); close(done); <-session.eventsDone })
			select {
			case <-turn:
			case <-time.After(time.Second):
				t.Fatal("错误回合未结束")
			}
			session.mutex.Lock()
			failure := errors.Join(session.failure, session.modelFailure)
			session.mutex.Unlock()
			if name == "model recovered" && failure != nil {
				t.Fatalf("恢复后仍失败：%v", failure)
			}
			if name == "delivery" && !errors.Is(failure, deliveryError) {
				t.Fatalf("交付错误被清除：%v", failure)
			}
			if name == "retry exhausted" && (failure == nil || !strings.Contains(failure.Error(), "529 overloaded_error: Overloaded")) {
				t.Fatalf("自动重试耗尽丢失最终错误：%v", failure)
			}
			if (name == "extension" || name == "model failed") && failure == nil {
				t.Fatal("扩展错误伪装为成功")
			}
		})
	}
}

// TestPiErrorEventsDoNotBecomeAssistantText 验证错误与正文分流，普通 error 词仍按原文交付。
func TestPiErrorEventsDoNotBecomeAssistantText(t *testing.T) {
	for _, failed := range []bool{true, false} {
		output := &bytes.Buffer{}
		reader, writer := io.Pipe()
		agent := &Agent{}
		agent.SetAgentConnection(acp.NewAgentSideConnection(agent, output, reader))
		events := make(chan map[string]any, 2)
		done := make(chan struct{})
		turn := make(chan acp.StopReason, 1)
		session := &session{id: "fixture", process: &rpcProcess{events: events, done: done}, turn: turn, eventsDone: make(chan struct{})}
		if failed {
			events <- map[string]any{"type": "extension_error", "error": "extension failed"}
		} else {
			events <- map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "explain error handling"}}
		}
		events <- map[string]any{"type": "agent_settled"}
		go agent.events(session)
		select {
		case <-turn:
		case <-time.After(time.Second):
			t.Fatal("事件交付超时")
		}
		session.mutex.Lock()
		failure := session.failure
		session.mutex.Unlock()
		close(events)
		close(done)
		<-session.eventsDone
		_ = writer.Close()
		_ = reader.Close()
		if failed && (failure == nil || strings.Contains(output.String(), "agent_message_chunk")) {
			t.Fatalf("错误混入正常回复：%v %s", failure, output.String())
		}
		if !failed && (failure != nil || !strings.Contains(output.String(), "explain error handling")) {
			t.Fatalf("普通消息被误判：%v %s", failure, output.String())
		}
	}
}

// TestPiStatusEventsDoNotBecomeAssistantText 验证重试和压缩状态不会创建正文，真实文本仍按顺序立即交付。
func TestPiStatusEventsDoNotBecomeAssistantText(t *testing.T) {
	output := &bytes.Buffer{}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	agent := &Agent{}
	agent.SetAgentConnection(acp.NewAgentSideConnection(agent, output, reader))
	events := make(chan map[string]any, 8)
	done := make(chan struct{})
	turn := make(chan acp.StopReason, 1)
	session := &session{id: "fixture", process: &rpcProcess{events: events, done: done}, turn: turn, eventsDone: make(chan struct{})}
	for _, kind := range []string{"auto_retry_start", "auto_retry_end", "auto_compaction_start", "auto_compaction_end", "compaction_start", "compaction_end"} {
		events <- map[string]any{"type": kind, "success": true}
	}
	events <- map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "real streamed text"}}
	events <- map[string]any{"type": "agent_settled"}
	go agent.events(session)
	select {
	case <-turn:
	case <-time.After(time.Second):
		t.Fatal("状态事件未正常结算")
	}
	close(events)
	close(done)
	<-session.eventsDone
	if strings.Count(output.String(), "agent_message_chunk") != 1 || !strings.Contains(output.String(), "real streamed text") {
		t.Fatalf("状态泄漏为正文或真实文本丢失：%s", output.String())
	}
}

// TestPiRetryCancellationKeepsCancellation 验证重试等待取消只保留取消事实，不升级为模型失败。
func TestPiRetryCancellationKeepsCancellation(t *testing.T) {
	for _, name := range []string{"user", "context", "deadline"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "context" {
				cancel()
			}
			if name == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer stop()
			}
			events := make(chan map[string]any, 2)
			done := make(chan struct{})
			turn := make(chan acp.StopReason, 1)
			session := &session{
				process: &rpcProcess{events: events, done: done},
				turn:    turn, eventsDone: make(chan struct{}), turnContext: ctx,
				cancelled: name == "user", modelFailure: errors.New("previous transient failure"),
			}
			events <- map[string]any{"type": "auto_retry_end", "success": false, "finalError": "Retry cancelled"}
			events <- map[string]any{"type": "agent_settled"}
			go (&Agent{}).events(session)
			select {
			case reason := <-turn:
				if name == "user" && reason != acp.StopReasonCancelled {
					t.Fatalf("用户取消丢失：%s", reason)
				}
			case <-time.After(time.Second):
				t.Fatal("取消回合未结算")
			}
			session.mutex.Lock()
			failure := session.modelFailure
			session.mutex.Unlock()
			close(events)
			close(done)
			<-session.eventsDone
			if failure != nil {
				t.Fatalf("取消被当成模型失败：%v", failure)
			}
		})
	}
}
