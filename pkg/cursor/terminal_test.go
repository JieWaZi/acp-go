//go:build unix

package cursor

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	acp "github.com/coder/acp-go-sdk"
)

// terminalHost 通过真实 ACP 回调返回测试审批，记录完整的工具与文本输出。
type terminalHost struct {
	// Client 保留测试未使用的宿主接口。
	acp.Client
	// mutex 保护异步 SDK 通知。
	mutex sync.Mutex
	// allow 控制下一次审批结果。
	allow bool
	// waiting 非空时测试宿主保持审批挂起直到当前轮取消。
	waiting chan struct{}
	// calls 记录包含原始参数的真实审批请求。
	calls []acp.RequestPermissionRequest
	// updates 收集本轮投影结果。
	updates []acp.SessionNotification
}

// RequestPermission 返回当前测试用户的真实选择。
func (h *terminalHost) RequestPermission(ctx context.Context, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if h.waiting != nil {
		close(h.waiting)
		<-ctx.Done()
		return acp.RequestPermissionResponse{}, ctx.Err()
	}
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.calls = append(h.calls, r)
	choice := "reject"
	if h.allow {
		choice = "allow"
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId(choice))}, nil
}

// SessionUpdate 记录顺序投影。
func (h *terminalHost) SessionUpdate(_ context.Context, r acp.SessionNotification) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.updates = append(h.updates, r)
	return nil
}

// TestCursorTerminalRoundTrip 通过真实 PTY、SQLite 和 ACP 回调验证拒绝、批准、常驻复用及逐轮用量。
func TestCursorTerminalRoundTrip(t *testing.T) {
	root := t.TempDir()
	events := filepath.Join(root, "events")
	if err := os.Mkdir(events, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	terminal, err := startTerminal(ctx, binary, []string{"-test.run=^TestCursorTerminalProcess$"}, append(os.Environ(), "CURSOR_TERMINAL_TEST="+root), root)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.close(context.Background())
	h := &terminalHost{}
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	defer agentIn.Close()
	defer hostOut.Close()
	defer hostIn.Close()
	defer agentOut.Close()
	a := &Agent{}
	a.host = acp.NewAgentSideConnection(a, agentOut, agentIn)
	_ = acp.NewClientSideConnection(h, hostOut, hostIn)
	s := &interactiveSession{id: "fixture-session", directory: root, store: newStoreCursor(filepath.Join(root, "store.db")), terminal: terminal}
	for round := 0; round < 2; round++ {
		h.mutex.Lock()
		h.allow = round == 1
		h.mutex.Unlock()
		if err = terminal.submit(ctx, fmt.Sprintf("测试第 %d 轮", round)); err != nil {
			t.Fatal(err)
		}
		response, runErr := a.runTurn(ctx, s)
		if runErr != nil {
			t.Fatalf("round %d: %v; screen=%s", round, runErr, terminal.text())
		}
		if response.StopReason != acp.StopReasonEndTurn || response.Usage == nil || response.Usage.InputTokens != 20 || response.Usage.OutputTokens != 5 || response.Usage.TotalTokens != 105 || *response.Usage.CachedReadTokens != 80 {
			t.Fatalf("round %d usage=%+v", round, response)
		}
		_, statErr := os.Stat(filepath.Join(root, "allowed.txt"))
		if (statErr == nil) != (round == 1) {
			t.Fatalf("round %d side effect: %v", round, statErr)
		}
	}
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if len(h.calls) != 2 {
		t.Fatalf("approval count=%d", len(h.calls))
	}
	if h.calls[0].ToolCall.RawInput == nil {
		t.Fatal("missing permission evidence")
	}
	messages := 0
	for _, update := range h.updates {
		if update.Update.AgentMessageChunk != nil {
			messages++
		}
	}
	if messages != 2 {
		t.Fatalf("repeated or missing transcript: %d", messages)
	}
}

// TestCursorTerminalProcess 模拟官方终端的执行门禁，使用真实数据库和受管结束文件。
func TestCursorTerminalProcess(t *testing.T) {
	root := os.Getenv("CURSOR_TERMINAL_TEST")
	if root == "" {
		t.Skip("subprocess fixture")
	}
	if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
		panic(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "store.db"))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	// 官方 Cursor 使用 WAL，测试写端也使用相同并发读写行为。
	if _, err = db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		panic(err)
	}
	if _, err = db.Exec(`PRAGMA busy_timeout=1000`); err != nil {
		panic(err)
	}
	if _, err = db.Exec(`CREATE TABLE blobs (id TEXT PRIMARY KEY,data BLOB)`); err != nil {
		panic(err)
	}
	input := bufio.NewReader(os.Stdin)
	screen := func(value string) { fmt.Print("\x1b[2J\x1b[H" + value) }
	put := func(id string, value any, pending bool) {
		data, _ := json.Marshal(value)
		if pending {
			data = append([]byte{0xff, 0}, data...)
		}
		if _, err := db.Exec(`INSERT INTO blobs VALUES (?,?)`, id, data); err != nil {
			panic(err)
		}
	}
	for round := 0; ; round++ {
		screen("Plan, search, build anything")
		for {
			b, err := input.ReadByte()
			if err != nil {
				os.Exit(0)
			}
			if b == '\r' {
				break
			}
			fmt.Print(string([]byte{b}))
		}
		id := fmt.Sprintf("tool-%d", round)
		call := map[string]any{"type": "tool-call", "toolCallId": id, "toolName": "Shell", "args": map[string]any{"command": "printf allowed > allowed.txt"}}
		put(id+"-pending", map[string]any{"role": "assistant", "content": []any{call}, "providerOptions": map[string]any{"cursor": map[string]any{"pendingToolCallStartedAtMs": 123}}}, true)
		screen("Run (once) (y)\r\nSkip & tell the agent what to do instead (esc or n)")
		b, err := input.ReadByte()
		if err != nil {
			os.Exit(0)
		}
		result := "Rejected: "
		if b == 'y' {
			if err = os.WriteFile(filepath.Join(root, "allowed.txt"), []byte("allowed"), 0600); err != nil {
				panic(err)
			}
			result = "success"
		} else if b == 27 {
			screen("Tell the agent what to do instead (Enter to send, empty to skip, Esc to cancel)")
			b, err = input.ReadByte()
			if err != nil || b != '\r' {
				panic("rejection not confirmed")
			}
		} else {
			panic("unexpected approval input")
		}
		put(id+"-committed", map[string]any{"role": "assistant", "content": []any{call}}, false)
		put(id+"-result", map[string]any{"role": "tool", "content": []any{map[string]any{"type": "tool-result", "toolCallId": id, "toolName": "Shell", "result": result}}}, false)
		put(id+"-text", map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "完成"}}}, false)
		data, _ := json.Marshal(hookEvent{Name: "stop", Session: "fixture-session", Generation: id, Status: "completed", Input: acp.Ptr(100), Output: acp.Ptr(5), CacheRead: 80})
		file := filepath.Join(root, "events", id+".json")
		if err = os.WriteFile(file+".tmp", data, 0600); err != nil {
			panic(err)
		}
		if err = os.Rename(file+".tmp", file); err != nil {
			panic(err)
		}
	}
}

