package pi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

type nativeModel struct {
	// Provider identifies the native model service.
	Provider string `json:"provider"`
	// ID is the exact model identifier passed separately to Pi.
	ID string `json:"id"`
	// Name is the native display label.
	Name string `json:"name"`
	// Reasoning is the model's reported support for thinking controls.
	Reasoning *bool `json:"reasoning"`
}

func (m nativeModel) key() string {
	if m.Provider == "" || m.ID == "" {
		return ""
	}
	return m.Provider + "/" + m.ID
}

type nativeState struct {
	// SessionFile is the path returned by Pi for its active transcript.
	SessionFile string `json:"sessionFile"`
	// SessionID is the native identity stored in that transcript header.
	SessionID string `json:"sessionId"`
	// Model is the effective model selected by Pi.
	Model *nativeModel `json:"model"`
	// ThinkingLevel is the effective thinking level of Model.
	ThinkingLevel string `json:"thinkingLevel"`
}

func (s *session) state(ctx context.Context) (nativeState, error) {
	var state nativeState
	err := s.rpc.call(ctx, "get_state", nil, &state)
	return state, err
}

func (s *session) models(ctx context.Context) ([]nativeModel, error) {
	var available struct {
		// Models contains the configured models visible to this process.
		Models []nativeModel
	}
	err := s.rpc.call(ctx, "get_available_models", nil, &available)
	if err == nil && available.Models == nil {
		err = errors.New("Pi did not return its model catalog")
	}
	return available.Models, err
}

func (s *session) levels(ctx context.Context) ([]string, error) {
	var available struct {
		// Levels contains the thinking menu for the selected model.
		Levels []string
	}
	err := s.rpc.call(ctx, "get_available_thinking_levels", nil, &available)
	if err == nil && len(available.Levels) == 0 {
		err = errors.New("Pi did not return supported thinking levels")
	}
	return available.Levels, err
}

func selectOption(id, name, category, current string, options []acp.SessionConfigSelectOption) acp.SessionConfigOption {
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: acp.SessionConfigId(id), Name: name, Category: pointer(acp.SessionConfigOptionCategory(category)),
		CurrentValue: acp.SessionConfigValueId(current), Type: "select",
		Options: acp.SessionConfigSelectOptions{Ungrouped: pointer(acp.SessionConfigSelectOptionsUngrouped(options))},
	}}
}

func thinkingOption(current string, levels []string) acp.SessionConfigOption {
	options := make([]acp.SessionConfigSelectOption, 0, len(levels))
	for _, level := range levels {
		options = append(options, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(level), Name: level})
	}
	return selectOption("reasoning", "Thinking", "thought_level", current, options)
}

func modelOptions(models []nativeModel, state nativeState, levels []string) []acp.SessionConfigSelectOption {
	rows := make([]acp.SessionConfigSelectOption, 0, len(models))
	seen := make(map[string]bool)
	for _, model := range models {
		key := model.key()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		name := model.Name
		if name == "" {
			name = model.ID
		}
		metadata := acpmeta.ModelConfigCatalog{}
		switch {
		case state.Model != nil && state.Model.key() == key && len(levels) != 0:
			metadata.Options = []acp.SessionConfigOption{thinkingOption(state.ThinkingLevel, levels)}
		case model.Reasoning != nil && !*model.Reasoning:
			metadata.Options = []acp.SessionConfigOption{thinkingOption("off", []string{"off"})}
		default:
			metadata.UnknownConfigIDs = []acp.SessionConfigId{"reasoning"}
		}
		rows = append(rows, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(key), Name: name, Meta: acpmeta.WithModelConfigCatalog(nil, metadata)})
	}
	return rows
}

