package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	askQuestion   = "cursor/ask_question"
	createPlan    = "cursor/create_plan"
	updateTodos   = "cursor/update_todos"
	updateTask    = "cursor/task"
	generateImage = "cursor/generate_image"
)

type reverseClient Client

// The receive hook captures ownership before dispatch. The SDK's ordinary
// session notification handler intentionally performs no application work.
func (*reverseClient) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }
func (*reverseClient) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return cancelledPermission(), nil
}
func (*reverseClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsReadTextFile)
}
func (*reverseClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsWriteTextFile)
}
func (*reverseClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalCreate)
}
func (*reverseClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalKill)
}
func (*reverseClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalOutput)
}
func (*reverseClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalRelease)
}
func (*reverseClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalWaitForExit)
}

// callbackIdentity permits explicit session routing or a tool already bound
// to the currently submitted invocation.
type callbackIdentity struct {
	// SessionID is present on the native request or its ACP metadata.
	SessionID acp.SessionId `json:"sessionId"`
	// ToolID refers to a previously observed native tool call.
	ToolID acp.ToolCallId `json:"toolCallId"`
	// Meta can carry the same optional explicit binding.
	Meta map[string]any `json:"_meta"`
}

type questionOption struct {
	// ID is the exact Cursor choice returned with the answer.
	ID string `json:"id"`
	// Label is the provider display text.
	Label string `json:"label"`
}

type question struct {
	// ID is the provider identity returned in questionId.
	ID string `json:"id"`
	// Prompt is the provider question text.
	Prompt string `json:"prompt"`
	// Options is the ordered native choice list.
	Options []questionOption `json:"options"`
	// Multiple allows more than one selected ID.
	Multiple bool `json:"allowMultiple"`
}

type privateInteraction struct {
	// ToolID identifies the plan or question tool.
	ToolID acp.ToolCallId `json:"toolCallId"`
	// Title and Questions describe the native form.
	Title     string     `json:"title"`
	Questions []question `json:"questions"`
	// Name and Plan describe native plan approval.
	Name string `json:"name"`
	Plan string `json:"plan"`
}

type todoEntry struct {
	// ID is the native merge key.
	ID string `json:"id"`
	// Text is the task's visible description.
	Text string `json:"content"`
	// Status is the native task lifecycle value.
	Status string `json:"status"`
}

type privateProgress struct {
	// callbackIdentity associates this progress with a known native invocation.
	callbackIdentity
	// Todos and Merge describe a Cursor plan update.
	Todos []todoEntry `json:"todos"`
	Merge bool        `json:"merge"`
	// Description and FilePath describe task or image progress.
	Description string `json:"description"`
	FilePath    string `json:"filePath"`
}

type reverseHandler = func(context.Context) (any, *acp.RequestError)

