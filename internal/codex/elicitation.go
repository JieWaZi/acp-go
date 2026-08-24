package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const userInputOtherSuffix = "__other"

// elicitationRequester 是 MCP 与 request_user_input 消费的最小 ACP Elicitation 能力。
type elicitationRequester interface {
	// UnstableCreateElicitation 请求客户端展示表单或打开 URL。
	UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error)
	// UnstableCompleteElicitation 通知客户端已接受的 URL 交互结束。
	UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error
}

// cloneElicitationCapabilities 只保存 initialize 时的能力布尔语义，不持有调用方可变 map。
func cloneElicitationCapabilities(value *acp.ElicitationCapabilities) *acp.ElicitationCapabilities {
	if value == nil {
		return nil
	}
	result := &acp.ElicitationCapabilities{}
	if value.Form != nil {
		result.Form = &acp.ElicitationFormCapabilities{}
	}
	if value.Url != nil {
		result.Url = &acp.ElicitationUrlCapabilities{}
	}
	return result
}

// currentElicitationSupport 返回 initialize 时协商的表单与 URL 能力。
func (a *Agent) currentElicitationSupport() (form bool, urlMode bool) {
	a.initializeMu.RLock()
	defer a.initializeMu.RUnlock()
	if a.elicitationCapabilities == nil {
		return false, false
	}
	return a.elicitationCapabilities.Form != nil, a.elicitationCapabilities.Url != nil
}

// currentElicitationRequester 返回当前 ACP connection 的交互能力快照。
func (a *Agent) currentElicitationRequester() elicitationRequester {
	a.connectionMu.RLock()
	defer a.connectionMu.RUnlock()
	return a.elicitationRequester
}

// failClosedMCPServerElicitation 返回 MCP 定义的安全取消响应。
func failClosedMCPServerElicitation() protocol.MCPServerElicitationRequestResponse {
	return protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionCancel}
}

// failClosedToolUserInput 返回 request_user_input 的安全空答案。
func failClosedToolUserInput() protocol.ToolRequestUserInputResponse {
	return protocol.ToolRequestUserInputResponse{Answers: map[string]protocol.ToolRequestUserInputAnswer{}}
}

// handleToolUserInput 将 Codex request_user_input 问题转换为 ACP form elicitation。
func (a *Agent) handleToolUserInput(
	ctx context.Context,
	params protocol.ToolRequestUserInputParams,
) protocol.ToolRequestUserInputResponse {
	formSupported, _ := a.currentElicitationSupport()
	requester := a.currentElicitationRequester()
	if !formSupported || requester == nil {
		return failClosedToolUserInput()
	}
	generation, current := a.currentTurnGeneration(params.ThreadID, params.TurnID)
	if !current {
		return failClosedToolUserInput()
	}
	requestContext, cancelTurn, current := a.turnElicitationContext(ctx, generation)
	if !current {
		return failClosedToolUserInput()
	}
	defer cancelTurn()

	request := userInputElicitationRequest(params)
	cancelTimeout := func() {}
	if params.AutoResolutionMS != nil {
		requestContext, cancelTimeout = context.WithTimeout(requestContext, time.Duration(*params.AutoResolutionMS)*time.Millisecond)
	}
	defer cancelTimeout()
	response, err := requester.UnstableCreateElicitation(requestContext, request)
	if err != nil {
		a.logger.Debug("ACP request_user_input 交互未完成", "thread_id", params.ThreadID, "item_id", params.ItemID, "error", err)
		return failClosedToolUserInput()
	}
	if !a.IsCurrent(generation) {
		return failClosedToolUserInput()
	}
	return convertToolUserInputResponse(response, params)
}

