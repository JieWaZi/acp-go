package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidEnvelope 表示协议 envelope 缺少必需字段、方法未知或方法与参数不匹配。
var ErrInvalidEnvelope = errors.New("invalid Codex protocol envelope")

// V1 客户端请求、服务端审批和通知方法常量集中维护，runtime 与 events 不再散落 wire 字符串。
const (
	MethodInitialize                      = "initialize"
	MethodThreadStart                     = "thread/start"
	MethodThreadResume                    = "thread/resume"
	MethodThreadRead                      = "thread/read"
	MethodThreadUnsubscribe               = "thread/unsubscribe"
	MethodTurnStart                       = "turn/start"
	MethodTurnSteer                       = "turn/steer"
	MethodTurnInterrupt                   = "turn/interrupt"
	MethodModelList                       = "model/list"
	MethodConfigRead                      = "config/read"
	MethodSkillsExtraRootsSet             = "skills/extraRoots/set"
	MethodSkillsList                      = "skills/list"
	MethodAccountRead                     = "account/read"
	MethodAccountLoginStart               = "account/login/start"
	MethodAccountLoginCancel              = "account/login/cancel"
	MethodAccountLogout                   = "account/logout"
	MethodInitialized                     = "initialized"
	MethodCommandExecutionRequestApproval = "item/commandExecution/requestApproval"
	MethodFileChangeRequestApproval       = "item/fileChange/requestApproval"
	MethodPermissionsRequestApproval      = "item/permissions/requestApproval"
	MethodMCPServerElicitationRequest     = "mcpServer/elicitation/request"
	MethodToolRequestUserInput            = "item/tool/requestUserInput"
	MethodError                           = "error"
	MethodTurnStarted                     = "turn/started"
	MethodTurnCompleted                   = "turn/completed"
	MethodTurnDiffUpdated                 = "turn/diff/updated"
	MethodItemStarted                     = "item/started"
	MethodItemCompleted                   = "item/completed"
	MethodAgentMessageDelta               = "item/agentMessage/delta"
	MethodReasoningSummaryTextDelta       = "item/reasoning/summaryTextDelta"
	MethodReasoningSummaryPartAdded       = "item/reasoning/summaryPartAdded"
	MethodReasoningTextDelta              = "item/reasoning/textDelta"
	MethodTurnPlanUpdated                 = "turn/plan/updated"
	MethodThreadTokenUsageUpdated         = "thread/tokenUsage/updated"
	MethodCommandExecutionOutputDelta     = "item/commandExecution/outputDelta"
	MethodTerminalInteraction             = "item/commandExecution/terminalInteraction"
	MethodMCPToolCallProgress             = "item/mcpToolCall/progress"
	MethodMCPServerStartupStatusUpdated   = "mcpServer/startupStatus/updated"
	MethodFileChangePatchUpdated          = "item/fileChange/patchUpdated"
	MethodServerRequestResolved           = "serverRequest/resolved"
	MethodThreadCompacted                 = "thread/compacted"
	MethodModelRerouted                   = "model/rerouted"
	MethodWarning                         = "warning"
	MethodAccountLoginCompleted           = "account/login/completed"
	// MethodAccountUpdated 仅作为 logout 完成屏障；V1 不消费其账号 payload。
	MethodAccountUpdated = "account/updated"
)

// ClientRequest 是 V1 客户端请求的封闭变体集合；每个具体类型固定唯一 method 和 Params 类型。
type ClientRequest interface {
	// Marshaler 把封闭请求变体编码为 Codex app-server wire 对象。
	json.Marshaler
	// Method 返回与 Params 类型固定绑定的方法名。
	Method() string
	// isClientRequest 阻止包外实现绕过封闭变体集合。
	isClientRequest()
}

// clientRequestEnvelope 实现所有有参数和无参数客户端请求共享的 wire 行为。
type clientRequestEnvelope[P any] struct {
	// ID 是 JSON-RPC 请求标识，支持 Codex 定义的整数或字符串。
	ID RequestID
	// Params 是与固定 method 对应的具名参数类型。
	Params P
	// method 由公开构造函数或受控解码器固定，调用方不能改配其他 Params。
	method string
	// includeParams 区分普通请求与不携带 params 的请求。
	includeParams bool
}

// Method 返回该变体固定的 Codex app-server 方法名。
func (r clientRequestEnvelope[P]) Method() string {
	return r.method
}

// isClientRequest 封闭 ClientRequest 变体，防止包外伪造未校验的方法组合。
func (r clientRequestEnvelope[P]) isClientRequest() {}

// MarshalJSON 只从变体内置常量产生 method，避免序列化无效 method/params 配对。
func (r clientRequestEnvelope[P]) MarshalJSON() ([]byte, error) {
	if r.method == "" {
		return nil, fmt.Errorf("%w: client request has no method", ErrInvalidEnvelope)
	}
	if !r.includeParams {
		return json.Marshal(struct {
			// ID 使用指针以显式触发生成的 RequestID union marshaler。
			ID *RequestID `json:"id"`
			// Method 是该具体变体固定的方法名。
			Method string `json:"method"`
		}{ID: &r.ID, Method: r.method})
	}
	return json.Marshal(struct {
		// ID 使用指针以显式触发生成的 RequestID union marshaler。
		ID *RequestID `json:"id"`
		// Method 是该具体变体固定的方法名。
		Method string `json:"method"`
		// Params 是与 Method 静态绑定的具名参数。
		Params P `json:"params"`
	}{ID: &r.ID, Method: r.method, Params: r.Params})
}

