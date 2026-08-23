package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

const (
	// approvalAllowOnce 是 upstream 基础单次授权 option id。
	approvalAllowOnce acp.PermissionOptionId = "allow_once"
	// approvalAllowAlways 是 upstream 基础 session 授权 option id。
	approvalAllowAlways acp.PermissionOptionId = "allow_always"
	// approvalRejectOnce 是 upstream 基础拒绝 option id。
	approvalRejectOnce acp.PermissionOptionId = "reject_once"
	// approvalAcceptExecpolicy 是应用命令策略修订的 option id。
	approvalAcceptExecpolicy acp.PermissionOptionId = "accept_execpolicy_amendment"
	// approvalApplyNetworkPolicyPrefix 是网络策略修订 option id 前缀。
	approvalApplyNetworkPolicyPrefix = "apply_network_policy_amendment"
	// approvalPermissionsTurn 是 turn 范围权限 option id。
	approvalPermissionsTurn acp.PermissionOptionId = "allow_permissions_turn"
	// approvalPermissionsSession 是 session 范围权限 option id。
	approvalPermissionsSession acp.PermissionOptionId = "allow_permissions_session"
	// approvalPermissionsReject 是权限提升拒绝 option id。
	approvalPermissionsReject acp.PermissionOptionId = "reject_permissions"
)

// permissionRequester 是 approval 组件消费的最小 ACP 连接能力。
type permissionRequester interface {
	// RequestPermission 向客户端展示 SDK 原生权限请求并等待结果。
	RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
}

// approvalHandler 将一个 runtime generation 的三类 Codex 审批桥接到 ACP。
type approvalHandler struct {
	// requester 是可能执行用户回调的 ACP 请求边界。
	requester permissionRequester
	// generation 是 handler 创建时绑定的不可变 runtime generation。
	generation turnGeneration
	// guard 在回调前后读取 runtime 当前 generation。
	guard generationGuard
	// logger 只记录错误类别，不记录审批 payload 或环境内容。
	logger *slog.Logger
}

// newApprovalHandler 使用显式注入创建无全局状态的审批处理器。
func newApprovalHandler(
	requester permissionRequester,
	generation turnGeneration,
	guard generationGuard,
	logger *slog.Logger,
) *approvalHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &approvalHandler{
		requester:  requester,
		generation: generation,
		guard:      guard,
		logger:     logger,
	}
}

// HandleCommand 映射命令审批，并在所有异常、取消或 stale 情况下返回 cancel。
func (h *approvalHandler) HandleCommand(
	ctx context.Context,
	params protocol.CommandExecutionRequestApprovalParams,
) protocol.CommandExecutionRequestApprovalResponse {
	failClosed := failClosedCommandApproval()
	if !h.matchesCurrent(params.ThreadID, params.TurnID) {
		return failClosed
	}
	request := commandPermissionRequest(h.generation.SessionID, params)
	response, ok := h.request(ctx, request)
	if !ok || !h.matchesCurrent(params.ThreadID, params.TurnID) {
		return failClosed
	}
	selected, ok := selectedOfferedOption(response, request.Options)
	if !ok {
		return failClosed
	}
	return commandApprovalResponse(selected, params)
}

// HandleFile 映射文件审批，并在所有异常、取消或 stale 情况下返回 cancel。
func (h *approvalHandler) HandleFile(
	ctx context.Context,
	params protocol.FileChangeRequestApprovalParams,
) protocol.FileChangeRequestApprovalResponse {
	failClosed := failClosedFileApproval()
	if !h.matchesCurrent(params.ThreadID, params.TurnID) {
		return failClosed
	}
	request := filePermissionRequest(h.generation.SessionID, params)
	response, ok := h.request(ctx, request)
	if !ok || !h.matchesCurrent(params.ThreadID, params.TurnID) {
		return failClosed
	}
	selected, ok := selectedOfferedOption(response, request.Options)
	if !ok {
		return failClosed
	}
	switch selected {
	case approvalAllowOnce:
		return protocol.FileChangeRequestApprovalResponse{Decision: protocol.Accept}
	case approvalAllowAlways:
		return protocol.FileChangeRequestApprovalResponse{Decision: protocol.AcceptForSession}
	case approvalRejectOnce:
		return protocol.FileChangeRequestApprovalResponse{Decision: protocol.Decline}
	default:
		return failClosed
	}
}

