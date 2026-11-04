package codex

import (
	"context"
	"errors"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// UnstableForkSession forks native context into an independently configured workspace.
func (a *Agent) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(request) {
		id, err := acpmeta.ReadForkReceipt(request)
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}

	if err := a.requireInitialized(); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err := a.checkAuthorization(ctx); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	instructions, err := acpmeta.DeveloperInstructions(request.Meta)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	workspace, err := normalizeCodexWorkspace(request.Cwd, request.AdditionalDirectories)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if err = a.refreshSkills(ctx, workspace); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	servers, err := acpmeta.ForkMCPServers(request.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	config, mcpNames, err := a.sessionConfig(ctx, workspace, servers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	settings, err := parseOpenSettings(request.Meta)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	settings.applyOpenEffort(config)
	if err = acpmeta.BeginForkReceipt(request); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	mcpAfterVersion := a.currentMCPStatusVersion()
	open := a.sessions.beginUnidentifiedOpen()
	defer a.sessions.abandonUnidentifiedOpen(open)
	response, err := a.client.ThreadFork(ctx, protocol.ThreadForkParams{
		ThreadID:              string(request.SessionId),
		LastTurnID:            optionalForkPosition(request.Meta),
		DeveloperInstructions: instructions,
		Model:                 settings.Model,
		ServiceTier:           settings.ServiceTier,
		Cwd:                   &workspace.CWD,
		Config:                config,
	})
	if err != nil {
		return acp.UnstableForkSessionResponse{}, codexOperationFailure("thread_fork", err, nil)
	}
	if response.Thread.ID == "" || response.Thread.ID == string(request.SessionId) {
		return acp.UnstableForkSessionResponse{}, errors.New("starting codex thread: empty thread id")
	}
	generation, err := a.sessions.identifyOpen(open, response.Thread.ID)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(response.Thread.ID)); err != nil {
		return acp.UnstableForkSessionResponse{}, a.cleanupFailedSubscribedOpen(ctx, response.Thread.ID, generation, err)
	}
	state, err := a.installOpenedSession(ctx, response.Thread.ID, generation, workspace, settings,
		"thread_fork", response.Model, response.ReasoningEffort, response.ServiceTier, false)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	a.publishKnownMCPStartupFailures(state, mcpNames, mcpAfterVersion)
	modes, options, meta := sessionConfigurationResponse(state)
	return acpmeta.ForkResponse(request.SessionId, acp.NewSessionResponse{
		SessionId:     acp.SessionId(response.Thread.ID),
		Meta:          meta,
		Modes:         modes,
		ConfigOptions: options,
	})
}

// optionalForkPosition 保持末尾分叉与精确定位的 wire 区别。
func optionalForkPosition(meta map[string]any) *string {
	value := acpmeta.ForkPosition(meta)
	if value == "" {
		return nil
	}
	return &value
}