// newClientRequest 创建一个携带强类型 Params 的固定方法请求。
func newClientRequest[P any](id RequestID, method string, params P) clientRequestEnvelope[P] {
	return clientRequestEnvelope[P]{ID: id, Params: params, method: method, includeParams: true}
}

// newClientRequestWithoutParams 创建不携带 params 的固定方法请求。
func newClientRequestWithoutParams[P any](id RequestID, method string) clientRequestEnvelope[P] {
	return clientRequestEnvelope[P]{ID: id, method: method}
}

// InitializeRequest 表示固定 method 为 initialize 的请求。
type InitializeRequest struct {
	// clientRequestEnvelope 提供固定方法和 InitializeParams 的耦合。
	clientRequestEnvelope[InitializeParams]
}

// NewInitializeRequest 创建 initialize 请求。
func NewInitializeRequest(id RequestID, params InitializeParams) InitializeRequest {
	return InitializeRequest{newClientRequest(id, MethodInitialize, params)}
}

// ThreadStartRequest 表示固定 method 为 thread/start 的请求。
type ThreadStartRequest struct {
	// clientRequestEnvelope 提供固定方法和 ThreadStartParams 的耦合。
	clientRequestEnvelope[ThreadStartParams]
}

// NewThreadStartRequest 创建 thread/start 请求。
func NewThreadStartRequest(id RequestID, params ThreadStartParams) ThreadStartRequest {
	return ThreadStartRequest{newClientRequest(id, MethodThreadStart, params)}
}

// ThreadResumeRequest 表示固定 method 为 thread/resume 的请求。
type ThreadResumeRequest struct {
	// clientRequestEnvelope 提供固定方法和 ThreadResumeParams 的耦合。
	clientRequestEnvelope[ThreadResumeParams]
}

// NewThreadResumeRequest 创建 thread/resume 请求。
func NewThreadResumeRequest(id RequestID, params ThreadResumeParams) ThreadResumeRequest {
	return ThreadResumeRequest{newClientRequest(id, MethodThreadResume, params)}
}

// ThreadReadRequest 表示固定 method 为 thread/read 的请求。
type ThreadReadRequest struct {
	// clientRequestEnvelope 提供固定方法和 ThreadReadParams 的耦合。
	clientRequestEnvelope[ThreadReadParams]
}

// NewThreadReadRequest 创建 thread/read 请求。
func NewThreadReadRequest(id RequestID, params ThreadReadParams) ThreadReadRequest {
	return ThreadReadRequest{newClientRequest(id, MethodThreadRead, params)}
}

// ThreadUnsubscribeRequest 表示固定 method 为 thread/unsubscribe 的请求。
type ThreadUnsubscribeRequest struct {
	// clientRequestEnvelope 提供固定方法和 ThreadUnsubscribeParams 的耦合。
	clientRequestEnvelope[ThreadUnsubscribeParams]
}

// NewThreadUnsubscribeRequest 创建 thread/unsubscribe 请求。
func NewThreadUnsubscribeRequest(id RequestID, params ThreadUnsubscribeParams) ThreadUnsubscribeRequest {
	return ThreadUnsubscribeRequest{newClientRequest(id, MethodThreadUnsubscribe, params)}
}

// TurnStartRequest 表示固定 method 为 turn/start 的请求。
type TurnStartRequest struct {
	// clientRequestEnvelope 提供固定方法和 TurnStartParams 的耦合。
	clientRequestEnvelope[TurnStartParams]
}

// NewTurnStartRequest 创建 turn/start 请求。
func NewTurnStartRequest(id RequestID, params TurnStartParams) TurnStartRequest {
	return TurnStartRequest{newClientRequest(id, MethodTurnStart, params)}
}

// TurnSteerRequest 表示固定 method 为 turn/steer 的请求。
type TurnSteerRequest struct {
	// clientRequestEnvelope 提供固定方法和 TurnSteerParams 的耦合。
	clientRequestEnvelope[TurnSteerParams]
}

// NewTurnSteerRequest 创建 turn/steer 请求。
func NewTurnSteerRequest(id RequestID, params TurnSteerParams) TurnSteerRequest {
	return TurnSteerRequest{newClientRequest(id, MethodTurnSteer, params)}
}

// TurnInterruptRequest 表示固定 method 为 turn/interrupt 的请求。
type TurnInterruptRequest struct {
	// clientRequestEnvelope 提供固定方法和 TurnInterruptParams 的耦合。
	clientRequestEnvelope[TurnInterruptParams]
}

// NewTurnInterruptRequest 创建 turn/interrupt 请求。
func NewTurnInterruptRequest(id RequestID, params TurnInterruptParams) TurnInterruptRequest {
	return TurnInterruptRequest{newClientRequest(id, MethodTurnInterrupt, params)}
}

// ModelListRequest 表示固定 method 为 model/list 的请求。
type ModelListRequest struct {
	// clientRequestEnvelope 提供固定方法和 ModelListParams 的耦合。
	clientRequestEnvelope[ModelListParams]
}

