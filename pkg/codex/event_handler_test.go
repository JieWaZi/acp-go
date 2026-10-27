package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// expectedCodexMessageChunk 描述消费者可观察的单个消息正文与原生证据。
type expectedCodexMessageChunk struct {
	// text 是该通知增量提供的正文，完成边界可以为空。
	text string
	// phase 是原生已知阶段；空值要求通知不提供阶段。
	phase string
	// phaseVerified 表示阶段是否来自原生 started/completed。
	phaseVerified bool
	// messageCompleted 表示该通知是否对应原生 item/completed。
	messageCompleted bool
}

// TestEventRouterPreservesMessageFinalityEvidence 验证缺阶段不升为 final，完成边界保留原生证据且不重复正文。
func TestEventRouterPreservesMessageFinalityEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// name 描述该用例保护的证据边界。
		name string
		// notifications 是实际 app-server 形状的按序通知。
		notifications []string
		// want 是消费者应收到的正文与证据序列。
		want []expectedCodexMessageChunk
	}{
		{
			name: "missing phase remains unknown",
			notifications: []string{
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Answer"}}}`,
			},
			want: []expectedCodexMessageChunk{{text: "Answer", messageCompleted: true}},
		},
		{
			name: "null phase remains unknown",
			notifications: []string{
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Answer","phase":null}}}`,
			},
			want: []expectedCodexMessageChunk{{text: "Answer", messageCompleted: true}},
		},
		{
			name: "completed final phase overrides started commentary without repeating text",
			notifications: []string{
				`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"agentMessage","id":"message-1","text":"","phase":"commentary"}}}`,
				`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"An"}}`,
				`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"swer"}}`,
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Answer","phase":"final_answer"}}}`,
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Answer","phase":"final_answer"}}}`,
			},
			want: []expectedCodexMessageChunk{
				{text: "An", phase: "commentary", phaseVerified: true},
				{text: "swer", phase: "commentary", phaseVerified: true},
				{phase: "final_answer", phaseVerified: true, messageCompleted: true},
			},
		},
		{
			name: "final phase first appears at completion",
			notifications: []string{
				`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"Answer"}}`,
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Answer","phase":"final_answer"}}}`,
			},
			want: []expectedCodexMessageChunk{
				{text: "Answer"},
				{phase: "final_answer", phaseVerified: true, messageCompleted: true},
			},
		},
		{
			name: "streamed completion inherits started phase",
			notifications: []string{
				`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"agentMessage","id":"message-1","text":"","phase":"final_answer"}}}`,
				`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"Answer"}}`,
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Answer"}}}`,
			},
			want: []expectedCodexMessageChunk{
				{text: "Answer", phase: "final_answer", phaseVerified: true},
				{phase: "final_answer", phaseVerified: true, messageCompleted: true},
			},
		},
		{
			name: "nonstreamed completion inherits started phase",
			notifications: []string{
				`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"agentMessage","id":"message-1","text":"","phase":"commentary"}}}`,
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Checking"}}}`,
			},
			want: []expectedCodexMessageChunk{{text: "Checking", phase: "commentary", phaseVerified: true, messageCompleted: true}},
		},
		{
			name: "partial answer is preserved",
			notifications: []string{
				`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"agentMessage","id":"message-1","text":"","phase":"partial_answer"}}}`,
				`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"Partial"}}`,
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Partial","phase":"partial_answer"}}}`,
			},
			want: []expectedCodexMessageChunk{
				{text: "Partial", phase: "partial_answer", phaseVerified: true},
				{phase: "partial_answer", phaseVerified: true, messageCompleted: true},
			},
		},
		{
			name: "completed plan stays unverified commentary",
			notifications: []string{
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"plan","id":"message-1","text":"Plan","phase":"final_answer"}}}`,
			},
			want: []expectedCodexMessageChunk{{text: "Plan", phase: "commentary", messageCompleted: true}},
		},
		{
			name: "empty final message still emits completion",
			notifications: []string{
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"","phase":"final_answer"}}}`,
			},
			want: []expectedCodexMessageChunk{{phase: "final_answer", phaseVerified: true, messageCompleted: true}},
		},
		{
			name: "absent final text still emits completion",
			notifications: []string{
				`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","phase":"final_answer"}}}`,
			},
			want: []expectedCodexMessageChunk{{phase: "final_answer", phaseVerified: true, messageCompleted: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			updater := &recordingSessionUpdater{}
			router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
			for _, raw := range tt.notifications {
				if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
					t.Fatalf("HandleJSON 返回错误: %v", err)
				}
			}
			if len(updater.notifications) != len(tt.want) {
				t.Fatalf("消息通知数 = %d，期望 %d", len(updater.notifications), len(tt.want))
			}
			for index, want := range tt.want {
				assertCodexMessageChunk(t, updater.notifications[index], want)
			}
		})
	}
}

// assertCodexMessageChunk 检查公开 ACP 通知的消息身份、增量正文与最终性元数据。
func assertCodexMessageChunk(t *testing.T, notification acp.SessionNotification, want expectedCodexMessageChunk) {
	t.Helper()
	message := notification.Update.AgentMessageChunk
	if message == nil || message.MessageId == nil || *message.MessageId != "message-1" || message.Content.Text == nil {
		t.Fatalf("消息身份或正文类型不正确: %#v", message)
	}
	if message.Content.Text.Text != want.text {
		t.Errorf("消息正文 = %q，期望 %q", message.Content.Text.Text, want.text)
	}
	meta, ok := message.Meta["codex"].(map[string]any)
	if !ok {
		t.Fatalf("消息缺少 Codex 证据: %#v", message.Meta)
	}
	if want.phase == "" {
		if phase, present := meta["phase"]; present {
			t.Errorf("未知阶段不应提供 phase，收到 %#v", phase)
		}
	} else if meta["phase"] != want.phase {
		t.Errorf("phase = %#v，期望 %q", meta["phase"], want.phase)
	}
	if got := meta["phaseVerified"]; got != want.phaseVerified {
		t.Errorf("phaseVerified = %#v，期望 %v", got, want.phaseVerified)
	}
	if got := meta["messageCompleted"]; got != want.messageCompleted {
		t.Errorf("messageCompleted = %#v，期望 %v", got, want.messageCompleted)
	}
}

