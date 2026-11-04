package codex

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// TestAgentModesExposeStablePolicies 锁定三个 V1 模式及其安全策略。
func TestAgentModesExposeStablePolicies(t *testing.T) {
	t.Parallel()

	modes := agentModes()
	if got, want := len(modes), 3; got != want {
		t.Fatalf("模式数 = %d，期望 %d", got, want)
	}
	if modes[0].ID != "read-only" || modes[0].ApprovalPolicy.Enum == nil || *modes[0].ApprovalPolicy.Enum != protocol.OnRequest {
		t.Fatalf("read-only = %#v，期望 on-request", modes[0])
	}
	if modes[0].SandboxPolicy.Type != protocol.SandboxPolicyTypeReadOnly || modes[0].SandboxPolicy.NetworkAccess == nil || modes[0].SandboxPolicy.NetworkAccess.Bool == nil || *modes[0].SandboxPolicy.NetworkAccess.Bool {
		t.Fatalf("read-only sandbox = %#v，期望只读且禁网", modes[0].SandboxPolicy)
	}
	if modes[1].ID != "agent" || modes[1].SandboxPolicy.Type != protocol.SandboxPolicyTypeWorkspaceWrite {
		t.Fatalf("agent = %#v，期望 workspaceWrite", modes[1])
	}
	if modes[1].SandboxPolicy.WritableRoots == nil || len(modes[1].SandboxPolicy.WritableRoots) != 0 {
		t.Fatalf("agent writable roots = %#v，期望非 nil 空数组", modes[1].SandboxPolicy.WritableRoots)
	}
	if modes[1].SandboxPolicy.ExcludeSlashTmp == nil || *modes[1].SandboxPolicy.ExcludeSlashTmp || modes[1].SandboxPolicy.ExcludeTmpdirEnvVar == nil || *modes[1].SandboxPolicy.ExcludeTmpdirEnvVar {
		t.Fatalf("agent tmp 策略 = %#v，期望两个 false", modes[1].SandboxPolicy)
	}
	if modes[2].ID != "agent-full-access" || modes[2].ApprovalPolicy.Enum == nil || *modes[2].ApprovalPolicy.Enum != protocol.Never || modes[2].SandboxPolicy.Type != protocol.SandboxPolicyTypeDangerFullAccess {
		t.Fatalf("full access = %#v，期望 never/dangerFullAccess", modes[2])
	}
}

// TestSessionConfigurationBuildsModelEffortAndModeOptions 验证 ACP config DTO 表达模型配置。
func TestSessionConfigurationBuildsModelEffortAndModeOptions(t *testing.T) {
	t.Parallel()

	config, err := newSessionConfiguration(testModels(), "fast-model", "medium", "agent")
	if err != nil {
		t.Fatalf("newSessionConfiguration 返回错误: %v", err)
	}
	options := config.Options()
	if got, want := len(options), 3; got != want {
		t.Fatalf("config option 数 = %d，期望 %d", got, want)
	}
	if got := selectOption(t, options, modeConfigID); got.CurrentValue != "agent" || got.Category == nil || *got.Category != acp.SessionConfigOptionCategoryMode {
		t.Fatalf("mode option = %#v", got)
	}
	model := selectOption(t, options, modelConfigID)
	if model.CurrentValue != "fast-model" || model.Category == nil || *model.Category != acp.SessionConfigOptionCategoryModel {
		t.Fatalf("model option = %#v", model)
	}
	wantModels := []acp.SessionConfigSelectOption{
		{Value: "fast-model", Name: "Fast model", Description: acp.Ptr("Frontier")},
		{Value: "slow-model", Name: "Slow model", Description: acp.Ptr("Strong")},
	}
	actualModels := append([]acp.SessionConfigSelectOption(nil), (*model.Options.Ungrouped)...)
	for i := range actualModels {
		actualModels[i].Meta = nil
	}
	if !reflect.DeepEqual(actualModels, wantModels) {
		t.Fatalf("model values = %#v，期望 %#v", model.Options.Ungrouped, wantModels)
	}
	effort := selectOption(t, options, reasoningEffortConfigID)
	if effort.CurrentValue != "medium" || effort.Category == nil || *effort.Category != acp.SessionConfigOptionCategoryThoughtLevel {
		t.Fatalf("effort option = %#v", effort)
	}
}