// NewModelListRequest 创建 model/list 请求。
func NewModelListRequest(id RequestID, params ModelListParams) ModelListRequest {
	return ModelListRequest{newClientRequest(id, MethodModelList, params)}
}

// ConfigReadRequest 表示固定 method 为 config/read 的请求。
type ConfigReadRequest struct {
	// clientRequestEnvelope 提供固定方法和 ConfigReadParams 的耦合。
	clientRequestEnvelope[ConfigReadParams]
}

// NewConfigReadRequest 创建 config/read 请求。
func NewConfigReadRequest(id RequestID, params ConfigReadParams) ConfigReadRequest {
	return ConfigReadRequest{newClientRequest(id, MethodConfigRead, params)}
}

// SkillsExtraRootsSetRequest 表示固定 method 为 skills/extraRoots/set 的请求。
type SkillsExtraRootsSetRequest struct {
	// clientRequestEnvelope 提供固定方法和 SkillsExtraRootsSetParams 的耦合。
	clientRequestEnvelope[SkillsExtraRootsSetParams]
}

// NewSkillsExtraRootsSetRequest 创建 skills/extraRoots/set 请求。
func NewSkillsExtraRootsSetRequest(
	id RequestID,
	params SkillsExtraRootsSetParams,
) SkillsExtraRootsSetRequest {
	return SkillsExtraRootsSetRequest{
		newClientRequest(id, MethodSkillsExtraRootsSet, params),
	}
}

// SkillsListRequest 表示固定 method 为 skills/list 的请求。
type SkillsListRequest struct {
	// clientRequestEnvelope 提供固定方法和 SkillsListParams 的耦合。
	clientRequestEnvelope[SkillsListParams]
}

// NewSkillsListRequest 创建 skills/list 请求。
func NewSkillsListRequest(id RequestID, params SkillsListParams) SkillsListRequest {
	return SkillsListRequest{newClientRequest(id, MethodSkillsList, params)}
}

// AccountReadRequest 表示固定 method 为 account/read 的请求。
type AccountReadRequest struct {
	// clientRequestEnvelope 提供固定方法和 GetAccountParams 的耦合。
	clientRequestEnvelope[GetAccountParams]
}

// NewAccountReadRequest 创建 account/read 请求。
func NewAccountReadRequest(id RequestID, params GetAccountParams) AccountReadRequest {
	return AccountReadRequest{newClientRequest(id, MethodAccountRead, params)}
}

// AccountLoginStartRequest 表示固定 method 为 account/login/start 的请求。
type AccountLoginStartRequest struct {
	// clientRequestEnvelope 提供固定方法和 LoginAccountParams 的耦合。
	clientRequestEnvelope[LoginAccountParams]
}

// NewAccountLoginStartRequest 创建 account/login/start 请求。
func NewAccountLoginStartRequest(id RequestID, params LoginAccountParams) AccountLoginStartRequest {
	return AccountLoginStartRequest{newClientRequest(id, MethodAccountLoginStart, params)}
}

// AccountLoginCancelRequest 表示固定 method 为 account/login/cancel 的请求。
type AccountLoginCancelRequest struct {
	// clientRequestEnvelope 提供固定方法和 CancelLoginAccountParams 的耦合。
	clientRequestEnvelope[CancelLoginAccountParams]
}

// NewAccountLoginCancelRequest 创建 account/login/cancel 请求。
func NewAccountLoginCancelRequest(id RequestID, params CancelLoginAccountParams) AccountLoginCancelRequest {
	return AccountLoginCancelRequest{newClientRequest(id, MethodAccountLoginCancel, params)}
}

// AccountLogoutRequest 表示固定 method 为 account/logout 且不携带 params 的请求。
type AccountLogoutRequest struct {
	// clientRequestEnvelope 提供固定方法与无参数语义。
	clientRequestEnvelope[struct{}]
}

// NewAccountLogoutRequest 创建 account/logout 请求。
func NewAccountLogoutRequest(id RequestID) AccountLogoutRequest {
	return AccountLogoutRequest{newClientRequestWithoutParams[struct{}](id, MethodAccountLogout)}
}

// requestWire 是解码阶段使用的最小请求 envelope，不把多种 Params 合并到同一结构。
type requestWire struct {
	// ID 暂存请求标识，后续按 Codex RequestID union 解码。
	ID json.RawMessage `json:"id"`
	// Method 选择唯一的 V1 具体请求类型。
	Method string `json:"method"`
	// Params 保持原始 JSON，选中 method 后才解码到对应具名类型。
	Params json.RawMessage `json:"params"`
}

