package claude

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type settingEvidence struct {
	requested    string
	acknowledged string
	observed     string
}

func (s settingEvidence) metadata() map[string]any {
	data := map[string]any{}
	if s.requested != "" {
		data["requested"] = s.requested
	}
	if s.acknowledged != "" {
		data["acknowledged"] = s.acknowledged
	}
	if s.observed != "" {
		data["observed"] = s.observed
	}
	return map[string]any{"claude.configuration": data}
}

type sessionSettings struct {
	models    []protocol.ModelInfo
	model     settingEvidence
	effort    settingEvidence
	fast      settingEvidence
	fastState string
	mode      string
	bypass    bool
}

func initialSettings(init protocol.SystemInitMessage, hello protocol.InitializeControlResponse, effective protocol.SettingsControlResponse, bypass bool) sessionSettings {
	s := sessionSettings{models: hello.Models, bypass: bypass, mode: "default"}
	s.observeFast(hello.FastModeState)
	s.observeInit(init)
	s.observe(effective)
	return s
}

func (s *sessionSettings) observeInit(init protocol.SystemInitMessage) bool {
	before := *s
	if init.Model != "" {
		s.model.observed = s.resolved(init.Model)
	}
	if len(init.Effort) > 0 {
		s.effort.observed = effortValue(init.Effort)
	}
	if init.PermissionMode != "" {
		s.mode = init.PermissionMode
	}
	s.observeFast(init.FastModeState)
	return before.model != s.model || before.effort != s.effort || before.mode != s.mode || before.fastState != s.fastState
}

func (s *sessionSettings) observe(effective protocol.SettingsControlResponse) {
	if model := decodeOptional[string](effective.Applied["model"]); model != nil && *model != "" {
		s.model.observed = s.resolved(*model)
	}
	if raw := effective.Applied["effort"]; len(raw) > 0 {
		s.effort.observed = effortValue(raw)
	}
	if effective.Effective != nil && effective.Effective.FastMode != nil {
		s.fast.observed = onOff(*effective.Effective.FastMode)
	}
}

func (s *sessionSettings) observeFast(value string) bool {
	if value == s.fastState || value != "on" && value != "off" && value != "cooldown" {
		return false
	}
	s.fastState = value
	return true
}

func effortValue(raw json.RawMessage) string {
	if string(raw) == "null" {
		return "default"
	}
	if v := decodeOptional[string](raw); v != nil {
		return *v
	}
	return ""
}

func modelID(row protocol.ModelInfo) string {
	if row.ResolvedModel != "" {
		return row.ResolvedModel
	}
	return row.Value
}

func (s sessionSettings) findModel(id string) (protocol.ModelInfo, bool) {
	for _, m := range s.models {
		if modelID(m) == id || m.Value == id {
			return m, true
		}
	}
	return protocol.ModelInfo{}, false
}

func (s sessionSettings) resolved(id string) string {
	if m, ok := s.findModel(id); ok {
		return modelID(m)
	}
	return id
}

func (s sessionSettings) currentModel() (protocol.ModelInfo, bool) {
	return s.findModel(s.model.observed)
}

func modelOffersFast(models []protocol.ModelInfo) bool {
	return slices.ContainsFunc(models, func(model protocol.ModelInfo) bool { return model.SupportsFastMode })
}

func supportsEffort(m protocol.ModelInfo) bool {
	return len(m.SupportedEffortLevels) > 0 && (m.SupportsEffort || !m.EffortSupportReported)
}

func (s sessionSettings) modes() *acp.SessionModeState {
	rows := []acp.SessionMode{
		{Id: "default", Name: "Default"}, {Id: "acceptEdits", Name: "Accept edits"},
		{Id: "plan", Name: "Plan"}, {Id: "dontAsk", Name: "Don't ask"},
	}
	if m, ok := s.currentModel(); ok && m.SupportsAutoMode {
		rows = append(rows, acp.SessionMode{Id: "auto", Name: "Auto"})
	}
	if s.bypass {
		rows = append(rows, acp.SessionMode{Id: "bypassPermissions", Name: "Bypass permissions"})
	}
	return &acp.SessionModeState{AvailableModes: rows, CurrentModeId: acp.SessionModeId(s.mode)}
}

