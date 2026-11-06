package userinput

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

var errEndpointUnavailable = errors.New("managed user input endpoint unavailable")

// Agent 在原生适配器外补充统一问答，模型、审批与执行仍由原适配器拥有。
type Agent struct {
	// Agent 保留标准 ACP 方法和原生能力。
	acp.Agent
	// mutex protects the connection, capabilities, endpoints, and publication state.
	mutex sync.Mutex
	// host 是唯一的宿主交互连接。
	host *acp.AgentSideConnection
	// enabled 表示客户端与 CLI 均能承载受管问答。
	enabled bool
	// closed 阻止关闭后创建受管资源。
	closed bool
	// sessions 按原生会话身份持有问答端点。
	sessions map[acp.SessionId]*endpoint
	// changing marks session operations that have yet to publish or retire an endpoint.
	changing map[acp.SessionId]bool
	// bound 在宿主连接注入后关闭，避免快速初始化越过绑定。
	bound chan struct{}
	// bindOnce 保证连接就绪通知只发送一次。
	bindOnce sync.Once
	// requireReady 要求执行前确认上游已发现受管问答工具。
	requireReady bool
}

// Wrap adds a model-callable input tool when the host supports forms.
func Wrap(agent acp.Agent) *Agent {
	return &Agent{
		Agent: agent, bound: make(chan struct{}),
		sessions: make(map[acp.SessionId]*endpoint),
		changing: make(map[acp.SessionId]bool),
	}
}

// ManagesSessionLifecycle preserves the native adapter's ACP lifecycle ownership.
func (a *Agent) ManagesSessionLifecycle() bool {
	owner, ok := a.Agent.(interface {
		// ManagesSessionLifecycle reports the underlying adapter's SDK opt-in.
		ManagesSessionLifecycle() bool
	})
	return ok && owner.ManagesSessionLifecycle()
}

// SetAgentConnection shares the same SDK connection with the adapter and the input tool.
func (a *Agent) SetAgentConnection(host *acp.AgentSideConnection) {
	host.SetPromptPreparation(a.capturePromptPreparation)
	if binder, ok := a.Agent.(interface {
		// SetAgentConnection 传递唯一宿主连接。
		SetAgentConnection(*acp.AgentSideConnection)
	}); ok {
		binder.SetAgentConnection(host)
	}
	a.mutex.Lock()
	a.host = host
	a.mutex.Unlock()
	a.bindOnce.Do(func() { close(a.bound) })
}

// Initialize enables managed input when standard forms and HTTP MCP are available.
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
	var requireReady bool
	if agent, ok := a.Agent.(interface {
		// UserInputRequiresReady 表示执行前必须验证受管工具已完成发现。
		UserInputRequiresReady() bool
	}); ok {
		requireReady = agent.UserInputRequiresReady()
	}
	a.mutex.Lock()
	a.enabled, a.requireReady = forms, requireReady
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

// attach publishes the endpoint for a successful session operation and closes unused endpoints.
func (a *Agent) attach(ep *endpoint, sid acp.SessionId, err error) error {
	if ep == nil {
		return err
	}
	if err != nil {
		ep.stop()
		return err
	}
	a.mutex.Lock()
	if a.closed || sid == "" {
		a.mutex.Unlock()
		ep.stop()
		return errors.New("session unavailable")
	}
	previous := a.sessions[sid]
	ep.mutex.Lock()
	ep.sid = sid
	ep.mutex.Unlock()
	a.sessions[sid] = ep
	a.mutex.Unlock()
	if previous != nil {
		previous.stop()
	}
	return nil
}

// beginEndpointChange reserves the session through the native change and endpoint publication.
func (a *Agent) beginEndpointChange(sid acp.SessionId) (func(), error) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if !a.enabled && a.sessions[sid] == nil {
		return func() {}, nil
	}
	if a.closed || a.changing[sid] {
		return nil, errEndpointUnavailable
	}
	a.changing[sid] = true
	return func() {
		a.mutex.Lock()
		delete(a.changing, sid)
		a.mutex.Unlock()
	}, nil
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

// LoadSession mounts this process’s input endpoint when restoring session history.
func (a *Agent) LoadSession(ctx context.Context, r acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	loader, ok := a.Agent.(acp.AgentLoader)
	if !ok {
		return acp.LoadSessionResponse{}, acp.NewMethodNotFound("session/load")
	}
	finish, err := a.beginEndpointChange(r.SessionId)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	defer finish()
	servers, ep, err := a.prepare(r.McpServers)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	r.McpServers = servers
	response, err := loader.LoadSession(ctx, r)
	return response, a.attach(ep, r.SessionId, err)
}