// DecodeClientRequest 先读取 method，再只解码其绑定的 Params 类型。
func DecodeClientRequest(data []byte) (ClientRequest, error) {
	var wire requestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("%w: decoding client request: %w", ErrInvalidEnvelope, err)
	}
	switch wire.Method {
	case MethodInitialize:
		request, err := decodeClientRequest[InitializeParams](wire, true)
		return clientRequestResult(&InitializeRequest{request}, err)
	case MethodThreadStart:
		request, err := decodeClientRequest[ThreadStartParams](wire, true)
		return clientRequestResult(&ThreadStartRequest{request}, err)
	case MethodThreadResume:
		request, err := decodeClientRequest[ThreadResumeParams](wire, true)
		return clientRequestResult(&ThreadResumeRequest{request}, err)
	case MethodThreadRead:
		request, err := decodeClientRequest[ThreadReadParams](wire, true)
		return clientRequestResult(&ThreadReadRequest{request}, err)
	case MethodThreadUnsubscribe:
		request, err := decodeClientRequest[ThreadUnsubscribeParams](wire, true)
		return clientRequestResult(&ThreadUnsubscribeRequest{request}, err)
	case MethodTurnStart:
		request, err := decodeClientRequest[TurnStartParams](wire, true)
		return clientRequestResult(&TurnStartRequest{request}, err)
	case MethodTurnSteer:
		request, err := decodeClientRequest[TurnSteerParams](wire, true)
		return clientRequestResult(&TurnSteerRequest{request}, err)
	case MethodTurnInterrupt:
		request, err := decodeClientRequest[TurnInterruptParams](wire, true)
		return clientRequestResult(&TurnInterruptRequest{request}, err)
	case MethodModelList:
		request, err := decodeClientRequest[ModelListParams](wire, true)
		return clientRequestResult(&ModelListRequest{request}, err)
	case MethodConfigRead:
		request, err := decodeClientRequest[ConfigReadParams](wire, true)
		return clientRequestResult(&ConfigReadRequest{request}, err)
	case MethodSkillsExtraRootsSet:
		request, err := decodeClientRequest[SkillsExtraRootsSetParams](wire, true)
		return clientRequestResult(&SkillsExtraRootsSetRequest{request}, err)
	case MethodSkillsList:
		request, err := decodeClientRequest[SkillsListParams](wire, true)
		return clientRequestResult(&SkillsListRequest{request}, err)
	case MethodAccountRead:
		request, err := decodeClientRequest[GetAccountParams](wire, true)
		return clientRequestResult(&AccountReadRequest{request}, err)
	case MethodAccountLoginStart:
		request, err := decodeClientRequest[LoginAccountParams](wire, true)
		return clientRequestResult(&AccountLoginStartRequest{request}, err)
	case MethodAccountLoginCancel:
		request, err := decodeClientRequest[CancelLoginAccountParams](wire, true)
		return clientRequestResult(&AccountLoginCancelRequest{request}, err)
	case MethodAccountLogout:
		request, err := decodeClientRequest[struct{}](wire, false)
		return clientRequestResult(&AccountLogoutRequest{request}, err)
	default:
		return nil, fmt.Errorf("%w: unsupported client request method %q", ErrInvalidEnvelope, wire.Method)
	}
}

// clientRequestResult 保证解码失败只返回错误，不泄漏部分构造的客户端请求。
func clientRequestResult(request ClientRequest, err error) (ClientRequest, error) {
	if err != nil {
		return nil, err
	}
	return request, nil
}

// decodeClientRequest 在 method 已选定后解析共同 ID 和唯一 Params 类型。
func decodeClientRequest[P any](wire requestWire, paramsRequired bool) (clientRequestEnvelope[P], error) {
	if len(wire.ID) == 0 {
		return clientRequestEnvelope[P]{}, fmt.Errorf("%w: request %q has no id", ErrInvalidEnvelope, wire.Method)
	}
	var id RequestID
	if err := json.Unmarshal(wire.ID, &id); err != nil {
		return clientRequestEnvelope[P]{}, fmt.Errorf("%w: decoding request id: %w", ErrInvalidEnvelope, err)
	}
	if !paramsRequired {
		return newClientRequestWithoutParams[P](id, wire.Method), nil
	}
	if len(wire.Params) == 0 {
		return clientRequestEnvelope[P]{}, fmt.Errorf("%w: request %q has no params", ErrInvalidEnvelope, wire.Method)
	}
	var params P
	if err := json.Unmarshal(wire.Params, &params); err != nil {
		return clientRequestEnvelope[P]{}, fmt.Errorf("%w: decoding params for %q: %w", ErrInvalidEnvelope, wire.Method, err)
	}
	return newClientRequest(id, wire.Method, params), nil
}

// ClientNotification 是 V1 客户端通知的封闭变体集合。
type ClientNotification interface {
	// Marshaler 把封闭通知变体编码为 Codex app-server wire 对象。
	json.Marshaler
	// Method 返回通知固定的方法名。
	Method() string
	// isClientNotification 阻止包外实现绕过封闭变体集合。
	isClientNotification()
}

// InitializedNotification 表示 initialize 成功后发送且无 params 的 initialized 通知。
type InitializedNotification struct{}

// Method 返回 initialized 固定方法名。
func (InitializedNotification) Method() string {
	return MethodInitialized
}

// isClientNotification 封闭客户端通知变体。
func (InitializedNotification) isClientNotification() {}

// MarshalJSON 生成不携带 params 的 initialized wire 对象。
func (InitializedNotification) MarshalJSON() ([]byte, error) {
	return []byte(`{"method":"` + MethodInitialized + `"}`), nil
}

// ServerRequest 是 V1 服务端请求的封闭变体集合。
type ServerRequest interface {
	// Marshaler 把封闭审批请求变体编码为 Codex app-server wire 对象。
	json.Marshaler
	// Method 返回与审批 Params 类型固定绑定的方法名。
	Method() string
	// isServerRequest 阻止包外实现绕过封闭变体集合。
	isServerRequest()
}