// userInputElicitationRequest 构造支持选项、自定义答案与密文提示的 ACP JSON Schema。
func userInputElicitationRequest(params protocol.ToolRequestUserInputParams) acp.UnstableCreateElicitationRequest {
	properties := make(map[string]any, len(params.Questions)*2)
	required := make([]string, 0, len(params.Questions))
	questionIDs := make(map[string]struct{}, len(params.Questions))
	for _, question := range params.Questions {
		questionIDs[question.ID] = struct{}{}
	}
	for _, question := range params.Questions {
		hasOptions := len(question.Options) > 0
		hasOther := boolValue(question.IsOther) && hasOptions
		field := map[string]any{
			"type":        "string",
			"title":       firstNonEmpty(question.Header, question.ID),
			"description": question.Question,
			"_meta": map[string]any{"codex": map[string]any{
				"isOther": boolValue(question.IsOther), "isSecret": boolValue(question.IsSecret),
			}},
		}
		if hasOptions {
			choices := make([]map[string]any, 0, len(question.Options))
			for _, option := range question.Options {
				choices = append(choices, map[string]any{
					"const": option.Label, "title": option.Label, "description": option.Description,
				})
			}
			field["oneOf"] = choices
		}
		properties[question.ID] = field
		if !hasOther {
			required = append(required, question.ID)
		}
		if hasOther {
			otherID := userInputOtherFieldID(question.ID, questionIDs)
			properties[otherID] = map[string]any{
				"type": "string", "title": "Other",
				"description": "Type your own answer instead of choosing an option above.",
				"_meta": map[string]any{"codex": map[string]any{
					"questionId": question.ID, "isOtherAnswer": true, "isSecret": boolValue(question.IsSecret),
				}},
			}
		}
	}
	message := "Input requested"
	if len(params.Questions) == 1 {
		message = params.Questions[0].Question
	}
	request := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{
		Type: acp.UnstableElicitationSchemaTypeObject, Properties: properties, Required: required,
	})
	request.Form.Message = message
	request.Form.Meta = map[string]any{"codex": map[string]any{
		"sessionId": params.ThreadID, "toolCallId": params.ItemID, "autoResolutionMs": params.AutoResolutionMS,
	}}
	return request
}

// convertToolUserInputResponse 把 ACP accept content 恢复为 Codex question-id 答案映射。
func convertToolUserInputResponse(
	response acp.UnstableCreateElicitationResponse,
	params protocol.ToolRequestUserInputParams,
) protocol.ToolRequestUserInputResponse {
	result := failClosedToolUserInput()
	if response.Accept == nil {
		return result
	}
	questionIDs := make(map[string]struct{}, len(params.Questions))
	for _, question := range params.Questions {
		questionIDs[question.ID] = struct{}{}
	}
	for _, question := range params.Questions {
		value, ok := response.Accept.Content[question.ID]
		if boolValue(question.IsOther) && len(question.Options) > 0 {
			if other, present := response.Accept.Content[userInputOtherFieldID(question.ID, questionIDs)]; present && answerHasValue(other) {
				value, ok = other, true
			}
		}
		if !ok {
			continue
		}
		result.Answers[question.ID] = protocol.ToolRequestUserInputAnswer{Answers: stringifyAnswers(value)}
	}
	return result
}

// handleMCPServerElicitation 优先使用 ACP Elicitation；客户端未声明对应能力时退回权限交互。
func (a *Agent) handleMCPServerElicitation(
	ctx context.Context,
	params protocol.MCPServerElicitationRequestParams,
) protocol.MCPServerElicitationRequestResponse {
	switch params.Mode {
	case protocol.Form:
		if len(params.RequestedSchema) == 0 {
			return failClosedMCPServerElicitation()
		}
	case protocol.URL:
		if params.ElicitationID == nil || *params.ElicitationID == "" || params.URL == nil || *params.URL == "" {
			return failClosedMCPServerElicitation()
		}
	case protocol.OpenaiForm:
		// openai/form 不是 ACP 标准 schema，固定走下面的 permission fallback。
	default:
		return failClosedMCPServerElicitation()
	}
	state, ok := a.sessions.get(params.ThreadID)
	if !ok || !a.sessions.isCurrent(state) {
		return failClosedMCPServerElicitation()
	}
	requestContext := ctx
	cancelTurn := func() {}
	var generation turnGeneration
	if params.TurnID != nil {
		var current bool
		generation, current = a.currentTurnGeneration(params.ThreadID, *params.TurnID)
		if !current {
			return failClosedMCPServerElicitation()
		}
		requestContext, cancelTurn, current = a.turnElicitationContext(ctx, generation)
		if !current {
			return failClosedMCPServerElicitation()
		}
	}
	defer cancelTurn()

	formSupported, urlSupported := a.currentElicitationSupport()
	requester := a.currentElicitationRequester()
	useElicitation := requester != nil && ((params.Mode == protocol.Form && formSupported) || (params.Mode == protocol.URL && urlSupported))
	if useElicitation {
		response, err := requester.UnstableCreateElicitation(requestContext, mcpElicitationRequest(params))
		if err != nil {
			a.logger.Debug("ACP MCP elicitation 未完成", "thread_id", params.ThreadID, "server", params.ServerName, "error", err)
			return failClosedMCPServerElicitation()
		}
		if !a.mcpElicitationStillCurrent(state, params, generation) {
			return failClosedMCPServerElicitation()
		}
		result := convertMCPElicitationResponse(response)
		if params.Mode == protocol.URL && result.Action == protocol.MCPServerElicitationActionAccept && params.ElicitationID != nil {
			a.trackPendingURLElicitation(params.ThreadID, acp.UnstableElicitationId(*params.ElicitationID))
		}
		return result
	}
	result := a.handleMCPPermissionFallback(requestContext, params)
	if !a.mcpElicitationStillCurrent(state, params, generation) {
		return failClosedMCPServerElicitation()
	}
	return result
}

