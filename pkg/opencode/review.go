package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// officialReviewProviderModule 只允许固定上游使用的官方 AI SDK，不加载任意 npm/file 模块。
func officialReviewProviderModule(value string) bool {
	switch value {
	case "", "@ai-sdk/openai", "@ai-sdk/openai-compatible", "@ai-sdk/anthropic", "@ai-sdk/google",
		"@ai-sdk/azure", "@ai-sdk/amazon-bedrock", "@ai-sdk/google-vertex", "@ai-sdk/groq",
		"@ai-sdk/mistral", "@ai-sdk/xai", "@ai-sdk/deepinfra", "@ai-sdk/cohere",
		"@ai-sdk/cerebras", "@ai-sdk/perplexity", "@ai-sdk/togetherai":
		return true
	default:
		return false
	}
}

// reviewProfilePath 使用原生 XDG 根；只读取原 profile，不修改账号配置。
func reviewProfilePath(environment []string, key, fallback string) (string, error) {
	if value := nativeacp.EnvironmentValue(environment, key); value != "" {
		return filepath.Join(value, "opencode"), nil
	}
	home := nativeacp.EnvironmentValue(environment, "HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(home, fallback, "opencode"), nil
}

// reviewProviders 读取真实来源中的 provider 子树，忽略工具/Hook/MCP/技能配置。
func reviewProviders(config Config, request autoreview.Request) (map[string]any, error) {
	root, err := reviewProfilePath(config.Environment, "XDG_CONFIG_HOME", ".config")
	if err != nil {
		return nil, err
	}
	if err := reviewUnsupportedConfiguration(config, request, root); err != nil {
		return nil, err
	}
	paths := []string{
		filepath.Join(root, "config.json"), filepath.Join(root, "opencode.json"), filepath.Join(root, "opencode.jsonc"),
	}
	if custom := nativeacp.EnvironmentValue(config.Environment, "OPENCODE_CONFIG"); custom != "" {
		paths = append(paths, custom)
	}
	if request.WorkingDirectory != "" {
		paths = append(paths,
			filepath.Join(request.WorkingDirectory, "opencode.json"),
			filepath.Join(request.WorkingDirectory, "opencode.jsonc"),
		)
	}
	providers := map[string]any{}
	// merge 只保留真实 provider 定义，非法 JSON 或模块使审查回退人工。
	merge := func(data []byte) error {
		var values map[string]any
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		if source, ok := values["provider"].(map[string]any); ok {
			mergeReviewProviderObjects(providers, source)
		}
		return nil
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := merge(data); err != nil {
			return nil, fmt.Errorf("review provider configuration cannot be safely read: %w", err)
		}
	}
	if content := nativeacp.EnvironmentValue(config.Environment, "OPENCODE_CONFIG_CONTENT"); content != "" {
		if err := merge([]byte(content)); err != nil {
			return nil, err
		}
	}
	provider, _, ok := strings.Cut(request.Model, "/")
	if !ok || provider == "" {
		return nil, errors.New("review requires a native provider/model identifier")
	}
	result := map[string]any{}
	if entry, exists := providers[provider]; exists {
		value, ok := entry.(map[string]any)
		if !ok {
			return nil, errors.New("review provider must be an object")
		}
		if !reviewProviderModulesSafe(value) {
			return nil, errors.New("review provider requires an executable module outside the official SDK allowlist")
		}
		encoded, err := json.Marshal(value)
		if err != nil || bytes.Contains(encoded, []byte("{file:")) {
			return nil, errors.New("review provider file substitutions require manual approval")
		}
		result[provider] = value
	}
	return result, nil
}

// reviewProviderModulesSafe 同时检查 provider 与模型级 npm 覆盖，防止嵌套模块绕过限制。
func reviewProviderModulesSafe(values map[string]any) bool {
	for key, value := range values {
		if key == "npm" {
			module, ok := value.(string)
			if !ok || !officialReviewProviderModule(module) {
				return false
			}
		}
		if nested, ok := value.(map[string]any); ok && !reviewProviderModulesSafe(nested) {
			return false
		}
	}
	return true
}

// reviewUnsupportedConfiguration 对未实现的原生配置来源保守转人工，避免换成另一账号端点。
func reviewUnsupportedConfiguration(config Config, request autoreview.Request, root string) error {
	if nativeacp.EnvironmentValue(config.Environment, "OPENCODE_MODELS_PATH") != "" ||
		nativeacp.EnvironmentValue(config.Environment, "OPENCODE_MODELS_URL") != "" {
		return errors.New("permission review requires manual approval for external model catalog overrides")
	}
	paths := []string{filepath.Join(root, "config")}
	directories := []string{}
	if directory := nativeacp.EnvironmentValue(config.Environment, "OPENCODE_CONFIG_DIR"); directory != "" {
		directories = append(directories, directory)
	}
	home := nativeacp.EnvironmentValue(config.Environment, "OPENCODE_TEST_HOME")
	if home == "" {
		home = nativeacp.EnvironmentValue(config.Environment, "HOME")
	}
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return err
		}
	}
	directories = append(directories, filepath.Join(home, ".opencode"))
	if request.WorkingDirectory != "" {
		for directory := filepath.Clean(request.WorkingDirectory); ; directory = filepath.Dir(directory) {
			directories = append(directories, filepath.Join(directory, ".opencode"))
			if directory != filepath.Clean(request.WorkingDirectory) {
				paths = append(paths, filepath.Join(directory, "opencode.json"), filepath.Join(directory, "opencode.jsonc"))
			}
			if directory == filepath.Dir(directory) {
				break
			}
		}
	}
	for _, directory := range directories {
		paths = append(paths, filepath.Join(directory, "opencode.json"), filepath.Join(directory, "opencode.jsonc"))
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return errors.New("permission review requires manual approval for additional native provider configuration sources")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// mergeReviewProviderObjects 保留原生深合并的对象字段；不合并任何可执行配置树。
func mergeReviewProviderObjects(target, source map[string]any) {
	for key, value := range source {
		incoming, incomingObject := value.(map[string]any)
		existing, existingObject := target[key].(map[string]any)
		if incomingObject && existingObject {
			mergeReviewProviderObjects(existing, incoming)
		} else {
			target[key] = value
		}
	}
}

// reviewManagedConfiguration 检查受保护配置；不能在无法隔离可执行策略时启动 reviewer。
func reviewManagedConfiguration(environment []string) error {
	directory := nativeacp.EnvironmentValue(environment, "OPENCODE_TEST_MANAGED_CONFIG_DIR")
	if directory == "" {
		switch runtime.GOOS {
		case "darwin":
			directory = "/Library/Application Support/opencode"
		case "windows":
			base := nativeacp.EnvironmentValue(environment, "ProgramData")
			if base == "" {
				base = `C:\ProgramData`
			}
			directory = filepath.Join(base, "opencode")
		default:
			directory = "/etc/opencode"
		}
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err == nil {
			return errors.New("permission review requires manual approval with managed OpenCode configuration")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if runtime.GOOS == "darwin" {
		paths := []string{"/Library/Managed Preferences/ai.opencode.managed.plist"}
		current, err := user.Current()
		if err != nil {
			return err
		}
		paths = append(paths, filepath.Join("/Library/Managed Preferences", current.Username, "ai.opencode.managed.plist"))
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return errors.New("permission review requires manual approval with managed OpenCode preferences")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// reviewEnvironment 准备隔离的 profile 和官方无工具 agent，失败不会启动审查程序。
func reviewEnvironment(config Config, directory string, request autoreview.Request) ([]string, error) {
	if err := reviewAccountConfiguration(config.Environment); err != nil {
		return nil, err
	}
	if err := reviewManagedConfiguration(config.Environment); err != nil {
		return nil, err
	}
	providers, err := reviewProviders(config, request)
	if err != nil {
		return nil, err
	}
	values := map[string]any{
		"provider": providers, "default_agent": "acp-go-reviewer",
		"agent": map[string]any{"acp-go-reviewer": map[string]any{
			"mode": "primary", "prompt": autoreview.SystemPrompt, "permission": map[string]any{"*": "deny"},
		}},
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	environment := nativeacp.WithEnvironment(config.Environment, "OPENCODE_CONFIG_CONTENT", string(data))
	for key, value := range map[string]string{
		"XDG_DATA_HOME": "data", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache",
		"XDG_STATE_HOME": "state", "HOME": "home",
	} {
		path := filepath.Join(directory, value)
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
		environment = nativeacp.WithEnvironment(environment, key, path)
	}
	for key, value := range map[string]string{
		"OPENCODE_CONFIG": "", "OPENCODE_CONFIG_DIR": filepath.Join(directory, "config", "opencode"),
		"OPENCODE_DB":         filepath.Join(directory, "data", "opencode", "review.db"),
		"OPENCODE_PERMISSION": `{"*":"deny"}`, "OPENCODE_PURE": "true",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS": "true", "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS": "true", "OPENCODE_DISABLE_CLAUDE_CODE": "true",
		"OPENCODE_DISABLE_AUTOUPDATE":   "true",
		"OPENCODE_DISABLE_MODELS_FETCH": "true",
	} {
		environment = nativeacp.WithEnvironment(environment, key, value)
	}
	environment = nativeacp.WithEnvironment(environment, "OPENCODE_TEST_HOME", filepath.Join(directory, "home"))
	environment = nativeacp.WithEnvironment(environment, "OPENCODE_AUTO_SHARE", "false")
	source, err := reviewProfilePath(config.Environment, "XDG_DATA_HOME", ".local/share")
	if err != nil {
		return nil, err
	}
	auth := map[string]json.RawMessage{}
	raw, err := os.ReadFile(filepath.Join(source, "auth.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &auth); err != nil {
			return nil, err
		}
	}
	for _, credential := range auth {
		var entry map[string]any
		if err := json.Unmarshal(credential, &entry); err != nil {
			return nil, err
		}
		if entry["type"] == "wellknown" {
			return nil, errors.New("permission review requires manual approval for remote well-known account configuration")
		}
	}
	provider, _, _ := strings.Cut(request.Model, "/")
	selected := map[string]json.RawMessage{}
	if credential := auth[provider]; len(credential) > 0 {
		var entry map[string]any
		if err := json.Unmarshal(credential, &entry); err != nil {
			return nil, err
		}
		if entry["type"] == "api" || entry["type"] == "oauth" {
			selected[provider] = credential
		}
	}
	data, err = json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(directory, "data", "opencode")
	if err := os.MkdirAll(target, 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(target, "auth.json"), data, 0600); err != nil {
		return nil, err
	}
	return environment, nil
}

// permissionReviewer 使用同一 CLI 和所选真实模型执行一次无工具审查，错误由共享门禁审计后转人工。
func permissionReviewer(
	config Config,
	directory string,
) func(context.Context, autoreview.Request) (autoreview.Decision, error) {
	return func(ctx context.Context, request autoreview.Request) (autoreview.Decision, error) {
		work, err := os.MkdirTemp(directory, "review-")
		if err != nil {
			return autoreview.Decision{}, err
		}
		defer os.RemoveAll(work)
		environment, err := reviewEnvironment(config, work, request)
		if err != nil {
			return autoreview.Decision{}, err
		}
		return autoreview.Review(ctx, func(ctx context.Context, input string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			args := append([]string{}, config.PrefixArgs...)
			args = append(args, "run", "--format", "json", "--agent", "acp-go-reviewer", "--model", request.Model)
			command := exec.CommandContext(ctx, config.OpenCodePath, args...)
			command.Env = environment
			command.Dir = work
			command.Stdin = strings.NewReader(input)
			command.Stderr = io.Discard
			command.WaitDelay = time.Second
			output := &limitedReviewOutput{}
			command.Stdout = output
			if err := command.Run(); err != nil {
				return nil, err
			}
			decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
			result := strings.Builder{}
			for {
				var event struct {
					// Type 是官方 --format json 的事件类别。
					Type string `json:"type"`
					// Part 是最终文本块。
					Part struct {
						// Text 是无工具 reviewer 的文本答案。
						Text string `json:"text"`
					} `json:"part"`
				}
				err := decoder.Decode(&event)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return nil, err
				}
				if event.Type == "error" {
					return nil, errors.New("native OpenCode reviewer reported an error")
				}
				if event.Type == "text" {
					result.WriteString(event.Part.Text)
				}
			}
			return []byte(result.String()), nil
		}, request)
	}
}

// limitedReviewOutput 限制原生 JSON 事件流，避免异常 CLI 无限占用内存。
type limitedReviewOutput struct {
	// Buffer 保存有界事件流，不向协议 stdout 写入。
	bytes.Buffer
}

// Write 对事件输出实施硬上限，失败使审批转人工。
func (output *limitedReviewOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 65536 {
		return 0, io.ErrShortBuffer
	}
	return output.Buffer.Write(data)
}