// serverRequestEnvelope 实现三类 V1 审批请求共享的强类型 wire 行为。
type serverRequestEnvelope[P any] struct {
	// ID 是服务端发起审批时使用的 JSON-RPC 请求标识。
	ID RequestID
	// Params 是与固定审批 method 对应的具名参数。
	Params P
	// method 由构造函数或解码器固定，包外无法改配。
	method string
}

// Method 返回该服务端请求变体的固定方法名。
func (r serverRequestEnvelope[P]) Method() string {
	return r.method
}

// isServerRequest 封闭 ServerRequest 变体。
func (r serverRequestEnvelope[P]) isServerRequest() {}

// MarshalJSON 将固定 method、ID 与强类型 Params 写入 wire。
func (r serverRequestEnvelope[P]) MarshalJSON() ([]byte, error) {
	if r.method == "" {
		return nil, fmt.Errorf("%w: server request has no method", ErrInvalidEnvelope)
	}
	return json.Marshal(struct {
		// ID 使用指针以显式触发生成的 RequestID union marshaler。
		ID *RequestID `json:"id"`
		// Method 是该具体变体固定的方法名。
		Method string `json:"method"`
		// Params 是与 Method 静态绑定的具名参数。
		Params P `json:"params"`
	}{ID: &r.ID, Method: r.method, Params: r.Params})
}

// newServerRequest 创建固定方法的强类型服务端请求。
func newServerRequest[P any](id RequestID, method string, params P) serverRequestEnvelope[P] {
	return serverRequestEnvelope[P]{ID: id, Params: params, method: method}
}

// CommandExecutionApprovalRequest 表示命令执行审批请求。
type CommandExecutionApprovalRequest struct {
	// serverRequestEnvelope 固定命令审批 method 与 Params 类型。
	serverRequestEnvelope[CommandExecutionRequestApprovalParams]
}

// FileChangeApprovalRequest 表示文件变更审批请求。
type FileChangeApprovalRequest struct {
	// serverRequestEnvelope 固定文件审批 method 与 Params 类型。
	serverRequestEnvelope[FileChangeRequestApprovalParams]
}

// PermissionsApprovalRequest 表示权限提升审批请求。
type PermissionsApprovalRequest struct {
	// serverRequestEnvelope 固定权限审批 method 与 Params 类型。
	serverRequestEnvelope[PermissionsRequestApprovalParams]
}

// MCPServerElicitationRequest 表示 MCP server 发起的表单或 URL 交互请求。
type MCPServerElicitationRequest struct {
	// serverRequestEnvelope 固定 MCP elicitation 方法与参数类型。
	serverRequestEnvelope[MCPServerElicitationRequestParams]
}

// ToolRequestUserInputRequest 表示 Codex request_user_input 工具发起的结构化提问。
type ToolRequestUserInputRequest struct {
	// serverRequestEnvelope 固定 request_user_input 方法与参数类型。
	serverRequestEnvelope[ToolRequestUserInputParams]
}

// DecodeServerRequest 先按 method 选择审批变体，再解析唯一对应的 Params 类型。
func DecodeServerRequest(data []byte) (ServerRequest, error) {
	var wire requestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("%w: decoding server request: %w", ErrInvalidEnvelope, err)
	}
	switch wire.Method {
	case MethodCommandExecutionRequestApproval:
		request, err := decodeServerRequest[CommandExecutionRequestApprovalParams](wire)
		return serverRequestResult(&CommandExecutionApprovalRequest{request}, err)
	case MethodFileChangeRequestApproval:
		request, err := decodeServerRequest[FileChangeRequestApprovalParams](wire)
		return serverRequestResult(&FileChangeApprovalRequest{request}, err)
	case MethodPermissionsRequestApproval:
		request, err := decodeServerRequest[PermissionsRequestApprovalParams](wire)
		return serverRequestResult(&PermissionsApprovalRequest{request}, err)
	case MethodMCPServerElicitationRequest:
		request, err := decodeServerRequest[MCPServerElicitationRequestParams](wire)
		return serverRequestResult(&MCPServerElicitationRequest{request}, err)
	case MethodToolRequestUserInput:
		request, err := decodeServerRequest[ToolRequestUserInputParams](wire)
		return serverRequestResult(&ToolRequestUserInputRequest{request}, err)
	default:
		return nil, fmt.Errorf("%w: unsupported server request method %q", ErrInvalidEnvelope, wire.Method)
	}
}

// serverRequestResult 保证解码失败只返回错误，不泄漏部分构造的服务端请求。
func serverRequestResult(request ServerRequest, err error) (ServerRequest, error) {
	if err != nil {
		return nil, err
	}
	return request, nil
}

// decodeServerRequest 解析服务端请求共同 ID 和已由 method 选定的 Params。
func decodeServerRequest[P any](wire requestWire) (serverRequestEnvelope[P], error) {
	if len(wire.ID) == 0 || len(wire.Params) == 0 {
		return serverRequestEnvelope[P]{}, fmt.Errorf("%w: server request %q lacks id or params", ErrInvalidEnvelope, wire.Method)
	}
	var id RequestID
	if err := json.Unmarshal(wire.ID, &id); err != nil {
		return serverRequestEnvelope[P]{}, fmt.Errorf("%w: decoding server request id: %w", ErrInvalidEnvelope, err)
	}
	var params P
	if err := json.Unmarshal(wire.Params, &params); err != nil {
		return serverRequestEnvelope[P]{}, fmt.Errorf("%w: decoding server request params for %q: %w", ErrInvalidEnvelope, wire.Method, err)
	}
	return newServerRequest(id, wire.Method, params), nil
}

