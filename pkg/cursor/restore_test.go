package cursor

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	_ "github.com/ncruces/go-sqlite3/driver"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestCursorRestoreIncludesWAL 验证恢复快照包含尚未 checkpoint 的已提交消息，并不改写真实执行库。
func TestCursorRestoreIncludesWAL(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	sum := md5.Sum([]byte(cwd))
	source := filepath.Join(state, "chats", hex.EncodeToString(sum[:]), "session", "store.db")
	destination := filepath.Join(state, "acp-sessions", "session")
	for _, directory := range []string{filepath.Dir(source), destination} {
		if err = os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(destination, "meta.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, `CREATE TABLE blobs (id TEXT PRIMARY KEY,data BLOB)`, `INSERT INTO blobs VALUES ('one','{"role":"assistant","content":[{"type":"text","text":"记住这个"}]}')`} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if stat, err := os.Stat(source + "-wal"); err != nil || stat.Size() == 0 {
		t.Fatalf("missing WAL: %v", err)
	}
	a := &Agent{state: state}
	if err = a.snapshotControlStore(context.Background(), "session", cwd); err != nil {
		t.Fatal(err)
	}
	copied, err := newStoreCursor(filepath.Join(destination, "store.db")).read(context.Background())
	if err != nil || len(copied.messages) != 1 || copied.messages[0].content[0].text != "记住这个" {
		t.Fatalf("snapshot=%+v err=%v", copied, err)
	}
	if _, err = db.Exec(`INSERT INTO blobs VALUES ('two','{}')`); err != nil {
		t.Fatalf("source no longer writable: %v", err)
	}
}

// TestCursorSessionOwnership 验证第二连接和重复加载不能在快照前取得同一身份。
func TestCursorSessionOwnership(t *testing.T) {
	state := t.TempDir()
	a := &Agent{state: state, config: Config{}, sessions: map[acp.SessionId]*interactiveSession{}}
	b := &Agent{state: state, config: Config{}}
	first, err := a.claim("session")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Unlock()
	if second, err := b.claim("session"); err == nil {
		second.Unlock()
		t.Fatal("duplicate ownership accepted")
	}
	if err = first.Unlock(); err != nil {
		t.Fatal(err)
	}
	next, err := b.claim("session")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Unlock()
	a.sessions["loaded"] = &interactiveSession{}
	if _, err = a.claim("loaded"); err == nil {
		t.Fatal("loaded session can be snapshotted")
	}
	for _, id := range []acp.SessionId{"../escape", "/absolute", "..", "bad\x00id"} {
		if _, err = a.claim(id); err == nil {
			t.Fatalf("invalid identity accepted: %q", id)
		}
	}
}
