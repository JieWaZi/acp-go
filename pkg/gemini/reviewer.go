package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// systemSettingsFiles 列出官方系统设置与默认层；不尝试绕过其 root 所有权规则。
func systemSettingsFiles(environment []string) []string {
	directory := "/etc/gemini-cli"
	if runtime.GOOS == "darwin" {
		directory = "/Library/Application Support/GeminiCli"
	}
	if runtime.GOOS == "windows" {
		directory = `C:\ProgramData\gemini-cli`
	}
	settings := nativeacp.EnvironmentValue(environment, "GEMINI_CLI_SYSTEM_SETTINGS_PATH")
	if settings == "" {
		settings = filepath.Join(directory, "settings.json")
	}
	defaults := nativeacp.EnvironmentValue(environment, "GEMINI_CLI_SYSTEM_DEFAULTS_PATH")
	if defaults == "" {
		defaults = filepath.Join(filepath.Dir(settings), "system-defaults.json")
	}
	files := []string{settings, defaults}
	return files
}

// settingsObject 读取存在的 JSONC 设置，任何读取或解析失败都保守返回错误。
func settingsObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	value := map[string]any{}
	if err := json.Unmarshal(stripSettingsComments(data), &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("Gemini settings must be an object")
	}
	return value, nil
}

// checkThinkingPrecedence 在更高优先级定义模型配置时拒绝不可兑现的选择。
func (agent *Agent) checkThinkingPrecedence(cwd string) error {
	files := append(systemSettingsFiles(agent.config.Environment), filepath.Join(cwd, ".gemini", "settings.json"))
	for _, path := range files {
		values, err := settingsObject(path)
		if err != nil {
			return err
		}
		if values["modelConfigs"] != nil {
			return errors.New("Gemini higher-priority modelConfigs prevent verified thinking override")
		}
	}
	return nil
}

// cappedOutput 限制原生审查器输出，避免不可信 stdout 无限增长。
type cappedOutput struct {
	// Buffer 保存有界输出。
	bytes.Buffer
}

// Write 超过约定上限后中止读取。
func (output *cappedOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 65536 {
		return 0, errors.New("Gemini reviewer output exceeds limit")
	}
	return output.Buffer.Write(data)
}