func (c *Client) prepareNativeRequest(_ context.Context, frame acp.ReceiveRequest) (reverseHandler, bool) {
	method := frame.Method
	if len(frame.Params) > maxOpeningBytes {
		return rejectedCallback(acp.NewInvalidParams(nil)), true
	}
	switch method {
	case acp.ClientMethodMcpConnect, acp.ClientMethodMcpDisconnect:
		return c.prepareMCP(frame), true
	case updateTodos, updateTask, generateImage:
		c.applyPrivateProgress(method, frame.Params)
		return func(context.Context) (any, *acp.RequestError) { return struct{}{}, nil }, true
	case acp.ClientMethodSessionRequestPermission, acp.ClientMethodElicitationCreate, askQuestion, createPlan,
		acp.ClientMethodFsReadTextFile, acp.ClientMethodFsWriteTextFile, acp.ClientMethodTerminalCreate,
		acp.ClientMethodTerminalKill, acp.ClientMethodTerminalOutput, acp.ClientMethodTerminalRelease, acp.ClientMethodTerminalWaitForExit:
	default:
		if !strings.HasPrefix(method, "_") {
			return nil, false
		}
	}
	identity, valid := decodeIdentity(frame.Params)
	if !valid {
		return rejectedCallback(acp.NewInvalidParams(nil)), true
	}
	if method == acp.ClientMethodSessionRequestPermission {
		var request acp.RequestPermissionRequest
		if json.Unmarshal(frame.Params, &request) != nil {
			return rejectedCallback(acp.NewInvalidParams(nil)), true
		}
		identity.ToolID = request.ToolCall.ToolCallId
	}
	c.mu.Lock()
	t := c.resolveCallbackLocked(identity)
	peer, caps := c.peer, c.clientCaps
	// Non-interactive wire callbacks may serve an explicitly bound session
	// during restore or release, including when no prompt is running.
	wireSession := peer != nil && !isInteraction(method) && c.wireSessionLocked(identity.SessionID)
	admitted := !c.closed && (t != nil || wireSession) && c.reverseCalls < 128 && c.reverseBytes+len(frame.Params) <= maxObservationBytes
	if admitted {
		c.reverseCalls++
		c.reverseBytes += len(frame.Params)
		// This count is released by the dispatched continuation. No callback
		// can register after Close marks the client closed.
		c.workers.Add(1)
	}
	c.mu.Unlock()
	if !admitted {
		return func(context.Context) (any, *acp.RequestError) { return inactiveCallback(method) }, true
	}
	params := slices.Clone(frame.Params)
	return func(ctx context.Context) (any, *acp.RequestError) {
		defer func() {
			c.mu.Lock()
			c.reverseCalls--
			c.reverseBytes -= len(params)
			c.mu.Unlock()
			c.workers.Done()
		}()
		parent := ctx
		if t != nil {
			parent = t.interactions
		}
		call, cancel := context.WithCancel(parent)
		defer cancel()
		if t != nil {
			stop := context.AfterFunc(ctx, cancel)
			defer stop()
		}
		if call.Err() != nil || ctx.Err() != nil {
			return inactiveCallback(method)
		}
		value, err := c.runCallback(call, t, peer, caps, method, params)
		if call.Err() != nil || ctx.Err() != nil {
			return inactiveCallback(method)
		}
		if err != nil {
			c.mu.Lock()
			if t != nil && isInteraction(method) {
				c.failDeliveryLocked(t.delivery, err)
			}
			c.mu.Unlock()
			return nil, safeCallbackError(err, method)
		}
		return value, nil
	}, true
}

func isInteraction(method string) bool {
	switch method {
	case askQuestion, createPlan, acp.ClientMethodElicitationCreate, acp.ClientMethodSessionRequestPermission:
		return true
	default:
		return false
	}
}

func (c *Client) wireSessionLocked(id acp.SessionId) bool {
	if id == "" {
		return false
	}
	if c.sessions[id] != nil {
		return true
	}
	return c.opening != nil && c.opening.started && c.opening.session.id == id
}

func decodeIdentity(raw json.RawMessage) (callbackIdentity, bool) {
	var id callbackIdentity
	if json.Unmarshal(raw, &id) != nil {
		return id, false
	}
	for _, key := range []string{"sessionId", "toolCallId"} {
		rawValue, exists := id.Meta[key]
		if !exists {
			continue
		}
		value, ok := rawValue.(string)
		if !ok {
			return id, false
		}
		if key == "sessionId" {
			if id.SessionID != "" && string(id.SessionID) != value {
				return id, false
			}
			id.SessionID = acp.SessionId(value)
		} else {
			if id.ToolID != "" && string(id.ToolID) != value {
				return id, false
			}
			id.ToolID = acp.ToolCallId(value)
		}
	}
	return id, true
}

func (c *Client) resolveCallbackLocked(id callbackIdentity) *turn {
	t := c.turn
	if c.closed || t == nil || !t.writing || t.cancelled || t.terminal {
		return nil
	}
	if id.SessionID != "" {
		if id.SessionID != t.session.id || id.ToolID != "" && !rememberTool(t, id.ToolID) {
			return nil
		}
		return t
	}
	if t.session.toolOwners[id.ToolID] == t.scope.NativeInvocationID {
		return t
	}
	return nil
}