// HandlePermissions 映射细粒度权限请求，异常路径返回空授权和 strict auto-review。
func (h *approvalHandler) HandlePermissions(
	ctx context.Context,
	params protocol.PermissionsRequestApprovalParams,
) protocol.PermissionsRequestApprovalResponse {
	failClosed := failClosedPermissionsApproval()
	if !h.matchesCurrent(params.ThreadID, params.TurnID) {
		return failClosed
	}
	request := permissionsPermissionRequest(h.generation.SessionID, params)
	response, ok := h.request(ctx, request)
	if !ok || !h.matchesCurrent(params.ThreadID, params.TurnID) {
		return failClosed
	}
	selected, ok := selectedOfferedOption(response, request.Options)
	if !ok {
		return failClosed
	}
	strict := protocol.NewOptionalValue(false)
	switch selected {
	case approvalPermissionsTurn:
		scope := protocol.Turn
		return protocol.PermissionsRequestApprovalResponse{
			Permissions: grantedPermissions(params.Permissions), Scope: &scope, StrictAutoReview: strict,
		}
	case approvalPermissionsSession:
		scope := protocol.Session
		return protocol.PermissionsRequestApprovalResponse{
			Permissions: grantedPermissions(params.Permissions), Scope: &scope, StrictAutoReview: strict,
		}
	case approvalPermissionsReject:
		return failClosed
	default:
		return failClosed
	}
}

// matchesCurrent 同时验证请求 thread/turn 和 runtime generation 当前性。
func (h *approvalHandler) matchesCurrent(threadID, turnID string) bool {
	return threadID == h.generation.ThreadID &&
		turnID == h.generation.TurnID &&
		h.guard != nil &&
		h.guard.IsCurrent(h.generation)
}

// request 包裹外部客户端回调，将 error、panic、nil handler 全部转换为失败结果。
func (h *approvalHandler) request(
	ctx context.Context,
	request acp.RequestPermissionRequest,
) (response acp.RequestPermissionResponse, ok bool) {
	if h.requester == nil {
		return acp.RequestPermissionResponse{}, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.logger.Error("ACP permission 回调异常，已 fail-closed")
			response = acp.RequestPermissionResponse{}
			ok = false
		}
	}()
	response, err := h.requester.RequestPermission(ctx, request)
	if err != nil {
		h.logger.Error("ACP permission 请求失败，已 fail-closed")
		return acp.RequestPermissionResponse{}, false
	}
	return response, true
}