// TestSessionConfigurationPreservesOrFallsBackEffort 验证切换模型时优先保留支持的 effort。
func TestSessionConfigurationPreservesOrFallsBackEffort(t *testing.T) {
	t.Parallel()

	config, err := newSessionConfiguration(testModels(), "fast-model", "medium", "agent")
	if err != nil {
		t.Fatalf("newSessionConfiguration 返回错误: %v", err)
	}
	if err := config.Select(modelConfigID, "slow-model"); err != nil {
		t.Fatalf("选择 slow-model 返回错误: %v", err)
	}
	if got := config.Selection(); got.Model != "slow-model" || got.Effort != "medium" {
		t.Fatalf("selection = %#v，期望保留 medium", got)
	}
	if err := config.Select(modelConfigID, "fast-model"); err != nil {
		t.Fatalf("选择 fast-model 返回错误: %v", err)
	}
	if err := config.Select(reasoningEffortConfigID, "high"); err != nil {
		t.Fatalf("选择 high 返回错误: %v", err)
	}
	if err := config.Select(modelConfigID, "slow-model"); err == nil {
		t.Fatal("model switch silently discarded explicitly selected effort")
	}
	if got := config.Selection(); got.Model != "fast-model" || got.Effort != "high" {
		t.Fatalf("selection changed after rejected switch: %#v", got)
	}
}

// TestSessionConfigurationKeepsUncataloguedCurrentModel 验证自定义 provider 模型不会被替换或隐藏。
func TestSessionConfigurationKeepsUncataloguedCurrentModel(t *testing.T) {
	t.Parallel()

	config, err := newSessionConfiguration(testModels(), "custom-model", "high", "agent")
	if err != nil {
		t.Fatalf("newSessionConfiguration 返回错误: %v", err)
	}
	options := config.Options()
	model := selectOption(t, options, modelConfigID)
	if model.Options.Ungrouped == nil || len(*model.Options.Ungrouped) != 3 || (*model.Options.Ungrouped)[0].Value != "custom-model" {
		t.Fatalf("自定义模型 options = %#v", model.Options.Ungrouped)
	}
	for _, option := range options {
		if option.Select != nil && option.Select.Id == reasoningEffortConfigID {
			t.Fatal("未编目模型不应声明 reasoning_effort option")
		}
	}
	if err := config.Select(modelConfigID, "custom-model"); err != nil {
		t.Fatalf("重新选择当前自定义模型返回错误: %v", err)
	}
	if got := config.Selection().Effort; got != "high" {
		t.Fatalf("自定义模型 effort = %q，期望保留 high", got)
	}
}

// TestSessionConfigurationKeepsUnknownEffortForCustomModel checks absent effort stays unknown.
func TestSessionConfigurationKeepsUnknownEffortForCustomModel(t *testing.T) {
	t.Parallel()

	config, err := newSessionConfiguration(testModels(), "custom-model", "", "agent")
	if err != nil {
		t.Fatalf("newSessionConfiguration 返回错误: %v", err)
	}
	if got := config.Selection(); got.Model != "custom-model" || got.Effort != "" {
		t.Fatalf("自定义模型选择为 %#v，期望未知 effort", got)
	}
}

// TestSessionConfigurationRejectsUnknownSelections 验证未知 model、effort、mode 和 config id 立即失败。
func TestSessionConfigurationRejectsUnknownSelections(t *testing.T) {
	t.Parallel()

	for name, selection := range map[string]struct {
		// id 是被设置的 session 配置项。
		id acp.SessionConfigId
		// value 是必须被拒绝的未知选择值。
		value string
	}{
		"model":  {modelConfigID, "unknown-model"},
		"effort": {reasoningEffortConfigID, "wishful"},
		"mode":   {modeConfigID, "no-such-mode"},
		"config": {"unknown", "value"},
	} {
		t.Run(name, func(t *testing.T) {
			config, err := newSessionConfiguration(testModels(), "fast-model", "medium", "agent")
			if err != nil {
				t.Fatalf("newSessionConfiguration 返回错误: %v", err)
			}
			if err := config.Select(selection.id, selection.value); err == nil {
				t.Fatalf("Select(%q, %q) 应返回错误", selection.id, selection.value)
			}
		})
	}
}

