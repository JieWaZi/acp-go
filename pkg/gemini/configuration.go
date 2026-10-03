package gemini

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// thinkingPreset 保存官方 GenerateContent 参数，不从模型名推测能力。
type thinkingPreset struct {
	// id 是宿主公开的真实档位或数值预算。
	id string
	// thinking 是只包含一种策略的官方 SDK 配置。
	thinking map[string]any
}

// thinkingPresets 是 2026-10-03 官方 GenerateContent thinking 文档的精确模型能力交集来源。
func thinkingPresets(model string) []thinkingPreset {
	levels := []string{}
	switch model {
	case "gemini-3.1-pro-preview":
		levels = []string{"low", "medium", "high"}
	case "gemini-3-pro-preview":
		levels = []string{"low", "high"}
	case "gemini-3-flash-preview", "gemini-3.5-flash":
		levels = []string{"minimal", "low", "medium", "high"}
	case "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite":
		budgets := []int{-1, 1024, 8192, 24576}
		if model == "gemini-2.5-pro" {
			budgets[len(budgets)-1] = 32768
		} else {
			budgets = append(budgets, 0)
		}
		result := []thinkingPreset{}
		for _, budget := range budgets {
			id := fmt.Sprint(budget)
			if budget == -1 {
				id = "dynamic"
			}
			if budget == 0 {
				id = "off"
			}
			result = append(result, thinkingPreset{
				id: id, thinking: map[string]any{"includeThoughts": true, "thinkingBudget": budget},
			})
		}
		return result
	}
	result := []thinkingPreset{}
	for _, level := range levels {
		result = append(result, thinkingPreset{
			id: level, thinking: map[string]any{"includeThoughts": true, "thinkingLevel": strings.ToUpper(level)},
		})
	}
	return result
}

// supportedThinkingModels 仅列出有精确官方证据的 ID，显示仍受原生 catalog 限制。
func supportedThinkingModels() []string {
	return []string{
		"gemini-3.1-pro-preview", "gemini-3-pro-preview", "gemini-3-flash-preview", "gemini-3.5-flash",
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite",
	}
}

// thinkingAlias 使用稳定命名空间，使持久会话可在新的进程中反向恢复。
func thinkingAlias(model, preset string) string { return "acp-go-thinking/" + model + "/" + preset }

// decodeThinkingAlias 只接受本包确实生成过的精确别名。
func decodeThinkingAlias(value string) (string, string, bool) {
	for _, model := range supportedThinkingModels() {
		for _, preset := range thinkingPresets(model) {
			if value == thinkingAlias(model, preset.id) {
				return model, preset.id, true
			}
		}
	}
	return value, "default", false
}

// geminiHome 使用官方 GEMINI_CLI_HOME 根目录，默认使用当前进程环境的用户目录。
func geminiHome(environment []string) (string, error) {
	if home := nativeacp.EnvironmentValue(environment, "GEMINI_CLI_HOME"); home != "" {
		return filepath.Abs(home)
	}
	if home := nativeacp.EnvironmentValue(environment, "HOME"); home != "" {
		return filepath.Abs(home)
	}
	return os.UserHomeDir()
}

// stripSettingsComments 与官方 strip-json-comments 保持 JSON 字符串中的 URL 和转义字符。
func stripSettingsComments(data []byte) []byte {
	result := slices.Clone(data)
	quoted := false
	for i := 0; i < len(result); i++ {
		if quoted {
			if result[i] == '\\' {
				i++
				continue
			}
			if result[i] == '"' {
				quoted = false
			}
			continue
		}
		if result[i] == '"' {
			quoted = true
			continue
		}
		if result[i] != '/' || i+1 >= len(result) {
			continue
		}
		if result[i+1] == '/' {
			for i < len(result) && result[i] != '\n' {
				result[i] = ' '
				i++
			}
			continue
		}
		if result[i+1] == '*' {
			result[i] = ' '
			i++
			result[i] = ' '
			for i+1 < len(result) {
				i++
				if result[i] == '*' && result[i+1] == '/' {
					result[i] = ' '
					i++
					result[i] = ' '
					break
				}
				if result[i] != '\n' && result[i] != '\r' {
					result[i] = ' '
				}
			}
		}
	}
	return result
}

// thinkingEnvironment 保留系统设置和默认层，只在私有副本加入可追溯 thinking 别名。
func thinkingEnvironment(environment []string) (string, []string, error) {
	return thinkingEnvironmentForState(environment, "")
}

