package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	acp "github.com/JieWaZi/acp-go/sdk"
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

type reverseHandler = func(context.Context) (any, *acp.RequestError)

func (c *Client) prepareNativeRequest(_ context.Context, frame acp.ReceiveRequest) (reverseHandler, bool) {
	method := frame.Method
	if len(frame.Params) > maxOpeningBytes {
		return rejectedCallback(acp.NewInvalidParams(nil)), true
	}
	var private *Interaction
	if codec := c.opts.Protocol.Private; codec != nil {
		if interaction, err, handled := codec.DecodeInteraction(method, frame.Params); handled {
			if err != nil {
				return rejectedCallback(safeCallbackError(err, method)), true
			}
			if interaction.Reply == nil || (interaction.Permission == nil) == (interaction.Elicitation == nil) {
				return rejectedCallback(acp.NewInvalidParams(nil)), true
			}
			if followup := interaction.Followup; followup != nil &&
				(followup.Method == "" || followup.Encode == nil || followup.Validate == nil || interaction.Identity.ToolID == "") {
				return rejectedCallback(acp.NewInvalidParams(nil)), true
			}
			private = &interaction
		} else if progress, err, handled := codec.DecodeProgress(method, frame.Params); handled {
			if err != nil {
				return rejectedCallback(safeCallbackError(err, method)), true
			}
			c.foldProgress(progress, len(frame.Params))
			return func(context.Context) (any, *acp.RequestError) { return struct{}{}, nil }, true
		}
	}
	if private == nil {
		switch method {
		case acp.ClientMethodMcpConnect, acp.ClientMethodMcpDisconnect:
			return c.prepareMCP(frame), true
		case acp.ClientMethodSessionRequestPermission, acp.ClientMethodElicitationCreate,
			acp.ClientMethodFsReadTextFile, acp.ClientMethodFsWriteTextFile, acp.ClientMethodTerminalCreate,
			acp.ClientMethodTerminalKill, acp.ClientMethodTerminalOutput, acp.ClientMethodTerminalRelease, acp.ClientMethodTerminalWaitForExit:
		default:
			if !strings.HasPrefix(method, "_") {
				return nil, false
			}
			if c.opts.Protocol.BlockUntypedExtensions {
				return rejectedCallback(acp.NewMethodNotFound(method)), true
			}
		}
	}
	var identity CallbackIdentity
	if private != nil {
		identity = private.Identity
	} else {
		var valid bool
		identity, valid = DecodeCallbackIdentity(frame.Params)
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
	}
	interaction := private != nil || method == acp.ClientMethodSessionRequestPermission || method == acp.ClientMethodElicitationCreate
	inactive := func() (any, *acp.RequestError) {
		if private == nil {
			return inactiveCallback(method)
		}
		value, err := private.Reply.EncodeReply(InteractionReply{})
		if err != nil {
			return nil, safeCallbackError(err, method)
		}
		return value, nil
	}
	c.mu.Lock()
	t := c.resolveCallbackLocked(identity)
	peer, caps := c.peer, c.clientCaps
	wireSession := peer != nil && !interaction && c.wireSessionLocked(identity.SessionID)
	admitted := !c.closed && (t != nil || wireSession) && c.reverseCalls < 128 && c.reverseBytes+len(frame.Params) <= maxObservationBytes
	if admitted && private != nil && private.Followup != nil {
		_, duplicate := t.followups[identity.ToolID]
		admitted = !duplicate
		if admitted {
			if t.followups == nil {
				t.followups = make(map[acp.ToolCallId]struct{})
			}
			t.followups[identity.ToolID] = struct{}{}
		}
	}
	if admitted {
		c.reverseCalls++
		c.reverseBytes += len(frame.Params)
		c.workers.Add(1)
	}
	c.mu.Unlock()
	if !admitted {
		return func(context.Context) (any, *acp.RequestError) { return inactive() }, true
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
			return inactive()
		}
		var value any
		var err error
		if private != nil {
			value, err = c.runPrivateInteraction(call, t, caps, *private)
		} else {
			value, err = c.runCallback(call, t, peer, caps, method, params)
		}
		if call.Err() != nil || ctx.Err() != nil {
			return inactive()
		}
		if err != nil {
			if t != nil && interaction {
				c.mu.Lock()
				c.failDeliveryLocked(t.delivery, err)
				c.mu.Unlock()
			}
			return nil, safeCallbackError(err, method)
		}
		return value, nil
	}, true
}

