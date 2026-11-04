package nativeacp

import (
	"context"
	"encoding/json"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"strings"
	"sync"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// sessionOptions maps normalized host configuration to native session state.
type sessionOptions struct {
	// transitioning excludes concurrent lifecycle RPCs and prompt admission.
	transitioning bool
	// workingDirectory retains the observed session cwd for permission evidence.
	workingDirectory string
	// queue owns prompt admission for this opened session generation.
	queue *promptQueue
	// configMutex serializes configuration writes that require multiple native calls.
	configMutex sync.Mutex
	// ids maps normalized categories to native configuration IDs.
	ids map[acp.SessionConfigId]acp.SessionConfigId
	// options retains the last observed configuration catalog.
	options []acp.SessionConfigOption
	// models retains the legacy native model catalog for this generation.
	models json.RawMessage
	// messageID identifies the current contiguous assistant message segment.
	messageID string
	// messageInfo preserves an explicit native phase across metadata-free deltas.
	messageInfo acpmeta.MessageInfo
	// messageKnownID remembers an explicitly phased native identity across tool activity.
	messageKnownID string
	// messageKnownInfo restores phase metadata only for the matching native identity.
	messageKnownInfo acpmeta.MessageInfo
}

// nativeSessionResponse accepts native implementations still returning a models catalog.
type nativeSessionResponse struct {
	// NewSessionResponse embeds the standard session response.
	acp.NewSessionResponse
	// Models retains native model metadata not yet represented as configOptions.
	Models json.RawMessage `json:"models,omitempty"`
}

// NewSession opens a native session and normalizes its model and reasoning catalog.
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if request.McpServers == nil {
		request.McpServers = []acp.McpServer{}
	}
	response, err := sendNativeRequest[nativeSessionResponse](agent, ctx, "session/new", request)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err = agent.normalizeSession(ctx, &response, response.SessionId, request.Cwd); err != nil {
		return acp.NewSessionResponse{}, err
	}
	return response.NewSessionResponse, nil
}

// LoadSession restores native history and its observed configuration catalog.
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	release, err := agent.beginSessionTransition(request.SessionId, false)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	defer release()
	if request.McpServers == nil {
		request.McpServers = []acp.McpServer{}
	}
	response, err := sendNativeRequest[nativeSessionResponse](agent, ctx, "session/load", request)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err = agent.normalizeSession(ctx, &response, request.SessionId, request.Cwd); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	return acp.LoadSessionResponse{Meta: response.Meta, ConfigOptions: response.ConfigOptions, Modes: response.Modes}, nil
}

// ResumeSession invokes native resume without replaying history.
func (agent *Agent) ResumeSession(
	ctx context.Context,
	request acp.ResumeSessionRequest,
) (acp.ResumeSessionResponse, error) {
	release, err := agent.beginSessionTransition(request.SessionId, false)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	defer release()
	response, err := sendNativeRequest[nativeSessionResponse](agent, ctx, "session/resume", request)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if err = agent.normalizeSession(ctx, &response, request.SessionId, request.Cwd); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{Meta: response.Meta, ConfigOptions: response.ConfigOptions, Modes: response.Modes}, nil
}

// beginSessionTransition reserves the native identity before issuing lifecycle RPCs.
// An active execution cannot be silently replaced by a load or resume response.
func (agent *Agent) beginSessionTransition(id acp.SessionId, closing bool) (func(), error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	state := agent.sessions[id]
	if state != nil && state.transitioning {
		return nil, acp.NewInvalidParams(map[string]any{"message": "session lifecycle operation is already active"})
	}
	if !closing && agent.turnOwners[id] != nil {
		return nil, acp.NewInvalidParams(map[string]any{"message": "cannot restore an active native execution"})
	}
	if state == nil {
		state = &sessionOptions{}
		agent.sessions[id] = state
	}
	state.transitioning = true
	if state.queue != nil {
		state.queue.close()
	}
	return func() {
		agent.mutex.Lock()
		defer agent.mutex.Unlock()
		if agent.sessions[id] == state {
			state.transitioning = false
		}
	}, nil
}

