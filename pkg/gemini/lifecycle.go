package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// cloneNewRequest 固定 MCP 和元数据，防止调用方更改重建输入。
func cloneNewRequest(request acp.NewSessionRequest) (acp.NewSessionRequest, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return request, err
	}
	var clone acp.NewSessionRequest
	if err := json.Unmarshal(data, &clone); err != nil {
		return clone, err
	}
	if resolved, err := filepath.EvalSymlinks(clone.Cwd); err == nil {
		clone.Cwd = resolved
	}
	return clone, nil
}

// NewSession 为每个公共会话创建独立原生进程。
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	request, err := cloneNewRequest(request)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	id, err := publicID()
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	child, err := agent.startChild(ctx, childConfiguration{id: id, reasoning: "default", cwd: request.Cwd})
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = agent.retireChild(context.Background(), child)
		}
	}()
	if err := agent.initializeChild(ctx, child); err != nil {
		return acp.NewSessionResponse{}, err
	}
	response, err := child.agent.NewSession(ctx, request)
	if err != nil {
		return response, err
	}
	record := sessionRecord{
		NativeID: response.SessionId, Cwd: request.Cwd,
		Model: currentModel(response.ConfigOptions), Reasoning: "default",
	}
	if response.Modes != nil {
		record.Mode = response.Modes.CurrentModeId
	}
	if err := agent.profile.persistOverlay(child.directory); err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err := agent.profile.saveRecord(id, record); err != nil {
		return acp.NewSessionResponse{}, err
	}
	session := &ownedSession{child: child, record: record, request: request, options: response.ConfigOptions, modes: response.Modes}
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if agent.closed {
		return acp.NewSessionResponse{}, errors.New("Gemini agent closed")
	}
	agent.sessions[id] = session
	committed = true
	response.SessionId = id
	return response, nil
}

// currentModel 读取来自真实原生目录的当前值。
func currentModel(options []acp.SessionConfigOption) string {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == "model" {
			return string(option.Select.CurrentValue)
		}
	}
	return ""
}

// findSession 查找稳定公共标识，不在持有全局锁时操作进程。
func (agent *Agent) findSession(id acp.SessionId) (*ownedSession, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if agent.closed {
		return nil, errors.New("Gemini agent closed")
	}
	session := agent.sessions[id]
	if session == nil {
		return nil, acp.NewInvalidParams(map[string]any{"message": "unknown Gemini session"})
	}
	return session, nil
}

// sessionPrompt 保存一次已经被公共会话接受的排队请求。
type sessionPrompt struct {
	// ready 在该请求取得 FIFO 执行权时关闭。
	ready chan struct{}
	// cancel 取消已接受但可能尚未进入原生队列的请求。
	cancel context.CancelFunc
}

// Prompt 在公共会话层排队，避免取消与原生队列入队之间的竞争窗口。
func (agent *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	id := request.SessionId
	session, err := agent.findSession(id)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	for {
		session.mutex.Lock()
		if session.closed {
			session.mutex.Unlock()
			return acp.PromptResponse{}, errors.New("Gemini session closed")
		}
		if changing := session.changing; changing != nil {
			session.mutex.Unlock()
			select {
			case <-changing:
				continue
			case <-ctx.Done():
				return acp.PromptResponse{}, ctx.Err()
			}
		}
		turnCtx, cancel := context.WithCancel(ctx)
		prompt := &sessionPrompt{ready: make(chan struct{}), cancel: cancel}
		session.queue = append(session.queue, prompt)
		session.pending++
		if len(session.queue) == 1 {
			close(prompt.ready)
		}
		child := session.child
		request.SessionId = session.record.NativeID
		session.mutex.Unlock()
		defer cancel()
		defer func() {
			session.mutex.Lock()
			defer session.mutex.Unlock()
			session.pending--
			for index, candidate := range session.queue {
				if candidate == prompt {
					session.queue = append(session.queue[:index], session.queue[index+1:]...)
					if index == 0 && len(session.queue) > 0 {
						close(session.queue[0].ready)
					}
					break
				}
			}
		}()
		select {
		case <-prompt.ready:
		case <-turnCtx.Done():
			return acp.PromptResponse{}, turnCtx.Err()
		}
		if err := turnCtx.Err(); err != nil {
			return acp.PromptResponse{}, err
		}
		session.mutex.Lock()
		session.record.HasHistory = true
		record := session.record
		session.mutex.Unlock()
		if err := agent.profile.saveRecord(id, record); err != nil {
			return acp.PromptResponse{}, err
		}
		response, err := child.agent.Prompt(turnCtx, request)
		persistErr := agent.profile.persistOverlay(child.directory)
		return projectUsage(response, errors.Join(err, persistErr))
	}
}