func inactiveCallback(method string) (any, *acp.RequestError) {
	switch method {
	case askQuestion, createPlan:
		return cursorDecision("cancelled"), nil
	case acp.ClientMethodSessionRequestPermission:
		return cancelledPermission(), nil
	case acp.ClientMethodElicitationCreate:
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func rejectedCallback(err *acp.RequestError) reverseHandler {
	return func(context.Context) (any, *acp.RequestError) { return nil, err }
}

func safeCallbackError(err error, method string) *acp.RequestError {
	var protocol *acp.RequestError
	if errors.As(err, &protocol) {
		return safePeerError(protocol, method)
	}
	return acp.NewInternalError(map[string]any{"method": method})
}

func cancelledPermission() acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
}

func (c *Client) runCallback(ctx context.Context, t *turn, peer *acp.AgentSideConnection, caps acp.ClientCapabilities, method string, params json.RawMessage) (any, error) {
	switch method {
	case acp.ClientMethodSessionRequestPermission:
		var request acp.RequestPermissionRequest
		if json.Unmarshal(params, &request) != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		return c.permission(ctx, t, request)
	case acp.ClientMethodElicitationCreate:
		var request acp.UnstableCreateElicitationRequest
		if json.Unmarshal(params, &request) != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		return c.elicitation(ctx, t, caps, request)
	case askQuestion, createPlan:
		var request privateInteraction
		if json.Unmarshal(params, &request) != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		if method == createPlan {
			return c.planApproval(ctx, t, request)
		}
		return c.questionForm(ctx, t, caps, request)
	}
	if peer == nil {
		return nil, acp.NewMethodNotFound(method)
	}
	switch method {
	case acp.ClientMethodFsReadTextFile:
		if caps.Fs.ReadTextFile {
			return forward(ctx, params, peer.ReadTextFile)
		}
	case acp.ClientMethodFsWriteTextFile:
		if caps.Fs.WriteTextFile {
			return forward(ctx, params, peer.WriteTextFile)
		}
	case acp.ClientMethodTerminalCreate:
		if caps.Terminal {
			return forward(ctx, params, peer.CreateTerminal)
		}
	case acp.ClientMethodTerminalKill:
		if caps.Terminal {
			return forward(ctx, params, peer.KillTerminal)
		}
	case acp.ClientMethodTerminalOutput:
		if caps.Terminal {
			return forward(ctx, params, peer.TerminalOutput)
		}
	case acp.ClientMethodTerminalRelease:
		if caps.Terminal {
			return forward(ctx, params, peer.ReleaseTerminal)
		}
	case acp.ClientMethodTerminalWaitForExit:
		if caps.Terminal {
			return forward(ctx, params, peer.WaitForTerminalExit)
		}
	default:
		return peer.CallExtension(ctx, method, params)
	}
	return nil, acp.NewMethodNotFound(method)
}

func forward[P, R any](ctx context.Context, params json.RawMessage, call func(context.Context, P) (R, error)) (R, error) {
	var request P
	if json.Unmarshal(params, &request) != nil {
		var zero R
		return zero, acp.NewInvalidParams(nil)
	}
	if valid, ok := any(&request).(interface{ Validate() error }); ok && valid.Validate() != nil {
		var zero R
		return zero, acp.NewInvalidParams(nil)
	}
	return call(ctx, request)
}

func (c *Client) permission(ctx context.Context, t *turn, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if c.host.RequestPermission == nil || request.SessionId != t.session.id {
		return cancelledPermission(), nil
	}
	response, err := c.host.RequestPermission(ctx, t.scope, request)
	if err == nil && (response.Outcome.Selected == nil || !approvalOption(request, response.Outcome.Selected.OptionId)) {
		return cancelledPermission(), nil
	}
	return response, err
}

func (c *Client) elicitation(ctx context.Context, t *turn, caps acp.ClientCapabilities, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	if c.host.CreateElicitation == nil || caps.Elicitation == nil {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	switch {
	case request.Form != nil && caps.Elicitation.Form != nil:
		request.Form.Meta = copyMeta(request.Form.Meta)
		request.Form.Meta["sessionId"] = string(t.session.id)
	case request.Url != nil && caps.Elicitation.Url != nil && c.host.CompleteElicitation != nil:
		request.Url.Meta = copyMeta(request.Url.Meta)
		request.Url.Meta["sessionId"] = string(t.session.id)
		id := request.Url.ElicitationId
		c.mu.Lock()
		_, duplicate := t.elicitations[id]
		if t.interactions.Err() != nil || id == "" || duplicate || len(t.elicitations) >= 256 {
			c.mu.Unlock()
			return acp.NewUnstableCreateElicitationResponseCancel(), nil
		}
		if t.elicitations == nil {
			t.elicitations = make(map[acp.UnstableElicitationId]struct{})
		}
		t.elicitations[id] = struct{}{}
		c.mu.Unlock()
		response, err := c.host.CreateElicitation(ctx, t.scope, request)
		if err != nil || response.Accept == nil {
			c.mu.Lock()
			delete(t.elicitations, id)
			c.mu.Unlock()
		}
		return response, err
	default:
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	return c.host.CreateElicitation(ctx, t.scope, request)
}

func (c *Client) planApproval(ctx context.Context, t *turn, request privateInteraction) (any, error) {
	title := request.Name
	if strings.TrimSpace(title) == "" {
		title = "Review plan"
	}
	response, err := c.permission(ctx, t, acp.RequestPermissionRequest{
		SessionId: t.session.id,
		ToolCall:  acp.ToolCallUpdate{ToolCallId: request.ToolID, Title: &title, Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(request.Plan))}},
		Options: []acp.PermissionOption{
			{OptionId: "accept", Name: "Accept plan", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "reject", Name: "Reject plan", Kind: acp.PermissionOptionKindRejectOnce},
		},
	})
	if err != nil {
		return nil, err
	}
	if response.Outcome.Selected != nil {
		if response.Outcome.Selected.OptionId == "accept" {
			return cursorDecision("accepted"), nil
		}
		return cursorDecision("rejected"), nil
	}
	return cursorDecision("cancelled"), nil
}

func (c *Client) questionForm(ctx context.Context, t *turn, caps acp.ClientCapabilities, request privateInteraction) (any, error) {
	if len(request.Questions) == 0 || len(request.Questions) > 64 {
		return nil, acp.NewInvalidParams(nil)
	}
	properties := make(map[string]any, len(request.Questions))
	required := make([]string, 0, len(request.Questions))
	for _, q := range request.Questions {
		if q.ID == "" || len(q.Options) == 0 || len(q.Options) > 128 || properties[q.ID] != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		ids, labels := make([]string, 0, len(q.Options)), make([]string, 0, len(q.Options))
		for _, option := range q.Options {
			if option.ID == "" || slices.Contains(ids, option.ID) {
				return nil, acp.NewInvalidParams(nil)
			}
			ids, labels = append(ids, option.ID), append(labels, option.Label)
		}
		choices := map[string]any{"type": "string", "enum": ids, "enumNames": labels}
		choices["title"] = q.Prompt
		if q.Multiple {
			choices = map[string]any{"type": "array", "title": q.Prompt, "items": choices, "minItems": 1, "uniqueItems": true}
		}
		properties[q.ID] = choices
		required = append(required, q.ID)
	}
	title := request.Title
	if strings.TrimSpace(title) == "" {
		title = "Input requested"
	}
	response, err := c.elicitation(ctx, t, caps, acp.UnstableCreateElicitationRequest{Form: &acp.UnstableCreateElicitationForm{
		Mode: "form", Message: title, Meta: map[string]any{"toolCallId": string(request.ToolID)},
		RequestedSchema: acp.UnstableElicitationSchema{Properties: properties, Required: required},
	}})
	if err != nil {
		return nil, err
	}
	if response.Decline != nil {
		return cursorDecision("skipped"), nil
	}
	if response.Accept == nil {
		return cursorDecision("cancelled"), nil
	}
	answers := make([]map[string]any, 0, len(request.Questions))
	for _, q := range request.Questions {
		values := selectedAnswers(response.Accept.Content[q.ID])
		if len(values) == 0 || !q.Multiple && len(values) != 1 {
			return cursorDecision("cancelled"), nil
		}
		seen := make(map[string]bool, len(values))
		for _, id := range values {
			if seen[id] || !slices.ContainsFunc(q.Options, func(option questionOption) bool { return option.ID == id }) {
				return cursorDecision("cancelled"), nil
			}
			seen[id] = true
		}
		answers = append(answers, map[string]any{"questionId": q.ID, "selectedOptionIds": values})
	}
	return map[string]any{"outcome": map[string]any{"outcome": "answered", "answers": answers}}, nil
}

func selectedAnswers(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil
			}
			values = append(values, text)
		}
		return values
	default:
		return nil
	}
}