// TestCursorPromptControls 验证终端控制字符不能伪造按键回填。
func TestCursorPromptControls(t *testing.T) {
	terminal := &cursorTerminal{}
	for _, value := range []string{"hello\x1by", "hello\x00"} {
		if err := terminal.submit(context.Background(), value); err == nil || !strings.Contains(err.Error(), "control") {
			t.Fatalf("control accepted: %v", err)
		}
	}
}

// TestCursorCancelPendingApproval 验证取消挂起审批时不回填允许，且终端可被完整回收。
func TestCursorCancelPendingApproval(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lifetime, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	terminal, err := startTerminal(lifetime, binary, []string{"-test.run=^TestCursorTerminalProcess$"}, append(os.Environ(), "CURSOR_TERMINAL_TEST="+root), root)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.close(context.Background())
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	defer agentIn.Close()
	defer hostOut.Close()
	defer hostIn.Close()
	defer agentOut.Close()
	h := &terminalHost{waiting: make(chan struct{})}
	a := &Agent{config: Config{Interactive: true}, sessions: map[acp.SessionId]*interactiveSession{}}
	a.host = acp.NewAgentSideConnection(a, agentOut, agentIn)
	_ = acp.NewClientSideConnection(h, hostOut, hostIn)
	ctx, cancel := context.WithCancel(lifetime)
	defer cancel()
	s := &interactiveSession{id: "fixture-session", directory: root, store: newStoreCursor(filepath.Join(root, "store.db")), terminal: terminal, turnCancel: cancel}
	a.sessions[s.id] = s
	if err = terminal.submit(ctx, "等待审批后取消"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := a.runTurn(ctx, s); done <- err }()
	select {
	case <-h.waiting:
	case <-lifetime.Done():
		t.Fatal("approval not delivered")
	}
	if err = a.Cancel(lifetime, acp.CancelNotification{SessionId: s.id}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("cancel reported success")
		}
	case <-lifetime.Done():
		t.Fatal("cancel did not unblock approval")
	}
	if err = s.stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminal.done:
	default:
		t.Fatal("child not reaped")
	}
	if _, err = os.Stat(filepath.Join(root, "allowed.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancel allowed side effect: %v", err)
	}
}
