package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	legacyModelID          = "model"
	unknownCurrentValueKey = "acp-go/cursor/unknown-current-value"
)

type configuration struct {
	// options is the exact directory projection, immutable until replaced.
	options []acp.SessionConfigOption
	// currentAt records the receive sequence of independently reported current values.
	currentAt map[acp.SessionConfigId]uint64
	// sequence is the latest native frame carrying this directory.
	sequence uint64
	// legacy is true when the model uses the old session/set_model method.
	legacy bool
	// legacyRequested is an accepted request without an echoed effective model.
	legacyRequested acp.SessionConfigValueId
}

type setting struct {
	// id is the exact advertised native config key.
	id acp.SessionConfigId
	// value is the native select option.
	value acp.SessionConfigValueId
	// toggle is present for boolean settings, including explicit false.
	toggle *bool
}

func (c *Client) applySetupLocked(s *session, setup setupResult, seq uint64) error {
	if err := c.applyConfigurationLocked(s, setup.ConfigOptions, setup.Models, seq); err != nil {
		return err
	}
	if setup.Modes != nil {
		s.modes = setup.Modes
	}
	return nil
}

func (c *Client) applyConfigurationLocked(s *session, raw, legacyRaw json.RawMessage, seq uint64) error {
	if seq < s.config.sequence {
		return nil
	}
	if !present(raw) && !present(legacyRaw) {
		return nil
	}
	next := s.config
	if present(raw) {
		options, currentAt, err := parseOptions(raw, s.config, seq)
		if err != nil {
			return err
		}
		next.options, next.currentAt, next.legacy = options, currentAt, false
	}
	if present(legacyRaw) && modelOption(next.options) == nil {
		legacy, current, err := parseLegacyModels(legacyRaw)
		if err != nil {
			return err
		}
		if legacy != nil {
			next.options = append(slices.Clone(next.options), acp.SessionConfigOption{Select: legacy})
			next.currentAt = cloneCurrent(next.currentAt)
			if current {
				next.currentAt[legacy.Id] = seq
			}
			next.legacy = true
		}
	} else if present(legacyRaw) && next.legacy {
		legacy, current, err := parseLegacyModels(legacyRaw)
		if err != nil {
			return err
		}
		if legacy != nil {
			old := modelOption(next.options)
			if !current {
				legacy.CurrentValue = old.CurrentValue
			}
			next.options = slices.Clone(next.options)
			next.options[slices.IndexFunc(next.options, func(option acp.SessionConfigOption) bool { return option.Select == old })] = acp.SessionConfigOption{Select: legacy}
			next.currentAt = cloneCurrent(next.currentAt)
			if current {
				next.currentAt[legacy.Id] = seq
			}
		}
	}
	next.sequence = seq
	s.config = next
	return nil
}

