package openclaw

import (
	"context"
	"errors"
	"slices"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

const (
	modelOptionID   = "model"
	thoughtOptionID = "thought_level"
)

type gatewayModel struct {
	// ID is the provider-relative native model identifier.
	ID string `json:"id"`
	// Provider routes the model to a specific OpenClaw provider.
	Provider string `json:"provider"`
	// Name is the model presentation label from the Gateway.
	Name string `json:"name"`
	// Available reports known account or provider readiness when published.
	Available *bool `json:"available"`
	// ManualSelectionAllowed reports the authenticated principal's selection policy when published.
	ManualSelectionAllowed *bool `json:"manualSelectionAllowed"`
	// ThinkingLevels lists the model's native supported thought levels when known.
	ThinkingLevels []struct {
		// ID is the value accepted by the native thought-level setter.
		ID acp.SessionConfigValueId `json:"id"`
		// Label is the Gateway's presentation label for this level.
		Label string `json:"label"`
	} `json:"thinkingLevels"`
	// ThinkingDefault is the model's configured default thought level when published.
	ThinkingDefault acp.SessionConfigValueId `json:"thinkingDefault"`
}

type modelIdentity struct {
	// Model contains the native provider-relative ID.
	Model string `json:"model"`
	// Provider identifies the configured model route.
	Provider string `json:"modelProvider"`
}

func modelID(provider, model string) acp.SessionConfigValueId {
	if provider == "" || model == "" || strings.TrimSpace(provider) != provider || strings.TrimSpace(model) != model ||
		strings.Contains(provider, "/") {
		return ""
	}
	return acp.SessionConfigValueId(provider + "/" + strings.TrimPrefix(model, provider+"/"))
}

func withModelOption(options []acp.SessionConfigOption, model acp.SessionConfigValueId) []acp.SessionConfigOption {
	options = slices.DeleteFunc(slices.Clone(options), func(option acp.SessionConfigOption) bool {
		return option.Select != nil && (option.Select.Id == modelOptionID ||
			option.Select.Category != nil && *option.Select.Category == acp.SessionConfigOptionCategoryModel)
	})
	if model == "" {
		return options
	}
	choices := acp.SessionConfigSelectOptionsUngrouped{{Value: model, Name: string(model)}}
	return append([]acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: modelOptionID, Name: "Model", Type: "select", Category: acp.Ptr(acp.SessionConfigOptionCategoryModel),
		CurrentValue: model, Options: acp.SessionConfigSelectOptions{Ungrouped: &choices},
	}}}, options...)
}

func (m gatewayModel) thinkingCatalog() map[string]any {
	if m.ThinkingLevels == nil {
		return nil
	}
	if len(m.ThinkingLevels) == 0 {
		return acpmeta.WithModelConfigOptions(nil, nil)
	}
	choices := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(m.ThinkingLevels))
	seen := make(map[acp.SessionConfigValueId]bool, len(m.ThinkingLevels))
	for _, level := range m.ThinkingLevels {
		if id := string(level.ID); id == "" || strings.TrimSpace(id) != id || seen[level.ID] {
			return nil
		}
		seen[level.ID] = true
		name := strings.TrimSpace(level.Label)
		if name == "" {
			name = string(level.ID)
		}
		choices = append(choices, acp.SessionConfigSelectOption{Value: level.ID, Name: name})
	}
	option := &acp.SessionConfigOptionSelect{
		Id: thoughtOptionID, Name: "Thought level", Type: "select", Category: acp.Ptr(acp.SessionConfigOptionCategoryThoughtLevel),
		Options: acp.SessionConfigSelectOptions{Ungrouped: &choices},
	}
	if seen[m.ThinkingDefault] {
		option.CurrentValue = m.ThinkingDefault
	}
	return acpmeta.WithModelConfigOptions(nil, []acp.SessionConfigOption{{Select: option}})
}