// ServerNotification 是 V1 服务端通知的封闭变体集合，已知 method 均绑定具名 Params。
type ServerNotification interface {
	// Marshaler 把已知或未知通知变体编码回 Codex app-server wire 对象。
	json.Marshaler
	// Method 返回通知分派使用的方法名。
	Method() string
	// isServerNotification 阻止包外实现绕过封闭变体集合。
	isServerNotification()
}

// notificationEnvelope 实现已知 V1 服务端通知共享的强类型 wire 行为。
type notificationEnvelope[P any] struct {
	// Params 是与固定通知 method 对应的具名参数类型。
	Params P
	// EmittedAtMS 保留 app-server 可选的通知产生时间。
	EmittedAtMS *int64
	// method 只由解码分派器设置，防止调用方改配 Params。
	method string
}

// Method 返回该通知变体固定的 app-server 方法名。
func (n notificationEnvelope[P]) Method() string {
	return n.method
}

// isServerNotification 封闭已知服务端通知变体。
func (n notificationEnvelope[P]) isServerNotification() {}

// MarshalJSON 将固定 method 与强类型 Params 写入通知 wire。
func (n notificationEnvelope[P]) MarshalJSON() ([]byte, error) {
	if n.method == "" {
		return nil, fmt.Errorf("%w: server notification has no method", ErrInvalidEnvelope)
	}
	return json.Marshal(struct {
		// Method 是该具体通知变体固定的方法名。
		Method string `json:"method"`
		// Params 是与 Method 静态绑定的具名参数。
		Params P `json:"params"`
		// EmittedAtMS 保留可选的服务端发出时间。
		EmittedAtMS *int64 `json:"emittedAtMs,omitempty"`
	}{Method: n.method, Params: n.Params, EmittedAtMS: n.EmittedAtMS})
}

// ErrorEnvelope 表示 error 通知。
type ErrorEnvelope struct {
	// notificationEnvelope 固定 error method 与 Params 类型。
	notificationEnvelope[ErrorNotification]
}

// TurnStartedEnvelope 表示 turn/started 通知。
type TurnStartedEnvelope struct {
	// notificationEnvelope 固定 turn/started method 与 Params 类型。
	notificationEnvelope[TurnStartedNotification]
}

// TurnCompletedEnvelope 表示 turn/completed 通知。
type TurnCompletedEnvelope struct {
	// notificationEnvelope 固定 turn/completed method 与 Params 类型。
	notificationEnvelope[TurnCompletedNotification]
}

// TurnDiffUpdatedEnvelope 表示 turn/diff/updated 通知。
type TurnDiffUpdatedEnvelope struct {
	// notificationEnvelope 固定 turn/diff/updated method 与 Params 类型。
	notificationEnvelope[TurnDiffUpdatedNotification]
}

// ItemStartedEnvelope 表示 item/started 通知。
type ItemStartedEnvelope struct {
	// notificationEnvelope 固定 item/started method 与强类型 Item Params。
	notificationEnvelope[ItemStartedNotification]
}

// ItemCompletedEnvelope 表示 item/completed 通知。
type ItemCompletedEnvelope struct {
	// notificationEnvelope 固定 item/completed method 与强类型 Item Params。
	notificationEnvelope[ItemCompletedNotification]
}

// AgentMessageDeltaEnvelope 表示 item/agentMessage/delta 通知。
type AgentMessageDeltaEnvelope struct {
	// notificationEnvelope 固定消息增量 method 与 Params 类型。
	notificationEnvelope[AgentMessageDeltaNotification]
}

// ReasoningSummaryTextDeltaEnvelope 表示 reasoning summary 文本增量通知。
type ReasoningSummaryTextDeltaEnvelope struct {
	// notificationEnvelope 固定 reasoning summary 文本增量 method 与 Params 类型。
	notificationEnvelope[ReasoningSummaryTextDeltaNotification]
}

// ReasoningSummaryPartAddedEnvelope 表示 reasoning summary 分段通知。
type ReasoningSummaryPartAddedEnvelope struct {
	// notificationEnvelope 固定 reasoning summary 分段 method 与 Params 类型。
	notificationEnvelope[ReasoningSummaryPartAddedNotification]
}

// ReasoningTextDeltaEnvelope 表示 reasoning 原文增量通知。
type ReasoningTextDeltaEnvelope struct {
	// notificationEnvelope 固定 reasoning 原文增量 method 与 Params 类型。
	notificationEnvelope[ReasoningTextDeltaNotification]
}

// TurnPlanUpdatedEnvelope 表示 turn/plan/updated 通知。
type TurnPlanUpdatedEnvelope struct {
	// notificationEnvelope 固定计划更新 method 与 Params 类型。
	notificationEnvelope[TurnPlanUpdatedNotification]
}

// ThreadTokenUsageUpdatedEnvelope 表示 thread/tokenUsage/updated 通知。
type ThreadTokenUsageUpdatedEnvelope struct {
	// notificationEnvelope 固定 token usage method 与 Params 类型。
	notificationEnvelope[ThreadTokenUsageUpdatedNotification]
}