func cloneCurrent(prior map[acp.SessionConfigId]uint64) map[acp.SessionConfigId]uint64 {
	result := make(map[acp.SessionConfigId]uint64, len(prior)+1)
	for id, seq := range prior {
		result[id] = seq
	}
	return result
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func parseOptions(raw json.RawMessage, prior configuration, seq uint64) ([]acp.SessionConfigOption, map[acp.SessionConfigId]uint64, error) {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || len(entries) > 512 {
		return nil, nil, errors.New("invalid Cursor configuration directory")
	}
	options := make([]acp.SessionConfigOption, 0, len(entries))
	currentAt := make(map[acp.SessionConfigId]uint64, len(entries))
	for _, entry := range entries {
		var option acp.SessionConfigOption
		if json.Unmarshal(entry, &option) != nil {
			// An unrelated future option cannot make the known choices disappear.
			continue
		}
		id := optionID(option)
		if id == "" {
			continue
		}
		if _, duplicate := currentAt[id]; duplicate {
			return nil, nil, errors.New("ambiguous Cursor configuration key")
		}
		var presence struct {
			// Current retains omission independently of a literal false or empty string.
			Current json.RawMessage `json:"currentValue"`
		}
		_ = json.Unmarshal(entry, &presence)
		currentAt[id] = 0
		if present(presence.Current) {
			currentAt[id] = seq
		} else if prior.currentAt[id] != 0 {
			previous, _ := findOption(prior.options, id)
			if option.Boolean != nil && previous.Boolean != nil {
				option.Boolean.CurrentValue = previous.Boolean.CurrentValue
				currentAt[id] = prior.currentAt[id]
			}
			if option.Select != nil && previous.Select != nil && hasChoice(option.Select, previous.Select.CurrentValue) {
				option.Select.CurrentValue = previous.Select.CurrentValue
				currentAt[id] = prior.currentAt[id]
			}
		}
		if option.Boolean != nil && currentAt[id] == 0 {
			// ACP represents boolean currentValue as required. Preserve the
			// distinction between its wire placeholder and explicit native false.
			option.Boolean.Meta = copyMeta(option.Boolean.Meta)
			option.Boolean.Meta[unknownCurrentValueKey] = true
		}
		options = append(options, option)
	}
	return options, currentAt, nil
}

func parseLegacyModels(raw json.RawMessage) (*acp.SessionConfigOptionSelect, bool, error) {
	var state struct {
		// Current is optional even when the model catalog is available.
		Current *acp.SessionConfigValueId `json:"currentModelId"`
		// Models contains the provider's legacy model directory.
		Models []struct {
			// ID is the original legacy setter value.
			ID acp.SessionConfigValueId `json:"modelId"`
			// Name labels the model.
			Name string `json:"name"`
			// Description is optional native model documentation.
			Description *string `json:"description"`
			// Meta includes model-specific settings evidence.
			Meta map[string]any `json:"_meta"`
		} `json:"availableModels"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return nil, false, errors.New("invalid Cursor legacy model directory")
	}
	if state.Models == nil {
		return nil, false, nil
	}
	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(state.Models))
	for _, model := range state.Models {
		if model.ID != "" {
			options = append(options, acp.SessionConfigSelectOption{Value: model.ID, Name: model.Name, Description: model.Description, Meta: model.Meta})
		}
	}
	result := &acp.SessionConfigOptionSelect{Id: legacyModelID, Name: "Model", Type: "select",
		Category: acp.Ptr(acp.SessionConfigOptionCategoryModel), Options: acp.SessionConfigSelectOptions{Ungrouped: &options}}
	if state.Current != nil {
		result.CurrentValue = *state.Current
	}
	return result, state.Current != nil, nil
}

func optionID(option acp.SessionConfigOption) acp.SessionConfigId {
	if option.Select != nil {
		return option.Select.Id
	}
	if option.Boolean != nil {
		return option.Boolean.Id
	}
	return ""
}

func findOption(options []acp.SessionConfigOption, id acp.SessionConfigId) (acp.SessionConfigOption, bool) {
	i := slices.IndexFunc(options, func(option acp.SessionConfigOption) bool { return optionID(option) == id })
	if i < 0 {
		return acp.SessionConfigOption{}, false
	}
	return options[i], true
}

func modelOption(options []acp.SessionConfigOption) *acp.SessionConfigOptionSelect {
	for _, option := range options {
		if option.Select != nil && option.Select.Category != nil && *option.Select.Category == acp.SessionConfigOptionCategoryModel {
			return option.Select
		}
	}
	for _, option := range options {
		if option.Select != nil && option.Select.Id == legacyModelID {
			return option.Select
		}
	}
	return nil
}

func hasChoice(option *acp.SessionConfigOptionSelect, value acp.SessionConfigValueId) bool {
	contains := func(options []acp.SessionConfigSelectOption) bool {
		return slices.ContainsFunc(options, func(choice acp.SessionConfigSelectOption) bool { return choice.Value == value })
	}
	if option.Options.Ungrouped != nil {
		return contains(*option.Options.Ungrouped)
	}
	if option.Options.Grouped != nil {
		for _, group := range *option.Options.Grouped {
			if contains(group.Options) {
				return true
			}
		}
	}
	return false
}

func (c *Client) SetSessionConfigOption(ctx context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	defer c.end()
	var id acp.SessionId
	var wanted setting
	switch {
	case request.ValueId != nil && request.Boolean == nil:
		id = request.ValueId.SessionId
		wanted = setting{id: request.ValueId.ConfigId, value: request.ValueId.Value}
	case request.Boolean != nil && request.ValueId == nil:
		id = request.Boolean.SessionId
		wanted = setting{id: request.Boolean.ConfigId, toggle: acp.Ptr(request.Boolean.Value)}
	default:
		return acp.SetSessionConfigOptionResponse{}, invalid("invalid Cursor setting")
	}
	c.mu.Lock()
	s := c.sessions[id]
	c.mu.Unlock()
	if s == nil {
		return acp.SetSessionConfigOptionResponse{}, invalid("Cursor session is not open")
	}
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	return c.setConfig(op, conn, s, wanted, request)
}

func (c *Client) setConfig(ctx context.Context, conn *acp.ClientSideConnection, s *session, wanted setting, params any) (acp.SetSessionConfigOptionResponse, error) {
	c.mu.Lock()
	option, advertised := findOption(s.config.options, wanted.id)
	if !advertised || wanted.toggle == nil && (option.Select == nil || !hasChoice(option.Select, wanted.value)) ||
		wanted.toggle != nil && option.Boolean == nil {
		c.mu.Unlock()
		return acp.SetSessionConfigOptionResponse{}, invalid("Cursor did not advertise the requested setting")
	}
	model := modelOption(s.config.options)
	modelWrite := model != nil && model.Id == wanted.id
	legacy := modelWrite && s.config.legacy
	confirmedCurrent := settingConfirmed(s.config, wanted) || legacy && s.config.legacyRequested == wanted.value
	if confirmedCurrent && (!modelWrite || c.processModel == wanted.value) {
		options := s.config.options
		c.mu.Unlock()
		result, err := clone(acp.SetSessionConfigOptionResponse{ConfigOptions: options})
		return result, err
	}
	c.mu.Unlock()
	var initial uint64
	method := acp.AgentMethodSessionSetConfigOption
	if legacy {
		method = "session/set_model"
		params = map[string]any{"sessionId": s.id, "modelId": wanted.value}
	}
	var confirmed bool
	var normalizationErr error
	hooks := boundedWrite(ctx, conn, func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || c.unusable || c.sessions[s.id] != s {
			return ErrRestartRequired
		}
		initial = s.config.sequence
		return nil
	})
	r := acp.SendClientRequestWithReceipt[setupResult](conn, ctx, method, params, hooks, func(r acp.RequestReceipt[setupResult]) {
		if !r.ResultValid {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		normalizationErr = c.applySetupLocked(s, r.Result, r.Sequence)
		confirmed = normalizationErr == nil && s.config.currentAt[wanted.id] > initial && settingConfirmed(s.config, wanted)
		if legacy && normalizationErr == nil && !confirmed {
			// A legacy ACK confirms the write but does not assert an echoed value.
			s.config.legacyRequested = wanted.value
			confirmed = true
		}
		if modelWrite && confirmed {
			c.processModel = wanted.value
		}
	})
	if err := sessionReceiptFailure(r, method, s.id); err != nil {
		if r.PeerErr == nil && r.Write.BytesWritten > 0 && !confirmed {
			c.invalidate()
		}
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if normalizationErr != nil || !confirmed {
		c.invalidate()
		return acp.SetSessionConfigOptionResponse{}, errors.Join(normalizationErr, errUnconfirmedSetting)
	}
	c.mu.Lock()
	options := s.config.options
	c.mu.Unlock()
	meta := copyMeta(r.Result.Meta)
	if legacy {
		meta["acp-go/cursor/model-acknowledgement"] = string(wanted.value)
	}
	return clone(acp.SetSessionConfigOptionResponse{ConfigOptions: options, Meta: meta})
}

func settingConfirmed(config configuration, wanted setting) bool {
	if config.currentAt[wanted.id] == 0 {
		return false
	}
	option, _ := findOption(config.options, wanted.id)
	if wanted.toggle != nil {
		return option.Boolean != nil && option.Boolean.CurrentValue == *wanted.toggle
	}
	return option.Select != nil && option.Select.CurrentValue == wanted.value
}

func freezeSettings(config configuration) []setting {
	var plan []setting
	model := modelOption(config.options)
	if model != nil {
		value := model.CurrentValue
		if config.legacyRequested != "" {
			value = config.legacyRequested
		}
		if value != "" {
			plan = append(plan, setting{id: model.Id, value: value})
		}
	}
	for _, option := range config.options {
		id := optionID(option)
		if config.currentAt[id] == 0 || model != nil && model.Id == id {
			continue
		}
		if selectOption := option.Select; selectOption != nil && executionCategory(selectOption.Category) && selectOption.CurrentValue != "" {
			plan = append(plan, setting{id: id, value: selectOption.CurrentValue})
		}
		if boolean := option.Boolean; boolean != nil && executionCategory(boolean.Category) {
			plan = append(plan, setting{id: id, toggle: acp.Ptr(boolean.CurrentValue)})
		}
	}
	return plan
}

func executionCategory(category *acp.SessionConfigOptionCategory) bool {
	return category != nil && (*category == acp.SessionConfigOptionCategoryThoughtLevel || *category == "model_config")
}

func (c *Client) prepareSettings(ctx context.Context, t *turn) error {
	for _, wanted := range t.settings {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		alreadySet := c.settingActiveLocked(t.session, wanted)
		c.mu.Unlock()
		if alreadySet {
			continue
		}
		var request acp.SetSessionConfigOptionRequest
		if wanted.toggle == nil {
			request.ValueId = &acp.SetSessionConfigOptionValueId{SessionId: t.session.id, ConfigId: wanted.id, Value: wanted.value}
		} else {
			request.Boolean = &acp.SetSessionConfigOptionBoolean{SessionId: t.session.id, ConfigId: wanted.id, Value: *wanted.toggle}
		}
		if _, err := c.setConfig(ctx, t.conn, t.session, wanted, request); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) settingActiveLocked(s *session, wanted setting) bool {
	config := s.config
	model := modelOption(config.options)
	isModel := model != nil && model.Id == wanted.id
	confirmed := settingConfirmed(config, wanted) || isModel && config.legacy && config.legacyRequested == wanted.value
	return confirmed && (!isModel || c.processModel == wanted.value)
}

func (c *Client) ReadModels(ctx context.Context) (acpmeta.ModelCatalog, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acpmeta.ModelCatalog{}, err
	}
	defer c.end()
	c.mu.Lock()
	if c.catalog != nil {
		catalog := *c.catalog
		c.mu.Unlock()
		return clone(catalog)
	}
	var fallback acpmeta.ModelCatalog
	var fallbackSeq uint64
	for _, s := range c.sessions {
		if model := modelOption(s.config.options); model != nil && s.config.sequence > fallbackSeq {
			fallback = acpmeta.ModelCatalog{Options: model.Options}
			fallbackSeq = s.config.sequence
		}
	}
	c.mu.Unlock()
	op, cancel := c.operationContext(ctx, 3*time.Second)
	defer cancel()
	type catalogResponse struct {
		// Models preserves each discovered model and its optional settings.
		Models []struct {
			// The ACP option contains the native model identity and label.
			acp.SessionConfigSelectOption
			// Options is nil when settings have not been discovered.
			Options json.RawMessage `json:"configOptions"`
		} `json:"models"`
		// DefaultID is independent from the first advertised model.
		DefaultID *acp.SessionConfigValueId `json:"defaultModelId"`
	}
	const method = "cursor/list_available_models"
	r := send[catalogResponse](conn, op, method, struct{}{})
	if err := receiptFailure(r, method); err != nil {
		if r.PeerErr != nil && r.PeerErr.Code == -32601 && fallbackSeq != 0 {
			return clone(fallback)
		}
		return acpmeta.ModelCatalog{}, err
	}
	if r.Result.Models == nil {
		if fallbackSeq != 0 {
			return clone(fallback)
		}
		return acpmeta.ModelCatalog{}, acp.NewMethodNotFound(acpmeta.ModelCatalogMethod)
	}
	models := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(r.Result.Models))
	seen := make(map[acp.SessionConfigValueId]bool)
	for _, row := range r.Result.Models {
		if strings.TrimSpace(string(row.Value)) == "" || seen[row.Value] {
			return acpmeta.ModelCatalog{}, errors.New("invalid Cursor model catalog")
		}
		seen[row.Value] = true
		if present(row.Options) {
			options, _, err := parseOptions(row.Options, configuration{}, r.Sequence)
			if err != nil {
				return acpmeta.ModelCatalog{}, err
			}
			row.Meta = acpmeta.WithModelConfigOptions(row.Meta, options)
		}
		models = append(models, row.SessionConfigSelectOption)
	}
	catalog := acpmeta.ModelCatalog{Options: acp.SessionConfigSelectOptions{Ungrouped: &models}}
	if r.Result.DefaultID != nil && seen[*r.Result.DefaultID] {
		catalog.DefaultID = r.Result.DefaultID
	}
	c.mu.Lock()
	c.catalog = &catalog
	c.mu.Unlock()
	return clone(catalog)
}
