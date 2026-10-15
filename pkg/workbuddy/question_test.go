package workbuddy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// questionTurnGate 在问题取得执行归属后暂停，让真实 SDK 完成取消和下一轮启动。
type questionTurnGate struct {
	// Agent 保留真实原生进程、FIFO 和会话归属解析。
	*nativeacp.Agent
	// captured 通知测试旧问题已取得所属执行。
	captured chan struct{}
	// resume 允许旧问题在新轮次已启动后继续。
	resume chan struct{}
	// asked 表示旧问题是否错误地再次到达宿主。
	asked bool
}

// ResolveInteractionSession 控制旧的分步解析路径，使回归测试在修复前稳定失败。
func (gate *questionTurnGate) ResolveInteractionSession(toolID, sessionID string) acp.SessionId {
	sid := gate.Agent.ResolveInteractionSession(toolID, sessionID)
	close(gate.captured)
	<-gate.resume
	return sid
}

// ResolveInteractionContext 在同一原生状态快照中取得会话与轮次，之后再进入调度闸门。
func (gate *questionTurnGate) ResolveInteractionContext(toolID, sessionID string) (acp.SessionId, context.Context) {
	sid, turn := gate.Agent.ResolveInteractionContext(toolID, sessionID)
	close(gate.captured)
	<-gate.resume
	return sid, turn
}

// CreateElicitation 接纳任何误入新轮次的旧问题，暴露错误的 submitted 回执。
func (gate *questionTurnGate) CreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	gate.asked = true
	response := acp.NewUnstableCreateElicitationResponseAccept()
	response.Accept.Content = map[string]any{"question_0": "A"}
	return response, nil
}

// TestQuestionCannotRebindCanceledTurn 验证旧问题不会因同会话新 Prompt 而重新获得有效上下文。
func TestQuestionCannotRebindCanceledTurn(t *testing.T) {
	agent, peer, host := startFixture(t, "default", "wait")
	initializeFixture(t, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("question")}})
			done <- err
		}()
		return done
	}
	await := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatal("question did not reach its scheduling gate")
		}
	}
	awaitPrompt := func(done <-chan error) {
		t.Helper()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("cancelled prompt did not finish")
		}
	}
	old := prompt()
	await(host.questions)
	gate := &questionTurnGate{Agent: agent.Agent, captured: make(chan struct{}), resume: make(chan struct{})}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(gate.resume) }) }
	defer resume()
	result := make(chan any, 1)
	go func() {
		answer, _, err := (questionAdapter{}).HandleCallback(ctx, gate, "_codebuddy.ai/question", json.RawMessage(`{"sessionId":"workbuddy-session","toolCallId":"old-tool","inputType":"question","schema":{"questions":[{"id":"q_0","question":"old question","options":[{"label":"A"}]}]}}`))
		if err != nil {
			result <- err
			return
		}
		result <- answer
	}()
	await(gate.captured)
	if err := peer.Cancel(ctx, acp.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	awaitPrompt(old)
	fresh := prompt()
	await(host.questions)
	resume()
	select {
	case answer := <-result:
		response, ok := answer.(map[string]any)
		if !ok || gate.asked || response["outcome"] != "cancelled" {
			t.Errorf("old question reached fresh turn: asked=%v result=%v", gate.asked, answer)
		}
	case <-ctx.Done():
		t.Fatal("old question did not return")
	}
	if err := peer.Cancel(ctx, acp.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	awaitPrompt(fresh)
}

// cancelledAnswerBridge 模拟宿主在执行取消后仍然提交已经填写的表单。
type cancelledAnswerBridge struct {
	// CallbackBridge 保留该测试无需调用的其余标准宿主端口。
	nativeacp.CallbackBridge
	// turn 是原始问题所属执行的权威上下文。
	turn context.Context
	// cancel 在宿主回执边界撤销原始执行。
	cancel context.CancelFunc
}

// ResolveInteractionContext 返回已冻结的执行身份，不随会话后续轮次变化。
func (bridge cancelledAnswerBridge) ResolveInteractionContext(string, string) (acp.SessionId, context.Context) {
	return "s", bridge.turn
}

// CreateElicitation 取消执行后仍返回 accept，验证迟到答案不能被提交。
func (bridge cancelledAnswerBridge) CreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	bridge.cancel()
	response := acp.NewUnstableCreateElicitationResponseAccept()
	response.Accept.Content = map[string]any{"question_0": "A"}
	return response, nil
}

// TestQuestionDiscardsAcceptedAnswerAfterCancellation 覆盖已经显示的表单不能提交迟到答案。
func TestQuestionDiscardsAcceptedAnswerAfterCancellation(t *testing.T) {
	turn, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge := cancelledAnswerBridge{turn: turn, cancel: cancel}
	answer, handled, err := (questionAdapter{}).HandleCallback(context.Background(), bridge, "_codebuddy.ai/question", json.RawMessage(`{"sessionId":"s","inputType":"question","schema":{"questions":[{"id":"q_0","question":"choose","options":[{"label":"A"}]}]}}`))
	response, ok := answer.(map[string]any)
	if err != nil || !handled || !ok || response["outcome"] != "cancelled" {
		t.Fatalf("cancelled turn retained answer: handled=%v result=%v error=%v", handled, answer, err)
	}
}
