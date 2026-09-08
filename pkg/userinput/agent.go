package userinput

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Agent 在原生适配器外补充统一问答，模型、审批与执行仍由原适配器拥有。
type Agent struct {
	// Agent 保留标准 ACP 方法和原生能力。
	acp.Agent
	// mutex 保护连接、能力、关闭标志和会话端点。
	mutex sync.Mutex
	// host 是唯一的宿主交互连接。
	host *acp.AgentSideConnection
	// enabled 表示客户端与 CLI 均能承载受管问答。
	enabled bool
	// closed 阻止关闭后创建受管资源。
	closed bool
	// sessions 按原生会话身份持有问答端点。
	sessions map[acp.SessionId]*endpoint
	// bound 在宿主连接注入后关闭，避免快速初始化越过绑定。
	bound chan struct{}
	// bindOnce 保证连接就绪通知只发送一次。
	bindOnce sync.Once
	// requireReady 要求执行前确认上游已发现受管问答工具。
	requireReady bool
}

// Wrap 补充模型可调用的问答工具；未声明表单交互的宿主保持原始行为。
func Wrap(agent acp.Agent) *Agent {
	return &Agent{Agent: agent, bound: make(chan struct{}), sessions: map[acp.SessionId]*endpoint{}}
}

// SetAgentConnection 将同一 SDK 连接注入原适配器与问答端口。
func (a *Agent) SetAgentConnection(host *acp.AgentSideConnection) {
	a.mutex.Lock()
	a.host = host
	a.mutex.Unlock()
	if binder, ok := a.Agent.(interface {
		// SetAgentConnection 传递唯一宿主连接。
		SetAgentConnection(*acp.AgentSideConnection)
	}); ok {
		binder.SetAgentConnection(host)
	}
	a.bindOnce.Do(func() { close(a.bound) })
}

// Initialize 只有标准表单和 HTTP MCP 都可用时启用问答工具。
func (a *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	select {
	case <-a.bound:
	case <-ctx.Done():
		return acp.InitializeResponse{}, ctx.Err()
	}
	response, err := a.Agent.Initialize(ctx, request)
	if err != nil {
		return response, err
	}
	forms := request.ClientCapabilities.Elicitation != nil && request.ClientCapabilities.Elicitation.Form != nil
	if native, ok := a.Agent.(interface {
		// ProvidesUserInput 声明原生问答在所有权限档位可用。
		ProvidesUserInput() bool
	}); ok && native.ProvidesUserInput() {
		forms = false
	}
	if forms && !response.AgentCapabilities.McpCapabilities.Http {
		return response, errors.New("this CLI version cannot provide unified user input: HTTP MCP is required")
	}
	a.mutex.Lock()
	a.enabled = forms
	if agent, ok := a.Agent.(interface {
		// UserInputRequiresReady 表示执行前必须验证受管工具已完成发现。
		UserInputRequiresReady() bool
	}); ok {
		a.requireReady = agent.UserInputRequiresReady()
	}
	a.mutex.Unlock()
	return response, nil
}

// prepare 给本次创建或恢复附加唯一问答服务，拒绝同名覆盖。
func (a *Agent) prepare(servers []acp.McpServer) ([]acp.McpServer, *endpoint, error) {
	a.mutex.Lock()
	enabled, closed, host := a.enabled, a.closed, a.host
	a.mutex.Unlock()
	if closed {
		return nil, nil, errors.New("agent closed")
	}
	if !enabled {
		return servers, nil, nil
	}
	for _, server := range servers {
		if server.Http != nil && server.Http.Name == serverName || server.Sse != nil && server.Sse.Name == serverName || server.Stdio != nil && server.Stdio.Name == serverName {
			return nil, nil, errors.New("reserved MCP user input server name")
		}
	}
	ep, err := newEndpoint(host)
	if err != nil {
		return nil, nil, err
	}
	return append(append([]acp.McpServer{}, servers...), ep.config), ep, nil
}