// CommandExecutionOutputDeltaEnvelope 表示命令输出增量通知。
type CommandExecutionOutputDeltaEnvelope struct {
	// notificationEnvelope 固定命令输出 method 与 Params 类型。
	notificationEnvelope[CommandExecutionOutputDeltaNotification]
}

// TerminalInteractionEnvelope 表示终端交互通知。
type TerminalInteractionEnvelope struct {
	// notificationEnvelope 固定终端交互 method 与 Params 类型。
	notificationEnvelope[TerminalInteractionNotification]
}

// MCPToolCallProgressEnvelope 表示 MCP 工具进度通知。
type MCPToolCallProgressEnvelope struct {
	// notificationEnvelope 固定 MCP 进度 method 与 Params 类型。
	notificationEnvelope[MCPToolCallProgressNotification]
}

// MCPServerStatusUpdatedEnvelope 表示单个 MCP server 启动状态变化。
type MCPServerStatusUpdatedEnvelope struct {
	// notificationEnvelope 固定 MCP 启动状态方法与参数类型。
	notificationEnvelope[MCPServerStatusUpdatedNotification]
}

// FileChangePatchUpdatedEnvelope 表示文件补丁更新通知。
type FileChangePatchUpdatedEnvelope struct {
	// notificationEnvelope 固定文件补丁 method 与 Params 类型。
	notificationEnvelope[FileChangePatchUpdatedNotification]
}

// ServerRequestResolvedEnvelope 表示 serverRequest/resolved 通知。
type ServerRequestResolvedEnvelope struct {
	// notificationEnvelope 固定请求完成 method 与 Params 类型。
	notificationEnvelope[ServerRequestResolvedNotification]
}

// ContextCompactedEnvelope 表示 thread/compacted 通知。
type ContextCompactedEnvelope struct {
	// notificationEnvelope 固定上下文压缩 method 与 Params 类型。
	notificationEnvelope[ContextCompactedNotification]
}

// ModelReroutedEnvelope 表示 model/rerouted 通知。
type ModelReroutedEnvelope struct {
	// notificationEnvelope 固定模型切换 method 与 Params 类型。
	notificationEnvelope[ModelReroutedNotification]
}

// WarningEnvelope 表示 warning 通知。
type WarningEnvelope struct {
	// notificationEnvelope 固定 warning method 与 Params 类型。
	notificationEnvelope[WarningNotification]
}

// AccountLoginCompletedEnvelope 表示登录流程完成通知。
type AccountLoginCompletedEnvelope struct {
	// notificationEnvelope 固定登录完成 method 与强类型 Params。
	notificationEnvelope[AccountLoginCompletedNotification]
}

// UnknownServerNotification 保存 V1 未识别的扩展通知，已知方法绝不会退化到此类型。
type UnknownServerNotification struct {
	// MethodName 是未识别的服务端方法名。
	MethodName string
	// Params 保留未知扩展的原始 JSON，避免使用 interface{} 造成数值或字段损失。
	Params json.RawMessage
	// EmittedAtMS 保留 app-server 可选的通知产生时间。
	EmittedAtMS *int64
}

// Method 返回未知通知的原始方法名。
func (n UnknownServerNotification) Method() string {
	return n.MethodName
}

// isServerNotification 将未知扩展纳入受控 fallback，而不开放任意已知变体实现。
func (n UnknownServerNotification) isServerNotification() {}

// MarshalJSON 原样写回未知 Params，同时保持规范 envelope 字段。
func (n UnknownServerNotification) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		// Method 是未识别的原始方法名。
		Method string `json:"method"`
		// Params 是未经结构改写的扩展载荷。
		Params json.RawMessage `json:"params"`
		// EmittedAtMS 保留可选的服务端发出时间。
		EmittedAtMS *int64 `json:"emittedAtMs,omitempty"`
	}{Method: n.MethodName, Params: n.Params, EmittedAtMS: n.EmittedAtMS})
}

// notificationWire 只读取分派必需字段，Params 在 method 确定后再强类型解码。
type notificationWire struct {
	// Method 选择唯一的已知通知变体或未知 fallback。
	Method string `json:"method"`
	// Params 暂存原始 JSON，避免 quicktype 字段并集。
	Params json.RawMessage `json:"params"`
	// EmittedAtMS 保留 envelope 上的可选发出时间。
	EmittedAtMS *int64 `json:"emittedAtMs,omitempty"`
}

