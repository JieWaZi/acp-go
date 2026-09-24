package codex

import (
	"context"
	"errors"
	"fmt"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// UnstableForkSession 通过 thread/fork 保留原生上下文，并绑定独立工作区。
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
	if err = acpmeta.BeginForkReceipt(request); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	mcpAfterVersion := a.currentMCPStatusVersion()
	response, err := a.client.ThreadFork(ctx, protocol.ThreadForkParams{
		ThreadID:   string(request.SessionId),
		LastTurnID: optionalForkPosition(request.Meta),
		Cwd:        &workspace.CWD,
		Config:     config,
	})
	if err != nil {
		return acp.UnstableForkSessionResponse{}, fmt.Errorf("starting codex thread: %w", err)
	}
	if response.Thread.ID == "" || response.Thread.ID == string(request.SessionId) {
		return acp.UnstableForkSessionResponse{}, errors.New("starting codex thread: empty thread id")
	}
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(response.Thread.ID)); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	configuration, err := a.configurationForSession(ctx, response.Model, response.ReasoningEffort)
	if err != nil {
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		unsubscribeErr := a.client.ThreadUnsubscribe(cleanupCtx, response.Thread.ID)
		return acp.UnstableForkSessionResponse{}, fmt.Errorf(
			"configuring new codex thread %q: %w",
			response.Thread.ID,
			errors.Join(err, unsubscribeErr),
		)
	}
	generation, err := a.sessions.beginOpen(response.Thread.ID)
	if err != nil {
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		return acp.UnstableForkSessionResponse{}, errors.Join(err, a.client.ThreadUnsubscribe(cleanupCtx, response.Thread.ID))
	}
	state, installed := a.sessions.installWorkspace(
		response.Thread.ID,
		workspace,
		generation,
		configuration,
		a.currentTerminalOutputMode(),
	)
	if !installed {
		return acp.UnstableForkSessionResponse{}, a.closeStaleOpen(ctx, response.Thread.ID, generation)
	}
	a.publishKnownMCPStartupFailures(state, mcpNames, mcpAfterVersion)
	modes, options := sessionConfigurationResponse(state)
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(response.Thread.ID)); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(request.SessionId, acp.NewSessionResponse{
		SessionId:     acp.SessionId(response.Thread.ID),
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