func cursorDecision(value string) map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": value}}
}

func (c *Client) prepareMCP(frame acp.ReceiveRequest) reverseHandler {
	method := frame.Method
	c.mu.Lock()
	defer c.mu.Unlock()
	peer := c.peer
	if peer == nil || c.closed {
		return rejectedCallback(acp.NewMethodNotFound(method))
	}
	if method == acp.ClientMethodMcpConnect {
		var request acp.UnstableConnectMcpRequest
		if json.Unmarshal(frame.Params, &request) != nil || request.Validate() != nil {
			return rejectedCallback(acp.NewInvalidParams(nil))
		}
		allowed := c.opening != nil && c.opening.started && slices.Contains(c.opening.session.mcpIDs, request.AcpId)
		for _, s := range c.sessions {
			allowed = allowed || slices.Contains(s.mcpIDs, request.AcpId)
		}
		if !allowed || len(c.mcpConnections) >= 256 {
			return rejectedCallback(acp.NewInvalidParams(nil))
		}
		c.workers.Add(1)
		return func(ctx context.Context) (any, *acp.RequestError) {
			defer c.workers.Done()
			response, err := peer.UnstableConnectMcp(ctx, request)
			if err != nil {
				return nil, safeCallbackError(err, method)
			}
			c.mu.Lock()
			if c.mcpConnections == nil {
				c.mcpConnections = make(map[acp.UnstableMcpConnectionId]struct{})
			}
			c.mcpConnections[response.ConnectionId] = struct{}{}
			c.mu.Unlock()
			return response, nil
		}
	}
	var request acp.UnstableDisconnectMcpRequest
	if json.Unmarshal(frame.Params, &request) != nil || request.Validate() != nil {
		return rejectedCallback(acp.NewInvalidParams(nil))
	}
	if _, known := c.mcpConnections[request.ConnectionId]; !known {
		return rejectedCallback(acp.NewInvalidParams(nil))
	}
	c.workers.Add(1)
	return func(ctx context.Context) (any, *acp.RequestError) {
		defer c.workers.Done()
		response, err := peer.UnstableDisconnectMcp(ctx, request)
		if err != nil {
			return nil, safeCallbackError(err, method)
		}
		c.mu.Lock()
		delete(c.mcpConnections, request.ConnectionId)
		c.mu.Unlock()
		return response, nil
	}
}

