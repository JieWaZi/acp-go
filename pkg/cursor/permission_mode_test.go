package cursor

import (
	"context"
	"database/sql"
	"encoding/hex"
	acp "github.com/coder/acp-go-sdk"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCursorStoredPermission 验证从官方元数据读取权限，优先当前策略并拒绝未知策略。
func TestCursorStoredPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	for raw, expected := range map[string]string{`{"approvalMode":"allowlist","isRunEverything":true}`: "allowlist", `{"isRunEverything":true}`: "unrestricted", `{"approvalMode":"auto-review"}`: "auto-review", `{"approvalMode":"unknown"}`: ""} {
		if _, err = db.Exec("INSERT OR REPLACE INTO meta VALUES ('0',?)", hex.EncodeToString([]byte(raw))); err != nil {
			t.Fatal(err)
		}
		got, err := newStoreCursor(path).approvalMode(context.Background())
		if expected == "" {
			if err == nil {
				t.Fatal("unknown permission accepted")
			}
		} else if err != nil || got != expected {
			t.Fatalf("mode=%q err=%v", got, err)
		}
	}
}

// TestCursorRealPermissionTransitions 显式启用时，不调用模型，验证真实 CLI 跨进程恢复后的权限提升及降级。
func TestCursorRealPermissionTransitions(t *testing.T) {
	if os.Getenv("ALLY_CURSOR_REAL_PROBE") != "1" {
		t.Skip("explicit real Cursor probe required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cwd, state := t.TempDir(), t.TempDir()
	var id acp.SessionId
	for _, mode := range []string{"full-access", "default", "auto", "default"} {
		a, err := NewAgent(ctx, Config{Interactive: true, StateDirectory: state, Environment: os.Environ(), Logger: slog.Default(), PermissionMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		reader, writer := io.Pipe()
		a.SetAgentConnection(acp.NewAgentSideConnection(a, io.Discard, reader))
		runErr := func() error {
			defer a.Close(context.Background())
			defer reader.Close()
			defer writer.Close()
			if _, err = a.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				return err
			}
			if id == "" {
				response, openErr := a.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
				id = response.SessionId
				err = openErr
			} else {
				_, err = a.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: id, Cwd: cwd})
			}
			if err != nil {
				return err
			}
			if _, err = a.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: id, ConfigId: "model", Value: "composer-2.5"}}); err != nil {
				return err
			}
			session, err := a.session(id)
			if err != nil {
				return err
			}
			if err = a.start(ctx, session); err != nil {
				return err
			}
			actual, err := session.store.approvalMode(ctx)
			if err != nil {
				return err
			}
			expected := map[string]string{"full-access": "unrestricted", "default": "allowlist", "auto": "auto-review"}[mode]
			if actual != expected {
				t.Errorf("requested=%s actual=%s", mode, actual)
			}
			t.Logf("requested=%s actual=%s session=%s", mode, actual, id)
			return nil
		}()
		if runErr != nil {
			t.Fatalf("%s: %v", mode, runErr)
		}
	}
}