// commandPermissionRequest 按 upstream 顺序构造基础选项和可选策略修订选项。
func commandPermissionRequest(
	sessionID acp.SessionId,
	params protocol.CommandExecutionRequestApprovalParams,
) acp.RequestPermissionRequest {
	baseOptions := baseApprovalOptions()
	options := append([]acp.PermissionOption(nil), baseOptions[:2]...)
	if context := params.NetworkApprovalContext; context != nil {
		options[1].Name = "Allow Host for Session"
		options[1].Meta["permission"] = map[string]any{
			"version": 1,
			"changes": []any{map[string]any{
				"type": "grant", "operation": "grant",
				"description": fmt.Sprintf("Allow access to %s for this session", context.Host),
				"lifetime":    map[string]any{"scope": "session"},
				"targets": []any{map[string]any{
					"type": "network",
					"matcher": map[string]any{
						"type": "host", "host": context.Host, "protocol": context.Protocol,
					},
				}},
			}},
		}
	}
	if len(params.ProposedExecpolicyAmendment) > 0 {
		options = append(options, acp.PermissionOption{
			OptionId: approvalAcceptExecpolicy,
			Name:     execpolicyAmendmentLabel(params.ProposedExecpolicyAmendment),
			Kind:     acp.PermissionOptionKindAllowAlways,
			Meta: map[string]any{
				"codex": map[string]any{
					"decision":            "acceptWithExecpolicyAmendment",
					"execpolicyAmendment": params.ProposedExecpolicyAmendment,
				},
				"permission": map[string]any{
					"version": 1,
					"changes": []any{map[string]any{
						"type":         "policy_rule",
						"operation":    "add",
						"ruleBehavior": "allow",
						"description": fmt.Sprintf(
							"Allow commands starting with %s",
							strings.Join(params.ProposedExecpolicyAmendment, " "),
						),
						"targets": []any{map[string]any{
							"type": "command",
							"matcher": map[string]any{
								"type": "argv_prefix", "argv": params.ProposedExecpolicyAmendment,
							},
						}},
					}},
				},
			},
		})
	}
	for index, amendment := range params.ProposedNetworkPolicyAmendments {
		optionID := acp.PermissionOptionId(fmt.Sprintf("%s:%d", approvalApplyNetworkPolicyPrefix, index))
		kind := acp.PermissionOptionKindAllowAlways
		if amendment.Action != protocol.Allow {
			kind = acp.PermissionOptionKindRejectAlways
		}
		options = append(options, acp.PermissionOption{
			OptionId: optionID,
			Name:     networkPolicyAmendmentLabel(amendment),
			Kind:     kind,
			Meta: map[string]any{
				"codex": map[string]any{
					"decision":               "applyNetworkPolicyAmendment",
					"networkPolicyAmendment": amendment,
				},
				"permission": map[string]any{
					"version": 1,
					"changes": []any{map[string]any{
						"type":         "policy_rule",
						"operation":    "add",
						"ruleBehavior": amendment.Action,
						"description":  networkPolicyAmendmentDescription(amendment),
						"targets": []any{map[string]any{
							"type":    "network",
							"matcher": map[string]any{"type": "host", "host": amendment.Host},
						}},
					}},
				},
			},
		})
	}
	options = append(options, baseOptions[2])
	toolCall := acp.ToolCallUpdate{
		ToolCallId: acp.ToolCallId(params.ItemID),
		Kind:       acp.Ptr(acp.ToolKindExecute),
		Status:     acp.Ptr(acp.ToolCallStatusPending),
	}
	if params.Command != nil || params.Cwd != nil {
		toolCall.RawInput = map[string]any{
			"command": stripShellPrefix(stringValue(params.Command)),
			"cwd":     stringValue(params.Cwd),
		}
	}
	return acp.RequestPermissionRequest{
		SessionId: sessionID,
		ToolCall:  toolCall,
		Options:   options,
		Meta:      codexApprovalParamsMeta(params),
	}
}

// execpolicyAmendmentLabel 等价生成 upstream 的命令策略修订标签。
func execpolicyAmendmentLabel(amendment []string) string {
	prefix := strings.Join(amendment, " ")
	if prefix == "" || strings.ContainsAny(prefix, "\r\n") {
		return "Allow and Remember Command Pattern"
	}
	return fmt.Sprintf("Allow Commands Starting With `%s`", prefix)
}

// networkPolicyAmendmentLabel 按 allow/deny 生成 upstream 网络策略标签。
func networkPolicyAmendmentLabel(amendment protocol.NetworkPolicyAmendment) string {
	if amendment.Action == protocol.Allow {
		return fmt.Sprintf("Allow %s in the Future", amendment.Host)
	}
	return fmt.Sprintf("Block %s in the Future", amendment.Host)
}

// networkPolicyAmendmentDescription 按 upstream 生成 common permission 规则说明。
func networkPolicyAmendmentDescription(amendment protocol.NetworkPolicyAmendment) string {
	if amendment.Action == protocol.Allow {
		return fmt.Sprintf("Allow access to %s", amendment.Host)
	}
	return fmt.Sprintf("Block access to %s", amendment.Host)
}

// filePermissionRequest 构造文件修改权限请求并保留 grantRoot 元数据。
func filePermissionRequest(
	sessionID acp.SessionId,
	params protocol.FileChangeRequestApprovalParams,
) acp.RequestPermissionRequest {
	options := baseApprovalOptions()
	grantRoot, hasGrantRoot := params.GrantRoot.Value()
	options[1].Meta["codex"].(map[string]any)["grantRoot"] = nil
	if hasGrantRoot {
		options[1].Name = "Allow Root for Session"
		options[1].Meta["codex"].(map[string]any)["grantRoot"] = grantRoot
		options[1].Meta["permission"] = map[string]any{
			"version": 1,
			"changes": []any{map[string]any{
				"type": "grant", "operation": "grant",
				"description": fmt.Sprintf("Allow writes under %s for this session", grantRoot),
				"lifetime":    map[string]any{"scope": "session"},
				"targets": []any{map[string]any{
					"type": "filesystem", "access": []string{"write"},
					"matcher": map[string]any{"type": "directory", "path": grantRoot},
				}},
			}},
		}
	}
	return acp.RequestPermissionRequest{
		SessionId: sessionID,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(params.ItemID),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
		},
		Options: options,
		Meta:    codexApprovalParamsMeta(params),
	}
}