// Cancel 不持有配置锁等待原生通知，保留共享桥的排队代取消语义。
func (agent *Agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	session, err := agent.findSession(request.SessionId)
	if err != nil {
		return err
	}
	session.mutex.Lock()
	for _, prompt := range session.queue {
		prompt.cancel()
	}
	child := session.child
	request.SessionId = session.record.NativeID
	session.mutex.Unlock()
	if child == nil {
		return errors.New("Gemini session unavailable")
	}
	return child.agent.Cancel(ctx, request)
}

// CloseSession 释放会话唯一拥有的原生进程，真实持久历史保留供恢复。
func (agent *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	session, err := agent.findSession(request.SessionId)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	session.mutex.Lock()
	session.closed = true
	for _, prompt := range session.queue {
		prompt.cancel()
	}
	if session.operationCancel != nil {
		session.operationCancel()
	}
	child := session.child
	session.mutex.Unlock()
	if child == nil {
		return acp.CloseSessionResponse{}, nil
	}
	return acp.CloseSessionResponse{}, agent.retireChild(ctx, child)
}

// SetSessionConfigOption 仅在空闲时准备新进程，原生恢复与模型设置均成功后原子替换。
func (agent *Agent) SetSessionConfigOption(
	ctx context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	value := request.ValueId
	session, err := agent.findSession(value.SessionId)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	session.mutex.Lock()
	if session.closed || session.pending > 0 || session.changing != nil {
		session.mutex.Unlock()
		return acp.SetSessionConfigOptionResponse{}, errors.New("Gemini session is busy or closed; configuration requires idle session")
	}
	record := session.record
	switch value.ConfigId {
	case "model":
		if strings.TrimSpace(string(value.Value)) == "" || strings.HasPrefix(string(value.Value), "acp-go-thinking/") {
			session.mutex.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
		}
		record.Model = string(value.Value)
		record.Reasoning = "default"
	case "reasoning":
		valid := false
		for _, option := range session.options {
			if option.Select != nil && option.Select.Id == "reasoning" {
				for _, candidate := range *option.Select.Options.Ungrouped {
					if candidate.Value == value.Value {
						valid = true
					}
				}
			}
		}
		if !valid {
			session.mutex.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
		}
		record.Reasoning = string(value.Value)
	default:
		session.mutex.Unlock()
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	session.changing = make(chan struct{})
	operationCtx, cancel := context.WithCancel(ctx)
	session.operationCancel = cancel
	session.mutex.Unlock()
	defer cancel()
	defer func() {
		session.mutex.Lock()
		defer session.mutex.Unlock()
		close(session.changing)
		session.changing = nil
		session.operationCancel = nil
	}()
	response, err := agent.replace(operationCtx, value.SessionId, session, record, true)
	return acp.SetSessionConfigOptionResponse{ConfigOptions: response.ConfigOptions}, err
}

// replace 保护原始历史并准备执行代，在最后一个可失败步骤完成后交换所有权。
func (agent *Agent) replace(
	ctx context.Context,
	id acp.SessionId,
	session *ownedSession,
	record sessionRecord,
	silent bool,
) (response acp.LoadSessionResponse, err error) {
	if record.Reasoning != "default" {
		if err := agent.checkThinkingPrecedence(record.Cwd); err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	committed := false
	var history map[string][]byte
	if record.HasHistory {
		history, err = agent.profile.historySnapshot(record.NativeID)
		if err != nil {
			return acp.LoadSessionResponse{}, err
		}
		defer func() {
			if !committed {
				if rollbackErr := agent.profile.restoreHistory(record.NativeID, history); rollbackErr != nil {
					err = errors.Join(err, rollbackErr)
				}
			}
		}()
		if err := agent.profile.protectTranscript(record.NativeID); err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	child, err := agent.startChild(ctx, childConfiguration{
		id: id, model: record.Model, reasoning: record.Reasoning, cwd: record.Cwd,
	})
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	defer func() {
		if !committed {
			_ = agent.retireChild(context.Background(), child)
		}
	}()
	if err := agent.initializeChild(ctx, child); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	response = acp.LoadSessionResponse{}
	if record.HasHistory {
		request := acp.LoadSessionRequest{
			SessionId: record.NativeID, Cwd: record.Cwd,
			McpServers: session.request.McpServers, Meta: session.request.Meta,
		}
		if silent {
			response, err = child.agent.LoadSessionConfiguration(ctx, request)
		} else {
			response, err = child.agent.LoadSession(ctx, request)
		}
	} else {
		request := session.request
		request.Cwd = record.Cwd
		created, createErr := child.agent.NewSession(ctx, request)
		err = createErr
		record.NativeID = created.SessionId
		response = acp.LoadSessionResponse{ConfigOptions: created.ConfigOptions, Modes: created.Modes, Meta: created.Meta}
	}
	if err != nil {
		return response, err
	}
	if record.Model == "" {
		record.Model = currentModel(response.ConfigOptions)
	}
	if record.Mode == "" && response.Modes != nil {
		record.Mode = response.Modes.CurrentModeId
	}
	if record.Model != "" {
		changed, err := child.agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: record.NativeID, ConfigId: "model", Value: acp.SessionConfigValueId(record.Model),
			},
		})
		if err != nil {
			return response, err
		}
		response.ConfigOptions = changed.ConfigOptions
	}
	if record.Mode != "" {
		_, err := child.agent.SetSessionMode(ctx, acp.SetSessionModeRequest{
			SessionId: record.NativeID, ModeId: record.Mode,
		})
		if err != nil {
			return response, err
		}
		if response.Modes != nil {
			response.Modes.CurrentModeId = record.Mode
		}
	}
	response.ConfigOptions = withReasoning(response.ConfigOptions, record.Model, record.Reasoning)
	if err := agent.profile.persistOverlay(child.directory); err != nil {
		return response, err
	}
	session.mutex.Lock()
	if session.closed || ctx.Err() != nil {
		session.mutex.Unlock()
		return response, errors.Join(errors.New("Gemini replacement aborted"), ctx.Err())
	}
	if err := agent.profile.saveRecord(id, record); err != nil {
		session.mutex.Unlock()
		return response, err
	}
	previous := session.child
	session.child = child
	session.record = record
	session.options = response.ConfigOptions
	session.modes = response.Modes
	committed = true
	session.mutex.Unlock()
	if previous != nil {
		if err := agent.retireChild(context.Background(), previous); err != nil {
			agent.config.Logger.Warn("Gemini previous child cleanup", "error", err)
		}
	}
	return response, nil
}

