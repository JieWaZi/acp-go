package cursor

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/gofrs/flock"
)

// snapshotControlStore 通过 SQLite 一致快照把真实历史交给官方 ACP 读取，避免跨路径遗漏 WAL 或双进程改写执行日志。
func (a *Agent) snapshotControlStore(ctx context.Context, id acp.SessionId, cwd string) error {
	if id == "" || strings.ContainsAny(string(id), "/\\\x00") || id == "." || id == ".." {
		return errors.New("invalid Cursor session identity")
	}
	path, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	sum := md5.Sum([]byte(path)) // 与官方工作目录索引一致，不用于安全校验。
	source := newStoreCursor(filepath.Join(a.state, "chats", hex.EncodeToString(sum[:]), string(id), "store.db"))
	db, err := source.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	destination := filepath.Join(a.state, "acp-sessions", string(id))
	if _, err = os.Stat(filepath.Join(destination, "meta.json")); err != nil {
		return err
	}
	temp, err := os.CreateTemp(destination, "restore-*.db")
	if err != nil {
		return err
	}
	name := temp.Name()
	if err = temp.Close(); err != nil {
		return err
	}
	defer os.Remove(name)
	snapshot, _, err := db.Prepare(`VACUUM INTO ?`)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	if err = snapshot.BindText(1, name); err != nil {
		return err
	}
	if err = snapshot.Exec(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(destination, "store.db"))
}

// claim 在读取或恢复日志之前独占原生身份；锁文件不删除，避免 inode 替换使互斥失效。
func (a *Agent) claim(id acp.SessionId) (*flock.Flock, error) {
	if !a.config.Interactive {
		return nil, nil
	}
	if id == "" || strings.ContainsAny(string(id), "/\\\x00") || id == "." || id == ".." {
		return nil, errors.New("invalid Cursor session identity")
	}
	a.mutex.Lock()
	unavailable := a.closed || a.sessions[id] != nil
	a.mutex.Unlock()
	if unavailable {
		return nil, errors.New("Cursor session already loaded or agent closed")
	}
	directory := filepath.Join(a.state, "locks")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	owner := flock.New(filepath.Join(directory, string(id)+".lock"))
	locked, err := owner.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("Cursor session is owned by another connection")
	}
	return owner, nil
}
