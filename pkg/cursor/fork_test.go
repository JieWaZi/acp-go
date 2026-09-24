//go:build unix

package cursor

import (
	"bufio"
	"context"
	"database/sql"
	"github.com/JieWaZi/acp-go/internal/sessionfiles"
	"github.com/charmbracelet/x/term"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCursorForkCommandAndIndependentColdStore 使用真实 PTY 与 SQLite 验证 /fork 管理命令和独立冷状态。
func TestCursorForkCommandAndIndependentColdStore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "parent")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(source, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE blobs(id TEXT PRIMARY KEY,data BLOB); INSERT INTO blobs VALUES('before','{"role":"user","content":[{"type":"text","text":"native-context"}]}')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	terminal, err := startTerminal(ctx, binary, []string{"-test.run=^TestCursorForkTerminalProcess$"}, append(os.Environ(), "CURSOR_FORK_TEST="+root), root)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.close(context.Background())
	if err = terminal.wait(ctx, func(screen string) bool { return strings.Contains(screen, "Plan, search, build anything") }); err != nil {
		t.Fatal(err)
	}
	session := &interactiveSession{terminal: terminal, store: newStoreCursor(filepath.Join(source, "store.db"))}
	id, err := forkTerminalSession(ctx, session)
	if err != nil || id != "child" {
		t.Fatalf("%s %v", id, err)
	}
	if err = terminal.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cwd, state := t.TempDir(), t.TempDir()
	if err = publishForkStore(filepath.Join(root, id), state, id, cwd); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	batch, err := newStoreCursor(filepath.Join(state, "acp-sessions", id, "store.db")).read(ctx)
	if err != nil || len(batch.messages) != 1 || batch.messages[0].content[0].text != "native-context" {
		t.Fatalf("cold context: %+v %v", batch, err)
	}
}

// TestCursorForkTerminalProcess 模拟上游先复制 blob、再写 sidecar 的 /fork 完成顺序。
func TestCursorForkTerminalProcess(t *testing.T) {
	root := os.Getenv("CURSOR_FORK_TEST")
	if root == "" {
		return
	}
	if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString("Plan, search, build anything\r\n")
	reader := bufio.NewReader(os.Stdin)
	var input strings.Builder
	var err error
	for {
		var b byte
		b, err = reader.ReadByte()
		if err != nil || b == '\r' {
			break
		}
		input.WriteByte(b)
		_, _ = os.Stdout.Write([]byte{b})
	}
	line := input.String()
	if err != nil || strings.TrimSpace(line) != "/fork" {
		os.Exit(3)
	}
	if err = sessionfiles.CopyTree(filepath.Join(root, "parent"), filepath.Join(root, "child")); err != nil {
		os.Exit(4)
	}
	if err = sessionfiles.WriteFile(filepath.Join(root, "child", "meta.json"), []byte(`{"title":"renamed native branch","hasConversation":true}`)); err != nil {
		os.Exit(5)
	}
	_, _ = os.Stdout.WriteString("fixture (forked)\r\n")
	_, _ = reader.ReadByte()
	os.Exit(0)
}