// permissionsPermissionRequest 构造细粒度权限 ACP toolCall 和三种 request-specific option。
func permissionsPermissionRequest(
	sessionID acp.SessionId,
	params protocol.PermissionsRequestApprovalParams,
) acp.RequestPermissionRequest {
	title := "Additional permissions requested"
	if params.Reason != nil && *params.Reason != "" {
		title = *params.Reason
	}
	content := permissionSummary(params)
	options := []acp.PermissionOption{
		permissionGrantOption(approvalPermissionsSession, "Allow for Session", acp.PermissionOptionKindAllowAlways, "allowPermissionsForSession", "session", params.Permissions),
		permissionGrantOption(approvalPermissionsTurn, "Allow Once", acp.PermissionOptionKindAllowOnce, "allowPermissionsForTurn", "turn", params.Permissions),
		{
			OptionId: approvalPermissionsReject, Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce,
			Meta: map[string]any{"codex": map[string]any{"decision": "rejectPermissions"}},
		},
	}
	return acp.RequestPermissionRequest{
		SessionId: sessionID,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(params.ItemID),
			Kind:       acp.Ptr(acp.ToolKindOther),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
			Title:      &title,
			RawInput:   params,
			Content:    []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(content))},
		},
		Options: options,
		Meta:    codexApprovalParamsMeta(params),
	}
}

// baseApprovalOptions 返回 upstream 三个基础选项及其 Codex decision 元数据。
func baseApprovalOptions() []acp.PermissionOption {
	return []acp.PermissionOption{
		{
			OptionId: approvalAllowOnce, Name: "Allow Once", Kind: acp.PermissionOptionKindAllowOnce,
			Meta: map[string]any{"codex": map[string]any{"decision": "accept"}},
		},
		{
			OptionId: approvalAllowAlways, Name: "Allow for Session", Kind: acp.PermissionOptionKindAllowAlways,
			Meta: map[string]any{"codex": map[string]any{"decision": "acceptForSession"}},
		},
		{
			OptionId: approvalRejectOnce, Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce,
			Meta: map[string]any{"codex": map[string]any{"decision": "decline"}},
		},
	}
}

// permissionGrantOption 构造 permissions 专用授权选项及 common permission 扩展元数据。
func permissionGrantOption(
	id acp.PermissionOptionId,
	name string,
	kind acp.PermissionOptionKind,
	decision string,
	scope string,
	permissions protocol.Permissions,
) acp.PermissionOption {
	meta := map[string]any{
		"codex": map[string]any{"decision": decision, "permissions": permissions},
	}
	// upstream 在没有任何 common permission change 时省略整个 permission 扩展。
	if changes := permissionChanges(scope, permissions); len(changes) > 0 {
		meta["permission"] = map[string]any{
			"version": 1,
			"changes": changes,
		}
	}
	return acp.PermissionOption{
		OptionId: id, Name: name, Kind: kind,
		Meta: meta,
	}
}

// permissionChanges 将生成协议权限转换为 ACP common permission extension 的 grant 列表。
func permissionChanges(scope string, permissions protocol.Permissions) []any {
	changes := make([]any, 0)
	if permissions.Network != nil && permissions.Network.Enabled != nil {
		if *permissions.Network.Enabled {
			changes = append(changes, permissionChange(
				fmt.Sprintf("Allow network access for this %s", scope), scope,
				map[string]any{"type": "network", "matcher": map[string]any{"type": "any"}},
			))
		} else {
			changes = append(changes, map[string]any{
				"type": "policy_rule", "operation": "add", "ruleBehavior": "deny",
				"description": fmt.Sprintf("Deny network access for this %s", scope),
				"lifetime":    map[string]any{"scope": scope},
				"targets":     []any{map[string]any{"type": "network", "matcher": map[string]any{"type": "any"}}},
			})
		}
	}
	if permissions.FileSystem != nil {
		for _, path := range permissions.FileSystem.Read {
			changes = append(changes, permissionChange(
				fmt.Sprintf("Allow read access to %s for this %s", path, scope), scope,
				map[string]any{"type": "filesystem", "access": []string{"read"}, "matcher": map[string]any{"type": "exact_path", "path": path}},
			))
		}
		for _, path := range permissions.FileSystem.Write {
			changes = append(changes, permissionChange(
				fmt.Sprintf("Allow write access to %s for this %s", path, scope), scope,
				map[string]any{"type": "filesystem", "access": []string{"write"}, "matcher": map[string]any{"type": "exact_path", "path": path}},
			))
		}
		for _, entry := range permissions.FileSystem.Entries {
			changes = append(changes, permissionEntryChange(scope, entry))
		}
	}
	return changes
}

