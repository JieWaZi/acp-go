package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	legacyModelID          = "model"
	unknownCurrentValueKey = "acp-go/unknown-current-value"
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
		options, currentAt, err := parseOptions(raw, s.config, seq, c.opts.Protocol.NormalizeOption)
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
				next.legacyRequested = ""
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
				next.legacyRequested = ""
			}
		}
	}
	next.sequence = seq
	if model := modelOption(next.options); model != nil && !next.legacy && next.currentAt[model.Id] == seq {
		next.legacyRequested = ""
	}
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

func parseOptions(raw json.RawMessage, prior configuration, seq uint64, normalize func(acp.SessionConfigOption) acp.SessionConfigOption) ([]acp.SessionConfigOption, map[acp.SessionConfigId]uint64, error) {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || len(entries) > 512 {
		return nil, nil, errors.New("invalid ACP agent configuration directory")
	}
	options := make([]acp.SessionConfigOption, 0, len(entries))
	currentAt := make(map[acp.SessionConfigId]uint64, len(entries))
	for _, entry := range entries {
		var option acp.SessionConfigOption
		if json.Unmarshal(entry, &option) != nil {
			// An unrelated future option cannot make the known choices disappear.
			continue
		}
		if normalize != nil {
			option = normalize(option)
		}
		id := optionID(option)
		if id == "" {
			continue
		}
		if _, duplicate := currentAt[id]; duplicate {
			return nil, nil, errors.New("ambiguous ACP agent configuration key")
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

// DecodeCatalogOptions preserves native option presence in a model catalog.
func DecodeCatalogOptions(raw json.RawMessage) ([]acp.SessionConfigOption, error) {
	options, _, err := parseOptions(raw, configuration{}, 1, nil)
	return options, err
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
		return nil, false, errors.New("invalid ACP agent legacy model directory")
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
		return acp.SetSessionConfigOptionResponse{}, invalid("invalid ACP agent setting")
	}
	c.mu.Lock()
	s := c.sessions[id]
	c.mu.Unlock()
	if s == nil {
		return acp.SetSessionConfigOptionResponse{}, invalid("ACP agent session is not open")
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
		return acp.SetSessionConfigOptionResponse{}, invalid("ACP agent did not advertise the requested setting")
	}
	model := modelOption(s.config.options)
	modelWrite := model != nil && model.Id == wanted.id
	legacy := modelWrite && s.config.legacy
	processWide := modelWrite && c.opts.Protocol.ModelScope == ModelPerProcess
	confirmedCurrent := currentSettingMatches(s.config, wanted, legacy)
	legacyBehavior := c.opts.Protocol.LegacyModel
	knownLegacy := legacy && legacyBehavior.ConfirmsValue != nil && legacyBehavior.ConfirmsValue(c.version)
	if knownLegacy && legacyBehavior.OpeningValueIsNoop && s.config.legacyRequested != "" && !confirmedCurrent && model.CurrentValue == wanted.value {
		c.mu.Unlock()
		// The native setter short-circuits on the stale opening model ID.
		return acp.SetSessionConfigOptionResponse{}, errUnconfirmedSetting
	}
	if confirmedCurrent && (!processWide || c.processModel == wanted.value) {
		options := s.config.options
		var meta map[string]any
		if legacy && s.config.legacyRequested == wanted.value {
			meta = map[string]any{"acp-go/model-acknowledgement": string(wanted.value)}
			if knownLegacy {
				options = withLegacyEffectiveModel(options, wanted)
			}
		}
		c.mu.Unlock()
		result, err := clone(acp.SetSessionConfigOptionResponse{ConfigOptions: options, Meta: meta})
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
		if processWide {
			// A rejected or interrupted write can still change the process model.
			c.processModel = ""
		}
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
	})
	appliedWithoutSaving := false
	if knownLegacy && r.PeerErr != nil && legacyBehavior.AppliedOnError != nil && legacyBehavior.AppliedOnError(r.PeerErr) {
		c.mu.Lock()
		if s.config.sequence == initial {
			s.config.legacyRequested = wanted.value
			appliedWithoutSaving, confirmed = true, true
		}
		c.mu.Unlock()
	}
	if err := sessionReceiptFailure(c, r, method, s.id); err != nil && !appliedWithoutSaving {
		if r.Write.BytesWritten > 0 && !confirmed &&
			(r.PeerErr == nil || knownLegacy && legacyBehavior.AppliedOnError != nil && r.PeerErr.Code == -32603) {
			c.invalidate()
		}
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if normalizationErr != nil || !confirmed {
		c.invalidate()
		return acp.SetSessionConfigOptionResponse{}, errors.Join(normalizationErr, errUnconfirmedSetting)
	}
	c.mu.Lock()
	if processWide {
		c.processModel = wanted.value
	}
	options := s.config.options
	c.mu.Unlock()
	meta := copyMeta(r.Result.Meta)
	if legacy {
		meta["acp-go/model-acknowledgement"] = string(wanted.value)
		if knownLegacy {
			options = withLegacyEffectiveModel(options, wanted)
		}
	}
	if appliedWithoutSaving {
		meta["acp-go/model-persistence"] = "unconfirmed"
	}
	return clone(acp.SetSessionConfigOptionResponse{ConfigOptions: options, Meta: meta})
}

// withLegacyEffectiveModel projects a verified setter result without replacing
// the native opening model that a version-specific setter may still compare.
func withLegacyEffectiveModel(options []acp.SessionConfigOption, wanted setting) []acp.SessionConfigOption {
	index := slices.IndexFunc(options, func(option acp.SessionConfigOption) bool {
		return option.Select != nil && option.Select.Id == wanted.id
	})
	if index < 0 {
		return options
	}
	projected := slices.Clone(options)
	model := *projected[index].Select
	model.CurrentValue = wanted.value
	projected[index].Select = &model
	return projected
}

func currentSettingMatches(config configuration, wanted setting, legacy bool) bool {
	if legacy && config.legacyRequested != "" {
		return config.legacyRequested == wanted.value
	}
	return settingConfirmed(config, wanted)
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

func (c *Client) freezeSettings(config configuration) []setting {
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
		if selectOption := option.Select; selectOption != nil && c.executionCategory(selectOption.Category) && selectOption.CurrentValue != "" {
			plan = append(plan, setting{id: id, value: selectOption.CurrentValue})
		}
		if boolean := option.Boolean; boolean != nil && c.executionCategory(boolean.Category) {
			plan = append(plan, setting{id: id, toggle: acp.Ptr(boolean.CurrentValue)})
		}
	}
	return plan
}

func (c *Client) executionCategory(category *acp.SessionConfigOptionCategory) bool {
	return category != nil && (*category == acp.SessionConfigOptionCategoryThoughtLevel ||
		c.opts.Protocol.ExecutionOption != nil && c.opts.Protocol.ExecutionOption(*category))
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
	confirmed := currentSettingMatches(config, wanted, isModel && config.legacy)
	return confirmed && (!isModel || c.opts.Protocol.ModelScope == ModelPerSession || c.processModel == wanted.value)
}

// ReadModels reads the advertised model directory without starting a Prompt.
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
	probe := c.opts.Protocol.Catalog
	if probe != nil {
		timeout := probe.Timeout
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		op, cancel := c.operationContext(ctx, timeout)
		defer cancel()
		r := send[json.RawMessage](conn, op, probe.Method, struct{}{})
		if err := receiptFailure(r, probe.Method); err != nil {
			if r.PeerErr == nil || r.PeerErr.Code != -32601 {
				return acpmeta.ModelCatalog{}, err
			}
		} else {
			catalog, err := probe.Decode(r.Result)
			if err == nil {
				c.mu.Lock()
				c.catalog = &catalog
				c.mu.Unlock()
				return clone(catalog)
			}
			var protocol *acp.RequestError
			if !errors.As(err, &protocol) || protocol.Code != -32601 {
				return acpmeta.ModelCatalog{}, err
			}
		}
	}
	if fallbackSeq != 0 {
		return clone(fallback)
	}
	return c.discoverModels(ctx, conn)
}

// discoverModels follows the standard ACP discovery path: an empty session,
// bounded per-model configuration receipts, restoration, then native close.
// It runs under ReadModels' single operation admission. The probe never enters
// the chat session registry, so its callbacks cannot acquire host privileges.
func (c *Client) discoverModels(ctx context.Context, conn *acp.ClientSideConnection) (catalog acpmeta.ModelCatalog, err error) {
	c.mu.Lock()
	if len(c.sessions) != 0 {
		c.mu.Unlock()
		return catalog, invalid("ACP discovery requires an isolated connection")
	}
	c.processModel = ""
	c.mu.Unlock()
	op, cancel := c.operationContext(ctx, 15*time.Second)
	defer cancel()
	r := send[setupResult](conn, op, acp.AgentMethodSessionNew, acp.NewSessionRequest{Cwd: c.opts.Process.Dir, McpServers: []acp.McpServer{}})
	if err = receiptFailure(r, acp.AgentMethodSessionNew); err != nil {
		if r.PeerErr == nil && r.Write.BytesWritten != 0 {
			c.invalidate()
		}
		return catalog, err
	}
	if r.Result.ID == "" {
		c.invalidate()
		return catalog, invalid("ACP discovery did not identify its probe session")
	}
	defer func() {
		if c.initialized.AgentCapabilities.SessionCapabilities.Close == nil {
			return
		}
		cleanup, stop := c.operationContext(context.WithoutCancel(ctx), time.Second)
		defer stop()
		closed := send[acp.CloseSessionResponse](conn, cleanup, acp.AgentMethodSessionClose, acp.CloseSessionRequest{SessionId: r.Result.ID})
		if failure := receiptFailure(closed, acp.AgentMethodSessionClose); failure != nil && closed.PeerErr == nil {
			c.invalidate()
		}
	}()
	probe := &session{}
	if err = c.applyConfigurationLocked(probe, r.Result.ConfigOptions, r.Result.Models, r.Sequence); err != nil {
		return catalog, err
	}
	model := modelOption(probe.config.options)
	if model == nil {
		return catalog, acp.NewMethodNotFound(acpmeta.ModelCatalogMethod)
	}
	if r.Result.Modes != nil {
		catalog.Modes = slices.Clone(r.Result.Modes.AvailableModes)
	}
	catalog.Options, err = clone(model.Options)
	if err != nil {
		return catalog, err
	}
	opening := model.CurrentValue
	if probe.config.currentAt[model.Id] != 0 && hasChoice(model, opening) {
		catalog.DefaultID = &opening
	}
	// Modern setters remain authoritative. Legacy rows can carry additional
	// per-model facts; retain those only for an identical listed model ID.
	if present(r.Result.Models) && !probe.config.legacy {
		legacy, current, legacyErr := parseLegacyModels(r.Result.Models)
		if legacyErr != nil {
			return catalog, legacyErr
		}
		if legacy != nil && legacy.Options.Ungrouped != nil {
			mergeRows := func(rows []acp.SessionConfigSelectOption) {
				for i := range rows {
					for _, old := range *legacy.Options.Ungrouped {
						if old.Value != rows[i].Value {
							continue
						}
						meta := copyMeta(old.Meta)
						for key, value := range rows[i].Meta {
							meta[key] = value
						}
						rows[i].Meta = meta
					}
				}
			}
			if catalog.Options.Ungrouped != nil {
				mergeRows(*catalog.Options.Ungrouped)
			}
			if catalog.Options.Grouped != nil {
				for _, group := range *catalog.Options.Grouped {
					mergeRows(group.Options)
				}
			}
			if catalog.DefaultID == nil && current && hasChoice(model, legacy.CurrentValue) {
				selected := legacy.CurrentValue
				catalog.DefaultID = &selected
			}
		}
	}
	if read := c.opts.Protocol.ReadModelConfig; read != nil {
		project := func(rows []acp.SessionConfigSelectOption) {
			for i := range rows {
				row := &rows[i]
				// The current model's real configuration receipt remains authoritative.
				if !probe.config.legacy && row.Value == opening {
					continue
				}
				if settings, known := read(row.Meta); known {
					row.Meta = acpmeta.WithModelConfigCatalog(row.Meta, settings)
				}
			}
		}
		if catalog.Options.Ungrouped != nil {
			project(*catalog.Options.Ungrouped)
		}
		if catalog.Options.Grouped != nil {
			for _, group := range *catalog.Options.Grouped {
				project(group.Options)
			}
		}
	}
	// Legacy catalogs without a configurable model cannot establish per-model
	// dependent selectors; their existing native model metadata is preserved.
	if !probe.config.legacy && probe.config.currentAt[model.Id] != 0 && hasChoice(model, opening) {
		deadline := time.Now().Add(5 * time.Second)
		if end, ok := op.Deadline(); ok && end.Add(-2250*time.Millisecond).Before(deadline) {
			deadline = end.Add(-2250 * time.Millisecond)
		}
		selected := opening
		uncertain := false
		visit := func(row *acp.SessionConfigSelectOption) {
			if _, known := acpmeta.ReadModelConfigCatalog(row.Meta); known {
				return
			}
			options := probe.config.options
			if row.Value != opening {
				options = nil
				if !uncertain && time.Now().Before(deadline) {
					until := time.Now().Add(time.Second)
					if deadline.Before(until) {
						until = deadline
					}
					requestCtx, stop := context.WithDeadline(op, until)
					receipt := send[setupResult](conn, requestCtx, acp.AgentMethodSessionSetConfigOption, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: r.Result.ID, ConfigId: model.Id, Value: row.Value}})
					stop()
					if receiptFailure(receipt, acp.AgentMethodSessionSetConfigOption) == nil {
						selected = row.Value
						var snapshot session
						if c.applyConfigurationLocked(&snapshot, receipt.Result.ConfigOptions, receipt.Result.Models, receipt.Sequence) == nil {
							if current := modelOption(snapshot.config.options); current != nil && snapshot.config.currentAt[current.Id] != 0 && current.CurrentValue == row.Value {
								options = snapshot.config.options
							}
						}
					} else if receipt.PeerErr == nil && receipt.Write.BytesWritten != 0 {
						uncertain = true
						c.invalidate()
					}
				}
			}
			// Model directories are represented once at the catalog level. Repeating
			// the full list inside each model would make snapshots quadratic.
			settings := slices.DeleteFunc(slices.Clone(options), func(option acp.SessionConfigOption) bool {
				return option.Select != nil && (option.Select.Id == model.Id || option.Select.Category != nil && *option.Select.Category == acp.SessionConfigOptionCategoryModel)
			})
			config := acpmeta.ModelConfigCatalog{Options: settings}
			if options == nil {
				config.UnknownConfigIDs = []acp.SessionConfigId{"reasoning", "fast"}
			}
			row.Meta = acpmeta.WithModelConfigCatalog(row.Meta, config)
		}
		if catalog.Options.Ungrouped != nil {
			for i := range *catalog.Options.Ungrouped {
				visit(&(*catalog.Options.Ungrouped)[i])
			}
		}
		if catalog.Options.Grouped != nil {
			for i := range *catalog.Options.Grouped {
				for j := range (*catalog.Options.Grouped)[i].Options {
					visit(&(*catalog.Options.Grouped)[i].Options[j])
				}
			}
		}
		if selected != opening && !uncertain {
			restore, stop := c.operationContext(context.WithoutCancel(ctx), time.Second)
			receipt := send[setupResult](conn, restore, acp.AgentMethodSessionSetConfigOption, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: r.Result.ID, ConfigId: model.Id, Value: opening}})
			stop()
			if failure := receiptFailure(receipt, acp.AgentMethodSessionSetConfigOption); failure != nil {
				c.invalidate()
				return acpmeta.ModelCatalog{}, failure
			}
			var restored session
			failure := c.applyConfigurationLocked(&restored, receipt.Result.ConfigOptions, receipt.Result.Models, receipt.Sequence)
			current := modelOption(restored.config.options)
			if failure != nil || current == nil || restored.config.currentAt[current.Id] == 0 || current.CurrentValue != opening {
				c.invalidate()
				return acpmeta.ModelCatalog{}, errUnconfirmedSetting
			}
		}
	}
	if ctx.Err() != nil {
		return acpmeta.ModelCatalog{}, ctx.Err()
	}
	c.mu.Lock()
	c.catalog = &catalog
	c.mu.Unlock()
	return clone(catalog)
}