// normalizeSession installs configuration and admission state for the opened generation.
func (agent *Agent) normalizeSession(
	ctx context.Context,
	response *nativeSessionResponse,
	id acp.SessionId,
	cwd string,
) error {
	state := &sessionOptions{workingDirectory: cwd, ids: make(map[acp.SessionConfigId]acp.SessionConfigId)}
	state.models = append(json.RawMessage{}, response.Models...)
	if agent.config.SessionAdapter != nil {
		snapshot := SessionSnapshot{
			ConfigOptions:    response.ConfigOptions,
			Modes:            response.Modes,
			Models:           response.Models,
			WorkingDirectory: cwd,
		}
		options, err := agent.config.SessionAdapter.NormalizeSession(ctx, agent, id, snapshot)
		if err != nil {
			return err
		}
		response.ConfigOptions = options
	} else {
		response.ConfigOptions = NormalizeOptions(response.ConfigOptions, state.ids)
	}
	response.ConfigOptions = agent.enrichConfigOptions(state, response.ConfigOptions)
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	state.options = response.ConfigOptions
	if old := agent.sessions[id]; old != nil && old.queue != nil {
		old.queue.close()
	}
	state.queue = &promptQueue{}
	agent.sessions[id] = state
	return nil
}

// NormalizeOptions changes category identifiers without changing native option values.
func NormalizeOptions(
	options []acp.SessionConfigOption,
	ids map[acp.SessionConfigId]acp.SessionConfigId,
) []acp.SessionConfigOption {
	result := make([]acp.SessionConfigOption, 0, len(options))
	for _, option := range options {
		if option.Select != nil {
			copy := *option.Select
			original := copy.Id
			category := ""
			if copy.Category != nil {
				category = string(*copy.Category)
			}
			switch {
			case category == "model" || original == "model":
				copy.Id = "model"
			case category == "thought_level" ||
				original == "thinking" ||
				original == "thought_level" ||
				original == "reasoning_effort":
				copy.Id = "reasoning"
			}
			ids[copy.Id] = original
			option.Select = &copy
		}
		result = append(result, option)
	}
	return result
}

// SetSessionConfigOption resolves normalized identifiers while preserving native model values.
func (agent *Agent) SetSessionConfigOption(
	ctx context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil {
		return sendNativeRequest[acp.SetSessionConfigOptionResponse](agent, ctx, "session/set_config_option", request)
	}
	value := *request.ValueId
	agent.mutex.Lock()
	state := agent.sessions[value.SessionId]
	if state == nil {
		agent.mutex.Unlock()
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	agent.mutex.Unlock()
	state.configMutex.Lock()
	defer state.configMutex.Unlock()
	if agent.config.SessionAdapter != nil {
		response, handled, err := agent.config.SessionAdapter.SetConfigOption(ctx, agent, request)
		if handled {
			if err == nil {
				agent.mutex.Lock()
				state.options = append([]acp.SessionConfigOption(nil), response.ConfigOptions...)
				agent.mutex.Unlock()
			}
			return response, err
		}
	}
	agent.mutex.Lock()
	nativeID, ok := state.ids[value.ConfigId]
	agent.mutex.Unlock()
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{
			"message": "configuration not advertised by agent",
		})
	}
	value.ConfigId = nativeID
	request.ValueId = &value
	response, err := sendNativeRequest[acp.SetSessionConfigOptionResponse](agent, ctx, "session/set_config_option", request)
	if err != nil {
		return response, err
	}
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	response.ConfigOptions = NormalizeOptions(response.ConfigOptions, state.ids)
	response.ConfigOptions = agent.enrichConfigOptions(state, response.ConfigOptions)
	state.options = response.ConfigOptions
	return response, nil
}