// permissionEntryChange 等价映射 exact path、glob、special 与 deny/read/write 组合。
func permissionEntryChange(scope string, entry protocol.FileSystemEntry) map[string]any {
	matcher := map[string]any{}
	descriptionValue := ""
	switch entry.Path.Type {
	case protocol.Path:
		matcher["type"] = "exact_path"
		matcher["path"] = stringValue(entry.Path.Path)
		descriptionValue = stringValue(entry.Path.Path)
	case protocol.GlobPattern:
		matcher["type"] = "glob"
		matcher["pattern"] = stringValue(entry.Path.Pattern)
		descriptionValue = stringValue(entry.Path.Pattern)
	case protocol.Special:
		matcher["type"] = "special"
		matcher["provider"] = "codex"
		matcher["value"] = entry.Path.Value
		encoded, _ := json.Marshal(entry.Path.Value)
		descriptionValue = string(encoded)
	}
	target := map[string]any{"type": "filesystem", "matcher": matcher}
	changeType := "grant"
	operation := "grant"
	verb := "Allow " + string(entry.Access) + " access to"
	change := map[string]any{}
	if entry.Access == protocol.AccessDeny {
		changeType = "policy_rule"
		operation = "add"
		verb = "Deny filesystem access to"
		change["ruleBehavior"] = "deny"
	} else {
		target["access"] = []string{string(entry.Access)}
	}
	change["type"] = changeType
	change["operation"] = operation
	change["description"] = fmt.Sprintf("%s %s for this %s", verb, descriptionValue, scope)
	change["lifetime"] = map[string]any{"scope": scope}
	change["targets"] = []any{target}
	return change
}

// permissionChange 构造一条 common permission grant，避免重复拼装字段。
func permissionChange(description, scope string, target map[string]any) map[string]any {
	return map[string]any{
		"type": "grant", "operation": "grant", "description": description,
		"lifetime": map[string]any{"scope": scope}, "targets": []any{target},
	}
}

// permissionSummary 等价生成 upstream 在审批工具卡片中展示的权限文本。
func permissionSummary(params protocol.PermissionsRequestApprovalParams) string {
	lines := make([]string, 0, 4)
	if params.Reason != nil && *params.Reason != "" {
		lines = append(lines, *params.Reason)
	}
	if params.Permissions.Network != nil && params.Permissions.Network.Enabled != nil {
		lines = append(lines, fmt.Sprintf("Network Access: %t", *params.Permissions.Network.Enabled))
	}
	if params.Permissions.FileSystem != nil {
		if len(params.Permissions.FileSystem.Read) > 0 {
			lines = append(lines, "File System Read Access: "+strings.Join(params.Permissions.FileSystem.Read, ", "))
		}
		if len(params.Permissions.FileSystem.Write) > 0 {
			lines = append(lines, "File System Write Access: "+strings.Join(params.Permissions.FileSystem.Write, ", "))
		}
		if len(params.Permissions.FileSystem.Entries) > 0 {
			encoded, _ := json.Marshal(params.Permissions.FileSystem.Entries)
			lines = append(lines, "File System Entries: "+string(encoded))
		}
	}
	if len(lines) == 0 {
		return "Additional permissions requested"
	}
	return strings.Join(lines, "\n\n")
}

