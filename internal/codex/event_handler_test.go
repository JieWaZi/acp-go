package codex

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// recordingSessionUpdater 记录事件组件通过 ACP SDK 发出的原生 session/update DTO。
type recordingSessionUpdater struct {
	// notifications 保存按调用顺序收到的 ACP 通知。
	notifications []acp.SessionNotification
}

// SessionUpdate 实现事件组件需要的最小 ACP 连接接口。
func (u *recordingSessionUpdater) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	u.notifications = append(u.notifications, notification)
	return nil
}

// fixedGenerationGuard 为测试提供可切换的 runtime generation 判断。
type fixedGenerationGuard struct {
	// current 表示目标 generation 是否仍然有效。
	current bool
}

// IsCurrent 返回测试指定的 generation 有效性。
func (g *fixedGenerationGuard) IsCurrent(turnGeneration) bool {
	return g.current
}

// TestEventRouterStreamsMessagesAndReasoningWithoutCompletionDuplicates 验证 delta 优先且完成项不重复。
func TestEventRouterStreamsMessagesAndReasoningWithoutCompletionDuplicates(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	notifications := []string{
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"agentMessage","id":"message-1","text":"","phase":"commentary"}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"Checking."}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Checking.","phase":"commentary"}}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"reason-1","summaryIndex":0,"delta":"First thought"}}`,
		`{"method":"item/reasoning/summaryPartAdded","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"reason-1","summaryIndex":1}}`,
		`{"method":"item/reasoning/textDelta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"reason-1","contentIndex":0,"delta":"Raw detail"}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":2,"item":{"type":"reasoning","id":"reason-1","summary":["duplicate"],"content":["duplicate raw"]}}}`,
	}
	for _, raw := range notifications {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 4; got != want {
		t.Fatalf("update 数 = %d，期望 %d", got, want)
	}
	message := updater.notifications[0].Update.AgentMessageChunk
	if message == nil || message.MessageId == nil || *message.MessageId != "message-1" || message.Content.Text == nil || message.Content.Text.Text != "Checking." {
		t.Fatalf("agent message = %#v", message)
	}
	if got := message.Meta["codex"].(map[string]any)["phase"]; got != "commentary" {
		t.Fatalf("message phase = %#v，期望 commentary", got)
	}
	wantThoughts := []string{"First thought", "\n\n", "Raw detail"}
	for index, want := range wantThoughts {
		thought := updater.notifications[index+1].Update.AgentThoughtChunk
		if thought == nil || thought.MessageId == nil || *thought.MessageId != "reason-1" || thought.Content.Text == nil || thought.Content.Text.Text != want {
			t.Fatalf("thought[%d] = %#v，期望 %q", index, thought, want)
		}
	}
}

// TestEventRouterFallsBackToCompletedMessageAndReasoning 验证未收到 delta 时仍输出完整完成项。
func TestEventRouterFallsBackToCompletedMessageAndReasoning(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	for _, raw := range []string{
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-2","text":"Final answer","phase":"final_answer"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":2,"item":{"type":"reasoning","id":"reason-2","summary":["First summary","Second summary"],"content":["raw fallback"]}}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 2; got != want {
		t.Fatalf("update 数 = %d，期望 %d", got, want)
	}
	if got := updater.notifications[0].Update.AgentMessageChunk.Content.Text.Text; got != "Final answer" {
		t.Fatalf("完成消息 = %q", got)
	}
	if got := updater.notifications[1].Update.AgentThoughtChunk.Content.Text.Text; got != "First summary\n\nSecond summary" {
		t.Fatalf("完成 reasoning = %q", got)
	}
}

// TestEventRouterIgnoresDuplicateCompletedItems 验证同一完成项重放时不会重复通知客户端。
func TestEventRouterIgnoresDuplicateCompletedItems(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	message := `{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-once","text":"Only once","phase":"final_answer"}}}`
	command := `{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":2,"item":{"type":"commandExecution","id":"command-once","command":"echo once","cwd":"/work","status":"completed","commandActions":[],"aggregatedOutput":"once\n","exitCode":0}}}`
	for _, raw := range []string{message, message, command, command} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 2; got != want {
		t.Fatalf("重复 completed 发出 %d 个 update，期望 %d", got, want)
	}
}