// ReadModels uses the Gateway management directory without creating a session or starting a prompt.
func (c *Client) ReadModels(ctx context.Context) (acpmeta.ModelCatalog, error) {
	if err := c.admit(ctx); err != nil {
		return acpmeta.ModelCatalog{}, err
	}
	defer c.op.Unlock()
	op, cancel := c.operationContext(ctx)
	defer cancel()
	var response struct {
		// Models is the native model catalog permitted for the authenticated caller.
		Models []gatewayModel `json:"models"`
	}
	if err := c.gateway.call(op, "models.list", map[string]any{}, &response); err != nil {
		return acpmeta.ModelCatalog{}, err
	}
	if response.Models == nil {
		return acpmeta.ModelCatalog{}, errors.New("OpenClaw Gateway did not return a model directory")
	}
	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(response.Models))
	seen := make(map[acp.SessionConfigValueId]bool, len(response.Models))
	for _, model := range response.Models {
		id := modelID(model.Provider, model.ID)
		if id == "" || seen[id] || model.Available != nil && !*model.Available ||
			model.ManualSelectionAllowed != nil && !*model.ManualSelectionAllowed {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = string(id)
		}
		options = append(options, acp.SessionConfigSelectOption{Value: id, Name: name, Meta: model.thinkingCatalog()})
	}
	catalog := acpmeta.ModelCatalog{Options: acp.SessionConfigSelectOptions{Ungrouped: &options}}
	var defaults struct {
		// Defaults describes the actual Gateway defaults apart from the catalog's ordering.
		Defaults modelIdentity `json:"defaults"`
	}
	if err := c.gateway.call(op, "sessions.list", map[string]any{"limit": 1}, &defaults); err == nil {
		if id := modelID(defaults.Defaults.Provider, defaults.Defaults.Model); seen[id] {
			catalog.DefaultID = &id
		}
	} else if op.Err() != nil {
		return acpmeta.ModelCatalog{}, op.Err()
	}
	return catalog, nil
}

// SetSessionConfigOption uses the native session model setter and requires its exact receipt.
func (c *Client) SetSessionConfigOption(ctx context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if err := c.admit(ctx); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	defer c.op.Unlock()
	if request.ValueId == nil || request.Boolean != nil || request.ValueId.ConfigId != modelOptionID {
		reply, err := c.bridge.Load().SetSessionConfigOption(ctx, request)
		if err == nil && request.ValueId != nil {
			reply.ConfigOptions = c.updateOptions(request.ValueId.SessionId, reply.ConfigOptions)
		} else if err == nil && request.Boolean != nil {
			reply.ConfigOptions = c.updateOptions(request.Boolean.SessionId, reply.ConfigOptions)
		}
		return reply, err
	}
	selected := request.ValueId
	provider, model, ok := strings.Cut(string(selected.Value), "/")
	if !ok || modelID(provider, model) != selected.Value {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams("OpenClaw requires a provider-qualified model")
	}
	c.sessionMu.Lock()
	state, ok := c.sessions[selected.SessionId]
	c.sessionMu.Unlock()
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, missingSession()
	}
	bound := state.binding
	op, cancel := c.operationContext(ctx)
	defer cancel()
	var response struct {
		// OK confirms a successful model override.
		OK bool `json:"ok"`
		// Key names the exact canonical session mutated by the Gateway.
		Key string `json:"key"`
		// Entry names the affected physical session.
		Entry gatewaySession `json:"entry"`
		// Resolved reports the effective native model after the override.
		Resolved modelIdentity `json:"resolved"`
	}
	err := c.gateway.call(op, "sessions.patch", map[string]any{
		"key": bound.Key, "expectedSessionId": bound.PhysicalID, "model": selected.Value,
	}, &response)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	id := modelID(response.Resolved.Provider, response.Resolved.Model)
	if !response.OK || response.Key != bound.Key || response.Entry.PhysicalID != bound.PhysicalID || id != selected.Value {
		return acp.SetSessionConfigOptionResponse{}, errors.New("OpenClaw Gateway did not confirm the selected native model")
	}
	c.sessionMu.Lock()
	state = c.sessions[selected.SessionId]
	state.selectedModel = id
	c.sessions[selected.SessionId] = state
	c.sessionMu.Unlock()
	if state.model != id {
		// ACP's advertised settings are specific to the bound model. Rebind
		// the same session without replay so dependent choices come from the
		// Gateway's new model, using the successful mutation receipt.
		row := response.Entry
		row.Provider, row.Model = response.Resolved.Provider, response.Resolved.Model
		reply, err := c.resume(op, bound, row, nil)
		return acp.SetSessionConfigOptionResponse{ConfigOptions: reply.ConfigOptions}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: state.options}, nil
}