func (s sessionSettings) options() []acp.SessionConfigOption {
	modes := s.modes()
	var modeOptions acp.SessionConfigSelectOptionsUngrouped
	for _, mode := range modes.AvailableModes {
		modeOptions = append(modeOptions, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(mode.Id), Name: mode.Name})
	}
	result := []acp.SessionConfigOption{choice("mode", "Mode", acp.SessionConfigOptionCategoryMode, s.mode, modeOptions, nil)}
	if len(s.models) > 0 || s.model.observed != "" {
		result = append(result, choice("model", "Model", acp.SessionConfigOptionCategoryModel, s.model.observed, modelOptions(s.models), s.model.metadata()))
	}
	if m, ok := s.currentModel(); ok {
		if supportsEffort(m) {
			result = append(result, effortOption(m, s.effort))
		}
		if m.SupportsFastMode {
			result = append(result, fastOption(s.fast, s.fastState))
		}
	}
	return result
}

func choice(id, name string, category acp.SessionConfigOptionCategory, current string, rows acp.SessionConfigSelectOptionsUngrouped, meta map[string]any) acp.SessionConfigOption {
	if current == "" {
		current = "unknown"
	}
	if current != "unknown" && !slices.ContainsFunc(rows, func(r acp.SessionConfigSelectOption) bool { return string(r.Value) == current }) {
		rows = append(rows, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(current), Name: current})
	}
	value := &acp.SessionConfigOptionSelect{Id: acp.SessionConfigId(id), Name: name, Type: "select", Meta: meta,
		CurrentValue: acp.SessionConfigValueId(current), Options: acp.SessionConfigSelectOptions{Ungrouped: &rows}}
	if category != "" {
		value.Category = &category
	}
	return acp.SessionConfigOption{Select: value}
}

func effortOption(m protocol.ModelInfo, evidence settingEvidence) acp.SessionConfigOption {
	rows := acp.SessionConfigSelectOptionsUngrouped{{Value: "default", Name: "Default"}}
	for _, level := range m.SupportedEffortLevels {
		if level != "" && level != "default" {
			rows = append(rows, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(level), Name: strings.ToUpper(level[:1]) + level[1:]})
		}
	}
	return choice("effort", "Effort", acp.SessionConfigOptionCategoryThoughtLevel, evidence.observed, rows, evidence.metadata())
}

func fastOption(evidence settingEvidence, state string) acp.SessionConfigOption {
	meta := evidence.metadata()
	if state != "" {
		meta["claude.fast-mode-state"] = state
	}
	return choice("fast", "Fast mode", "", evidence.observed, acp.SessionConfigSelectOptionsUngrouped{
		{Value: "default", Name: "Default"}, {Value: "on", Name: "On"}, {Value: "off", Name: "Off"},
	}, meta)
}

func modelOptions(models []protocol.ModelInfo) acp.SessionConfigSelectOptionsUngrouped {
	rows := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		id := modelID(m)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := m.DisplayName
		if name == "" {
			name = id
		}
		catalog := acpmeta.ModelConfigCatalog{}
		if supportsEffort(m) {
			catalog.Options = append(catalog.Options, effortOption(m, settingEvidence{}))
		} else if !m.EffortSupportReported || m.SupportsEffort {
			catalog.UnknownConfigIDs = append(catalog.UnknownConfigIDs, "effort")
		}
		if m.SupportsFastMode {
			catalog.Options = append(catalog.Options, fastOption(settingEvidence{}, ""))
		} else if !m.FastSupportReported {
			catalog.UnknownConfigIDs = append(catalog.UnknownConfigIDs, "fast")
		}
		rows = append(rows, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(id), Name: name, Description: &m.Description, Meta: acpmeta.WithModelConfigCatalog(nil, catalog)})
	}
	return rows
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func (c *Client) bypassEnabled() bool {
	return !c.options.Launch.WorkspaceOnly && (os.Geteuid() != 0 || c.environment("IS_SANDBOX") != "")
}

// SetSessionConfigOption applies a supported native setting to the current idle owner.
func (c *Client) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	var sessionID acp.SessionId
	var id, value string
	switch {
	case req.ValueId != nil:
		sessionID, id, value = req.ValueId.SessionId, string(req.ValueId.ConfigId), string(req.ValueId.Value)
	case req.Boolean != nil && req.Boolean.ConfigId == "fast":
		sessionID, id, value = req.Boolean.SessionId, "fast", onOff(req.Boolean.Value)
	default:
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	s, err := c.getSession(sessionID)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if err := s.configure(ctx, id, value); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return acp.SetSessionConfigOptionResponse{ConfigOptions: s.settings.options()}, nil
}

// SetSessionMode uses the same generation and control gate as configuration.
func (c *Client) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	s, err := c.getSession(req.SessionId)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return acp.SetSessionModeResponse{}, s.configure(ctx, "mode", string(req.ModeId))
}