func (s *session) configuration(ctx context.Context) ([]acp.SessionConfigOption, error) {
	state, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	models, err := s.models(ctx)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 || state.Model == nil || state.Model.key() == "" {
		return nil, classified("authentication", "session_open")
	}
	levels, err := s.levels(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(levels, state.ThinkingLevel) {
		return nil, errors.New("Pi effective thinking level is absent from its native menu")
	}
	return []acp.SessionConfigOption{
		selectOption("model", "Model", "model", state.Model.key(), modelOptions(models, state, levels)),
		thinkingOption(state.ThinkingLevel, levels),
	}, nil
}

func (c *Client) SetSessionConfigOption(ctx context.Context, r acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if r.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	value := r.ValueId
	options, err := c.configure(ctx, value.SessionId, string(value.ConfigId), string(value.Value))
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, err
}

func (c *Client) configure(ctx context.Context, id acp.SessionId, key, value string) ([]acp.SessionConfigOption, error) {
	s, err := c.session(id)
	if err != nil {
		return nil, err
	}
	end, err := s.begin()
	if err != nil {
		return nil, err
	}
	defer end()
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	expected := value
	switch key {
	case "model":
		models, err := s.models(ctx)
		if err != nil {
			return nil, err
		}
		model := c.lookupModel(models, value)
		if model == nil {
			return nil, acp.NewInvalidParams(map[string]any{"errorKind": "model_unavailable"})
		}
		expected = model.key()
		if err := s.rpc.call(ctx, "set_model", map[string]any{"provider": model.Provider, "modelId": model.ID}, nil); err != nil {
			return nil, err
		}
	case "reasoning":
		levels, err := s.levels(ctx)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(levels, value) {
			return nil, acp.NewInvalidParams(nil)
		}
		if err := s.rpc.call(ctx, "set_thinking_level", map[string]any{"level": value}, nil); err != nil {
			return nil, err
		}
	default:
		return nil, acp.NewInvalidParams(nil)
	}
	options, err := s.configuration(ctx)
	if err != nil {
		return nil, err
	}
	for _, option := range options {
		if string(option.Select.Id) == key {
			if string(option.Select.CurrentValue) != expected {
				return options, classified("configuration_rejected", "configure")
			}
		}
	}
	return options, nil
}

func (c *Client) lookupModel(models []nativeModel, value string) *nativeModel {
	preferred := c.getenv("ACP_GO_PI_PREFERRED_PROVIDER")
	var qualified, preferredMatch, legacy *nativeModel
	legacyCount := 0
	for _, model := range models {
		if model.key() == "" {
			continue
		}
		if model.key() == value {
			qualified = &model
		}
		if model.ID == value {
			if model.Provider == preferred {
				preferredMatch = &model
			}
			legacyCount++
			legacy = &model
		}
	}
	if qualified != nil {
		return qualified
	}
	if preferredMatch != nil {
		return preferredMatch
	}
	if legacyCount == 1 {
		return legacy
	}
	return nil
}

func (*Client) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound("session/set_mode")
}

// ReadModels probes the CLI without opening persistent history or sending a prompt.
func (c *Client) ReadModels(ctx context.Context) (result acpmeta.ModelCatalog, err error) {
	unlock, err := c.lockOpen(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	s, err := c.start(ctx, c.options.WorkingDirectory, []string{"--no-session", "--no-tools", "--no-extensions"}, resources{probe: true}, "")
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, c.retire(s)) }()
	state, err := s.state(ctx)
	if err != nil {
		return result, err
	}
	models, err := s.models(ctx)
	if err != nil {
		return result, err
	}
	var levels []string
	if state.Model != nil && state.Model.key() != "" {
		result.DefaultID = pointer(acp.SessionConfigValueId(state.Model.key()))
		levels, err = s.levels(ctx)
		if err != nil {
			return result, err
		}
	}
	result.Options.Ungrouped = pointer(acp.SessionConfigSelectOptionsUngrouped(modelOptions(models, state, levels)))
	result.Modes = []acp.SessionMode{{Id: "default", Name: "Default"}}
	if c.options.MCPModulePath != "" {
		result.Modes = append(result.Modes, acp.SessionMode{Id: "auto", Name: "Auto"})
	}
	result.Modes = append(result.Modes, acp.SessionMode{Id: "full-access", Name: "Full access"})
	return result, nil
}

func (c *Client) HandleExtensionMethod(ctx context.Context, method string, _ json.RawMessage) (any, error) {
	if method == acpmeta.ModelCatalogMethod {
		return c.ReadModels(ctx)
	}
	return nil, acp.NewMethodNotFound(method)
}

func nativeFailure(detail string) error {
	lower := strings.ToLower(detail)
	kind := "native_execution_failed"
	switch {
	case strings.Contains(lower, "invalid api key"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "authentication"), strings.Contains(lower, "no api key"):
		kind = "authentication"
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many requests"):
		kind = "rate_limit"
	case strings.Contains(lower, "insufficient_quota"), strings.Contains(lower, "quota exceeded"):
		kind = "quota_exhausted"
	case strings.Contains(lower, "insufficient balance"), strings.Contains(lower, "payment required"):
		kind = "billing_error"
	case strings.Contains(lower, "context length"), strings.Contains(lower, "context window"):
		kind = "context_limit"
	case strings.Contains(lower, "model not found"), strings.Contains(lower, "model unavailable"):
		kind = "model_unavailable"
	}
	return classifiedError{kind: kind, phase: "prompt", detail: agentclient.NativeFailureMessage(detail)}
}