// testModels 创建覆盖 Session 配置选项的稳定模型目录。
func testModels() []protocol.ModelListResponseDatum {
	return []protocol.ModelListResponseDatum{
		{
			ID: "fast-model", DisplayName: "Fast model", Description: "Frontier", DefaultReasoningEffort: "medium",
			SupportedReasoningEfforts: []protocol.SupportedReasoningEffortElement{
				{ReasoningEffort: "low", Description: "Fast"},
				{ReasoningEffort: "medium", Description: "Balanced"},
				{ReasoningEffort: "high", Description: "Thorough"},
			},
		},
		{
			ID: "slow-model", DisplayName: "Slow model", Description: "Strong", DefaultReasoningEffort: "low",
			SupportedReasoningEfforts: []protocol.SupportedReasoningEffortElement{
				{ReasoningEffort: "low", Description: "Fast"},
				{ReasoningEffort: "medium", Description: "Balanced"},
			},
		},
	}
}

// selectOption 按 id 取得 ACP select option，失败时立即结束测试。
func selectOption(t *testing.T, options []acp.SessionConfigOption, id acp.SessionConfigId) *acp.SessionConfigOptionSelect {
	t.Helper()
	for _, option := range options {
		if option.Select != nil && option.Select.Id == id {
			return option.Select
		}
	}
	t.Fatalf("未找到 config option %q", id)
	return nil
}

// TestModelCatalogExposesPerModelEffortAndFastTier ensures discovery does not require switching models.
func TestModelCatalogExposesPerModelEffortAndFastTier(t *testing.T) {
	models := testModels()
	models[0].ServiceTiers = []protocol.ServiceTierElement{{ID: "express-v2", Name: "Fast"}}
	models[1].AdditionalSpeedTiers = []string{"fast"}
	config, err := newSessionConfiguration(models, "fast-model", "medium", "agent")
	if err != nil {
		t.Fatal(err)
	}
	option := selectOption(t, config.Options(), modelConfigID)
	for i, wantTier := range []string{"express-v2", "fast"} {
		model := (*option.Options.Ungrouped)[i]
		options, known := acpmeta.ModelConfigOptions(model.Meta)
		if !known {
			t.Fatalf("model %s parameters are unknown", model.Value)
		}
		_ = selectOption(t, options, reasoningEffortConfigID)
		tier := selectOption(t, options, "service_tier")
		found := false
		for _, value := range *tier.Options.Ungrouped {
			if string(value.Value) == wantTier {
				found = true
			}
		}
		if !found {
			t.Fatalf("model %s does not expose tier %s", model.Value, wantTier)
		}
	}
}

// TestServiceTierSelectionUsesActualWireValues checks inherit, off, and advertised IDs.
func TestServiceTierSelectionUsesActualWireValues(t *testing.T) {
	models := testModels()
	models[0].ServiceTiers = []protocol.ServiceTierElement{{ID: "express-v2", Name: "Fast"}}
	config, err := newSessionConfiguration(models, "fast-model", "medium", "agent")
	if err != nil {
		t.Fatal(err)
	}
	state := &sessionState{id: "thread", configuration: config}
	for _, tc := range []struct {
		// selection is the ACP choice; want is the resulting JSON value.
		selection, want string
	}{{"inherit", ""}, {"off", "null"}, {"express-v2", `"express-v2"`}} {
		if err := config.Select("service_tier", tc.selection); err != nil {
			t.Fatal(err)
		}
		params := mustTurnParams(t, state)
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if string(wire["serviceTier"]) != tc.want {
			t.Fatalf("%s: got %s, want %s", tc.selection, wire["serviceTier"], tc.want)
		}
	}
	if err := config.Select("service_tier", "priority"); err == nil {
		t.Fatal("unadvertised tier accepted")
	}
}