func (s *session) configure(ctx context.Context, id, value string) error {
	s.mu.Lock()
	if err := s.admissibleLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.busyLocked() {
		s.mu.Unlock()
		return errBusy
	}
	s.setting = true
	before := s.settings
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.setting = false
		s.announceSettingsLocked()
		s.signalLocked()
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, s.client.options.ControlTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	mode := func(value string) error {
		return s.wire.call(ctx, protocol.SetPermissionModeControlRequest{Subtype: protocol.ControlSetPermissionMode, Mode: value}, nil, s.settingGuard, func(json.RawMessage) {
			s.mu.Lock()
			s.settings.mode = value
			s.mu.Unlock()
		})
	}
	setting := func(id, value string, native any) error {
		return s.setFlag(ctx, id, value, native)
	}
	model, modelKnown := before.currentModel()
	switch id {
	case "mode":
		if !slices.ContainsFunc(before.modes().AvailableModes, func(m acp.SessionMode) bool { return string(m.Id) == value }) {
			return acp.NewInvalidParams(nil)
		}
		return mode(value)
	case "model":
		target, ok := before.findModel(value)
		if !ok {
			return acp.NewInvalidParams(nil)
		}
		if before.mode == "auto" && !target.SupportsAutoMode {
			if err := mode("default"); err != nil {
				return err
			}
		}
		effort := firstString(before.effort.observed, before.effort.acknowledged)
		if effort != "" && effort != "default" && !slices.Contains(target.SupportedEffortLevels, effort) {
			if err := setting("effort", "default", nil); err != nil {
				return err
			}
		}
		fast := firstString(before.fast.observed, before.fast.acknowledged)
		if fast == "on" && !target.SupportsFastMode {
			if err := setting("fast", "off", false); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.settings.model.requested = modelID(target)
		s.mu.Unlock()
		err := s.wire.call(ctx, protocol.SetModelControlRequest{Subtype: protocol.ControlSetModel, Model: &target.Value}, nil, s.settingGuard, func(json.RawMessage) {
			s.mu.Lock()
			s.settings.model.acknowledged = modelID(target)
			s.settings.model.observed = ""
			s.settings.effort.observed = ""
			s.mu.Unlock()
		})
		if err != nil {
			return err
		}
	case "effort":
		if !modelKnown || !supportsEffort(model) || value != "default" && !slices.Contains(model.SupportedEffortLevels, value) {
			return acp.NewInvalidParams(nil)
		}
		var native any
		if value != "default" {
			native = value
		}
		if err := setting(id, value, native); err != nil {
			return err
		}
	case "fast":
		if !modelKnown || !model.SupportsFastMode || value != "default" && value != "on" && value != "off" {
			return acp.NewInvalidParams(nil)
		}
		var native any
		if value != "default" {
			native = value == "on"
		}
		if err := setting(id, value, native); err != nil {
			return err
		}
	default:
		return acp.NewInvalidParams(nil)
	}
	// A setter ACK proves acceptance, while effective settings prove the
	// configuration in use. Keep both even when readback is unavailable.
	_ = s.wire.call(ctx, protocol.GetSettingsControlRequest{Subtype: "get_settings"}, nil, s.settingReadGuard, func(raw json.RawMessage) {
		if value := decodeOptional[protocol.SettingsControlResponse](raw); value != nil {
			s.mu.Lock()
			s.settings.observe(*value)
			s.mu.Unlock()
		}
	})
	return nil
}

func (s *session) settingReadGuard() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guardSessionLocked()
}

func (s *session) settingGuard() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guardSessionLocked(); err != nil {
		return err
	}
	if s.current != nil || len(s.sent) > 0 || s.nativeQueued > 0 {
		return errBusy
	}
	return nil
}

func (s *session) setFlag(ctx context.Context, id, value string, native any) error {
	field := &s.settings.effort
	key := "effortLevel"
	if id == "fast" {
		field = &s.settings.fast
		key = "fastMode"
	}
	s.mu.Lock()
	field.requested = value
	s.mu.Unlock()
	return s.wire.call(ctx, protocol.ApplyFlagSettingsControlRequest{
		Subtype: protocol.ControlApplyFlagSettings, Settings: map[string]any{key: native},
	}, nil, s.settingGuard, func(json.RawMessage) {
		s.mu.Lock()
		field.acknowledged = value
		field.observed = ""
		s.mu.Unlock()
	})
}

func (s *session) announceSettingsLocked() {
	if !s.open || s.closing {
		return
	}
	s.sessionUpdateLocked(acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
		SessionUpdate: "config_option_update", ConfigOptions: s.settings.options(),
	}}, nil)
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
