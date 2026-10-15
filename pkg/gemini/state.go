package gemini

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// profileState 保存只读基础设置与适配器拥有的持久状态。
type profileState struct {
	// mutex 串行化原子文件写入与新凭据合并。
	mutex sync.Mutex
	// root 是明确受管的 .gemini 目录。
	root string
	// settings 是启动时捕获的原始用户配置。
	settings []byte
}

// privateDirectory 拒绝符号链接目标并以私有权限创建目录。
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Gemini state is not a private directory: %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := privateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return os.Mkdir(path, 0700)
}

// newProfileState 固定原始设置，默认状态按账号与工作区区分。
func newProfileState(environment []string, stateDirectory, workspace string) (*profileState, error) {
	home, err := geminiHome(environment)
	if err != nil {
		return nil, err
	}
	// 系统临时目录可能经 /var 链接进入 /private；只规范化已明确选择的账号根。
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	source := filepath.Join(home, ".gemini")
	data, err := os.ReadFile(filepath.Join(source, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		data = []byte("{}")
	} else if err != nil {
		return nil, err
	}
	values := map[string]any{}
	if err := json.Unmarshal(stripSettingsComments(data), &values); err != nil {
		return nil, fmt.Errorf("reading Gemini settings: %w", err)
	}
	if values == nil {
		return nil, errors.New("Gemini settings must be an object")
	}
	data, _ = json.Marshal(values)
	root := source
	if stateDirectory != "" {
		if info, err := os.Lstat(stateDirectory); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("Gemini StateDirectory must not be a symlink")
		}
		root = filepath.Join(stateDirectory, ".gemini")
	} else if nativeacp.EnvironmentValue(environment, "GEMINI_CLI_HOME") == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256([]byte(home + "\x00" + workspace))
		root = filepath.Join(cache, "acp-go", "gemini", fmt.Sprintf("%x", digest[:16]), ".gemini")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// 规范化现有父目录，受管根自身必须不是链接。
	parent := filepath.Dir(root)
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		root = filepath.Join(resolved, filepath.Base(root))
	}
	if err := privateDirectory(root); err != nil {
		return nil, err
	}
	state := &profileState{root: root, settings: data}
	for _, name := range []string{"tmp", "history", "acknowledgments", "acp-go-sessions"} {
		if err := privateDirectory(filepath.Join(root, name)); err != nil {
			return nil, err
		}
	}
	if root != source {
		for _, name := range []string{"oauth_creds.json", "google_accounts.json", "google_credentials.json", "trustedFolders.json"} {
			if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
				continue
			}
			content, err := os.ReadFile(filepath.Join(source, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if err := state.write(name, content); err != nil {
				return nil, err
			}
		}
	}
	return state, nil
}

// write 通过同目录临时文件原子更新状态，拒绝已有链接。
func (state *profileState) write(name string, data []byte) error {
	if !filepath.IsLocal(name) {
		return errors.New("Gemini state path must remain inside owned root")
	}
	info, err := os.Lstat(state.root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe Gemini state root")
	}
	root, err := os.OpenRoot(state.root)
	if err != nil {
		return err
	}
	defer root.Close()
	for current := name; current != "."; current = filepath.Dir(current) {
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && current == name {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe Gemini state symlink")
		}
		if current == name && !info.Mode().IsRegular() {
			return errors.New("Gemini state target is not a regular file")
		}
	}
	temporary := filepath.Join(filepath.Dir(name), ".acp-write-"+rand.Text())
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}

// environment 创建不可变 canonical 覆盖，持久目录与新凭据保留在受管状态内。
func (state *profileState) environment(environment []string, model, reasoning string) (string, []string, error) {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	values := map[string]any{}
	_ = json.Unmarshal(state.settings, &values)
	if model != "" {
		existing, _ := values["model"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		existing["name"] = model
		values["model"] = existing
	}
	if reasoning != "" && reasoning != "default" {
		configs, ok := values["modelConfigs"].(map[string]any)
		if !ok {
			if values["modelConfigs"] != nil {
				return "", nil, errors.New("modelConfigs must be an object")
			}
			configs = map[string]any{}
			values["modelConfigs"] = configs
		}
		overrides, ok := configs["customOverrides"].([]any)
		if !ok && configs["customOverrides"] != nil {
			return "", nil, errors.New("customOverrides must be an array")
		}
		found := false
		for _, preset := range thinkingPresets(model) {
			if preset.id != reasoning {
				continue
			}
			found = true
			for _, thinking := range []any{nil, preset.thinking} {
				overrides = append(overrides, map[string]any{
					"match":       map[string]any{"model": model},
					"modelConfig": map[string]any{"generateContentConfig": map[string]any{"thinkingConfig": thinking}},
				})
			}
		}
		if !found {
			return "", nil, errors.New("thinking preset is not supported by exact model")
		}
		configs["customOverrides"] = overrides
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	directory, err := os.MkdirTemp("", "acp-go-gemini-")
	if err != nil {
		return "", nil, err
	}
	profile := filepath.Join(directory, ".gemini")
	if err := os.Mkdir(profile, 0700); err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, err
	}
	entries, err := os.ReadDir(state.root)
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, err
	}
	for _, entry := range entries {
		if entry.Name() == "settings.json" || strings.HasPrefix(entry.Name(), ".acp-") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			_ = os.RemoveAll(directory)
			return "", nil, fmt.Errorf("unsafe Gemini state symlink: %s", entry.Name())
		}
		if err := os.Symlink(filepath.Join(state.root, entry.Name()), filepath.Join(profile, entry.Name())); err != nil {
			_ = os.RemoveAll(directory)
			return "", nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), data, 0600); err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, err
	}
	return directory, nativeacp.WithEnvironment(slices.Clone(environment), "GEMINI_CLI_HOME", directory), nil
}

// persistOverlay 保存原生原子替换产生的新凭据；不读取或写入原始用户设置。
func (state *profileState) persistOverlay(directory string) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	profile := filepath.Join(directory, ".gemini")
	entries, err := os.ReadDir(profile)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "settings.json" || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected Gemini overlay directory: %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(profile, entry.Name()))
		if err != nil {
			return err
		}
		if err := state.write(entry.Name(), data); err != nil {
			return err
		}
		// 原生 rename 可能替换已有链接；持久化后重新指向共享状态，避免旧子进程关闭时回写过期凭据。
		link := filepath.Join(profile, ".acp-link-"+entry.Name())
		if err := os.Symlink(filepath.Join(state.root, entry.Name()), link); err != nil {
			return err
		}
		if err := os.Rename(link, filepath.Join(profile, entry.Name())); err != nil {
			_ = os.Remove(link)
			return err
		}
	}
	return nil
}

// thinkingEnvironment 为独立配置测试创建默认覆盖。
func thinkingEnvironment(environment []string) (string, []string, error) {
	return thinkingEnvironmentForState(environment, "")
}

// thinkingEnvironmentForState 保留原有内部测试入口，不再生成无效 alias 覆盖。
func thinkingEnvironmentForState(environment []string, stateDirectory string) (string, []string, error) {
	state, err := newProfileState(environment, stateDirectory, "")
	if err != nil {
		return "", nil, err
	}
	return state.environment(environment, "", "default")
}