// permissionReviewer 使用同一个官方 CLI 的零工具 headless 路由执行风险分类。
func (agent *Agent) permissionReviewer(ctx context.Context, request autoreview.Request) (autoreview.Decision, error) {
	restricted := nativeacp.EnvironmentValue(agent.config.Environment, "GEMINI_RESTRICTED_MODE") == "true"
	untrusted := nativeacp.EnvironmentValue(agent.config.Environment, "GEMINI_CLI_TRUST_WORKSPACE") == "false"
	if restricted || untrusted {
		return autoreview.Decision{}, errors.New("Gemini explicit restricted policy requires manual review")
	}

	for _, path := range systemSettingsFiles(agent.config.Environment) {
		values, err := settingsObject(path)
		if err != nil {
			return autoreview.Decision{}, err
		}
		if len(values) != 0 {
			return autoreview.Decision{}, errors.New("Gemini system policy requires manual permission review")
		}
	}
	if request.Model == "" {
		return autoreview.Decision{}, errors.New("Gemini reviewer requires exact current model")
	}
	directory, err := os.MkdirTemp("", "acp-go-gemini-review-")
	if err != nil {
		return autoreview.Decision{}, err
	}
	defer os.RemoveAll(directory)
	home := filepath.Join(directory, "home")
	profile := filepath.Join(home, ".gemini")
	work := filepath.Join(directory, "work")
	if err := os.MkdirAll(profile, 0700); err != nil {
		return autoreview.Decision{}, err
	}
	if err := os.Mkdir(work, 0700); err != nil {
		return autoreview.Decision{}, err
	}
	settings := map[string]any{
		"tools":       map[string]any{"core": []string{}},
		"hooksConfig": map[string]any{"enabled": false},
		"admin": map[string]any{
			"mcp":        map[string]any{"enabled": false},
			"extensions": map[string]any{"enabled": false},
			"skills":     map[string]any{"enabled": false},
		},
		"telemetry": map[string]any{"enabled": false},
	}
	original := map[string]any{}
	_ = json.Unmarshal(agent.profile.settings, &original)
	if security, ok := original["security"].(map[string]any); ok {
		if auth, ok := security["auth"].(map[string]any); ok {
			settings["security"] = map[string]any{"auth": auth}
		}
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), data, 0600); err != nil {
		return autoreview.Decision{}, err
	}
	snapshots := map[string][]byte{}
	agent.profile.mutex.Lock()
	for _, name := range []string{"oauth_creds.json", "google_accounts.json", "google_credentials.json"} {
		data, readErr := os.ReadFile(filepath.Join(agent.profile.root, name))
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			err = readErr
			break
		}
		snapshots[name] = data
		if writeErr := os.WriteFile(filepath.Join(profile, name), data, 0600); writeErr != nil {
			err = writeErr
			break
		}
	}
	agent.profile.mutex.Unlock()
	if err != nil {
		return autoreview.Decision{}, err
	}
	environment := nativeacp.WithEnvironment(agent.config.Environment, "HOME", home)
	environment = nativeacp.WithEnvironment(environment, "GEMINI_CLI_HOME", home)
	trustedPath := filepath.Join(profile, "trustedFolders.json")
	realWork, err := filepath.EvalSymlinks(work)
	if err != nil {
		return autoreview.Decision{}, err
	}
	trusted, _ := json.Marshal(map[string]string{realWork: "TRUST_FOLDER"})
	if err := os.WriteFile(trustedPath, trusted, 0600); err != nil {
		return autoreview.Decision{}, err
	}
	environment = nativeacp.WithEnvironment(environment, "GEMINI_CLI_TRUSTED_FOLDERS_PATH", trustedPath)
	decision, reviewErr := autoreview.Review(ctx, func(ctx context.Context, input string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		args := append(
			slices.Clone(agent.config.PrefixArgs),
			"--prompt", autoreview.SystemPrompt+"\nEvidence:\n"+input,
			"--output-format", "json", "--model", request.Model,
		)
		command := exec.CommandContext(ctx, agent.config.GeminiPath, args...)
		command.Env = environment
		command.Dir = work
		command.Stdin = bytes.NewReader(nil)
		command.Stderr = io.Discard
		command.WaitDelay = time.Second
		var output cappedOutput
		command.Stdout = &output
		if err := command.Run(); err != nil {
			return nil, err
		}
		var result struct {
			// Response 是模型文本结果。
			Response string `json:"response"`
			// Stats 包含真实调用模型的统计键。
			Stats struct {
				// Models 只用于确认没有发生模型提升或回退。
				Models map[string]json.RawMessage `json:"models"`
			} `json:"stats"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			return nil, err
		}
		if len(result.Stats.Models) != 1 || result.Stats.Models[request.Model] == nil {
			return nil, errors.New("Gemini reviewer did not confirm exact requested model")
		}
		return []byte(result.Response), nil
	}, request)
	return decision, errors.Join(reviewErr, agent.profile.persistReviewerCredentials(profile, snapshots))
}

// persistReviewerCredentials 用比较后替换保留审查器刷新，避免覆盖其他会话更新的凭据。
func (state *profileState) persistReviewerCredentials(profile string, snapshots map[string][]byte) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	for name, before := range snapshots {
		path := filepath.Join(profile, name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unsafe reviewer credential file")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Equal(before, after) {
			continue
		}
		target := filepath.Join(state.root, name)
		info, err = os.Lstat(target)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unsafe persistent credential file")
		}
		current, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if bytes.Equal(current, before) {
			if err := state.write(name, after); err != nil {
				return err
			}
		}
	}
	return nil
}
