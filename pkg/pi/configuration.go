package pi

import (
	"context"
	"errors"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// configuration 对照 pi-acp 从 Pi 实际状态与已认证模型构建选项。
func (s *session) configuration(ctx context.Context) (map[string]any, error) {
	var state, available map[string]any
	if err := s.process.call(ctx, "get_state", nil, &state); err != nil {
		return nil, err
	}
	if err := s.process.call(ctx, "get_available_models", nil, &available); err != nil {
		return nil, err
	}
	thinkingLevels, err := s.availableThinkingLevels(ctx)
	if err != nil {
		return nil, err
	}
	models := []any{}
	for _, entry := range list(available["models"]) {
		m := object(entry)
		provider, id := text(m["provider"]), text(m["id"])
		if provider == "" || id == "" {
			continue
		}
		name := text(m["name"])
		if name == "" {
			name = id
		}
		models = append(models, map[string]any{"value": provider + "/" + id, "name": provider + "/" + name})
	}
	if len(models) == 0 {
		return nil, acp.NewAuthRequired(nil)
	}
	model := object(state["model"])
	current := text(model["provider"]) + "/" + text(model["id"])
	if current == "/" {
		current = text(object(models[0])["value"])
	}
	level := text(state["thinkingLevel"])
	if !contains(thinkingLevels, level) {
		level = thinkingLevels[0]
	}
	thoughts, modes := []any{}, []any{}
	for _, value := range thinkingLevels {
		thoughts = append(thoughts, map[string]any{"value": value, "name": value})
		modes = append(modes, map[string]any{"id": value, "name": "Thinking: " + value})
	}
	configOptions := []any{
		map[string]any{
			"id":           "model",
			"category":     "model",
			"type":         "select",
			"name":         "Model",
			"currentValue": current,
			"options":      models,
		},
		map[string]any{
			"id":           "reasoning",
			"category":     "thought_level",
			"type":         "select",
			"name":         "Thinking",
			"currentValue": level,
			"options":      thoughts,
		},
	}
	return map[string]any{
		"configOptions": configOptions,
		"modes": map[string]any{
			"currentModeId":  level,
			"availableModes": modes,
		},
	}, nil
}

// availableThinkingLevels 从当前模型读取 Pi 权威思考等级，并过滤无效值。
func (s *session) availableThinkingLevels(ctx context.Context) ([]string, error) {
	var result map[string]any
	if err := s.process.call(ctx, "get_available_thinking_levels", nil, &result); err != nil {
		return nil, err
	}
	levels := make([]string, 0, len(list(result["levels"])))
	for _, entry := range list(result["levels"]) {
		if level := text(entry); level != "" && !contains(levels, level) {
			levels = append(levels, level)
		}
	}
	if len(levels) == 0 {
		return nil, errors.New("Pi did not return available thinking levels")
	}
	return levels, nil
}

// contains 判断字符串是否在当前模型返回的有序集合中。
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// setModel 校验模型来自 Pi 实际目录，再按原生 provider/modelId 切换。
func (s *session) setModel(ctx context.Context, value string) error {
	var available map[string]any
	if err := s.process.call(ctx, "get_available_models", nil, &available); err != nil {
		return err
	}
	for _, entry := range list(available["models"]) {
		m := object(entry)
		if text(m["provider"])+"/"+text(m["id"]) == value {
			params := map[string]any{
				"provider": m["provider"],
				"modelId":  m["id"],
			}
			return s.process.call(ctx, "set_model", params, nil)
		}
	}
	return acp.NewInvalidParams(map[string]any{"message": "unknown Pi model"})
}

// setThinking 只接受当前模型实时公布的等级，不允许把权限名发给思考配置。
func (s *session) setThinking(ctx context.Context, value string) error {
	levels, err := s.availableThinkingLevels(ctx)
	if err != nil {
		return err
	}
	if contains(levels, value) {
		params := map[string]any{"level": value}
		return s.process.call(ctx, "set_thinking_level", params, nil)
	}
	return acp.NewInvalidParams(nil)
}

// configurationUpdate 发布权威配置和思考模式变更。
func (a *Agent) configurationUpdate(ctx context.Context, s *session) error {
	options, err := s.configuration(ctx)
	if err != nil {
		return err
	}
	modeUpdate := map[string]any{
		"sessionUpdate": "current_mode_update",
		"currentModeId": object(options["modes"])["currentModeId"],
	}
	if err = a.emit(ctx, s, modeUpdate); err != nil {
		return err
	}
	configUpdate := map[string]any{
		"sessionUpdate": "config_option_update",
		"configOptions": options["configOptions"],
	}
	return a.emit(ctx, s, configUpdate)
}

// SetSessionConfigOption 切换当前会话模型或思考等级，保留其独立 MCP 配置。
func (a *Agent) SetSessionConfigOption(
	ctx context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	s, err := a.get(request.ValueId.SessionId)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if err := s.operation.Lock(ctx); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	defer s.operation.Unlock()
	params, _ := convert[map[string]any](request)
	value := text(params["value"])
	switch text(params["configId"]) {
	case "model":
		err = s.setModel(ctx, value)
	case "reasoning", "thought_level":
		err = s.setThinking(ctx, value)
	default:
		err = acp.NewInvalidParams(nil)
	}
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if err = a.configurationUpdate(ctx, s); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	options, err := s.configuration(ctx)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return convert[acp.SetSessionConfigOptionResponse](map[string]any{"configOptions": options["configOptions"]})
}

// SetSessionMode 保留 pi-acp 将 mode 解释为思考等级的协议语义。
func (a *Agent) SetSessionMode(ctx context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	s, err := a.get(request.SessionId)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	if err := s.operation.Lock(ctx); err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	defer s.operation.Unlock()
	if err = s.setThinking(ctx, string(request.ModeId)); err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return acp.SetSessionModeResponse{}, a.configurationUpdate(ctx, s)
}

// commands 发布 Pi 原生扩展、技能和模板命令，加上 pi-acp 的适配命令。
func (a *Agent) commands(ctx context.Context, s *session) error {
	var result map[string]any
	if err := s.process.call(ctx, "get_commands", nil, &result); err != nil {
		return err
	}
	commands := []any{}
	seen := map[string]bool{}
	for _, name := range []string{"compact", "autocompact", "export", "session", "name", "steering", "follow-up", "changelog"} {
		seen[name] = true
		commands = append(commands, map[string]any{"name": name, "description": "Pi /" + name})
	}
	for _, entry := range list(result["commands"]) {
		command := object(entry)
		name := text(command["name"])
		if command["source"] == "extension" || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		description := text(command["description"])
		if strings.TrimSpace(description) == "" {
			description = name
		}
		commands = append(commands, map[string]any{"name": name, "description": description})
	}
	return a.emit(ctx, s, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": commands})
}