// thinkingEnvironmentForState 将默认 profile 状态写入适配器缓存，显式 home 则使用受管账号目录。
func thinkingEnvironmentForState(environment []string, stateDirectory string) (string, []string, error) {
	home, err := geminiHome(environment)
	if err != nil {
		return "", nil, err
	}
	profile := filepath.Join(home, ".gemini")
	path := filepath.Join(profile, "settings.json")
	values := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	if err == nil {
		if err := json.Unmarshal(stripSettingsComments(data), &values); err != nil {
			return "", nil, fmt.Errorf("reading Gemini user settings: %w", err)
		}
	}
	if values == nil {
		return "", nil, errors.New("Gemini user settings must be a JSON object")
	}
	modelConfigs, ok := values["modelConfigs"].(map[string]any)
	if !ok {
		if values["modelConfigs"] != nil {
			return "", nil, errors.New("Gemini modelConfigs must be an object")
		}
		modelConfigs = map[string]any{}
		values["modelConfigs"] = modelConfigs
	}
	aliases, ok := modelConfigs["customAliases"].(map[string]any)
	if !ok {
		if modelConfigs["customAliases"] != nil {
			return "", nil, errors.New("Gemini customAliases must be an object")
		}
		aliases = map[string]any{}
		modelConfigs["customAliases"] = aliases
	}
	overrides, ok := modelConfigs["customOverrides"].([]any)
	if !ok && modelConfigs["customOverrides"] != nil {
		return "", nil, errors.New("Gemini customOverrides must be an array")
	}
	overrides = slices.Clone(overrides)
	for _, model := range supportedThinkingModels() {
		for _, preset := range thinkingPresets(model) {
			alias := thinkingAlias(model, preset.id)
			if _, exists := aliases[alias]; exists {
				return "", nil, fmt.Errorf("Gemini settings already use reserved alias %q", alias)
			}
			aliases[alias] = map[string]any{"extends": model, "modelConfig": map[string]any{"model": model}}
			// 官方 deepMerge 先用 null 清空旧 budget/level，再用对象覆盖；只对显式选中的别名生效。
			for _, thinking := range []any{nil, preset.thinking} {
				overrides = append(overrides, map[string]any{
					"match":       map[string]any{"model": alias},
					"modelConfig": map[string]any{"generateContentConfig": map[string]any{"thinkingConfig": thinking}},
				})
			}
		}
	}
	modelConfigs["customOverrides"] = overrides
	data, err = json.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	directory, err := os.MkdirTemp("", "acp-go-gemini-")
	if err != nil {
		return "", nil, err
	}
	overlayProfile := filepath.Join(directory, ".gemini")
	if err := os.Mkdir(overlayProfile, 0700); err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, err
	}
	persistent := profile
	if nativeacp.EnvironmentValue(environment, "GEMINI_CLI_HOME") == "" {
		if stateDirectory == "" {
			cache, err := os.UserCacheDir()
			if err != nil {
				_ = os.RemoveAll(directory)
				return "", nil, err
			}
			digest := sha256.Sum256([]byte(home))
			stateDirectory = filepath.Join(cache, "acp-go", "gemini", fmt.Sprintf("%x", digest[:12]))
		}
		persistent = filepath.Join(stateDirectory, ".gemini")
		if err := os.MkdirAll(persistent, 0700); err != nil {
			_ = os.RemoveAll(directory)
			return "", nil, err
		}
		// 默认凭据只读取一次，刷新和原生会话写入适配器拥有的缓存，不回写用户 CLI profile。
		for _, name := range []string{
			"oauth_creds.json", "google_accounts.json", "google_credentials.json", "trustedFolders.json",
		} {
			target := filepath.Join(persistent, name)
			if _, err := os.Stat(target); err == nil {
				continue
			}
			content, err := os.ReadFile(filepath.Join(profile, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				_ = os.RemoveAll(directory)
				return "", nil, err
			}
			if err := os.WriteFile(target, content, 0600); err != nil {
				_ = os.RemoveAll(directory)
				return "", nil, err
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(persistent, "tmp"), 0700); err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, err
	}
	entries, err := os.ReadDir(persistent)
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, err
	}
	for _, entry := range entries {
		if entry.Name() == "settings.json" {
			continue
		}
		source := filepath.Join(persistent, entry.Name())
		target := filepath.Join(overlayProfile, entry.Name())
		if err := os.Symlink(source, target); err != nil {
			_ = os.RemoveAll(directory)
			return "", nil, err
		}
	}
	overlay := filepath.Join(overlayProfile, "settings.json")
	if err := os.WriteFile(overlay, data, 0600); err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, err
	}
	environment = nativeacp.WithEnvironment(environment, "GEMINI_CLI_HOME", directory)
	return directory, environment, nil
}