// turnElicitationContext 把嵌套 ACP 请求绑定到当前 prompt 的取消和 close 生命周期。
func (a *Agent) turnElicitationContext(
	parent context.Context,
	generation turnGeneration,
) (context.Context, context.CancelFunc, bool) {
	state, ok := a.sessions.get(generation.ThreadID)
	if !ok || !a.sessions.isCurrent(state) {
		return nil, func() {}, false
	}
	state.mu.Lock()
	prompt := state.activePrompt
	state.mu.Unlock()
	if prompt == nil || !a.IsCurrent(generation) {
		return nil, func() {}, false
	}
	requestContext, cancel := context.WithCancel(prompt.runCtx)
	stopParent := context.AfterFunc(parent, cancel)
	go func() {
		select {
		case <-prompt.cancelSignal:
			cancel()
		case <-requestContext.Done():
		}
	}()
	return requestContext, func() {
		stopParent()
		cancel()
	}, true
}

// mcpElicitationStillCurrent 在嵌套客户端回调后重新验证 session 或精确 turn 身份。
func (a *Agent) mcpElicitationStillCurrent(
	state *sessionState,
	params protocol.MCPServerElicitationRequestParams,
	generation turnGeneration,
) bool {
	if params.TurnID != nil {
		return a.IsCurrent(generation)
	}
	return a.sessions.isCurrent(state)
}

// mcpElicitationRequest 将标准 MCP form/url 请求转换为 ACP Elicitation DTO。
func mcpElicitationRequest(params protocol.MCPServerElicitationRequestParams) acp.UnstableCreateElicitationRequest {
	meta := rawMetaRecord(params.Meta)
	codexMeta, _ := meta["codex"].(map[string]any)
	if codexMeta == nil {
		codexMeta = make(map[string]any)
	}
	codexMeta["sessionId"] = params.ThreadID
	codexMeta["serverName"] = params.ServerName
	codexMeta["turnId"] = params.TurnID
	meta["codex"] = codexMeta
	if params.Mode == protocol.URL && params.ElicitationID != nil && params.URL != nil {
		request := acp.NewUnstableCreateElicitationRequestUrl(acp.UnstableElicitationId(*params.ElicitationID), *params.URL)
		request.Url.Message = params.Message
		request.Url.Meta = meta
		return request
	}
	var schema acp.UnstableElicitationSchema
	if json.Unmarshal(params.RequestedSchema, &schema) != nil {
		schema = acp.UnstableElicitationSchema{Type: acp.UnstableElicitationSchemaTypeObject, Properties: map[string]any{}}
	}
	request := acp.NewUnstableCreateElicitationRequestForm(schema)
	request.Form.Message = params.Message
	request.Form.Meta = meta
	return request
}

// convertMCPElicitationResponse 将 ACP accept/decline/cancel 映射回 MCP action。
func convertMCPElicitationResponse(response acp.UnstableCreateElicitationResponse) protocol.MCPServerElicitationRequestResponse {
	if response.Accept != nil {
		content, _ := json.Marshal(response.Accept.Content)
		return protocol.MCPServerElicitationRequestResponse{
			Action:  protocol.MCPServerElicitationActionAccept,
			Content: content,
			Meta:    marshalRawMeta(response.Accept.Meta),
		}
	}
	if response.Decline != nil {
		return protocol.MCPServerElicitationRequestResponse{
			Action: protocol.MCPServerElicitationActionDecline,
			Meta:   marshalRawMeta(response.Decline.Meta),
		}
	}
	if response.Cancel != nil {
		return protocol.MCPServerElicitationRequestResponse{
			Action: protocol.MCPServerElicitationActionCancel,
			Meta:   marshalRawMeta(response.Cancel.Meta),
		}
	}
	return failClosedMCPServerElicitation()
}

