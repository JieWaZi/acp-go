package acpmeta

import (
	"encoding/json"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// modelConfigOptionsKey 在模型选项上携带该模型权威配置目录，避免探测时逐个切换模型。
	modelConfigOptionsKey = "acp-go/model-config-options"
)

// WithModelConfigOptions 复制模型元数据并写入该模型支持的配置；空目录表示确认不支持可选参数。
func WithModelConfigOptions(meta map[string]any, options []acp.SessionConfigOption) map[string]any {
	result := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		result[key] = value
	}
	if options == nil {
		options = []acp.SessionConfigOption{}
	}
	result[modelConfigOptionsKey] = options
	return result
}

// ModelConfigOptions 读取模型权威参数目录；布尔值区分未知与已确认的空目录。
func ModelConfigOptions(meta map[string]any) ([]acp.SessionConfigOption, bool) {
	value, ok := meta[modelConfigOptionsKey]
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var options []acp.SessionConfigOption
	if json.Unmarshal(encoded, &options) != nil {
		return nil, false
	}
	return options, true
}