func (c *Client) receivePrivateNotification(frame acp.ReceiveNotification) {
	switch frame.Method {
	case updateTodos, updateTask, generateImage:
		c.applyPrivateProgress(frame.Method, frame.Params)
	case acp.ClientMethodElicitationComplete:
		var notification acp.UnstableCompleteElicitationNotification
		if json.Unmarshal(frame.Params, &notification) != nil {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		t := c.turn
		if t == nil || t.terminal || c.host.CompleteElicitation == nil {
			return
		}
		if _, owned := t.elicitations[notification.ElicitationId]; owned {
			delete(t.elicitations, notification.ElicitationId)
			c.enqueueActionLocked(t.delivery, t.scope, len(frame.Params), func(ctx context.Context) error {
				return c.host.CompleteElicitation(ctx, t.scope, notification)
			})
		}
	default:
		if strings.HasPrefix(frame.Method, "_") {
			c.mu.Lock()
			defer c.mu.Unlock()
			if peer := c.peer; peer != nil && !c.closed {
				raw := slices.Clone(frame.Params)
				c.enqueueActionLocked(nil, agentclient.Scope{}, len(raw), func(ctx context.Context) error {
					return peer.NotifyExtension(ctx, frame.Method, json.RawMessage(raw))
				})
			}
		}
	}
}

func (c *Client) applyPrivateProgress(method string, raw json.RawMessage) {
	if len(raw) > maxOpeningBytes {
		return
	}
	identity, ok := decodeIdentity(raw)
	var progress privateProgress
	if !ok || json.Unmarshal(raw, &progress) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.resolveCallbackLocked(identity)
	if t == nil {
		return
	}
	var update acp.SessionUpdate
	if method == updateTodos {
		if len(progress.Todos) > 256 {
			c.failDeliveryLocked(t.delivery, errDeliveryLimit)
			return
		}
		if !progress.Merge {
			t.session.todos = nil
		}
		for _, item := range progress.Todos {
			if item.ID == "" {
				continue
			}
			i := slices.IndexFunc(t.session.todos, func(todo todoEntry) bool { return todo.ID == item.ID })
			if i >= 0 {
				t.session.todos[i] = item
			} else if len(t.session.todos) < 256 {
				t.session.todos = append(t.session.todos, item)
			}
		}
		entries := make([]acp.PlanEntry, 0, len(t.session.todos))
		for _, todo := range t.session.todos {
			status, content := acp.PlanEntryStatus(todo.Status), todo.Text
			if todo.Status == "cancelled" {
				status, content = acp.PlanEntryStatusCompleted, "[Cancelled] "+content
			}
			if status == acp.PlanEntryStatusPending || status == acp.PlanEntryStatusInProgress || status == acp.PlanEntryStatusCompleted {
				entries = append(entries, acp.PlanEntry{Status: status, Content: content, Priority: acp.PlanEntryPriorityMedium})
			}
		}
		update = acp.UpdatePlan(entries...)
	} else {
		if identity.ToolID == "" {
			return
		}
		var locations []acp.ToolCallLocation
		if progress.FilePath != "" {
			locations = []acp.ToolCallLocation{{Path: progress.FilePath}}
		}
		update = acp.UpdateToolCall(identity.ToolID, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(progress.Description))}), acp.WithUpdateLocations(locations))
	}
	t.associated, t.observedOutput = true, true
	c.enqueueLocked(t.delivery, t.scope, acp.SessionNotification{SessionId: t.session.id, Update: update}, len(raw))
}

// HandleExtensionMethod projects only Cursor management operations and the
// public wire peer's explicitly named extensions.
func (c *Client) HandleExtensionMethod(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case acpmeta.ModelCatalogMethod:
		return c.ReadModels(ctx)
	case acpmeta.AccountReadMethod:
		return c.ReadAccount(ctx)
	case acpmeta.AccountUsageMethod:
		return c.ReadAccountUsage(ctx)
	}
	c.mu.Lock()
	wire := c.peer != nil
	c.mu.Unlock()
	if !wire || !strings.HasPrefix(method, "_") {
		return nil, acp.NewMethodNotFound(method)
	}
	conn, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.end()
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	r := send[json.RawMessage](conn, op, method, raw)
	return r.Result, receiptFailure(r, method)
}