// DecodeServerNotification 按 method 分派到唯一具名 Params；未知方法保留 RawMessage。
func DecodeServerNotification(data []byte) (ServerNotification, error) {
	var wire notificationWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("%w: decoding server notification: %w", ErrInvalidEnvelope, err)
	}
	if wire.Method == "" || len(wire.Params) == 0 {
		return nil, fmt.Errorf("%w: notification lacks method or params", ErrInvalidEnvelope)
	}

	switch wire.Method {
	case MethodError:
		envelope, err := decodeNotification[ErrorNotification](wire)
		return serverNotificationResult(&ErrorEnvelope{envelope}, err)
	case MethodTurnStarted:
		envelope, err := decodeNotification[TurnStartedNotification](wire)
		return serverNotificationResult(&TurnStartedEnvelope{envelope}, err)
	case MethodTurnCompleted:
		envelope, err := decodeNotification[TurnCompletedNotification](wire)
		return serverNotificationResult(&TurnCompletedEnvelope{envelope}, err)
	case MethodTurnDiffUpdated:
		envelope, err := decodeNotification[TurnDiffUpdatedNotification](wire)
		return serverNotificationResult(&TurnDiffUpdatedEnvelope{envelope}, err)
	case MethodItemStarted:
		envelope, err := decodeNotification[ItemStartedNotification](wire)
		return serverNotificationResult(&ItemStartedEnvelope{envelope}, err)
	case MethodItemCompleted:
		envelope, err := decodeNotification[ItemCompletedNotification](wire)
		return serverNotificationResult(&ItemCompletedEnvelope{envelope}, err)
	case MethodAgentMessageDelta:
		envelope, err := decodeNotification[AgentMessageDeltaNotification](wire)
		return serverNotificationResult(&AgentMessageDeltaEnvelope{envelope}, err)
	case MethodReasoningSummaryTextDelta:
		envelope, err := decodeNotification[ReasoningSummaryTextDeltaNotification](wire)
		return serverNotificationResult(&ReasoningSummaryTextDeltaEnvelope{envelope}, err)
	case MethodReasoningSummaryPartAdded:
		envelope, err := decodeNotification[ReasoningSummaryPartAddedNotification](wire)
		return serverNotificationResult(&ReasoningSummaryPartAddedEnvelope{envelope}, err)
	case MethodReasoningTextDelta:
		envelope, err := decodeNotification[ReasoningTextDeltaNotification](wire)
		return serverNotificationResult(&ReasoningTextDeltaEnvelope{envelope}, err)
	case MethodTurnPlanUpdated:
		envelope, err := decodeNotification[TurnPlanUpdatedNotification](wire)
		return serverNotificationResult(&TurnPlanUpdatedEnvelope{envelope}, err)
	case MethodThreadTokenUsageUpdated:
		envelope, err := decodeNotification[ThreadTokenUsageUpdatedNotification](wire)
		return serverNotificationResult(&ThreadTokenUsageUpdatedEnvelope{envelope}, err)
	case MethodCommandExecutionOutputDelta:
		envelope, err := decodeNotification[CommandExecutionOutputDeltaNotification](wire)
		return serverNotificationResult(&CommandExecutionOutputDeltaEnvelope{envelope}, err)
	case MethodTerminalInteraction:
		envelope, err := decodeNotification[TerminalInteractionNotification](wire)
		return serverNotificationResult(&TerminalInteractionEnvelope{envelope}, err)
	case MethodMCPToolCallProgress:
		envelope, err := decodeNotification[MCPToolCallProgressNotification](wire)
		return serverNotificationResult(&MCPToolCallProgressEnvelope{envelope}, err)
	case MethodMCPServerStartupStatusUpdated:
		envelope, err := decodeNotification[MCPServerStatusUpdatedNotification](wire)
		return serverNotificationResult(&MCPServerStatusUpdatedEnvelope{envelope}, err)
	case MethodFileChangePatchUpdated:
		envelope, err := decodeNotification[FileChangePatchUpdatedNotification](wire)
		return serverNotificationResult(&FileChangePatchUpdatedEnvelope{envelope}, err)
	case MethodServerRequestResolved:
		envelope, err := decodeNotification[ServerRequestResolvedNotification](wire)
		return serverNotificationResult(&ServerRequestResolvedEnvelope{envelope}, err)
	case MethodThreadCompacted:
		envelope, err := decodeNotification[ContextCompactedNotification](wire)
		return serverNotificationResult(&ContextCompactedEnvelope{envelope}, err)
	case MethodModelRerouted:
		envelope, err := decodeNotification[ModelReroutedNotification](wire)
		return serverNotificationResult(&ModelReroutedEnvelope{envelope}, err)
	case MethodWarning:
		envelope, err := decodeNotification[WarningNotification](wire)
		return serverNotificationResult(&WarningEnvelope{envelope}, err)
	case MethodAccountLoginCompleted:
		envelope, err := decodeNotification[AccountLoginCompletedNotification](wire)
		return serverNotificationResult(&AccountLoginCompletedEnvelope{envelope}, err)
	default:
		return &UnknownServerNotification{
			MethodName:  wire.Method,
			Params:      append(json.RawMessage(nil), wire.Params...),
			EmittedAtMS: wire.EmittedAtMS,
		}, nil
	}
}

// serverNotificationResult 保证解码失败只返回错误，不泄漏部分构造的通知。
func serverNotificationResult(notification ServerNotification, err error) (ServerNotification, error) {
	if err != nil {
		return nil, err
	}
	return notification, nil
}

// decodeNotification 将已选择 method 的原始 Params 解码到唯一具名类型。
func decodeNotification[P any](wire notificationWire) (notificationEnvelope[P], error) {
	var params P
	if err := json.Unmarshal(wire.Params, &params); err != nil {
		return notificationEnvelope[P]{}, fmt.Errorf(
			"%w: decoding notification params for %q: %w",
			ErrInvalidEnvelope,
			wire.Method,
			err,
		)
	}
	return notificationEnvelope[P]{Params: params, EmittedAtMS: wire.EmittedAtMS, method: wire.Method}, nil
}
