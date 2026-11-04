package nativeacp

import (
	"context"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// toolEvidenceRevision has nonzero size so distinct updates retain distinct pointer identities.
type toolEvidenceRevision struct {
	// marker ensures unique addresses without retaining account or tool content.
	marker byte
}

// requestPermission freezes turn evidence and delegates any private approval policy.
func (agent *Agent) requestPermission(
	ctx context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	agent.awaitToolEvidence(ctx, request)
	agent.mutex.Lock()
	turnOwner := agent.turnOwners[request.SessionId]
	var prompt []acp.ContentBlock
	var turnCtx context.Context
	if turnOwner != nil {
		prompt, turnCtx = turnOwner.prompt, turnOwner.context
	}
	detail := agent.toolDetails[request.ToolCall.ToolCallId]
	owner := agent.toolSessions[request.ToolCall.ToolCallId]
	active := turnOwner != nil
	revision := agent.toolRevisions[request.ToolCall.ToolCallId]
	session, sessionExists := agent.currentSessionLocked(request.SessionId)
	agent.mutex.Unlock()
	if turnCtx != nil {
		scoped, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(turnCtx, cancel)
		defer stop()
		defer cancel()
		ctx = scoped
	}
	cancelled := func() bool {
		return ctx.Err() != nil || (turnCtx != nil && turnCtx.Err() != nil)
	}
	if adapter := agent.config.PermissionAdapter; adapter != nil {
		evidence := PermissionEvidence{
			Request:   request,
			Detail:    detail,
			Prompt:    prompt,
			ToolOwner: owner,
			Active:    active,
			unchanged: func() bool {
				agent.mutex.Lock()
				defer agent.mutex.Unlock()
				current, exists := agent.currentSessionLocked(request.SessionId)
				return sessionExists && exists && current == session && turnCtx != nil && turnCtx.Err() == nil &&
					agent.turnOwners[request.SessionId] == turnOwner &&
					agent.toolSessions[request.ToolCall.ToolCallId] == request.SessionId && revision != nil &&
					agent.toolRevisions[request.ToolCall.ToolCallId] == revision
			},
		}
		response, handled, err := adapter.Review(ctx, agent, evidence)
		// AfterFunc delivery is asynchronous; recheck authoritative cancellation before granting approval.
		if cancelled() {
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
		}
		if err != nil || handled {
			return response, err
		}
	}
	if cancelled() {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	response, err := agent.RequestPermission(ctx, request)
	if cancelled() {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	return response, err
}

// awaitToolEvidence waits briefly for preceding notifications; missing evidence never grants approval.
func (agent *Agent) awaitToolEvidence(ctx context.Context, request acp.RequestPermissionRequest) {
	if agent.config.PermissionAdapter == nil {
		return
	}
	wait, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	for {
		agent.mutex.Lock()
		ready := agent.turnOwners[request.SessionId] == nil || agent.toolSessions[request.ToolCall.ToolCallId] == request.SessionId
		if agent.toolChanged == nil {
			agent.toolChanged = make(chan struct{})
		}
		changed := agent.toolChanged
		agent.mutex.Unlock()
		if ready {
			return
		}
		select {
		case <-changed:
		case <-wait.Done():
			return
		case <-agent.closed:
			return
		}
	}
}