// TestEventRouterMapsPlanAndLatestUsage 验证结构化计划与最新 turn usage 的 ACP 映射。
func TestEventRouterMapsPlanAndLatestUsage(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	for _, raw := range []string{
		`{"method":"turn/plan/updated","params":{"threadId":"thread-1","turnId":"turn-1","explanation":"Implement and verify.","plan":[{"step":"Add mapping","status":"completed"},{"step":"Verify it","status":"inProgress"}]}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":5000,"inputTokens":4000,"cachedInputTokens":1000,"outputTokens":900,"reasoningOutputTokens":100},"last":{"totalTokens":2500,"inputTokens":2000,"cachedInputTokens":500,"outputTokens":450,"reasoningOutputTokens":50},"modelContextWindow":128000}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":7000,"inputTokens":5600,"cachedInputTokens":1000,"outputTokens":1200,"reasoningOutputTokens":200},"last":{"totalTokens":1500,"inputTokens":1200,"cachedInputTokens":0,"outputTokens":200,"reasoningOutputTokens":100},"modelContextWindow":128000}}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	plan := updater.notifications[0].Update.Plan
	if plan == nil || len(plan.Entries) != 2 || plan.Entries[0].Status != acp.PlanEntryStatusCompleted || plan.Entries[1].Status != acp.PlanEntryStatusInProgress {
		t.Fatalf("plan = %#v", plan)
	}
	if got := updater.notifications[2].Update.UsageUpdate; got == nil || got.Used != 1500 || got.Size != 128000 {
		t.Fatalf("latest usage update = %#v", got)
	}
	usage, ok := router.Usage()
	if !ok || usage.LastTokens != 1500 || usage.TotalTokens != 7000 {
		t.Fatalf("latest usage = %#v, %v", usage, ok)
	}
}

// TestEventRouterDoesNotLeakUsageAcrossTurns 验证新 generation 的 handler 不继承旧 turn usage。
func TestEventRouterDoesNotLeakUsageAcrossTurns(t *testing.T) {
	t.Parallel()

	guard := &fixedGenerationGuard{current: true}
	first := newTestEventRouter(&recordingSessionUpdater{}, guard, slog.Default())
	err := first.HandleJSON(context.Background(), []byte(
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":7000,"inputTokens":5600,"cachedInputTokens":1000,"outputTokens":1200,"reasoningOutputTokens":200},"last":{"totalTokens":1500,"inputTokens":1200,"cachedInputTokens":0,"outputTokens":200,"reasoningOutputTokens":100},"modelContextWindow":128000}}}`,
	))
	if err != nil {
		t.Fatalf("首个 turn usage 返回错误: %v", err)
	}
	if _, ok := first.Usage(); !ok {
		t.Fatal("首个 turn 应保存 usage")
	}

	second := newTestEventRouter(&recordingSessionUpdater{}, guard, slog.Default())
	if usage, ok := second.Usage(); ok {
		t.Fatalf("新 turn 泄漏 usage: %#v", usage)
	}
}

// TestEventRouterSuppressesStaleGeneration 验证失效 generation 的通知不能进入 ACP session。
func TestEventRouterSuppressesStaleGeneration(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	guard := &fixedGenerationGuard{current: false}
	router := newTestEventRouter(updater, guard, slog.Default())
	err := router.HandleJSON(context.Background(), []byte(
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"stale"}}`,
	))
	if err != nil {
		t.Fatalf("stale HandleJSON 返回错误: %v", err)
	}
	if len(updater.notifications) != 0 {
		t.Fatalf("stale generation 发出了 %d 个 update", len(updater.notifications))
	}
}

// TestEventRouterLogsOnlySafeUnknownSummary 验证未知 method/item 的日志不会包含原始敏感 payload。
func TestEventRouterLogsOnlySafeUnknownSummary(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	var logs bytes.Buffer
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.New(slog.NewTextHandler(&logs, nil)))
	for _, raw := range []string{
		`{"method":"future/notification","params":{"threadId":"thread-1","turnId":"turn-1","secret":"never-log-this"}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"futureItem","id":"future-1","text":"never-log-item-text"}}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("unknown HandleJSON 返回错误: %v", err)
		}
	}
	got := logs.String()
	if !strings.Contains(got, "future/notification") || !strings.Contains(got, "futureItem") || !strings.Contains(got, "future-1") {
		t.Fatalf("安全摘要日志 = %q，缺少 method/type/id", got)
	}
	if strings.Contains(got, "never-log-this") || strings.Contains(got, "never-log-item-text") {
		t.Fatalf("日志泄漏原始 payload: %q", got)
	}
}

// TestEventRouterLogsSafeUnknownStartedItem 验证未知 started item 也只记录 discriminator 与 id。
func TestEventRouterLogsSafeUnknownStartedItem(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	var logs bytes.Buffer
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.New(slog.NewTextHandler(&logs, nil)))
	err := router.HandleJSON(context.Background(), []byte(
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":1,"item":{"type":"futureStartedItem","id":"future-start-1","text":"never-log-started-text"}}}`,
	))
	if err != nil {
		t.Fatalf("unknown started item 返回错误: %v", err)
	}
	got := logs.String()
	if !strings.Contains(got, "futureStartedItem") || !strings.Contains(got, "future-start-1") {
		t.Fatalf("started item 安全摘要 = %q", got)
	}
	if strings.Contains(got, "never-log-started-text") {
		t.Fatalf("started item 日志泄漏 payload: %q", got)
	}
}

// newTestEventRouter 创建绑定固定 session/thread/turn 的事件路由器。
func newTestEventRouter(updater sessionUpdater, guard generationGuard, logger *slog.Logger) *eventRouter {
	return newEventRouter(updater, turnGeneration{
		SessionID: "session-1", ThreadID: "thread-1", TurnID: "turn-1", Generation: 7,
	}, guard, logger)
}