// normalizeUpdate records tool ownership and updates the observed catalog.
func (agent *Agent) normalizeUpdate(request *acp.SessionNotification) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	agent.normalizeMessageLocked(request)
	if update := request.Update.ToolCall; update != nil {
		agent.toolRevisions[update.ToolCallId] = &toolEvidenceRevision{}
		agent.toolSessions[update.ToolCallId] = request.SessionId
		agent.toolDetails[update.ToolCallId] = acp.ToolCallUpdate{
			ToolCallId: update.ToolCallId,
			Title:      acp.Ptr(update.Title),
			Kind:       acp.Ptr(update.Kind),
			RawInput:   update.RawInput,
			Content:    update.Content,
		}
	}
	if update := request.Update.ToolCallUpdate; update != nil {
		agent.toolRevisions[update.ToolCallId] = &toolEvidenceRevision{}
		agent.toolSessions[update.ToolCallId] = request.SessionId
		previous := agent.toolDetails[update.ToolCallId]
		if update.RawInput != nil {
			previous.RawInput = update.RawInput
		}
		if update.Title != nil {
			previous.Title = update.Title
		}
		if update.Kind != nil {
			previous.Kind = update.Kind
		}
		if update.Content != nil {
			previous.Content = update.Content
		}
		previous.ToolCallId = update.ToolCallId
		agent.toolDetails[update.ToolCallId] = previous
	}
	if update := request.Update.ConfigOptionUpdate; update != nil {
		if state := agent.sessions[request.SessionId]; state != nil {
			if agent.config.SessionAdapter != nil {
				update.ConfigOptions = agent.config.SessionAdapter.NormalizeUpdate(request.SessionId, update.ConfigOptions)
			} else {
				update.ConfigOptions = NormalizeOptions(update.ConfigOptions, state.ids)
			}
			update.ConfigOptions = agent.enrichConfigOptions(state, update.ConfigOptions)
			state.options = update.ConfigOptions
		}
	}
	if request.Update.ToolCall != nil || request.Update.ToolCallUpdate != nil {
		if agent.toolChanged != nil {
			close(agent.toolChanged)
		}
		agent.toolChanged = make(chan struct{})
	}
}

// enrichConfigOptions supplies an immutable catalog snapshot to the configured pure projection.
func (agent *Agent) enrichConfigOptions(
	state *sessionOptions,
	options []acp.SessionConfigOption,
) []acp.SessionConfigOption {
	if agent.config.EnrichConfigOptions == nil {
		return options
	}
	return agent.config.EnrichConfigOptions(SessionSnapshot{
		ConfigOptions:    options,
		Models:           append(json.RawMessage{}, state.models...),
		WorkingDirectory: state.workingDirectory,
	})
}

// interactionSession resolves missing session identities only when ownership is unambiguous.
func (agent *Agent) interactionSession(toolID string, explicit string) acp.SessionId {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	return agent.interactionSessionLocked(toolID, explicit)
}

// interactionSessionLocked resolves the identity while the caller holds the ownership lock.
func (agent *Agent) interactionSessionLocked(toolID string, explicit string) acp.SessionId {
	if explicit != "" {
		if agent.turnOwners[acp.SessionId(explicit)] != nil {
			if owner := agent.toolSessions[acp.ToolCallId(toolID)]; owner != "" && owner != acp.SessionId(explicit) {
				return ""
			}
			return acp.SessionId(explicit)
		}
		return ""
	}
	if sid := agent.toolSessions[acp.ToolCallId(toolID)]; sid != "" {
		if agent.turnOwners[sid] != nil {
			return sid
		}
		return ""
	}
	if len(agent.turnOwners) == 1 {
		for sid := range agent.turnOwners {
			return sid
		}
	}
	return ""
}

// cleanMessage selects the first nonempty interaction description.
func cleanMessage(parts ...string) string {
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			return part
		}
	}
	return "Input requested"
}