// withReasoning 将已提交的真实启动选择投影到会话目录。
func withReasoning(options []acp.SessionConfigOption, model, reasoning string) []acp.SessionConfigOption {
	result := []acp.SessionConfigOption{}
	for _, option := range options {
		if option.Select != nil && option.Select.Id == "reasoning" {
			continue
		}
		result = append(result, option)
	}
	for _, option := range result {
		if option.Select != nil && option.Select.Id == "model" && option.Select.Options.Ungrouped != nil {
			for _, entry := range *option.Select.Options.Ungrouped {
				if string(entry.Value) == model {
					return append(result, reasoningOptions(model, reasoning)...)
				}
			}
		}
	}
	return result
}

// LoadSession 恢复受管公共身份，原生历史可以按协议回放。
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return agent.load(ctx, request, false)
}

// ResumeSession 恢复真实持久历史但抑制历史通知重放。
func (agent *Agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	response, err := agent.load(ctx, acp.LoadSessionRequest{
		SessionId: request.SessionId, Cwd: request.Cwd, McpServers: request.McpServers, Meta: request.Meta,
	}, true)
	return acp.ResumeSessionResponse{ConfigOptions: response.ConfigOptions, Modes: response.Modes, Meta: response.Meta}, err
}

// load 对正在使用的公共身份采用相同空闲替换屏障。
func (agent *Agent) load(ctx context.Context, request acp.LoadSessionRequest, silent bool) (acp.LoadSessionResponse, error) {
	clone, err := cloneNewRequest(acp.NewSessionRequest{Cwd: request.Cwd, McpServers: request.McpServers, Meta: request.Meta})
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	agent.mutex.Lock()
	session := agent.sessions[request.SessionId]
	if agent.closed {
		agent.mutex.Unlock()
		return acp.LoadSessionResponse{}, errors.New("Gemini agent closed")
	}
	if session == nil {
		session = &ownedSession{request: clone}
		agent.sessions[request.SessionId] = session
	}
	agent.mutex.Unlock()
	session.mutex.Lock()
	if session.pending > 0 || session.changing != nil {
		session.mutex.Unlock()
		return acp.LoadSessionResponse{}, errors.New("Gemini session is busy")
	}
	record, err := agent.profile.loadRecord(request.SessionId, clone.Cwd)
	if err != nil {
		session.mutex.Unlock()
		return acp.LoadSessionResponse{}, err
	}
	session.closed = false
	session.changing = make(chan struct{})
	operationCtx, cancel := context.WithCancel(ctx)
	session.operationCancel = cancel
	session.request = clone
	session.mutex.Unlock()
	defer cancel()
	defer func() {
		session.mutex.Lock()
		close(session.changing)
		session.changing = nil
		session.operationCancel = nil
		if session.child == nil {
			session.closed = true
		}
		session.mutex.Unlock()
	}()
	return agent.replace(operationCtx, request.SessionId, session, record, silent)
}

