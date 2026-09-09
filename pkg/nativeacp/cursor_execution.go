package nativeacp

import (
	"context"
	"errors"
	"sort"

	acp "github.com/coder/acp-go-sdk"
)

// LoadCursorSessionConfiguration 恢复官方配置而不回放历史，用于交互驱动的标准 Resume。
func (agent *Agent) LoadCursorSessionConfiguration(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	agent.mutex.Lock()
	if agent.silentLoads == nil {
		agent.silentLoads = map[acp.SessionId]bool{}
	}
	agent.silentLoads[request.SessionId] = true
	agent.mutex.Unlock()
	defer func() { agent.mutex.Lock(); delete(agent.silentLoads, request.SessionId); agent.mutex.Unlock() }()
	return agent.LoadSession(ctx, request)
}

// CursorModelSelection 保存官方配置文件可直接接受的模型选择。
type CursorModelSelection struct {
	// ModelID 是官方模型目录中的参数式模型标识。
	ModelID string `json:"modelId"`
	// Parameters 是已经得到原生回执的实际参数。
	Parameters []CursorModelParameter `json:"parameters"`
}

// CursorModelParameter 保留厂商参数身份与原始值。
type CursorModelParameter struct {
	// ID 是官方参数名称。
	ID string `json:"id"`
	// Value 是官方目录中的参数值。
	Value string `json:"value"`
}

// CursorExecutionModel 返回已获原生回执的完整模型参数，供同一适配器的交互执行使用。
func (agent *Agent) CursorExecutionModel(id acp.SessionId) (CursorModelSelection, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	state := agent.sessions[id]
	if !agent.config.CursorExtensions || state == nil {
		return CursorModelSelection{}, errors.New("Cursor session configuration unavailable")
	}
	model := ""
	parameters := []CursorModelParameter{}
	for _, option := range state.cursorNativeOptions {
		if option.Select == nil {
			continue
		}
		selected := option.Select
		if selected.Id == "model" || selected.Category != nil && *selected.Category == acp.SessionConfigOptionCategoryModel {
			model = string(selected.CurrentValue)
			continue
		}
		if selected.CurrentValue == "" {
			continue
		}
		if selected.Category == nil || (*selected.Category != acp.SessionConfigOptionCategoryThoughtLevel && string(*selected.Category) != "model_config") {
			continue
		}
		parameters = append(parameters, CursorModelParameter{ID: string(selected.Id), Value: string(selected.CurrentValue)})
	}
	if model == "" {
		return CursorModelSelection{}, errors.New("Cursor did not confirm a current model")
	}
	sort.Slice(parameters, func(i, j int) bool { return parameters[i].ID < parameters[j].ID })
	return CursorModelSelection{ModelID: model, Parameters: parameters}, nil
}
