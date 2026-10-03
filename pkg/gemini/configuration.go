package gemini

import (
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
	case "gemini-3.1-pro-preview", "gemini-3.8-flash":
		levels = []string{"low", "medium", "high"}
	case "gemini-3-pro-preview":
		levels = []string{"low", "high"}
	case "gemini-3-flash-preview":
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
