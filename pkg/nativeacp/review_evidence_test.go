package nativeacp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	acp "github.com/coder/acp-go-sdk"
)

// evidenceReviewHost 阻塞审计交付并记录真正到达宿主的人工审批。
type evidenceReviewHost struct {
	// Client 保留未用到的宿主接口。
	acp.Client
	// audits 保存每次风险审查的通知。
	audits chan acp.SessionNotification
	// releaseAudit 由测试释放真实 SDK 的审计请求。
	releaseAudit chan struct{}
	// auditPending 标识审计写入尚未交付的窗口。
	auditPending chan struct{}
	// permissions 保存回退人工的原始请求。
	permissions chan acp.RequestPermissionRequest
}

// SessionUpdate 收集已经真正送达宿主的审计通知。
func (host *evidenceReviewHost) SessionUpdate(_ context.Context, request acp.SessionNotification) error {
	host.audits <- request
	return nil
}

// evidenceAuditWriter 在真实协议写入返回前暂停审计，区别于通知异步接收回调。
type evidenceAuditWriter struct {
	// Writer 是真实 SDK 协议管道。
	io.Writer
	// host 提供发送阶段的同步边界。
	host *evidenceReviewHost
}

// Write 仅阻塞风险审计，不阻塞人工审批请求或其它协议数据。
func (writer *evidenceAuditWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("acp-go/permission-review")) {
		close(writer.host.auditPending)
		<-writer.host.releaseAudit
	}
	return writer.Writer.Write(data)
}

// RequestPermission 显式拒绝人工回退，使测试能区别自动一次批准。
func (host *evidenceReviewHost) RequestPermission(
	_ context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	host.permissions <- request
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected("reject")}, nil
}

// evidenceReviewResult 保存独立审批请求的最终协议结果。
type evidenceReviewResult struct {
	// response 是真实审批路径返回的结果。
	response acp.RequestPermissionResponse
	// err 是协议或执行错误。
	err error
}

// awaitEvidenceReview 为所有阻塞边界提供有界同步，不使用时间猜测。
func awaitEvidenceReview[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("permission evidence synchronization timed out")
		var zero T
		return zero
	}
}