// SetSessionMode 在同一空闲屏障内调用真实官方模式 setter。
func (agent *Agent) SetSessionMode(ctx context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	id := request.SessionId
	session, err := agent.findSession(id)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	session.mutex.Lock()
	if session.closed || session.pending > 0 || session.changing != nil {
		session.mutex.Unlock()
		return acp.SetSessionModeResponse{}, errors.New("Gemini session is busy or closed")
	}
	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session.operationCancel = cancel
	session.changing = make(chan struct{})
	child := session.child
	request.SessionId = session.record.NativeID
	session.mutex.Unlock()
	defer func() {
		session.mutex.Lock()
		close(session.changing)
		session.changing = nil
		session.operationCancel = nil
		session.mutex.Unlock()
	}()
	response, err := child.agent.SetSessionMode(operationCtx, request)
	if err != nil {
		return response, err
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.closed {
		return response, errors.New("Gemini session closed")
	}
	session.record.Mode = request.ModeId
	return response, agent.profile.saveRecord(id, session.record)
}

// CallNative 将显式带公共会话的扩展定向到正确子进程。
func (agent *Agent) CallNative(ctx context.Context, method string, request any) (json.RawMessage, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	_ = json.Unmarshal(data, &fields)
	var id acp.SessionId
	_ = json.Unmarshal(fields["sessionId"], &id)
	if id == "" {
		return agent.Agent.CallNative(ctx, method, request)
	}
	session, err := agent.findSession(id)
	if err != nil {
		return nil, err
	}
	session.mutex.Lock()
	child := session.child
	nativeID := session.record.NativeID
	closed := session.closed
	session.mutex.Unlock()
	if closed || child == nil {
		return nil, errors.New("Gemini session closed")
	}
	fields["sessionId"], _ = json.Marshal(nativeID)
	return child.agent.CallNative(ctx, method, fields)
}

// HandleExtensionMethod 使用同一公共到原生标识路由，不声明官方未实现的 steering。
func (agent *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return agent.CallNative(ctx, method, params)
}

// LoadSessionConfiguration 保留既有静默恢复入口并使用公共会话协调器。
func (agent *Agent) LoadSessionConfiguration(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return agent.load(ctx, request, true)
}