func (c *Client) runPrivateInteraction(ctx context.Context, t *turn, caps acp.ClientCapabilities, request Interaction) (any, error) {
	var reply InteractionReply
	if request.Permission != nil {
		permission := *request.Permission
		permission.SessionId = t.session.id
		result, err := c.permission(ctx, t, permission)
		if err != nil {
			return nil, err
		}
		reply.Permission = &result
	} else if request.Elicitation != nil {
		result, err := c.elicitation(ctx, t, caps, *request.Elicitation)
		if err != nil {
			return nil, err
		}
		reply.Elicitation = &result
	}
	if followup := request.Followup; followup != nil {
		if err := c.submitInteractionAnswer(ctx, t, followup, reply); err != nil {
			return nil, err
		}
	}
	return request.Reply.EncodeReply(reply)
}

func (c *Client) submitInteractionAnswer(ctx context.Context, t *turn, followup *InteractionFollowup, reply InteractionReply) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := followup.Encode(reply)
	if err != nil {
		return err
	}
	op, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	hooks := boundedWrite(op, t.conn, func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || c.turn != t || t.cancelled || t.terminal {
			return context.Canceled
		}
		return op.Err()
	})
	var ackErr error
	receipt := acp.SendClientRequestWithReceipt[json.RawMessage](t.conn, op, followup.Method, input, hooks,
		func(receipt acp.RequestReceipt[json.RawMessage]) {
			ackErr = receiptFailure(receipt, followup.Method)
			if ackErr == nil {
				ackErr = followup.Validate(receipt.Result)
			}
			if ackErr != nil {
				// An immediately following terminal frame must retain this failure.
				c.mu.Lock()
				if c.turn == t && !t.cancelled && !t.terminal {
					c.failDeliveryLocked(t.delivery, ackErr)
				}
				c.mu.Unlock()
			}
		})
	return errors.Join(receiptFailure(receipt, followup.Method), ackErr, ctx.Err())
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

// DecodeCallbackIdentity reads and checks the standard identity fields shared by native extensions.
func DecodeCallbackIdentity(raw json.RawMessage) (CallbackIdentity, bool) {
	var input struct {
		// SessionID explicitly names the native session.
		SessionID acp.SessionId `json:"sessionId"`
		// ToolID identifies the provider's tool call.
		ToolID acp.ToolCallId `json:"toolCallId"`
		// Meta carries optional duplicates that must agree with top-level values.
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return CallbackIdentity{}, false
	}
	id := CallbackIdentity{SessionID: input.SessionID, ToolID: input.ToolID}
	for _, key := range []string{"sessionId", "toolCallId"} {
		rawValue, exists := input.Meta[key]
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

func (c *Client) resolveCallbackLocked(id CallbackIdentity) *turn {
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
	if codec := c.opts.Protocol.Private; codec != nil && len(frame.Params) <= maxOpeningBytes {
		if progress, err, handled := codec.DecodeProgress(frame.Method, frame.Params); handled {
			if err == nil {
				c.foldProgress(progress, len(frame.Params))
			}
			return
		}
	}
	switch frame.Method {
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

func (c *Client) foldProgress(progress Progress, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.resolveCallbackLocked(progress.Identity)
	if t == nil {
		return
	}
	var update acp.SessionUpdate
	switch {
	case progress.Plan != nil:
		if len(progress.Plan.Entries) > MaxPlanEntries {
			c.failDeliveryLocked(t.delivery, errDeliveryLimit)
			return
		}
		if !progress.Plan.Merge {
			t.session.plan = nil
		}
		for _, item := range progress.Plan.Entries {
			if item.ID == "" {
				continue
			}
			i := slices.IndexFunc(t.session.plan, func(existing PlanItem) bool { return existing.ID == item.ID })
			if i >= 0 {
				t.session.plan[i] = item
			} else if len(t.session.plan) < MaxPlanEntries {
				t.session.plan = append(t.session.plan, item)
			}
		}
		entries := make([]acp.PlanEntry, 0, len(t.session.plan))
		for _, item := range t.session.plan {
			if item.Entry != nil {
				entries = append(entries, *item.Entry)
			}
		}
		update = acp.UpdatePlan(entries...)
	case progress.Update != nil:
		update = *progress.Update
	default:
		return
	}
	t.associated, t.observedOutput = true, true
	c.enqueueLocked(t.delivery, t.scope, acp.SessionNotification{SessionId: t.session.id, Update: update}, size)
}

// HandleExtensionMethod projects the standard model directory and the
// public wire peer's explicitly named extensions.
func (c *Client) HandleExtensionMethod(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case acpmeta.ModelCatalogMethod:
		return c.ReadModels(ctx)
	}
	c.mu.Lock()
	wire := c.peer != nil
	c.mu.Unlock()
	if !wire || c.opts.Protocol.BlockUntypedExtensions || !strings.HasPrefix(method, "_") {
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
