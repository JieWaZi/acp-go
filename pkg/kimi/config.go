package kimi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	"github.com/pelletier/go-toml/v2"
)

// isolatedEnvironment 使用 Python Kimi 官方 KIMI_SHARE_DIR 隔离默认模型写入。
// TypeScript Kimi 使用独立的 KIMI_CODE_HOME，不受这个变量影响。
func isolatedEnvironment(config Config) (directory string, environment []string, err error) {
	source := nativeacp.EnvironmentValue(config.Environment, "KIMI_SHARE_DIR")
	if source == "" {
		home := nativeacp.EnvironmentValue(config.Environment, "HOME")
		if home == "" {
			home = nativeacp.EnvironmentValue(config.Environment, "USERPROFILE")
		}
		if home == "" {
			home, err = os.UserHomeDir()
			if err != nil {
				return "", nil, err
			}
		}
		source = filepath.Join(home, ".kimi")
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(config.WorkingDirectory, source)
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", nil, err
	}
	state := config.StateDirectory
	if state == "" {
		cache, cacheErr := os.UserCacheDir()
		if cacheErr != nil {
			return "", nil, cacheErr
		}
		digest := sha256.Sum256([]byte(source + "\x00" + config.WorkingDirectory + "\x00" + config.KimiPath))
		state = filepath.Join(cache, "acp-go", "kimi", hex.EncodeToString(digest[:16]))
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return "", nil, err
	}
	directory, err = os.MkdirTemp("", "acp-go-kimi-")
	if err != nil {
		return "", nil, err
	}
	ownedDirectory := directory
	defer func() {
		if err != nil {
			_ = os.RemoveAll(ownedDirectory)
		}
	}()
	for _, name := range []string{"sessions", "credentials"} {
		owned := filepath.Join(state, name)
		if err = os.MkdirAll(owned, 0700); err != nil {
			return "", nil, err
		}
		if name == "credentials" {
			if err = copyCredentials(filepath.Join(source, name), owned); err != nil {
				return "", nil, err
			}
		}
		if err = os.Symlink(owned, filepath.Join(directory, name)); err != nil {
			return "", nil, err
		}
	}
	values := map[string]any{}
	data, readErr := os.ReadFile(filepath.Join(source, "config.toml"))
	if readErr == nil {
		err = toml.Unmarshal(data, &values)
		if err != nil {
			err = errors.New("invalid Kimi config.toml")
		}
	} else if errors.Is(readErr, fs.ErrNotExist) {
		data, readErr = os.ReadFile(filepath.Join(source, "config.json"))
		if readErr == nil {
			err = json.Unmarshal(data, &values)
			if err != nil {
				err = errors.New("invalid Kimi config.json")
			}
		} else if !errors.Is(readErr, fs.ErrNotExist) {
			err = readErr
		}
	} else {
		err = readErr
	}
	if err != nil {
		return "", nil, err
	}
	if values == nil {
		return "", nil, errors.New("Kimi configuration must be an object")
	}
	values["default_yolo"] = config.PermissionMode == "full-access"
	data, err = toml.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	if err = os.WriteFile(filepath.Join(directory, "config.toml"), data, 0600); err != nil {
		return "", nil, err
	}
	// Session.find 先检查工作目录元数据，再从 sessions/<cwd-hash> 读取原生会话。
	metadata, _ := json.Marshal(map[string]any{"work_dirs": []any{map[string]any{"path": config.WorkingDirectory, "kaos": "local"}}})
	if err = os.WriteFile(filepath.Join(directory, "kimi.json"), metadata, 0600); err != nil {
		return "", nil, err
	}
	if data, readErr = os.ReadFile(filepath.Join(source, "device_id")); readErr == nil {
		err = os.WriteFile(filepath.Join(directory, "device_id"), data, 0600)
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		err = readErr
	}
	if err != nil {
		return "", nil, err
	}
	return directory, nativeacp.WithEnvironment(config.Environment, "KIMI_SHARE_DIR", directory), nil
}

// copyCredentials 只复制官方凭据文件；保留受管目录中更新的刷新结果。
func copyCredentials(source, destination string) error {
	entries, err := os.ReadDir(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(destination, entry.Name())
		if current, err := os.Stat(target); err == nil && !info.ModTime().After(current.ModTime()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return err
		}
	}
	return nil
}