// attach 绑定创建成功的原生会话；关闭或失败后立即回收临时端点。
func (a *Agent) attach(ep *endpoint, sid acp.SessionId, err error) error {
	if ep == nil {
		return err
	}
	if err != nil {
		ep.stop()
		return err
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if a.closed || sid == "" {
		ep.stop()
		return errors.New("session unavailable")
	}
	if previous := a.sessions[sid]; previous != nil {
		previous.stop()
	}
	ep.mutex.Lock()
	ep.sid = sid
	ep.mutex.Unlock()
	a.sessions[sid] = ep
	return nil
}

// NewSession 在原生会话初始化时提供问答工具。
func (a *Agent) NewSession(ctx context.Context, r acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	servers, ep, err := a.prepare(r.McpServers)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	r.McpServers = servers
	response, err := a.Agent.NewSession(ctx, r)
	return response, a.attach(ep, response.SessionId, err)
}

// LoadSession 恢复历史时重新挂载当前进程的问答端点。
func (a *Agent) LoadSession(ctx context.Context, r acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	loader, ok := a.Agent.(acp.AgentLoader)
	if !ok {
		return acp.LoadSessionResponse{}, acp.NewMethodNotFound("session/load")
	}
	servers, ep, err := a.prepare(r.McpServers)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	r.McpServers = servers
	response, err := loader.LoadSession(ctx, r)
	return response, a.attach(ep, r.SessionId, err)
}

// ResumeSession 无重放恢复也使用新凭据，避免沿用历史端口。
func (a *Agent) ResumeSession(ctx context.Context, r acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	servers, ep, err := a.prepare(r.McpServers)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	r.McpServers = servers
	response, err := a.Agent.ResumeSession(ctx, r)
	return response, a.attach(ep, r.SessionId, err)
}

// Prompt 将问答等待限定在当前执行中，不修改用户消息或原生历史。
func (a *Agent) Prompt(ctx context.Context, r acp.PromptRequest) (acp.PromptResponse, error) {
	a.mutex.Lock()
	ep := a.sessions[r.SessionId]
	ready := a.requireReady
	a.mutex.Unlock()
	if ep == nil {
		return a.Agent.Prompt(ctx, r)
	}
	if ready {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-ep.ready:
		case <-timer.C:
			return acp.PromptResponse{}, errors.New("managed user input tool was not loaded by CLI")
		case <-ctx.Done():
			return acp.PromptResponse{}, ctx.Err()
		}
	}
	ep.mutex.Lock()
	if ep.turn != nil {
		ep.mutex.Unlock()
		return acp.PromptResponse{}, errors.New("session turn already active")
	}
	ep.turn, ep.cancel = context.WithCancel(ctx)
	ep.mutex.Unlock()
	defer ep.endTurn()
	return a.Agent.Prompt(ctx, r)
}

// Cancel 先解除问答等待，再取消原生执行。
func (a *Agent) Cancel(ctx context.Context, r acp.CancelNotification) error {
	a.mutex.Lock()
	ep := a.sessions[r.SessionId]
	a.mutex.Unlock()
	if ep != nil {
		ep.cancelTurn()
	}
	return a.Agent.Cancel(ctx, r)
}

// remove 回收指定会话的受管资源。
func (a *Agent) remove(sid acp.SessionId) {
	a.mutex.Lock()
	ep := a.sessions[sid]
	delete(a.sessions, sid)
	a.mutex.Unlock()
	if ep != nil {
		ep.stop()
	}
}

// CloseSession 释放本机端点与原生会话。
func (a *Agent) CloseSession(ctx context.Context, r acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.remove(r.SessionId)
	return a.Agent.CloseSession(ctx, r)
}

// UnstableDeleteSession 保留原适配器的可选历史删除能力。
func (a *Agent) UnstableDeleteSession(ctx context.Context, r acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error) {
	agent, ok := a.Agent.(interface {
		// UnstableDeleteSession 只删除适配器拥有的历史。
		UnstableDeleteSession(context.Context, acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error)
	})
	if !ok {
		return acp.UnstableDeleteSessionResponse{}, acp.NewMethodNotFound("session/delete")
	}
	response, err := agent.UnstableDeleteSession(ctx, r)
	if err == nil {
		a.remove(r.SessionId)
	}
	return response, err
}

// HandleExtensionMethod 保留原适配器声明的扩展方法。
func (a *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if agent, ok := a.Agent.(acp.ExtensionMethodHandler); ok {
		return agent.HandleExtensionMethod(ctx, method, params)
	}
	return nil, acp.NewMethodNotFound(method)
}

// Close 取消所有问答并回收端口，随后关闭原适配器。
func (a *Agent) Close(ctx context.Context) error {
	a.mutex.Lock()
	a.closed = true
	sessions := a.sessions
	a.sessions = map[acp.SessionId]*endpoint{}
	a.mutex.Unlock()
	for _, ep := range sessions {
		ep.stop()
	}
	if closer, ok := a.Agent.(interface {
		// Close 释放适配器拥有的资源。
		Close(context.Context) error
	}); ok {
		return closer.Close(ctx)
	}
	return nil
}
