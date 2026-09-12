package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestCursorMissingStopHookFailure 回放真实额度错误，验证没有 stop Hook 时仍及时结束当前轮且保留可重试分类。
func TestCursorMissingStopHookFailure(t *testing.T) {
	root := t.TempDir()
	events := filepath.Join(root, "events")
	if err := os.Mkdir(events, 0700); err != nil {
		t.Fatal(err)
	}
	c := &outcomeCursor{directory: root, pid: 123, started: time.Now().Add(-time.Second), offsets: map[string]int64{}}
	path := filepath.Join(root, "session-2026-09-09T03-03-01-857Z-123-1.log")
	raw := `[2026-09-09T03:03:18.122Z] structured-log.info {"message":"agent_cli.turn.outcome","metadata":{"outcome":"error","error_type":"action_required","error_code":"upgrade","grpc_code":"resource_exhausted","user_email":"private@example.test","error_text":"private details"}}` + "\n"
	if err := os.WriteFile(path, []byte(raw+raw), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	s := &interactiveSession{id: "session", directory: root, store: newStoreCursor(filepath.Join(root, "missing.db")), terminal: &cursorTerminal{outcome: c}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := a.runTurn(ctx, s)
	var request *acp.RequestError
	if !errors.As(err, &request) {
		t.Fatalf("missing failure: %+v %v", response, err)
	}
	data, ok := request.Data.(map[string]any)
	if !ok || data["errorKind"] != "billing_error" {
		t.Fatalf("wrong classification: %+v", request)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("private diagnostic escaped")
	}
	if response.Usage != nil {
		t.Fatal("invented usage")
	}
	if err = c.failure(); err != nil {
		t.Fatalf("failure replayed into next turn: %v", err)
	}
}

// TestCursorOutcomePartialAndForeignLogs 验证分段写入不会丢事件，其他进程与旧日志不能结束当前轮。
func TestCursorOutcomePartialAndForeignLogs(t *testing.T) {
	root := t.TempDir()
	c := &outcomeCursor{directory: root, pid: 123, started: time.Now().Add(-time.Second), offsets: map[string]int64{}}
	raw := `[2026-09-09T03:03:18.122Z] structured-log.info {"message":"agent_cli.turn.outcome","metadata":{"outcome":"error","grpc_code":"resource_exhausted"}}`
	foreign := filepath.Join(root, "session-date-124-1.log")
	if err := os.WriteFile(foreign, []byte(raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session-date-123-1.log")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.failure(); err != nil {
		t.Fatalf("foreign or partial log accepted: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.failure(); err == nil {
		t.Fatal("partial line lost")
	}
	old := time.Now().Add(-time.Hour)
	if err = os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	c.offsets = map[string]int64{}
	if err = c.failure(); err != nil {
		t.Fatalf("old process log replayed: %v", err)
	}
}