// handleMCPPermissionFallback 为未声明 Elicitation 能力的客户端提供可操作的允许/拒绝界面。
func (a *Agent) handleMCPPermissionFallback(
	ctx context.Context,
	params protocol.MCPServerElicitationRequestParams,
) protocol.MCPServerElicitationRequestResponse {
	requester := a.currentApprovalRequester()
	if requester == nil {
		return failClosedMCPServerElicitation()
	}
	title := fmt.Sprintf("MCP %s requests input", params.ServerName)
	toolCallID := "elicitation-" + params.ServerName
	kind := acp.ToolKindOther
	rawInput := map[string]any{"serverName": params.ServerName, "mode": params.Mode}
	if params.Mode == protocol.URL && params.ElicitationID != nil {
		toolCallID = "elicitation-" + *params.ElicitationID
		kind = acp.ToolKindFetch
		rawInput["url"] = stringValue(params.URL)
	} else {
		rawInput["schema"] = json.RawMessage(params.RequestedSchema)
	}
	response, err := requester.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(params.ThreadID),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(toolCallID), Kind: &kind,
			Status: acp.Ptr(acp.ToolCallStatusPending), Title: &title, RawInput: rawInput,
			Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(params.Message))},
		},
		Options: []acp.PermissionOption{
			{OptionId: "accept", Name: "Allow Once", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "accept_session", Name: "Allow for Session", Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: "accept_always", Name: "Always Allow", Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: "decline", Name: "Decline", Kind: acp.PermissionOptionKindRejectOnce},
		},
	})
	if err != nil || response.Outcome.Selected == nil {
		return failClosedMCPServerElicitation()
	}
	switch response.Outcome.Selected.OptionId {
	case "accept":
		return protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionAccept}
	case "accept_session":
		meta, _ := json.Marshal(map[string]any{"persist": "session"})
		return protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionAccept, Meta: meta}
	case "accept_always":
		meta, _ := json.Marshal(map[string]any{"persist": "always"})
		return protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionAccept, Meta: meta}
	default:
		return protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionDecline}
	}
}

// trackPendingURLElicitation 保存已接受且等待上游完成信号的 URL 交互。
func (a *Agent) trackPendingURLElicitation(threadID string, elicitationID acp.UnstableElicitationId) {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	pending := a.pendingURLElicitations[threadID]
	if pending == nil {
		pending = make(map[acp.UnstableElicitationId]struct{})
		a.pendingURLElicitations[threadID] = pending
	}
	pending[elicitationID] = struct{}{}
}

// completePendingURLElicitations 在 serverRequest/resolved 后结束该 thread 的 URL 交互。
func (a *Agent) completePendingURLElicitations(ctx context.Context, threadID string) {
	a.mcpMu.Lock()
	pending := a.pendingURLElicitations[threadID]
	delete(a.pendingURLElicitations, threadID)
	a.mcpMu.Unlock()
	requester := a.currentElicitationRequester()
	if requester == nil {
		return
	}
	for elicitationID := range pending {
		if err := requester.UnstableCompleteElicitation(ctx, acp.UnstableCompleteElicitationNotification{ElicitationId: elicitationID}); err != nil {
			a.logger.Debug("完成 ACP URL elicitation 失败", "thread_id", threadID, "error", err)
		}
	}
}

// userInputOtherFieldID 生成不会撞到真实 question id 的自定义答案字段名。
func userInputOtherFieldID(questionID string, questionIDs map[string]struct{}) string {
	candidate := questionID + userInputOtherSuffix
	for {
		if _, exists := questionIDs[candidate]; !exists {
			return candidate
		}
		candidate += userInputOtherSuffix
	}
}

// stringifyAnswers 按 Codex wire 约定把标量或数组统一为字符串数组。
func stringifyAnswers(value any) []string {
	switch typed := value.(type) {
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, fmt.Sprint(item))
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return []string{fmt.Sprint(value)}
	}
}

// answerHasValue 判断自定义答案是否应覆盖已选选项。
func answerHasValue(value any) bool {
	if text, ok := value.(string); ok {
		return text != ""
	}
	return value != nil
}

// boolValue 将可选布尔值按上游默认 false 读取。
func boolValue(value *bool) bool { return value != nil && *value }

// firstNonEmpty 返回第一个非空展示文本。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// rawMetaRecord 把上游开放 _meta 安全转换为 ACP map，非对象按空元数据处理。
func rawMetaRecord(raw json.RawMessage) map[string]any {
	result := make(map[string]any)
	if len(raw) != 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

// marshalRawMeta 把 ACP 响应元数据恢复为上游开放 JSON；空元数据保持省略。
func marshalRawMeta(meta map[string]any) json.RawMessage {
	if len(meta) == 0 {
		return nil
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return raw
}
