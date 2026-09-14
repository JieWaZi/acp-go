package cursor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// hookLease 仅登记当前适配器自己的 Hook，结束时保留用户同期修改。
type hookLease struct {
	// path 是官方仅在项目范围识别的 Hook 配置。
	path string
	// lock 是受管缓存中的跨进程文件锁，不在项目留下锁文件。
	lock *flock.Flock
	// entries 保存本次添加的精确条目，清理不按脚本名称模糊匹配。
	entries map[string][]json.RawMessage
	// original 是修改前的原始文件，用于无并发修改时恢复原格式。
	original []byte
	// installed 是本次写入的完整快照。
	installed []byte
	// mode 保留用户配置文件的权限。
	mode os.FileMode
	// createdDirectory 标记本次创建的空配置目录，清理不递归删除。
	createdDirectory bool
}

// installHookLease 在文件锁内合并本会话 Hook，记录可精确撤回的安装快照。
func installHookLease(ctx context.Context, cwd, configPath string) (lease *hookLease, err error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var owned map[string]json.RawMessage
	if json.Unmarshal(data, &owned) != nil {
		return nil, errors.New("invalid managed Cursor hook configuration")
	}
	entries := map[string][]json.RawMessage{}
	if json.Unmarshal(owned["hooks"], &entries) != nil {
		return nil, errors.New("invalid managed Cursor hooks")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	locks := filepath.Join(cache, "acp-go", "cursor-hook-locks")
	if err = os.MkdirAll(locks, 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(cwd))
	lease = &hookLease{path: filepath.Join(cwd, ".cursor", "hooks.json"), lock: flock.New(filepath.Join(locks, hex.EncodeToString(sum[:])+".lock")), entries: entries, mode: 0600}
	locked, err := lease.lock.TryLockContext(ctx, 40*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ctx.Err()
	}
	defer lease.lock.Unlock()
	stat, statErr := os.Lstat(lease.path)
	if statErr == nil {
		if !stat.Mode().IsRegular() {
			return nil, errors.New("Cursor project hooks must be a regular file")
		}
		lease.mode = stat.Mode().Perm()
		lease.original, err = os.ReadFile(lease.path)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	fields, hooks, err := decodeProjectHooks(lease.original)
	if err != nil {
		return nil, err
	}
	for name, values := range entries {
		hooks[name] = append(hooks[name], values...)
	}
	fields["hooks"], _ = json.Marshal(hooks)
	lease.installed, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(lease.path)
	if _, statErr = os.Stat(directory); errors.Is(statErr, os.ErrNotExist) {
		if err = os.Mkdir(directory, 0700); err != nil {
			return nil, err
		}
		lease.createdDirectory = true
	} else if statErr != nil {
		return nil, statErr
	}
	if err = writeHookFile(lease.path, lease.installed, lease.mode); err != nil {
		if lease.createdDirectory {
			_ = os.Remove(directory)
		}
		return nil, err
	}
	return lease, nil
}

// decodeProjectHooks 保留未知配置字段并校验官方 Hook 对象结构。
func decodeProjectHooks(data []byte) (map[string]json.RawMessage, map[string][]json.RawMessage, error) {
	fields := map[string]json.RawMessage{"version": json.RawMessage("1")}
	hooks := map[string][]json.RawMessage{}
	if len(data) == 0 {
		return fields, hooks, nil
	}
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, nil, errors.New("invalid Cursor project hooks configuration")
	}
	if raw, exists := fields["hooks"]; exists {
		if json.Unmarshal(raw, &hooks) != nil || hooks == nil {
			return nil, nil, errors.New("invalid Cursor project hooks entries")
		}
	}
	return fields, hooks, nil
}

// close 仅撤回自己添加的精确条目，用户改动存在时合并清理而不覆盖原文件。
func (l *hookLease) close(ctx context.Context) error {
	locked, err := l.lock.TryLockContext(ctx, 40*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	defer l.lock.Unlock()
	stat, err := os.Lstat(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return errors.New("Cursor hook file changed type during session")
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	if bytes.Equal(data, l.installed) {
		if l.original == nil {
			err = os.Remove(l.path)
		} else {
			err = writeHookFile(l.path, l.original, l.mode)
		}
	} else {
		fields, hooks, decodeErr := decodeProjectHooks(data)
		if decodeErr != nil {
			return decodeErr
		}
		for name, owned := range l.entries {
			kept := []json.RawMessage{}
			for _, entry := range hooks[name] {
				remove := false
				for _, candidate := range owned {
					var actualValue, ownedValue any
					_ = json.Unmarshal(entry, &actualValue)
					_ = json.Unmarshal(candidate, &ownedValue)
					actualJSON, _ := json.Marshal(actualValue)
					ownedJSON, _ := json.Marshal(ownedValue)
					if bytes.Equal(actualJSON, ownedJSON) {
						remove = true
						break
					}
				}
				if !remove {
					kept = append(kept, entry)
				}
			}
			if len(kept) == 0 {
				delete(hooks, name)
			} else {
				hooks[name] = kept
			}
		}
		if len(hooks) == 0 && l.original == nil && len(fields) == 2 && string(fields["version"]) == "1" {
			err = os.Remove(l.path)
		} else {
			fields["hooks"], _ = json.Marshal(hooks)
			data, err = json.MarshalIndent(fields, "", "  ")
			if err == nil {
				err = writeHookFile(l.path, data, stat.Mode().Perm())
			}
		}
	}
	if err == nil && l.createdDirectory {
		_ = os.Remove(filepath.Dir(l.path))
	}
	return err
}

// writeHookFile 以临时文件原子替换配置，同时保留指定权限。
func writeHookFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".acp-go-hooks-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