// ResumeSession mounts a fresh input endpoint when resuming without replay.
func (a *Agent) ResumeSession(ctx context.Context, r acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	finish, err := a.beginEndpointChange(r.SessionId)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	defer finish()
	servers, ep, err := a.prepare(r.McpServers)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	r.McpServers = servers
	response, err := a.Agent.ResumeSession(ctx, r)
	return response, a.attach(ep, r.SessionId, err)
}

// UnstableForkSession 保留可选原生分叉能力，并为子会话分配独立问答端点。
func (a *Agent) UnstableForkSession(ctx context.Context, r acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	agent, ok := a.Agent.(interface {
		// UnstableForkSession 创建独立原生上下文。
		UnstableForkSession(context.Context, acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error)
	})
	if !ok {
		return acp.UnstableForkSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionFork)
	}
	if acpmeta.ReconcileOnly(r) {
		return agent.UnstableForkSession(ctx, r)
	}
	servers, err := acpmeta.ForkMCPServers(r.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	servers, ep, err := a.prepare(servers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	encoded, err := json.Marshal(servers)
	if err == nil {
		err = json.Unmarshal(encoded, &r.McpServers)
	}
	if err != nil {
		return acp.UnstableForkSessionResponse{}, a.attach(ep, "", err)
	}
	response, err := agent.UnstableForkSession(ctx, r)
	return response, a.attach(ep, response.SessionId, err)
}

// capturePromptPreparation freezes the endpoint and readiness policy at ingress.
func (a *Agent) capturePromptPreparation(r acp.PromptRequest) acp.PromptPreparation {
	sid := r.SessionId
	a.mutex.Lock()
	ep, ready := a.sessions[sid], a.requireReady
	unavailable := a.closed || a.changing[sid]
	a.mutex.Unlock()
	if unavailable {
		return func(context.Context) (func(), error) { return nil, errEndpointUnavailable }
	}
	if ep == nil {
		return nil
	}

	return func(ctx context.Context) (func(), error) {
		if ready {
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case <-ep.ready:
			case <-ep.closed:
				return nil, errEndpointUnavailable
			case <-timer.C:
				return nil, errors.New("managed user input tool was not loaded by CLI")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		a.mutex.Lock()
		defer a.mutex.Unlock()
		if a.closed || a.changing[sid] || a.sessions[sid] != ep {
			return nil, errEndpointUnavailable
		}
		return ep.bindTurn(ctx)
	}
}

// boundAdmission retrieves the operation owner explicitly installed on the connection.
func (a *Agent) boundAdmission() (acp.SessionAdmission, bool) {
	a.mutex.Lock()
	host := a.host
	a.mutex.Unlock()
	if host == nil {
		return acp.SessionAdmission{}, false
	}
	return host.SessionAdmission()
}

// Prompt scopes input to the current operation and forwards the original request.
func (a *Agent) Prompt(ctx context.Context, r acp.PromptRequest) (acp.PromptResponse, error) {
	preparation := a.capturePromptPreparation(r)
	if admission, ok := a.boundAdmission(); ok {
		run, err := admission.Prompt(ctx, r, preparation)
		if err != nil {
			return acp.PromptResponse{}, err
		}
		return run()
	}
	if preparation != nil {
		cleanup, err := preparation(ctx)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return acp.PromptResponse{}, err
		}
	}
	return a.Agent.Prompt(ctx, r)
}

// Cancel interrupts the operation and its associated input wait.
func (a *Agent) Cancel(ctx context.Context, r acp.CancelNotification) error {
	if admission, ok := a.boundAdmission(); ok {
		run, err := admission.Cancel(r)
		if err != nil || run == nil {
			return err
		}
		return run(ctx)
	}
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

// CloseSession releases the local input endpoint and closes the native session.
func (a *Agent) CloseSession(ctx context.Context, r acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	finish, err := a.beginEndpointChange(r.SessionId)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	defer finish()
	a.remove(r.SessionId)
	return a.Agent.CloseSession(ctx, r)
}

// UnstableDeleteSession forwards optional history deletion and releases its input endpoint.
func (a *Agent) UnstableDeleteSession(ctx context.Context, r acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error) {
	agent, ok := a.Agent.(interface {
		// UnstableDeleteSession 只删除适配器拥有的历史。
		UnstableDeleteSession(context.Context, acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error)
	})
	if !ok {
		return acp.UnstableDeleteSessionResponse{}, acp.NewMethodNotFound("session/delete")
	}
	finish, err := a.beginEndpointChange(r.SessionId)
	if err != nil {
		return acp.UnstableDeleteSessionResponse{}, err
	}
	defer finish()
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