// selectedOfferedOption 校验 SDK outcome discriminator 且拒绝客户端返回未提供的 option。
func selectedOfferedOption(
	response acp.RequestPermissionResponse,
	options []acp.PermissionOption,
) (acp.PermissionOptionId, bool) {
	if response.Outcome.Selected == nil || response.Outcome.Cancelled != nil {
		return "", false
	}
	selected := response.Outcome.Selected.OptionId
	for _, option := range options {
		if option.OptionId == selected {
			return selected, true
		}
	}
	return "", false
}

// commandApprovalResponse 将已验证 option 映射为生成协议的 command decision union。
func commandApprovalResponse(
	selected acp.PermissionOptionId,
	params protocol.CommandExecutionRequestApprovalParams,
) protocol.CommandExecutionRequestApprovalResponse {
	switch selected {
	case approvalAllowOnce:
		return commandEnumResponse(protocol.Accept)
	case approvalAllowAlways:
		return commandEnumResponse(protocol.AcceptForSession)
	case approvalRejectOnce:
		return commandEnumResponse(protocol.Decline)
	case approvalAcceptExecpolicy:
		if len(params.ProposedExecpolicyAmendment) == 0 {
			return failClosedCommandApproval()
		}
		return protocol.CommandExecutionRequestApprovalResponse{
			Decision: &protocol.CommandExecutionApprovalDecision{
				PolicyAmendmentCommandExecutionApprovalDecision: &protocol.PolicyAmendmentCommandExecutionApprovalDecision{
					AcceptWithExecpolicyAmendment: &protocol.AcceptWithExecpolicyAmendment{
						ExecpolicyAmendment: append([]string(nil), params.ProposedExecpolicyAmendment...),
					},
				},
			},
		}
	default:
		prefix := approvalApplyNetworkPolicyPrefix + ":"
		if !strings.HasPrefix(string(selected), prefix) {
			return failClosedCommandApproval()
		}
		var index int
		if _, err := fmt.Sscanf(string(selected), prefix+"%d", &index); err != nil || index < 0 || index >= len(params.ProposedNetworkPolicyAmendments) {
			return failClosedCommandApproval()
		}
		return protocol.CommandExecutionRequestApprovalResponse{
			Decision: &protocol.CommandExecutionApprovalDecision{
				PolicyAmendmentCommandExecutionApprovalDecision: &protocol.PolicyAmendmentCommandExecutionApprovalDecision{
					ApplyNetworkPolicyAmendment: &protocol.ApplyNetworkPolicyAmendment{
						NetworkPolicyAmendment: params.ProposedNetworkPolicyAmendments[index],
					},
				},
			},
		}
	}
}

// commandEnumResponse 构造 command approval 的字符串 union 变体。
func commandEnumResponse(decision protocol.FileChangeApprovalDecision) protocol.CommandExecutionRequestApprovalResponse {
	return protocol.CommandExecutionRequestApprovalResponse{
		Decision: &protocol.CommandExecutionApprovalDecision{Enum: &decision},
	}
}

// failClosedCommandApproval 返回 app-server command 请求的拒绝安全值。
func failClosedCommandApproval() protocol.CommandExecutionRequestApprovalResponse {
	return commandEnumResponse(protocol.Cancel)
}

// failClosedFileApproval 返回 app-server file 请求的拒绝安全值。
func failClosedFileApproval() protocol.FileChangeRequestApprovalResponse {
	return protocol.FileChangeRequestApprovalResponse{Decision: protocol.Cancel}
}

// failClosedPermissionsApproval 返回空授权、turn scope 与 strict auto-review。
func failClosedPermissionsApproval() protocol.PermissionsRequestApprovalResponse {
	scope := protocol.Turn
	return protocol.PermissionsRequestApprovalResponse{
		Permissions:      protocol.GrantedPermissionProfile{},
		Scope:            &scope,
		StrictAutoReview: protocol.NewOptionalValue(true),
	}
}

// grantedPermissions 只复制生成协议的明确权限字段，不扩大授权范围。
func grantedPermissions(permissions protocol.Permissions) protocol.GrantedPermissionProfile {
	return protocol.GrantedPermissionProfile{
		FileSystem: permissions.FileSystem,
		Network:    permissions.Network,
	}
}

// codexApprovalParamsMeta 在 ACP 扩展元数据中保留生成协议 typed params 供客户端展示。
func codexApprovalParamsMeta(params any) map[string]any {
	return map[string]any{"codex": map[string]any{"params": params}}
}