// evidenceReviewAgent 构造当前回合事实并使用真实 SDK 连接宿主审批与审计。
func evidenceReviewAgent(
	t *testing.T,
	reviewer func(context.Context, autoreview.Request) (autoreview.Decision, error),
) (*Agent, *evidenceReviewHost, context.CancelFunc) {
	t.Helper()
	turn, cancel := context.WithCancel(context.Background())
	agent := &Agent{
		bound: make(chan struct{}),
		sessions: map[acp.SessionId]*sessionOptions{"s": {
			workingDirectory: "/work", options: []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
				Id: "model", CurrentValue: "actual-model",
			}}},
		}},
		active:        map[acp.SessionId]bool{"s": true},
		prompts:       map[acp.SessionId][]acp.ContentBlock{"s": {acp.TextBlock("list files")}},
		turnContexts:  map[acp.SessionId]context.Context{"s": turn},
		turnOwners:    map[acp.SessionId]*promptWaiter{"s": {context: turn}},
		toolSessions:  map[acp.ToolCallId]acp.SessionId{},
		toolDetails:   map[acp.ToolCallId]acp.ToolCallUpdate{},
		toolRevisions: map[acp.ToolCallId]*toolEvidenceRevision{},
	}
	agent.config.PermissionAdapter = NewReviewPermissionAdapter(ReviewPermissionConfig{
		Mode: "auto", Reviewer: reviewer, CurrentSession: agent.CurrentSession,
	})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	host := &evidenceReviewHost{
		audits: make(chan acp.SessionNotification, 2), releaseAudit: make(chan struct{}),
		auditPending: make(chan struct{}), permissions: make(chan acp.RequestPermissionRequest, 2),
	}
	agent.SetAgentConnection(acp.NewAgentSideConnection(agent, &evidenceAuditWriter{Writer: outW, host: host}, inR))
	_ = acp.NewClientSideConnection(host, inW, outR)
	t.Cleanup(func() {
		cancel()
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	return agent, host, cancel
}

// changeReviewTool 使用原生通知的真实投影入口改变当前工具证据。
func changeReviewTool(t *testing.T, agent *Agent, data string) {
	t.Helper()
	var notification acp.SessionNotification
	if err := json.Unmarshal([]byte(data), &notification); err != nil {
		t.Fatal(err)
	}
	agent.normalizeUpdate(&notification)
}

// TestReviewRevalidatesToolEvidence 验证审查和审计两个阻塞窗口内的证据变化都安全转人工。
func TestReviewRevalidatesToolEvidence(t *testing.T) {
	changes := []struct {
		// name 描述原生证据变化。
		name string
		// update 是工具事件，空值表示控制场景或回合变化。
		update string
	}{
		{name: "unchanged"},
		{name: "input", update: `{
  "sessionId": "s",
  "update": {
    "sessionUpdate": "tool_call_update",
    "toolCallId": "tool",
    "rawInput": {
      "command": "rm -rf /work"
    }
  }
}`},
		{name: "title", update: `{
  "sessionId": "s",
  "update": {
    "sessionUpdate": "tool_call_update",
    "toolCallId": "tool",
    "title": "Delete files"
  }
}`},
		{name: "kind", update: `{
  "sessionId": "s",
  "update": {
    "sessionUpdate": "tool_call_update",
    "toolCallId": "tool",
    "kind": "delete"
  }
}`},
		{name: "content", update: `{
  "sessionId": "s",
  "update": {
    "sessionUpdate": "tool_call_update",
    "toolCallId": "tool",
    "content": [
      {
        "type": "content",
        "content": {
          "type": "text",
          "text": "Delete instead"
        }
      }
    ]
  }
}`},
		{name: "owner", update: `{
  "sessionId": "other",
  "update": {
    "sessionUpdate": "tool_call_update",
    "toolCallId": "tool",
    "title": "Same tool"
  }
}`},
		{name: "owner-restored", update: `{
  "sessionId": "other",
  "update": {
    "sessionUpdate": "tool_call_update",
    "toolCallId": "tool",
    "title": "Same tool"
  }
}`},
		{name: "inactive"},
		{name: "turn-replaced"},
		{name: "cancelled"},
	}
	for _, stage := range []string{"review", "audit"} {
		for _, missingInput := range []bool{false, true} {
			for _, change := range changes {
				inputSource := "request-input"
				if missingInput {
					inputSource = "cached-input"
				}
				t.Run(stage+"/"+change.name+"/"+inputSource, func(t *testing.T) {
					entered := make(chan autoreview.Request, 1)
					release := make(chan struct{})
					agent, host, cancel := evidenceReviewAgent(t, func(
						ctx context.Context,
						request autoreview.Request,
					) (autoreview.Decision, error) {
						entered <- request
						select {
						case <-release:
							return autoreview.Decision{Outcome: "allow"}, nil
						case <-ctx.Done():
							return autoreview.Decision{}, ctx.Err()
						}
					})
					changeReviewTool(t, agent, `{
  "sessionId": "s",
  "update": {
    "sessionUpdate": "tool_call",
    "toolCallId": "tool",
    "title": "List files",
    "kind": "execute",
    "status": "pending",
    "rawInput": {
      "command": "ls"
    }
  }
}`)
					request := acp.RequestPermissionRequest{
						SessionId: "s", ToolCall: acp.ToolCallUpdate{ToolCallId: "tool"},
						Options: []acp.PermissionOption{
							{OptionId: "once", Name: "Once", Kind: acp.PermissionOptionKindAllowOnce},
							{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
						},
					}
					if !missingInput {
						request.ToolCall.RawInput = map[string]any{"command": "ls"}
					}
					result := make(chan evidenceReviewResult, 1)
					go func() {
						response, err := agent.requestPermission(context.Background(), request)
						result <- evidenceReviewResult{response: response, err: err}
					}()
					reviewed := awaitEvidenceReview(t, entered)
					if reviewed.Tool.RawInput.(map[string]any)["command"] != "ls" {
						t.Fatal("review did not use original full input")
					}
					if stage == "audit" {
						close(release)
						awaitEvidenceReview(t, host.auditPending)
					}
					if change.update != "" {
						changeReviewTool(t, agent, change.update)
						if change.name == "owner-restored" {
							changeReviewTool(t, agent, `{
  "sessionId": "s",
  "update": {
    "sessionUpdate": "tool_call_update",
    "toolCallId": "tool",
    "title": "List files"
  }
}`)
						}
					}
					if change.name == "inactive" || change.name == "turn-replaced" {
						agent.mutex.Lock()
						if change.name == "inactive" {
							agent.active["s"] = false
						} else {
							agent.turnOwners["s"] = &promptWaiter{context: agent.turnContexts["s"]}
						}
						agent.mutex.Unlock()
					}
					if change.name == "cancelled" {
						cancel()
					}
					if stage == "review" {
						close(release)
						if change.name != "cancelled" {
							awaitEvidenceReview(t, host.auditPending)
						}
					}
					close(host.releaseAudit)
					final := awaitEvidenceReview(t, result)
					if final.err != nil {
						t.Fatal(final.err)
					}
					if stage == "audit" || change.name != "cancelled" {
						audit := awaitEvidenceReview(t, host.audits)
						if audit.Update.ToolCallUpdate == nil || audit.Update.ToolCallUpdate.Meta["acp-go/permission-review"] == nil {
							t.Fatal("manual fallback lost risk audit")
						}
						metadata := audit.Update.ToolCallUpdate.Meta["acp-go/permission-review"].(map[string]any)
						wantAudit := "allow"
						if stage == "review" && change.name != "unchanged" {
							wantAudit = "ask"
							if metadata["reason"] != "review_unavailable" {
								t.Fatalf("stale evidence audit lacked fallback reason: %#v", metadata)
							}
						}
						if metadata["outcome"] != wantAudit {
							t.Fatalf("wrong frozen-evidence audit: %#v, want %s", metadata, wantAudit)
						}
					}
					if change.name == "cancelled" {
						if final.response.Outcome.Cancelled == nil {
							t.Fatalf("cancelled review granted: %+v", final.response)
						}
					} else {
						want := "reject"
						if change.name == "unchanged" {
							want = "once"
						}
						if final.response.Outcome.Selected == nil || string(final.response.Outcome.Selected.OptionId) != want {
							t.Fatalf("tool evidence change granted stale approval: %+v, want %s", final.response, want)
						}
						if change.name != "unchanged" {
							manual := awaitEvidenceReview(t, host.permissions)
							if manual.SessionId != "s" || manual.ToolCall.ToolCallId != "tool" {
								t.Fatal("manual fallback lost native ownership")
							}
						}
					}
					if change.name == "cancelled" || change.name == "unchanged" {
						select {
						case <-host.permissions:
							t.Fatal("unchanged approval/cancellation unexpectedly called host")
						default:
						}
					}
				})
			}
		}
	}
}
