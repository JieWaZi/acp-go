package codex

import (
	"reflect"
	"testing"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// TestAgentModesMatchUpstream 锁定 codex-acp 的三个 V1 模式及其安全策略。
func TestAgentModesMatchUpstream(t *testing.T) {
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

// TestSessionConfigurationBuildsModelEffortAndModeOptions 验证 ACP config DTO 直接表达上游模型配置。
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
	if model.Options.Ungrouped == nil || !reflect.DeepEqual([]acp.SessionConfigSelectOption(*model.Options.Ungrouped), wantModels) {
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
	if err := config.Select(modelConfigID, "slow-model"); err != nil {
		t.Fatalf("选择 slow-model 返回错误: %v", err)
	}
	if got := config.Selection(); got.Effort != "low" {
		t.Fatalf("effort = %q，期望回退 slow-model 默认 low", got.Effort)
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

// TestSessionConfigurationUsesUpstreamFallbackEffortForCustomModel 验证未编目模型缺省 effort 使用 upstream medium。
func TestSessionConfigurationUsesUpstreamFallbackEffortForCustomModel(t *testing.T) {
	t.Parallel()

	config, err := newSessionConfiguration(testModels(), "custom-model", "", "agent")
	if err != nil {
		t.Fatalf("newSessionConfiguration 返回错误: %v", err)
	}
	if got := config.Selection(); got.Model != "custom-model" || got.Effort != "medium" {
		t.Fatalf("自定义模型选择为 %#v，期望 medium fallback", got)
	}
}

// TestSessionConfigurationRejectsUnknownSelections 验证未知 model、effort、mode 和 config id 立即失败。
func TestSessionConfigurationRejectsUnknownSelections(t *testing.T) {
	t.Parallel()

	for name, selection := range map[string]struct {
		id    acp.SessionConfigId
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

// testModels 创建与 upstream session-config-options 测试一致的模型目录。
func testModels() []protocol.DatumElement {
	return []protocol.DatumElement{
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