// TestObservedModelDoesNotBecomeTurnOverride checks that an open response is not a user request.
func TestObservedModelDoesNotBecomeTurnOverride(t *testing.T) {
	config, err := newSessionConfiguration(testModels(), "fast-model", "medium", "agent")
	if err != nil {
		t.Fatal(err)
	}
	state := &sessionState{id: "thread", configuration: config}
	params := mustTurnParams(t, state)
	if params.Model != nil || params.Effort != nil {
		t.Fatalf("observed values sent as overrides: %+v", params)
	}
	if err = config.Select(modelConfigID, "slow-model"); err != nil {
		t.Fatal(err)
	}
	params = mustTurnParams(t, state)
	if params.Model == nil || *params.Model != "slow-model" || params.Effort != nil {
		t.Fatalf("unexpected requested overrides: %+v", params)
	}
	if err = config.Select(reasoningEffortConfigID, "low"); err != nil {
		t.Fatal(err)
	}
	params = mustTurnParams(t, state)
	if params.Effort == nil || *params.Effort != "low" {
		t.Fatalf("selected effort missing: %+v", params)
	}
}

// TestModelCatalogKeepsUnknownAndEmptyParameterListsDistinct protects discovery presence.
func TestModelCatalogKeepsUnknownAndEmptyParameterListsDistinct(t *testing.T) {
	models := testModels()
	models[0].SupportedReasoningEfforts = nil
	models[1].SupportedReasoningEfforts = []protocol.SupportedReasoningEffortElement{}
	models[1].ServiceTiers = []protocol.ServiceTierElement{}
	config, err := newSessionConfiguration(models, "fast-model", "", "agent")
	if err != nil {
		t.Fatal(err)
	}
	model := selectOption(t, config.Options(), modelConfigID)
	unknown, ok := acpmeta.ModelConfigOptions((*model.Options.Ungrouped)[0].Meta)
	if ok {
		t.Fatalf("unknown model parameters reported as known: %#v", unknown)
	}
	empty, ok := acpmeta.ModelConfigOptions((*model.Options.Ungrouped)[1].Meta)
	if !ok || len(empty) != 0 {
		t.Fatalf("empty model parameters not preserved: %#v %v", empty, ok)
	}
}

// TestPerModelEffortSelectionUsesThatModelsDefault protects catalog validity before a switch.
func TestPerModelEffortSelectionUsesThatModelsDefault(t *testing.T) {
	models := testModels()
	models[0].ServiceTiers = []protocol.ServiceTierElement{}
	models[1].ServiceTiers = []protocol.ServiceTierElement{}
	config, err := newSessionConfiguration(models, "fast-model", "high", "agent")
	if err != nil {
		t.Fatal(err)
	}
	model := selectOption(t, config.Options(), modelConfigID)
	options, known := acpmeta.ModelConfigOptions((*model.Options.Ungrouped)[1].Meta)
	if !known {
		t.Fatal("missing model parameters")
	}
	effort := selectOption(t, options, reasoningEffortConfigID)
	if effort.CurrentValue != "low" {
		t.Fatalf("noncurrent model effort = %s, want low", effort.CurrentValue)
	}
}

// TestModelCatalogRetainsKnownEffortAlongsideUnknownTier protects partial catalogs.
func TestModelCatalogRetainsKnownEffortAlongsideUnknownTier(t *testing.T) {
	models := testModels()
	config, err := newSessionConfiguration(models, "fast-model", "", "agent")
	if err != nil {
		t.Fatal(err)
	}
	model := selectOption(t, config.Options(), modelConfigID)
	meta := (*model.Options.Ungrouped)[0].Meta
	if _, complete := acpmeta.ModelConfigOptions(meta); complete {
		t.Fatal("legacy reader mistook the partial catalog for a complete catalog")
	}
	wire, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"unknown_config_ids":["service_tier"]`)) ||
		!bytes.Contains(wire, []byte(`"id":"reasoning_effort"`)) {
		t.Fatalf("partial model catalog: %s", wire)
	}
	var transported map[string]any
	if err := json.Unmarshal(wire, &transported); err != nil {
		t.Fatal(err)
	}
	catalog, reported := acpmeta.ReadModelConfigCatalog(transported)
	if !reported || len(catalog.UnknownConfigIDs) != 1 || catalog.UnknownConfigIDs[0] != serviceTierConfigID {
		t.Fatalf("decoded partial model catalog: %#v", catalog)
	}
	selectOption(t, catalog.Options, reasoningEffortConfigID)

}

// mustTurnParams retrieves a valid session's wire settings.
func mustTurnParams(t *testing.T, state *sessionState) protocol.TurnStartParams {
	t.Helper()
	params, err := turnParamsForSession(state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return params
}
