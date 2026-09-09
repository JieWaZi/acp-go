package cursor

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestCursorStorePendingAndResume 验证二进制待审查标记、工具结果消解和恢复边界。
func TestCursorStorePendingAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE blobs (id TEXT PRIMARY KEY,data BLOB)`); err != nil {
		t.Fatal(err)
	}
	pending := []byte("\x00\xff" + `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"call-1","toolName":"Shell","args":{"command":"echo 中文 {x}"}}],"providerOptions":{"cursor":{"pendingToolCallStartedAtMs":123}}}` + "\x00")
	if _, err = db.Exec(`INSERT INTO blobs VALUES ('pending',?)`, pending); err != nil {
		t.Fatal(err)
	}
	s := newStoreCursor(path)
	batch, err := s.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.pending) != 1 || batch.pending[0].name != "Shell" {
		t.Fatalf("pending: %+v", batch)
	}
	if _, err = db.Exec(`INSERT INTO blobs VALUES ('done',?)`, []byte(`{"role":"tool","content":[{"type":"tool-result","toolCallId":"call-1","result":{"success":true}}]}`)); err != nil {
		t.Fatal(err)
	}
	batch, err = s.read(context.Background())
	if err != nil || len(batch.pending) != 0 || len(batch.messages) != 1 {
		t.Fatalf("resolved: %+v %v", batch, err)
	}
	resumed := newStoreCursor(path)
	if err = resumed.seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO blobs VALUES ('next',?)`, []byte(`{"role":"assistant","content":[{"type":"text","text":"新回合"}]}`)); err != nil {
		t.Fatal(err)
	}
	batch, err = resumed.read(context.Background())
	if err != nil || len(batch.messages) != 1 || batch.messages[0].content[0].text != "新回合" {
		t.Fatalf("resume repeated history: %+v %v", batch, err)
	}
}