// failOnceMessageUpdater 模拟 ACP 完成边界首次发送失败，成功通知仍按真实 DTO 保存。
type failOnceMessageUpdater struct {
	// failed 标记唯一一次预期的边界发送失败是否已发生。
	failed bool
	// notifications 保存成功发出的消息通知。
	notifications []acp.SessionNotification
}

// SessionUpdate 在首次完成边界返回错误，后续重试正常记录。
func (u *failOnceMessageUpdater) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	message := notification.Update.AgentMessageChunk
	if message != nil && !u.failed {
		if meta, ok := message.Meta["codex"].(map[string]any); ok && meta["messageCompleted"] == true {
			u.failed = true
			return errors.New("test completion delivery failed")
		}
	}
	u.notifications = append(u.notifications, notification)
	return nil
}

// TestEventRouterRetriesCompletionWithoutRepeatingStreamedText 验证发送失败后可重试边界且保留 started 阶段。
func TestEventRouterRetriesCompletionWithoutRepeatingStreamedText(t *testing.T) {
	t.Parallel()
	updater := &failOnceMessageUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	for _, raw := range []string{
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"agentMessage","id":"message-1","text":"","phase":"final_answer"}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"Answer"}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	completed := []byte(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"message-1","text":"Answer"}}}`)
	if err := router.HandleJSON(context.Background(), completed); err == nil {
		t.Fatal("首次完成边界发送应返回错误")
	}
	if err := router.HandleJSON(context.Background(), completed); err != nil {
		t.Fatalf("完成边界重试失败: %v", err)
	}
	if len(updater.notifications) != 2 {
		t.Fatalf("重试后成功通知数 = %d，期望 2", len(updater.notifications))
	}
	assertCodexMessageChunk(t, updater.notifications[0], expectedCodexMessageChunk{text: "Answer", phase: "final_answer", phaseVerified: true})
	assertCodexMessageChunk(t, updater.notifications[1], expectedCodexMessageChunk{phase: "final_answer", phaseVerified: true, messageCompleted: true})
}

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
	if got, want := len(updater.notifications), 5; got != want {
		t.Fatalf("update 数 = %d，期望 %d", got, want)
	}
	message := updater.notifications[0].Update.AgentMessageChunk
	if message == nil || message.MessageId == nil || *message.MessageId != "message-1" || message.Content.Text == nil || message.Content.Text.Text != "Checking." {
		t.Fatalf("agent message = %#v", message)
	}
	if got := message.Meta["codex"].(map[string]any)["phase"]; got != "commentary" {
		t.Fatalf("message phase = %#v，期望 commentary", got)
	}
	assertCodexMessageChunk(t, updater.notifications[1], expectedCodexMessageChunk{
		phase: "commentary", phaseVerified: true, messageCompleted: true,
	})
	wantThoughts := []string{"First thought", "\n\n", "Raw detail"}
	for index, want := range wantThoughts {
		thought := updater.notifications[index+2].Update.AgentThoughtChunk
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
	promptUsage := usage.PromptUsage()
	if promptUsage == nil || promptUsage.TotalTokens != 1500 || promptUsage.InputTokens != 1200 ||
		promptUsage.OutputTokens != 200 || promptUsage.ThoughtTokens == nil || *promptUsage.ThoughtTokens != 100 {
		t.Fatalf("Prompt Usage = %#v", promptUsage)
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

// TestEventRouterLogsOnlySafeUnknownSummary 验证未知 method 单独记录安全 identity 而不泄漏 payload。
func TestEventRouterLogsOnlySafeUnknownSummary(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	var logs bytes.Buffer
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.New(slog.NewTextHandler(&logs, nil)))
	raw := `{"method":"future/notification","params":{"threadId":"thread-1","turnId":"turn-1","secret":"never-log-this"}}`
	if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
		t.Fatalf("unknown HandleJSON 返回错误: %v", err)
	}
	got := logs.String()
	for _, identity := range []string{
		"future/notification", "session-1", "thread-1", "turn-1", "payload_bytes=",
	} {
		if !strings.Contains(got, identity) {
			t.Fatalf("未知 method 安全摘要 = %q，缺少 %q", got, identity)
		}
	}
	if strings.Contains(got, "never-log-this") {
		t.Fatalf("日志泄漏原始 payload: %q", got)
	}
}

// TestEventRouterLogsOnlySafeUnknownItemSummary 验证未知 completed item 的摘要与未知 method 相互隔离。
func TestEventRouterLogsOnlySafeUnknownItemSummary(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	var logs bytes.Buffer
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.New(slog.NewTextHandler(&logs, nil)))
	raw := `{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"futureItem","id":"future-1","text":"never-log-item-text"}}}`
	if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
		t.Fatalf("unknown item HandleJSON 返回错误: %v", err)
	}
	got := logs.String()
	if !strings.Contains(got, "futureItem") || !strings.Contains(got, "future-1") {
		t.Fatalf("未知 item 安全摘要 = %q，缺少 type/id", got)
	}
	if strings.Contains(got, "never-log-item-text") || strings.Contains(got, "future/notification") {
		t.Fatalf("未知 item 日志泄漏或混入其他测试: %q", got)
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
	}, guard, logger, terminalOutputModeDelta)
}
