// Code generated from JSON Schema using quicktype. DO NOT EDIT.
// To parse and unparse this JSON data, add this code to your project and do:
//
//    protocol, err := UnmarshalProtocol(bytes)
//    bytes, err = protocol.Marshal()

package protocol

import "bytes"
import "errors"

import "encoding/json"

func UnmarshalProtocol(data []byte) (Protocol, error) {
	var r Protocol
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *Protocol) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

// V1 Codex app-server protocol roots used by the runtime.
type Protocol struct {
	AccountLoginCompletedNotification       *AccountLoginCompletedNotification       `json:"accountLoginCompletedNotification,omitempty"`
	AgentMessageDeltaNotification           *AgentMessageDeltaNotification           `json:"agentMessageDeltaNotification,omitempty"`
	CancelLoginAccountParams                *CancelLoginAccountParams                `json:"cancelLoginAccountParams,omitempty"`
	CancelLoginAccountResponse              *CancelLoginAccountResponse              `json:"cancelLoginAccountResponse,omitempty"`
	CommandExecutionOutputDeltaNotification *CommandExecutionOutputDeltaNotification `json:"commandExecutionOutputDeltaNotification,omitempty"`
	CommandExecutionRequestApprovalParams   *CommandExecutionRequestApprovalParams   `json:"commandExecutionRequestApprovalParams,omitempty"`
	CommandExecutionRequestApprovalResponse *CommandExecutionRequestApprovalResponse `json:"commandExecutionRequestApprovalResponse,omitempty"`
	ConfigReadParams                        *ConfigReadParams                        `json:"configReadParams,omitempty"`
	ConfigReadResponse                      *ConfigReadResponse                      `json:"configReadResponse,omitempty"`
	ContextCompactedNotification            *ContextCompactedNotification            `json:"contextCompactedNotification,omitempty"`
	ErrorNotification                       *ErrorNotification                       `json:"errorNotification,omitempty"`
	FileChangePatchUpdatedNotification      *FileChangePatchUpdatedNotification      `json:"fileChangePatchUpdatedNotification,omitempty"`
	FileChangeRequestApprovalParams         *FileChangeRequestApprovalParams         `json:"fileChangeRequestApprovalParams,omitempty"`
	FileChangeRequestApprovalResponse       *FileChangeRequestApprovalResponse       `json:"fileChangeRequestApprovalResponse,omitempty"`
	GetAccountParams                        *GetAccountParams                        `json:"getAccountParams,omitempty"`
	GetAccountResponse                      *GetAccountResponse                      `json:"getAccountResponse,omitempty"`
	InitializeParams                        *InitializeParams                        `json:"initializeParams,omitempty"`
	InitializeResponse                      *InitializeResponse                      `json:"initializeResponse,omitempty"`
	ItemCompletedNotification               *ItemCompletedNotification               `json:"itemCompletedNotification,omitempty"`
	ItemStartedNotification                 *ItemStartedNotification                 `json:"itemStartedNotification,omitempty"`
	LoginAccountParams                      *LoginAccountParams                      `json:"loginAccountParams,omitempty"`
	LoginAccountResponse                    *LoginAccountResponse                    `json:"loginAccountResponse,omitempty"`
	LogoutAccountResponse                   map[string]json.RawMessage               `json:"logoutAccountResponse,omitempty"`
	MCPServerElicitationRequestParams       *MCPServerElicitationRequestParams       `json:"mcpServerElicitationRequestParams,omitempty"`
	MCPServerElicitationRequestResponse     *MCPServerElicitationRequestResponse     `json:"mcpServerElicitationRequestResponse,omitempty"`
	MCPServerStatusUpdatedNotification      *MCPServerStatusUpdatedNotification      `json:"mcpServerStatusUpdatedNotification,omitempty"`
	MCPToolCallProgressNotification         *MCPToolCallProgressNotification         `json:"mcpToolCallProgressNotification,omitempty"`
	ModelListParams                         *ModelListParams                         `json:"modelListParams,omitempty"`
	ModelListResponse                       *ModelListResponse                       `json:"modelListResponse,omitempty"`
	ModelReroutedNotification               *ModelReroutedNotification               `json:"modelReroutedNotification,omitempty"`
	PermissionsRequestApprovalParams        *PermissionsRequestApprovalParams        `json:"permissionsRequestApprovalParams,omitempty"`
	PermissionsRequestApprovalResponse      *PermissionsRequestApprovalResponse      `json:"permissionsRequestApprovalResponse,omitempty"`
	ReasoningSummaryPartAddedNotification   *ReasoningSummaryPartAddedNotification   `json:"reasoningSummaryPartAddedNotification,omitempty"`
	ReasoningSummaryTextDeltaNotification   *ReasoningSummaryTextDeltaNotification   `json:"reasoningSummaryTextDeltaNotification,omitempty"`
	ReasoningTextDeltaNotification          *ReasoningTextDeltaNotification          `json:"reasoningTextDeltaNotification,omitempty"`
	ServerRequestResolvedNotification       *ServerRequestResolvedNotification       `json:"serverRequestResolvedNotification,omitempty"`
	SkillsExtraRootsSetParams               *SkillsExtraRootsSetParams               `json:"skillsExtraRootsSetParams,omitempty"`
	SkillsExtraRootsSetResponse             map[string]interface{}                   `json:"skillsExtraRootsSetResponse,omitempty"`
	SkillsListParams                        *SkillsListParams                        `json:"skillsListParams,omitempty"`
	SkillsListResponse                      *SkillsListResponse                      `json:"skillsListResponse,omitempty"`
	TerminalInteractionNotification         *TerminalInteractionNotification         `json:"terminalInteractionNotification,omitempty"`
	ThreadReadParams                        *ThreadReadParams                        `json:"threadReadParams,omitempty"`
	ThreadReadResponse                      *ThreadReadResponse                      `json:"threadReadResponse,omitempty"`
	ThreadResumeParams                      *ThreadResumeParams                      `json:"threadResumeParams,omitempty"`
	ThreadResumeResponse                    *ThreadResumeResponse                    `json:"threadResumeResponse,omitempty"`
	ThreadStartParams                       *ThreadStartParams                       `json:"threadStartParams,omitempty"`
	ThreadStartResponse                     *ThreadStartResponse                     `json:"threadStartResponse,omitempty"`
	ThreadTokenUsageUpdatedNotification     *ThreadTokenUsageUpdatedNotification     `json:"threadTokenUsageUpdatedNotification,omitempty"`
	ThreadUnsubscribeParams                 *ThreadUnsubscribeParams                 `json:"threadUnsubscribeParams,omitempty"`
	ThreadUnsubscribeResponse               *ThreadUnsubscribeResponse               `json:"threadUnsubscribeResponse,omitempty"`
	ToolRequestUserInputParams              *ToolRequestUserInputParams              `json:"toolRequestUserInputParams,omitempty"`
	ToolRequestUserInputResponse            *ToolRequestUserInputResponse            `json:"toolRequestUserInputResponse,omitempty"`
	TurnCompletedNotification               *TurnCompletedNotification               `json:"turnCompletedNotification,omitempty"`
	TurnInterruptParams                     *TurnInterruptParams                     `json:"turnInterruptParams,omitempty"`
	TurnInterruptResponse                   map[string]json.RawMessage               `json:"turnInterruptResponse,omitempty"`
	TurnPlanUpdatedNotification             *TurnPlanUpdatedNotification             `json:"turnPlanUpdatedNotification,omitempty"`
	TurnStartedNotification                 *TurnStartedNotification                 `json:"turnStartedNotification,omitempty"`
	TurnStartParams                         *TurnStartParams                         `json:"turnStartParams,omitempty"`
	TurnStartResponse                       *TurnStartResponse                       `json:"turnStartResponse,omitempty"`
	TurnSteerParams                         *TurnSteerParams                         `json:"turnSteerParams,omitempty"`
	TurnSteerResponse                       *TurnSteerResponse                       `json:"turnSteerResponse,omitempty"`
	WarningNotification                     *WarningNotification                     `json:"warningNotification,omitempty"`
}

type AccountLoginCompletedNotification struct {
	Error                *string                   `json:"error,omitempty"`
	LoginID              *string                   `json:"loginId,omitempty"`
	OnboardingEntrypoint *OnboardingEntrypointEnum `json:"onboardingEntrypoint,omitempty"`
	Success              bool                      `json:"success"`
}

type AgentMessageDeltaNotification struct {
	Delta    string `json:"delta"`
	ItemID   string `json:"itemId"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type CancelLoginAccountParams struct {
	LoginID string `json:"loginId"`
}

type CancelLoginAccountResponse struct {
	Status CancelLoginAccountResponseStatus `json:"status"`
}

type CommandExecutionOutputDeltaNotification struct {
	Delta    string `json:"delta"`
	ItemID   string `json:"itemId"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type CommandExecutionRequestApprovalParams struct {
	// Unique identifier for this specific approval callback.
	//
	// For regular shell/unified_exec approvals, this is null.
	//
	// For zsh-exec-bridge subcommand approvals, multiple callbacks can belong to one parent
	// `itemId`, so `approvalId` is a distinct opaque callback id (a UUID) used to disambiguate
	// routing.
	ApprovalID *string `json:"approvalId,omitempty"`
	// The command to be executed.
	Command *string `json:"command,omitempty"`
	// Best-effort parsed command actions for friendly display.
	CommandActions []CommandAction `json:"commandActions,omitempty"`
	// The command's working directory.
	Cwd *string `json:"cwd,omitempty"`
	// Environment in which the command will run.
	EnvironmentID *string `json:"environmentId,omitempty"`
	ItemID        string  `json:"itemId"`
	// Optional context for a managed-network approval prompt.
	NetworkApprovalContext *NetworkApprovalContext `json:"networkApprovalContext,omitempty"`
	// Optional proposed execpolicy amendment to allow similar commands without prompting.
	ProposedExecpolicyAmendment []string `json:"proposedExecpolicyAmendment,omitempty"`
	// Optional proposed network policy amendments (allow/deny host) for future requests.
	ProposedNetworkPolicyAmendments []NetworkPolicyAmendment `json:"proposedNetworkPolicyAmendments,omitempty"`
	// Optional explanatory reason (e.g. request for network access).
	Reason *string `json:"reason,omitempty"`
	// Unix timestamp (in milliseconds) when this approval request started.
	StartedAtMS int64  `json:"startedAtMs"`
	ThreadID    string `json:"threadId"`
	TurnID      string `json:"turnId"`
}

type CommandAction struct {
	Command string            `json:"command"`
	Name    *string           `json:"name,omitempty"`
	Path    *string           `json:"path,omitempty"`
	Type    CommandActionType `json:"type"`
	Query   *string           `json:"query,omitempty"`
}

type NetworkApprovalContext struct {
	Host     string       `json:"host"`
	Protocol ProtocolEnum `json:"protocol"`
}

type NetworkPolicyAmendment struct {
	Action NetworkPolicyRuleAction `json:"action"`
	Host   string                  `json:"host"`
}

type CommandExecutionRequestApprovalResponse struct {
	Decision *CommandExecutionApprovalDecision `json:"decision"`
}

// User approved the command, and wants to apply the proposed execpolicy amendment so future
// matching commands can run without prompting.
//
// User chose a persistent network policy rule (allow/deny) for this host.
type PolicyAmendmentCommandExecutionApprovalDecision struct {
	AcceptWithExecpolicyAmendment *AcceptWithExecpolicyAmendment `json:"acceptWithExecpolicyAmendment,omitempty"`
	ApplyNetworkPolicyAmendment   *ApplyNetworkPolicyAmendment   `json:"applyNetworkPolicyAmendment,omitempty"`
}

type AcceptWithExecpolicyAmendment struct {
	ExecpolicyAmendment []string `json:"execpolicy_amendment"`
}

type ApplyNetworkPolicyAmendment struct {
	NetworkPolicyAmendment NetworkPolicyAmendment `json:"network_policy_amendment"`
}

type ConfigReadParams struct {
	// Optional working directory to resolve project config layers. If specified, return the
	// effective config as seen from that directory (i.e., including any project layers between
	// `cwd` and the project/repo root).
	Cwd           *string `json:"cwd,omitempty"`
	IncludeLayers *bool   `json:"includeLayers,omitempty"`
}

type ConfigReadResponse struct {
	Config  json.RawMessage        `json:"config"`
	Layers  []LayerElement         `json:"layers,omitempty"`
	Origins map[string]OriginValue `json:"origins"`
}

type Config struct {
	Analytics      *AnalyticsClass `json:"analytics,omitempty"`
	ApprovalPolicy *ApprovalPolicy `json:"approval_policy,omitempty"`
	// [UNSTABLE] Optional default for where approval requests are routed for review.
	ApprovalsReviewer               *ApprovalsReviewerEnum               `json:"approvals_reviewer,omitempty"`
	CompactPrompt                   *string                              `json:"compact_prompt,omitempty"`
	Desktop                         map[string]json.RawMessage           `json:"desktop,omitempty"`
	DeveloperInstructions           *string                              `json:"developer_instructions,omitempty"`
	ForcedChatgptWorkspaceID        *ForcedChatgptWorkspaceID            `json:"forced_chatgpt_workspace_id,omitempty"`
	ForcedLoginMethod               *ForcedLoginMethodEnum               `json:"forced_login_method,omitempty"`
	Instructions                    *string                              `json:"instructions,omitempty"`
	Model                           *string                              `json:"model,omitempty"`
	ModelAutoCompactTokenLimit      *int64                               `json:"model_auto_compact_token_limit,omitempty"`
	ModelAutoCompactTokenLimitScope *ModelAutoCompactTokenLimitScopeEnum `json:"model_auto_compact_token_limit_scope,omitempty"`
	ModelContextWindow              *int64                               `json:"model_context_window,omitempty"`
	ModelProvider                   *string                              `json:"model_provider,omitempty"`
	ModelReasoningEffort            *string                              `json:"model_reasoning_effort,omitempty"`
	ModelReasoningSummary           *SummaryEnum                         `json:"model_reasoning_summary,omitempty"`
	ModelVerbosity                  *ModelVerbosityEnum                  `json:"model_verbosity,omitempty"`
	ReviewModel                     *string                              `json:"review_model,omitempty"`
	SandboxMode                     *SandboxEnum                         `json:"sandbox_mode,omitempty"`
	SandboxWorkspaceWrite           *SandboxWorkspaceWriteClass          `json:"sandbox_workspace_write,omitempty"`
	ServiceTier                     *string                              `json:"service_tier,omitempty"`
	Tools                           *ToolsClass                          `json:"tools,omitempty"`
	WebSearch                       *WebSearchEnum                       `json:"web_search,omitempty"`
}

type AnalyticsClass struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type GranularAskForApproval struct {
	Granular Granular `json:"granular"`
}

type Granular struct {
	MCPElicitations    bool  `json:"mcp_elicitations"`
	RequestPermissions *bool `json:"request_permissions,omitempty"`
	Rules              bool  `json:"rules"`
	SandboxApproval    bool  `json:"sandbox_approval"`
	SkillApproval      *bool `json:"skill_approval,omitempty"`
}

type SandboxWorkspaceWriteClass struct {
	ExcludeSlashTmp     *bool    `json:"exclude_slash_tmp,omitempty"`
	ExcludeTmpdirEnvVar *bool    `json:"exclude_tmpdir_env_var,omitempty"`
	NetworkAccess       *bool    `json:"network_access,omitempty"`
	WritableRoots       []string `json:"writable_roots,omitempty"`
}

type ToolsClass struct {
	WebSearch *WebSearchClass `json:"web_search,omitempty"`
}

type WebSearchClass struct {
	AllowedDomains []string            `json:"allowed_domains,omitempty"`
	ContextSize    *ModelVerbosityEnum `json:"context_size,omitempty"`
	Location       *LocationClass      `json:"location,omitempty"`
}

type LocationClass struct {
	City     *string `json:"city,omitempty"`
	Country  *string `json:"country,omitempty"`
	Region   *string `json:"region,omitempty"`
	Timezone *string `json:"timezone,omitempty"`
}

type LayerElement struct {
	Config         json.RawMessage   `json:"config"`
	DisabledReason *string           `json:"disabledReason,omitempty"`
	Name           ConfigLayerSource `json:"name"`
	Version        string            `json:"version"`
}

// Default configuration supplied with the installed Codex package.
//
// Managed preferences layer delivered by MDM (macOS only).
//
// Managed config layer from a file (usually `managed_config.toml`).
//
// Enterprise-managed config layer delivered by the cloud config bundle.
//
// User config layer from $CODEX_HOME/config.toml. This layer is special in that it is
// expected to be: - writable by the user - generally outside the workspace directory
//
// Path to a .codex/ folder within a project. There could be multiple of these between `cwd`
// and the project/repo root.
//
// Session-layer overrides supplied via `-c`/`--config`.
//
// `managed_config.toml` was designed to be a config that was loaded as the last layer on
// top of everything else. This scheme did not quite work out as intended, but we keep this
// variant as a "best effort" while we phase out `managed_config.toml` in favor of
// `requirements.toml`.
type ConfigLayerSource struct {
	// Path to the packaged default configuration file.
	//
	// This is the path to the system config.toml file, though it is not guaranteed to exist.
	//
	// This is the path to the user's config.toml file, though it is not guaranteed to exist.
	File   *string               `json:"file,omitempty"`
	Type   ConfigLayerSourceType `json:"type"`
	Domain *string               `json:"domain,omitempty"`
	Key    *string               `json:"key,omitempty"`
	// Stable identifier for the delivered layer.
	ID *string `json:"id,omitempty"`
	// Admin-facing name for the delivered layer. This is surfaced in diagnostics so users know
	// which cloud layer needs administrator attention.
	Name *string `json:"name,omitempty"`
	// Name of the selected profile-v2 config layered on top of the base user config, when this
	// layer represents one.
	Profile        *string `json:"profile,omitempty"`
	DotCodexFolder *string `json:"dotCodexFolder,omitempty"`
}

type OriginValue struct {
	Name    ConfigLayerSource `json:"name"`
	Version string            `json:"version"`
}

// Deprecated: Use `ContextCompaction` item type instead.
type ContextCompactedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type ErrorNotification struct {
	Error     Error  `json:"error"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	WillRetry bool   `json:"willRetry"`
}

type Error struct {
	AdditionalDetails *string              `json:"additionalDetails,omitempty"`
	CodexErrorInfo    *CodexErrorInfoUnion `json:"codexErrorInfo,omitempty"`
	Message           string               `json:"message"`
}

// Failed to connect to the response SSE stream.
//
// The response SSE stream disconnected in the middle of a turn before completion.
//
// Reached the retry limit for responses.
//
// Returned when `turn/start` or `turn/steer` is submitted while the current active turn
// cannot accept same-turn steering, for example `/review` or manual `/compact`.
type CodexErrorInfo struct {
	HTTPConnectionFailed           *HTTPConnectionFailed           `json:"httpConnectionFailed,omitempty"`
	ResponseStreamConnectionFailed *ResponseStreamConnectionFailed `json:"responseStreamConnectionFailed,omitempty"`
	ResponseStreamDisconnected     *ResponseStreamDisconnected     `json:"responseStreamDisconnected,omitempty"`
	ResponseTooManyFailedAttempts  *ResponseTooManyFailedAttempts  `json:"responseTooManyFailedAttempts,omitempty"`
	ActiveTurnNotSteerable         *ActiveTurnNotSteerable         `json:"activeTurnNotSteerable,omitempty"`
}

type ActiveTurnNotSteerable struct {
	TurnKind TurnKind `json:"turnKind"`
}

type HTTPConnectionFailed struct {
	HTTPStatusCode *int64 `json:"httpStatusCode,omitempty"`
}

type ResponseStreamConnectionFailed struct {
	HTTPStatusCode *int64 `json:"httpStatusCode,omitempty"`
}

type ResponseStreamDisconnected struct {
	HTTPStatusCode *int64 `json:"httpStatusCode,omitempty"`
}

type ResponseTooManyFailedAttempts struct {
	HTTPStatusCode *int64 `json:"httpStatusCode,omitempty"`
}

type FileChangePatchUpdatedNotification struct {
	Changes  []ChangeElement `json:"changes"`
	ItemID   string          `json:"itemId"`
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
}

type ChangeElement struct {
	Diff string          `json:"diff"`
	Kind PatchChangeKind `json:"kind"`
	Path string          `json:"path"`
}

type PatchChangeKind struct {
	Type     PatchChangeKindType `json:"type"`
	MovePath *string             `json:"move_path,omitempty"`
}

type FileChangeRequestApprovalParams struct {
	// [UNSTABLE] When set, the agent is asking the user to allow writes under this root for the
	// remainder of the session (unclear if this is honored today).
	GrantRoot OptionalNullable[string] `json:"grantRoot,omitzero"`
	ItemID    string                   `json:"itemId"`
	// Optional explanatory reason (e.g. request for extra write access).
	Reason *string `json:"reason,omitempty"`
	// Unix timestamp (in milliseconds) when this approval request started.
	StartedAtMS int64  `json:"startedAtMs"`
	ThreadID    string `json:"threadId"`
	TurnID      string `json:"turnId"`
}

type FileChangeRequestApprovalResponse struct {
	Decision FileChangeApprovalDecision `json:"decision"`
}

type GetAccountParams struct {
	// When `true`, requests a proactive token refresh before returning.
	//
	// In managed auth mode this triggers the normal refresh-token flow. In external auth mode
	// this flag is ignored. Clients should refresh tokens themselves and call
	// `account/login/start` with `chatgptAuthTokens`.
	RefreshToken *bool `json:"refreshToken,omitempty"`
}

type GetAccountResponse struct {
	Account            *Account `json:"account,omitempty"`
	RequiresOpenaiAuth bool     `json:"requiresOpenaiAuth"`
}

type Account struct {
	Type                        AccountType `json:"type"`
	Email                       *string     `json:"email,omitempty"`
	PlanType                    *PlanType   `json:"planType,omitempty"`
	UsesCodexManagedCredentials *bool       `json:"usesCodexManagedCredentials,omitempty"`
}

type InitializeParams struct {
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
	ClientInfo   ClientInfo              `json:"clientInfo"`
}

// Client-declared capabilities negotiated during initialize.
type InitializeCapabilities struct {
	// Opt into receiving experimental API methods and fields.
	ExperimentalAPI *bool `json:"experimentalApi,omitempty"`
	// MCP extension settings declared by the app-server client.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
	// Legacy opt-in for the `openai/form` MCP extension.
	//
	// New clients should declare `openai/form` in [`Self::extensions`].
	MCPServerOpenaiFormElicitation *bool `json:"mcpServerOpenaiFormElicitation,omitempty"`
	// Exact notification method names that should be suppressed for this connection (for
	// example `thread/started`).
	OptOutNotificationMethods []string `json:"optOutNotificationMethods,omitempty"`
	// Opt into `attestation/generate` requests for upstream `x-oai-attestation`.
	RequestAttestation *bool `json:"requestAttestation,omitempty"`
}

type ClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitempty"`
	Version string  `json:"version"`
}

type InitializeResponse struct {
	// Absolute path to the server's $CODEX_HOME directory.
	CodexHome string `json:"codexHome"`
	// Platform family for the running app-server target, for example `"unix"` or `"windows"`.
	PlatformFamily string `json:"platformFamily"`
	// Operating system for the running app-server target, for example `"macos"`, `"linux"`, or
	// `"windows"`.
	PlatformOS string `json:"platformOs"`
	UserAgent  string `json:"userAgent"`
}

type ItemCompletedNotification struct {
	// Unix timestamp (in milliseconds) when this item lifecycle completed.
	CompletedAtMS int64      `json:"completedAtMs"`
	Item          ThreadItem `json:"item"`
	ThreadID      string     `json:"threadId"`
	TurnID        string     `json:"turnId"`
}

// Display item emitted by the interruptible `clock.sleep` tool.
type ThreadItem struct {
	ClientID *string          `json:"clientId,omitempty"`
	Content  []ContentElement `json:"content,omitempty"`
	// Unique identifier for this collab tool call.
	ID             string               `json:"id"`
	Type           ThreadItemType       `json:"type"`
	Fragments      []FragmentElement    `json:"fragments,omitempty"`
	MemoryCitation *MemoryCitationClass `json:"memoryCitation,omitempty"`
	Phase          *PhaseEnum           `json:"phase,omitempty"`
	Text           *string              `json:"text,omitempty"`
	Summary        []string             `json:"summary,omitempty"`
	// The command's output, aggregated from stdout and stderr.
	AggregatedOutput *string `json:"aggregatedOutput,omitempty"`
	// The command to be executed.
	Command *string `json:"command,omitempty"`
	// A best-effort parsing of the command to understand the action(s) it will perform. This
	// returns a list of CommandAction objects because a single shell command may be composed of
	// many commands piped together.
	CommandActions []CommandAction `json:"commandActions,omitempty"`
	// The command's working directory.
	Cwd *string `json:"cwd,omitempty"`
	// The duration of the command execution in milliseconds.
	//
	// The duration of the MCP tool call in milliseconds.
	//
	// The duration of the dynamic tool call in milliseconds.
	DurationMS *int64 `json:"durationMs,omitempty"`
	// The command's exit code.
	ExitCode *int64 `json:"exitCode,omitempty"`
	// Trusted first-party plugin id when this command resolves to one plugin script.
	PluginID *string `json:"pluginId,omitempty"`
	// Identifier for the underlying PTY process (when available).
	ProcessID *string `json:"processId,omitempty"`
	// Safe plugin-relative path when this command resolves to one plugin script.
	ScriptPath *string     `json:"scriptPath,omitempty"`
	Source     *ItemSource `json:"source,omitempty"`
	// Current status of the collab tool call.
	Status     *string          `json:"status,omitempty"`
	Changes    []ChangeElement  `json:"changes,omitempty"`
	AppContext *AppContextClass `json:"appContext,omitempty"`
	Arguments  json.RawMessage  `json:"arguments,omitempty"`
	Error      *ItemError       `json:"error,omitempty"`
	// Deprecated: use `appContext.resourceUri` instead.
	MCPAppResourceURI *string `json:"mcpAppResourceUri,omitempty"`
	ReadOnlyHint      *bool   `json:"readOnlyHint,omitempty"`
	Result            *Result `json:"result,omitempty"`
	Server            *string `json:"server,omitempty"`
	// Name of the collab tool that was invoked.
	Tool         *string                                 `json:"tool,omitempty"`
	ContentItems []InputDynamicToolCallOutputContentItem `json:"contentItems,omitempty"`
	Namespace    *string                                 `json:"namespace,omitempty"`
	Success      *bool                                   `json:"success,omitempty"`
	// Last known status of the target agents, when available.
	AgentsStates map[string]AgentsStateValue `json:"agentsStates,omitempty"`
	// Model requested for the spawned agent, when applicable.
	Model *string `json:"model,omitempty"`
	// Prompt text sent as part of the collab tool call, when available.
	Prompt *string `json:"prompt,omitempty"`
	// Reasoning effort requested for the spawned agent, when applicable.
	ReasoningEffort *string `json:"reasoningEffort,omitempty"`
	// Thread ID of the receiving agent, when applicable. In case of spawn operation, this
	// corresponds to the newly spawned agent.
	ReceiverThreadIDS []string `json:"receiverThreadIds,omitempty"`
	// Thread ID of the agent issuing the collab request.
	SenderThreadID *string          `json:"senderThreadId,omitempty"`
	AgentPath      *string          `json:"agentPath,omitempty"`
	AgentThreadID  *string          `json:"agentThreadId,omitempty"`
	Kind           *ItemKind        `json:"kind,omitempty"`
	Action         *WebSearchAction `json:"action,omitempty"`
	Query          *string          `json:"query,omitempty"`
	// Structured search results returned out-of-band by standalone web search.
	//
	// These stay as opaque JSON at the extension/app-server boundary so new result fields and
	// result types can pass through without a Codex release.
	Results               []json.RawMessage                         `json:"results,omitempty"`
	Path                  *string                                   `json:"path,omitempty"`
	Failure               *UsageLimitExceededImageGenerationFailure `json:"failure,omitempty"`
	RevisedPrompt         *string                                   `json:"revisedPrompt,omitempty"`
	SavedPath             *string                                   `json:"savedPath,omitempty"`
	TransparentBackground *bool                                     `json:"transparentBackground,omitempty"`
	Review                *string                                   `json:"review,omitempty"`
}

type WebSearchAction struct {
	Queries []string            `json:"queries,omitempty"`
	Query   *string             `json:"query,omitempty"`
	Type    WebSearchActionType `json:"type"`
	URL     *string             `json:"url,omitempty"`
	Pattern *string             `json:"pattern,omitempty"`
}

type AgentsStateValue struct {
	Message *string           `json:"message,omitempty"`
	Status  AgentsStateStatus `json:"status"`
}

type AppContextClass struct {
	ActionName  *string `json:"actionName,omitempty"`
	AppName     *string `json:"appName,omitempty"`
	ConnectorID string  `json:"connectorId"`
	LinkID      *string `json:"linkId,omitempty"`
	ResourceURI *string `json:"resourceUri,omitempty"`
}

type UserInput struct {
	Text *string `json:"text,omitempty"`
	// UI-defined spans within `text` used to render or persist special elements.
	TextElements []TextElementElement `json:"text_elements,omitempty"`
	Type         UserInputType        `json:"type"`
	Detail       *DetailEnum          `json:"detail,omitempty"`
	URL          *string              `json:"url,omitempty"`
	Path         *string              `json:"path,omitempty"`
	Name         *string              `json:"name,omitempty"`
}

type TextElementElement struct {
	// Byte range in the parent `text` buffer that this element occupies.
	ByteRange ByteRange `json:"byteRange"`
	// Optional human-readable placeholder for the element, displayed in the UI.
	Placeholder *string `json:"placeholder,omitempty"`
}

// Byte range in the parent `text` buffer that this element occupies.
type ByteRange struct {
	End   int64 `json:"end"`
	Start int64 `json:"start"`
}

type InputDynamicToolCallOutputContentItem struct {
	Text     *string                                   `json:"text,omitempty"`
	Type     InputDynamicToolCallOutputContentItemType `json:"type"`
	ImageURL *string                                   `json:"imageUrl,omitempty"`
	AudioURL *string                                   `json:"audioUrl,omitempty"`
}

type ItemError struct {
	Message string `json:"message"`
}

type UsageLimitExceededImageGenerationFailure struct {
	LimitID  string                                       `json:"limitId"`
	ResetsAt *int64                                       `json:"resetsAt,omitempty"`
	Type     UsageLimitExceededImageGenerationFailureType `json:"type"`
}

type FragmentElement struct {
	HookRunID string `json:"hookRunId"`
	Text      string `json:"text"`
}

type MemoryCitationClass struct {
	Entries   []MemoryCitationEntry `json:"entries"`
	ThreadIDS []string              `json:"threadIds"`
}

type MemoryCitationEntry struct {
	LineEnd   int64  `json:"lineEnd"`
	LineStart int64  `json:"lineStart"`
	Note      string `json:"note"`
	Path      string `json:"path"`
}

type ResultClass struct {
	Meta              json.RawMessage   `json:"_meta,omitempty"`
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
}

type ItemStartedNotification struct {
	Item ThreadItem `json:"item"`
	// Unix timestamp (in milliseconds) when this item lifecycle started.
	StartedAtMS int64  `json:"startedAtMs"`
	ThreadID    string `json:"threadId"`
	TurnID      string `json:"turnId"`
}

// [UNSTABLE] FOR OPENAI INTERNAL USE ONLY - DO NOT USE. The access token must contain the
// same scopes that Codex-managed ChatGPT auth tokens have.
type LoginAccountParams struct {
	APIKey                    *string       `json:"apiKey,omitempty"`
	Type                      Type          `json:"type"`
	AppBrand                  *AppBrandEnum `json:"appBrand,omitempty"`
	CodexStreamlinedLogin     *bool         `json:"codexStreamlinedLogin,omitempty"`
	UseHostedLoginSuccessPage *bool         `json:"useHostedLoginSuccessPage,omitempty"`
	// Access token (JWT) supplied by the client. This token is used for backend API requests
	// and email extraction.
	AccessToken *string `json:"accessToken,omitempty"`
	// Workspace/account identifier supplied by the client.
	ChatgptAccountID *string `json:"chatgptAccountId,omitempty"`
	// Optional plan type supplied by the client.
	//
	// When `null`, Codex attempts to derive the plan type from access-token claims. If
	// unavailable, the plan defaults to `unknown`.
	ChatgptPlanType *string `json:"chatgptPlanType,omitempty"`
}

type LoginAccountResponse struct {
	Type Type `json:"type"`
	// URL the client should open in a browser to initiate the OAuth flow.
	AuthURL *string `json:"authUrl,omitempty"`
	LoginID *string `json:"loginId,omitempty"`
	// One-time code the user must enter after signing in.
	UserCode *string `json:"userCode,omitempty"`
	// URL the client should open in a browser to complete device code authorization.
	VerificationURL *string `json:"verificationUrl,omitempty"`
}

type MCPServerElicitationRequestParams struct {
	ServerName string `json:"serverName"`
	ThreadID   string `json:"threadId"`
	// Active Codex turn when this elicitation was observed, if app-server could correlate one.
	//
	// This is nullable because MCP models elicitation as a standalone server-to-client request
	// identified by the MCP server request id. It may be triggered during a turn, but turn
	// context is app-server correlation rather than part of the protocol identity of the
	// elicitation itself.
	TurnID          *string         `json:"turnId,omitempty"`
	Meta            json.RawMessage `json:"_meta,omitempty"`
	Message         string          `json:"message"`
	Mode            Mode            `json:"mode"`
	RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"`
	ElicitationID   *string         `json:"elicitationId,omitempty"`
	URL             *string         `json:"url,omitempty"`
}

type MCPServerElicitationRequestResponse struct {
	// Optional client metadata for form-mode action handling.
	Meta   json.RawMessage            `json:"_meta,omitempty"`
	Action MCPServerElicitationAction `json:"action"`
	// Structured user input for accepted elicitations, mirroring RMCP
	// `CreateElicitationResult`.
	//
	// This is nullable because decline/cancel responses have no content.
	Content json.RawMessage `json:"content,omitempty"`
}

type MCPServerStatusUpdatedNotification struct {
	Error         *string                                  `json:"error,omitempty"`
	FailureReason *FailureReasonEnum                       `json:"failureReason,omitempty"`
	Name          string                                   `json:"name"`
	Status        MCPServerStatusUpdatedNotificationStatus `json:"status"`
	ThreadID      *string                                  `json:"threadId,omitempty"`
}

type MCPToolCallProgressNotification struct {
	ItemID   string `json:"itemId"`
	Message  string `json:"message"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type ModelListParams struct {
	// Opaque pagination cursor returned by a previous call.
	Cursor *string `json:"cursor,omitempty"`
	// When true, include models that are hidden from the default picker list.
	IncludeHidden *bool `json:"includeHidden,omitempty"`
	// Optional page size; defaults to a reasonable server-side value.
	Limit *int64 `json:"limit,omitempty"`
}

type ModelListResponse struct {
	Data []ModelListResponseDatum `json:"data"`
	// Opaque cursor to pass to the next call to continue after the last item. If None, there
	// are no more items to return.
	NextCursor *string `json:"nextCursor,omitempty"`
}

type ModelListResponseDatum struct {
	// Deprecated: use `serviceTiers` instead.
	AdditionalSpeedTiers   []string              `json:"additionalSpeedTiers,omitempty"`
	AvailabilityNux        *AvailabilityNuxClass `json:"availabilityNux,omitempty"`
	DefaultReasoningEffort string                `json:"defaultReasoningEffort"`
	// Catalog default service tier id for this model, when one is configured.
	DefaultServiceTier *string                `json:"defaultServiceTier,omitempty"`
	Description        string                 `json:"description"`
	DisplayName        string                 `json:"displayName"`
	Hidden             bool                   `json:"hidden"`
	ID                 string                 `json:"id"`
	InputModalities    []InputModalityElement `json:"inputModalities,omitempty"`
	IsDefault          bool                   `json:"isDefault"`
	Model              string                 `json:"model"`
	ModelSpecialty     *string                `json:"modelSpecialty,omitempty"`
	// Multi-agent runtime declared by this model, when available.
	MultiAgentVersion         *MultiAgentVersionEnum            `json:"multiAgentVersion,omitempty"`
	ServiceTiers              []ServiceTierElement              `json:"serviceTiers,omitempty"`
	SupportedReasoningEfforts []SupportedReasoningEffortElement `json:"supportedReasoningEfforts"`
	SupportsPersonality       *bool                             `json:"supportsPersonality,omitempty"`
	Upgrade                   *string                           `json:"upgrade,omitempty"`
	UpgradeInfo               *UpgradeInfoClass                 `json:"upgradeInfo,omitempty"`
}

type AvailabilityNuxClass struct {
	Message string `json:"message"`
}

type ServiceTierElement struct {
	Description string `json:"description"`
	ID          string `json:"id"`
	Name        string `json:"name"`
}

type SupportedReasoningEffortElement struct {
	Description     string `json:"description"`
	ReasoningEffort string `json:"reasoningEffort"`
}

type UpgradeInfoClass struct {
	MigrationMarkdown *string `json:"migrationMarkdown,omitempty"`
	Model             string  `json:"model"`
	ModelLink         *string `json:"modelLink,omitempty"`
	// Informational Unix timestamp for this upgrade's scheduled retirement, if known.
	RetirementAt *int64  `json:"retirementAt,omitempty"`
	UpgradeCopy  *string `json:"upgradeCopy,omitempty"`
}

type ModelReroutedNotification struct {
	FromModel string `json:"fromModel"`
	Reason    Reason `json:"reason"`
	ThreadID  string `json:"threadId"`
	ToModel   string `json:"toModel"`
	TurnID    string `json:"turnId"`
}

type PermissionsRequestApprovalParams struct {
	Cwd           string      `json:"cwd"`
	EnvironmentID *string     `json:"environmentId,omitempty"`
	ItemID        string      `json:"itemId"`
	Permissions   Permissions `json:"permissions"`
	Reason        *string     `json:"reason,omitempty"`
	// Unix timestamp (in milliseconds) when this approval request started.
	StartedAtMS int64  `json:"startedAtMs"`
	ThreadID    string `json:"threadId"`
	TurnID      string `json:"turnId"`
}

type Permissions struct {
	FileSystem *FileSystemClass `json:"fileSystem,omitempty"`
	Network    *NetworkClass    `json:"network,omitempty"`
}

type FileSystemClass struct {
	Entries          []FileSystemEntry `json:"entries,omitempty"`
	GlobScanMaxDepth *int64            `json:"globScanMaxDepth,omitempty"`
	// This will be removed in favor of `entries`.
	Read []string `json:"read,omitempty"`
	// This will be removed in favor of `entries`.
	Write []string `json:"write,omitempty"`
}

type FileSystemEntry struct {
	Access Access         `json:"access"`
	Path   FileSystemPath `json:"path"`
}

type FileSystemPath struct {
	Path    *string                `json:"path,omitempty"`
	Type    FileSystemPathType     `json:"type"`
	Pattern *string                `json:"pattern,omitempty"`
	Value   *FileSystemSpecialPath `json:"value,omitempty"`
}

type FileSystemSpecialPath struct {
	Kind    ValueKind `json:"kind"`
	Subpath *string   `json:"subpath,omitempty"`
	Path    *string   `json:"path,omitempty"`
}

type NetworkClass struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type PermissionsRequestApprovalResponse struct {
	Permissions GrantedPermissionProfile `json:"permissions"`
	Scope       *PermissionGrantScope    `json:"scope,omitempty"`
	// Review every subsequent command in this turn before normal sandboxed execution.
	StrictAutoReview OptionalNullable[bool] `json:"strictAutoReview,omitzero"`
}

type GrantedPermissionProfile struct {
	FileSystem *FileSystemClass `json:"fileSystem,omitempty"`
	Network    *NetworkClass    `json:"network,omitempty"`
}

type ReasoningSummaryPartAddedNotification struct {
	ItemID       string `json:"itemId"`
	SummaryIndex int64  `json:"summaryIndex"`
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
}

type ReasoningSummaryTextDeltaNotification struct {
	Delta        string `json:"delta"`
	ItemID       string `json:"itemId"`
	SummaryIndex int64  `json:"summaryIndex"`
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
}

type ReasoningTextDeltaNotification struct {
	ContentIndex int64  `json:"contentIndex"`
	Delta        string `json:"delta"`
	ItemID       string `json:"itemId"`
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
}

type ServerRequestResolvedNotification struct {
	RequestID *RequestID `json:"requestId"`
	ThreadID  string     `json:"threadId"`
}

type SkillsExtraRootsSetParams struct {
	ExtraRoots []string `json:"extraRoots"`
}

type SkillsListParams struct {
	// When empty, defaults to the current session working directory.
	Cwds []string `json:"cwds,omitempty"`
	// When true, bypass the skills cache and re-scan skills from disk.
	ForceReload *bool `json:"forceReload,omitempty"`
}

type SkillsListResponse struct {
	Data []SkillsListResponseDatum `json:"data"`
}

type SkillsListResponseDatum struct {
	Cwd    string         `json:"cwd"`
	Errors []ErrorElement `json:"errors"`
	Skills []SkillElement `json:"skills"`
}

type ErrorElement struct {
	Message string `json:"message"`
	Path    string `json:"path"`
}

type SkillElement struct {
	Dependencies *DependenciesClass `json:"dependencies,omitempty"`
	Description  string             `json:"description"`
	Enabled      bool               `json:"enabled"`
	Interface    *InterfaceClass    `json:"interface,omitempty"`
	Name         string             `json:"name"`
	Path         string             `json:"path"`
	Scope        Scope              `json:"scope"`
	// Legacy short_description from SKILL.md. Prefer SKILL.json interface.short_description.
	ShortDescription *string `json:"shortDescription,omitempty"`
}

type DependenciesClass struct {
	Tools []ToolElement `json:"tools"`
}

type ToolElement struct {
	Command     *string `json:"command,omitempty"`
	Description *string `json:"description,omitempty"`
	Transport   *string `json:"transport,omitempty"`
	Type        string  `json:"type"`
	URL         *string `json:"url,omitempty"`
	Value       string  `json:"value"`
}

type InterfaceClass struct {
	BrandColor    *string `json:"brandColor,omitempty"`
	DefaultPrompt *string `json:"defaultPrompt,omitempty"`
	DisplayName   *string `json:"displayName,omitempty"`
	IconLarge     *string `json:"iconLarge,omitempty"`
	// Remote large icon URL from the plugin catalog.
	IconLargeURL *string `json:"iconLargeUrl,omitempty"`
	IconSmall    *string `json:"iconSmall,omitempty"`
	// Remote small icon URL from the plugin catalog.
	IconSmallURL     *string `json:"iconSmallUrl,omitempty"`
	ShortDescription *string `json:"shortDescription,omitempty"`
}

type TerminalInteractionNotification struct {
	ItemID    string `json:"itemId"`
	ProcessID string `json:"processId"`
	Stdin     string `json:"stdin"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
}

type ThreadReadParams struct {
	// When true, include turns and their items from rollout history.
	IncludeTurns *bool  `json:"includeTurns,omitempty"`
	ThreadID     string `json:"threadId"`
}

type ThreadReadResponse struct {
	Thread Thread `json:"thread"`
}

type Thread struct {
	// Optional random unique nickname assigned to an AgentControl-spawned sub-agent.
	AgentNickname *string `json:"agentNickname,omitempty"`
	// Optional role (agent_role) assigned to an AgentControl-spawned sub-agent.
	AgentRole *string `json:"agentRole,omitempty"`
	// Version of the CLI that created the thread.
	CLIVersion string `json:"cliVersion"`
	// Unix timestamp (in seconds) when the thread was created.
	CreatedAt int64 `json:"createdAt"`
	// Working directory captured for the thread.
	Cwd string `json:"cwd"`
	// Whether the thread is ephemeral and should not be materialized on disk.
	Ephemeral bool `json:"ephemeral"`
	// Source thread id when this thread was created by forking another thread.
	ForkedFromID *string `json:"forkedFromId,omitempty"`
	// Optional Git metadata captured when the thread was created.
	GitInfo *GitInfoClass `json:"gitInfo,omitempty"`
	// Identifier for this thread. Codex-generated thread IDs are UUIDv7.
	ID string `json:"id"`
	// Model provider used for this thread (for example, 'openai').
	ModelProvider string `json:"modelProvider"`
	// Optional user-facing thread title.
	Name *string `json:"name,omitempty"`
	// The ID of the parent thread. This will only be set if this thread is a subagent.
	ParentThreadID *string `json:"parentThreadId,omitempty"`
	// [UNSTABLE] Path to the thread on disk.
	Path *string `json:"path,omitempty"`
	// Usually the first user message in the thread, if available.
	Preview string `json:"preview"`
	// Unix timestamp (in seconds) used for thread recency ordering.
	RecencyAt *int64 `json:"recencyAt,omitempty"`
	// The independently persisted section selected for this thread, if any.
	Section *SectionClass `json:"section,omitempty"`
	// Unix timestamp in seconds when the thread entered its current section.
	SectionEnteredAt *int64 `json:"sectionEnteredAt,omitempty"`
	// Session id shared by threads that belong to the same session tree.
	SessionID string `json:"sessionId"`
	// Origin of the thread (CLI, VSCode, codex exec, codex app-server, etc.).
	Source *SourceUnion `json:"source"`
	// Current runtime status for the thread.
	Status ThreadStatus `json:"status"`
	// Optional analytics source classification for this thread.
	ThreadSource *string `json:"threadSource,omitempty"`
	// Only populated on `thread/resume`, `thread/rollback`, `thread/fork`, and `thread/read`
	// (when `includeTurns` is true) responses. For all other responses and notifications
	// returning a Thread, the turns field will be an empty list.
	Turns []TurnElement `json:"turns"`
	// Unix timestamp (in seconds) when the thread was last updated.
	UpdatedAt int64 `json:"updatedAt"`
}

type GitInfoClass struct {
	Branch    *string `json:"branch,omitempty"`
	OriginURL *string `json:"originUrl,omitempty"`
	SHA       *string `json:"sha,omitempty"`
}

// An independently persisted, user-visible thread section.
type SectionClass struct {
	// Optional appearance synchronized across clients.
	Appearance *AppearanceClass `json:"appearance,omitempty"`
	// Opaque UUIDv7 identity that remains stable when the section is renamed.
	ID string `json:"id"`
	// The current user-visible section name.
	Name string `json:"name"`
}

// Extensible visual presentation for a custom thread section.
type AppearanceClass struct {
	Color *string `json:"color,omitempty"`
	Icon  *string `json:"icon,omitempty"`
}

type SessionSource struct {
	Custom   *string   `json:"custom,omitempty"`
	SubAgent *SubAgent `json:"subAgent,omitempty"`
}

type SubAgentSource struct {
	ThreadSpawn *ThreadSpawn `json:"thread_spawn,omitempty"`
	Other       *string      `json:"other,omitempty"`
}

type ThreadSpawn struct {
	AgentNickname  *string `json:"agent_nickname,omitempty"`
	AgentPath      *string `json:"agent_path,omitempty"`
	AgentRole      *string `json:"agent_role,omitempty"`
	Depth          int64   `json:"depth"`
	ParentThreadID string  `json:"parent_thread_id"`
}

// Current runtime status for the thread.
type ThreadStatus struct {
	Type        ThreadStatusType    `json:"type"`
	ActiveFlags []ActiveFlagElement `json:"activeFlags,omitempty"`
}

type TurnElement struct {
	// Unix timestamp (in seconds) when the turn completed.
	CompletedAt *int64 `json:"completedAt,omitempty"`
	// Duration between turn start and completion in milliseconds, if known.
	DurationMS *int64 `json:"durationMs,omitempty"`
	// Only populated when the Turn's status is failed.
	Error *Error `json:"error,omitempty"`
	// Identifier for this turn. Codex-generated turn IDs are UUIDv7.
	ID string `json:"id"`
	// Thread items currently included in this turn payload.
	Items []ThreadItem `json:"items"`
	// Describes how much of `items` has been loaded for this turn.
	ItemsView *ItemsView `json:"itemsView,omitempty"`
	// Unix timestamp (in seconds) when the turn started.
	StartedAt *int64     `json:"startedAt,omitempty"`
	Status    TurnStatus `json:"status"`
}

// There are three ways to resume a thread: 1. By thread_id: load the thread from disk by
// thread_id and resume it. 2. By history: instantiate the thread from memory and resume it.
// 3. By path: load the thread from disk by path and resume it.
//
// For non-running threads, the precedence is: history > non-empty path > thread_id. If
// using history or a non-empty path for a non-running thread, the thread_id param will be
// ignored.
//
// If thread_id identifies a running thread, app-server rejoins that thread and treats a
// non-empty path as a consistency check against the active rollout path. Empty string path
// values are treated as absent.
//
// Prefer using thread_id whenever possible.
type ThreadResumeParams struct {
	ApprovalPolicy *ApprovalPolicy `json:"approvalPolicy,omitempty"`
	// Override where approval requests are routed for review on this thread and subsequent
	// turns.
	ApprovalsReviewer     *ApprovalsReviewerEnum     `json:"approvalsReviewer,omitempty"`
	BaseInstructions      *string                    `json:"baseInstructions,omitempty"`
	Config                map[string]json.RawMessage `json:"config,omitempty"`
	Cwd                   *string                    `json:"cwd,omitempty"`
	DeveloperInstructions *string                    `json:"developerInstructions,omitempty"`
	// Configuration overrides for the resumed thread, if any.
	Model         *string          `json:"model,omitempty"`
	ModelProvider *string          `json:"modelProvider,omitempty"`
	Personality   *PersonalityEnum `json:"personality,omitempty"`
	Sandbox       *SandboxEnum     `json:"sandbox,omitempty"`
	ServiceTier   *string          `json:"serviceTier,omitempty"`
	ThreadID      string           `json:"threadId"`
}

type ThreadResumeResponse struct {
	ApprovalPolicy *ApprovalPolicyUnion `json:"approvalPolicy"`
	// Reviewer currently used for approval requests on this thread.
	ApprovalsReviewer ApprovalsReviewerEnum `json:"approvalsReviewer"`
	Cwd               string                `json:"cwd"`
	// Environment-native paths to instruction source files currently loaded for this thread.
	InstructionSources []string `json:"instructionSources,omitempty"`
	Model              string   `json:"model"`
	ModelProvider      string   `json:"modelProvider"`
	ReasoningEffort    *string  `json:"reasoningEffort,omitempty"`
	// Legacy sandbox policy retained for compatibility. Experimental clients should prefer
	// `activePermissionProfile` for profile provenance.
	Sandbox     SandboxClass `json:"sandbox"`
	ServiceTier *string      `json:"serviceTier,omitempty"`
	Thread      Thread       `json:"thread"`
}

// Legacy sandbox policy retained for compatibility. Experimental clients should prefer
// `activePermissionProfile` for profile provenance.
type SandboxClass struct {
	Type                SandboxPolicyType   `json:"type"`
	NetworkAccess       *NetworkAccessUnion `json:"networkAccess,omitempty"`
	ExcludeSlashTmp     *bool               `json:"excludeSlashTmp,omitempty"`
	ExcludeTmpdirEnvVar *bool               `json:"excludeTmpdirEnvVar,omitempty"`
	WritableRoots       []string            `json:"writableRoots,omitempty"`
}

type ThreadStartParams struct {
	ApprovalPolicy *ApprovalPolicy `json:"approvalPolicy,omitempty"`
	// Override where approval requests are routed for review on this thread and subsequent
	// turns.
	ApprovalsReviewer     *ApprovalsReviewerEnum     `json:"approvalsReviewer,omitempty"`
	BaseInstructions      *string                    `json:"baseInstructions,omitempty"`
	Config                map[string]json.RawMessage `json:"config,omitempty"`
	Cwd                   *string                    `json:"cwd,omitempty"`
	DeveloperInstructions *string                    `json:"developerInstructions,omitempty"`
	Ephemeral             *bool                      `json:"ephemeral,omitempty"`
	Model                 *string                    `json:"model,omitempty"`
	ModelProvider         *string                    `json:"modelProvider,omitempty"`
	Personality           *PersonalityEnum           `json:"personality,omitempty"`
	Sandbox               *SandboxEnum               `json:"sandbox,omitempty"`
	ServiceName           *string                    `json:"serviceName,omitempty"`
	ServiceTier           *string                    `json:"serviceTier,omitempty"`
	SessionStartSource    *SessionStartSourceEnum    `json:"sessionStartSource,omitempty"`
	// Optional client-supplied analytics source classification for this thread.
	ThreadSource *string `json:"threadSource,omitempty"`
}

type ThreadStartResponse struct {
	ApprovalPolicy *ApprovalPolicyUnion `json:"approvalPolicy"`
	// Reviewer currently used for approval requests on this thread.
	ApprovalsReviewer ApprovalsReviewerEnum `json:"approvalsReviewer"`
	Cwd               string                `json:"cwd"`
	// Environment-native paths to instruction source files currently loaded for this thread.
	InstructionSources []string `json:"instructionSources,omitempty"`
	Model              string   `json:"model"`
	ModelProvider      string   `json:"modelProvider"`
	ReasoningEffort    *string  `json:"reasoningEffort,omitempty"`
	// Legacy sandbox policy retained for compatibility. Experimental clients should prefer
	// `activePermissionProfile` for profile provenance.
	Sandbox     SandboxClass `json:"sandbox"`
	ServiceTier *string      `json:"serviceTier,omitempty"`
	Thread      Thread       `json:"thread"`
}

type ThreadTokenUsageUpdatedNotification struct {
	ThreadID   string     `json:"threadId"`
	TokenUsage TokenUsage `json:"tokenUsage"`
	TurnID     string     `json:"turnId"`
}

type TokenUsage struct {
	Last               Last   `json:"last"`
	ModelContextWindow *int64 `json:"modelContextWindow,omitempty"`
	Total              Last   `json:"total"`
}

type Last struct {
	CachedInputTokens     int64  `json:"cachedInputTokens"`
	CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens,omitempty"`
	InputTokens           int64  `json:"inputTokens"`
	OutputTokens          int64  `json:"outputTokens"`
	ReasoningOutputTokens int64  `json:"reasoningOutputTokens"`
	TotalTokens           int64  `json:"totalTokens"`
}

type ThreadUnsubscribeParams struct {
	ThreadID string `json:"threadId"`
}

type ThreadUnsubscribeResponse struct {
	Status ThreadUnsubscribeResponseStatus `json:"status"`
}

// Params sent with a request_user_input event.
type ToolRequestUserInputParams struct {
	// @deprecated Use `isBlocking` to decide whether the request should block.
	AutoResolutionMS *int64                         `json:"autoResolutionMs,omitempty"`
	IsBlocking       bool                           `json:"isBlocking"`
	ItemID           string                         `json:"itemId"`
	Questions        []ToolRequestUserInputQuestion `json:"questions"`
	ThreadID         string                         `json:"threadId"`
	TurnID           string                         `json:"turnId"`
}

// Represents one request_user_input question and its required options.
type ToolRequestUserInputQuestion struct {
	Header   string                       `json:"header"`
	ID       string                       `json:"id"`
	IsOther  *bool                        `json:"isOther,omitempty"`
	IsSecret *bool                        `json:"isSecret,omitempty"`
	Options  []ToolRequestUserInputOption `json:"options,omitempty"`
	Question string                       `json:"question"`
}

// Defines a single selectable option for request_user_input.
type ToolRequestUserInputOption struct {
	Description string `json:"description"`
	Label       string `json:"label"`
}

// Response payload mapping question ids to answers.
type ToolRequestUserInputResponse struct {
	Answers map[string]ToolRequestUserInputAnswer `json:"answers"`
}

// Captures a user's answer to a request_user_input question.
type ToolRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type TurnCompletedNotification struct {
	ThreadID string      `json:"threadId"`
	Turn     TurnElement `json:"turn"`
}

type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type TurnPlanUpdatedNotification struct {
	Explanation *string       `json:"explanation,omitempty"`
	Plan        []PlanElement `json:"plan"`
	ThreadID    string        `json:"threadId"`
	TurnID      string        `json:"turnId"`
}

type PlanElement struct {
	Status PlanStatus `json:"status"`
	Step   string     `json:"step"`
}

type TurnStartParams struct {
	// Override the approval policy for this turn and subsequent turns.
	ApprovalPolicy *ApprovalPolicy `json:"approvalPolicy,omitempty"`
	// Override where approval requests are routed for review on this turn and subsequent turns.
	ApprovalsReviewer   *ApprovalsReviewerEnum `json:"approvalsReviewer,omitempty"`
	ClientUserMessageID *string                `json:"clientUserMessageId,omitempty"`
	// Override the working directory for this turn and subsequent turns.
	Cwd *string `json:"cwd,omitempty"`
	// Override the reasoning effort for this turn and subsequent turns.
	Effort *string        `json:"effort,omitempty"`
	Input  []InputElement `json:"input"`
	// Override the model for this turn and subsequent turns.
	Model *string `json:"model,omitempty"`
	// Optional JSON Schema used to constrain the final assistant message for this turn.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// Override the personality for this turn and subsequent turns.
	Personality *PersonalityEnum `json:"personality,omitempty"`
	// Override the sandbox policy for this turn and subsequent turns.
	SandboxPolicy *SandboxPolicy `json:"sandboxPolicy,omitempty"`
	// Override the service tier for this turn and subsequent turns.
	ServiceTier *string `json:"serviceTier,omitempty"`
	// Override the reasoning summary for this turn and subsequent turns.
	Summary  *SummaryEnum `json:"summary,omitempty"`
	ThreadID string       `json:"threadId"`
}

type InputElement struct {
	Text *string `json:"text,omitempty"`
	// UI-defined spans within `text` used to render or persist special elements.
	TextElements []TextElementElement `json:"text_elements,omitempty"`
	Type         UserInputType        `json:"type"`
	Detail       *DetailEnum          `json:"detail,omitempty"`
	URL          *string              `json:"url,omitempty"`
	Path         *string              `json:"path,omitempty"`
	Name         *string              `json:"name,omitempty"`
}

type SandboxPolicy struct {
	Type                SandboxPolicyType   `json:"type"`
	NetworkAccess       *NetworkAccessUnion `json:"networkAccess,omitempty"`
	ExcludeSlashTmp     *bool               `json:"excludeSlashTmp,omitempty"`
	ExcludeTmpdirEnvVar *bool               `json:"excludeTmpdirEnvVar,omitempty"`
	WritableRoots       []string            `json:"writableRoots,omitempty"`
}

type TurnStartResponse struct {
	Turn TurnElement `json:"turn"`
}

type TurnStartedNotification struct {
	ThreadID string      `json:"threadId"`
	Turn     TurnElement `json:"turn"`
}

type TurnSteerParams struct {
	ClientUserMessageID *string `json:"clientUserMessageId,omitempty"`
	// Required active turn id precondition. The request fails when it does not match the
	// currently active turn.
	ExpectedTurnID string         `json:"expectedTurnId"`
	Input          []InputElement `json:"input"`
	ThreadID       string         `json:"threadId"`
}

type TurnSteerResponse struct {
	TurnID string `json:"turnId"`
}

type WarningNotification struct {
	// Concise warning message for the user.
	Message string `json:"message"`
	// Optional thread target when the warning applies to a specific thread.
	ThreadID *string `json:"threadId,omitempty"`
}

type OnboardingEntrypointEnum string

const (
	LifeSciences OnboardingEntrypointEnum = "life_sciences"
)

type CancelLoginAccountResponseStatus string

const (
	Canceled       CancelLoginAccountResponseStatus = "canceled"
	PurpleNotFound CancelLoginAccountResponseStatus = "notFound"
)

type CommandActionType string

const (
	CommandActionTypeRead    CommandActionType = "read"
	CommandActionTypeSearch  CommandActionType = "search"
	CommandActionTypeUnknown CommandActionType = "unknown"
	ListFiles                CommandActionType = "listFiles"
)

type ProtocolEnum string

const (
	HTTP      ProtocolEnum = "http"
	HTTPS     ProtocolEnum = "https"
	Socks5TCP ProtocolEnum = "socks5Tcp"
	Socks5UDP ProtocolEnum = "socks5Udp"
)

type NetworkPolicyRuleAction string

const (
	Allow                       NetworkPolicyRuleAction = "allow"
	NetworkPolicyRuleActionDeny NetworkPolicyRuleAction = "deny"
)

// User approved the command.
//
// User approved the command and future prompts in the same session-scoped approval cache
// should run without prompting.
//
// User denied the command. The agent will continue the turn.
//
// User denied the command. The turn will also be immediately interrupted.
//
// User approved the file changes.
//
// User approved the file changes and future changes to the same files should run without
// prompting.
//
// User denied the file changes. The agent will continue the turn.
//
// User denied the file changes. The turn will also be immediately interrupted.
type FileChangeApprovalDecision string

const (
	AcceptForSession FileChangeApprovalDecision = "acceptForSession"
	Accept           FileChangeApprovalDecision = "accept"
	Cancel           FileChangeApprovalDecision = "cancel"
	Decline          FileChangeApprovalDecision = "decline"
)

type ApprovalPolicyEnum string

const (
	Never     ApprovalPolicyEnum = "never"
	OnRequest ApprovalPolicyEnum = "on-request"
	Untrusted ApprovalPolicyEnum = "untrusted"
)

// Configures who approval requests are routed to for review. Examples include sandbox
// escapes, blocked network access, MCP approval prompts, and ARC escalations. Defaults to
// `user`. `auto_review` uses a carefully prompted subagent to gather relevant context and
// apply a risk-based decision framework before approving or denying the request. The legacy
// value `guardian_subagent` is accepted for compatibility.
//
// Reviewer currently used for approval requests on this thread.
type ApprovalsReviewerEnum string

const (
	AutoReview                        ApprovalsReviewerEnum = "auto_review"
	CodexAppServerProtocolSchemasUser ApprovalsReviewerEnum = "user"
	GuardianSubagent                  ApprovalsReviewerEnum = "guardian_subagent"
)

type ForcedLoginMethodEnum string

const (
	API           ForcedLoginMethodEnum = "api"
	PurpleChatgpt ForcedLoginMethodEnum = "chatgpt"
)

// Count the full active context against the limit.
//
// Count sampled output and later growth after the carried window prefix.
type ModelAutoCompactTokenLimitScopeEnum string

const (
	BodyAfterPrefix ModelAutoCompactTokenLimitScopeEnum = "body_after_prefix"
	Total           ModelAutoCompactTokenLimitScopeEnum = "total"
)

// Option to disable reasoning summaries.
type SummaryEnum string

const (
	Concise    SummaryEnum = "concise"
	Detailed   SummaryEnum = "detailed"
	PurpleAuto SummaryEnum = "auto"
	PurpleNone SummaryEnum = "none"
)

// Controls output length/detail on GPT-5 models via the Responses API. Serialized with
// lowercase values to match the OpenAI API.
type ModelVerbosityEnum string

const (
	Medium     ModelVerbosityEnum = "medium"
	PurpleHigh ModelVerbosityEnum = "high"
	PurpleLow  ModelVerbosityEnum = "low"
)

type SandboxEnum string

const (
	DangerFullAccess SandboxEnum = "danger-full-access"
	ReadOnly         SandboxEnum = "read-only"
	WorkspaceWrite   SandboxEnum = "workspace-write"
)

type WebSearchEnum string

const (
	Cached         WebSearchEnum = "cached"
	Indexed        WebSearchEnum = "indexed"
	Live           WebSearchEnum = "live"
	PurpleDisabled WebSearchEnum = "disabled"
)

type ConfigLayerSourceType string

const (
	ConfigLayerSourceTypeSystem     ConfigLayerSourceType = "system"
	ConfigLayerSourceTypeUser       ConfigLayerSourceType = "user"
	EnterpriseManaged               ConfigLayerSourceType = "enterpriseManaged"
	LegacyManagedConfigTomlFromFile ConfigLayerSourceType = "legacyManagedConfigTomlFromFile"
	LegacyManagedConfigTomlFromMdm  ConfigLayerSourceType = "legacyManagedConfigTomlFromMdm"
	Mdm                             ConfigLayerSourceType = "mdm"
	PackagedDefaults                ConfigLayerSourceType = "packagedDefaults"
	Project                         ConfigLayerSourceType = "project"
	SessionFlags                    ConfigLayerSourceType = "sessionFlags"
)

type TurnKind string

const (
	TurnKindCompact TurnKind = "compact"
	TurnKindReview  TurnKind = "review"
)

type CodexErrorInfoEnum string

const (
	BadRequest                                      CodexErrorInfoEnum = "badRequest"
	CodexAppServerProtocolSchemasOther              CodexErrorInfoEnum = "other"
	CodexAppServerProtocolSchemasUsageLimitExceeded CodexErrorInfoEnum = "usageLimitExceeded"
	ContextWindowExceeded                           CodexErrorInfoEnum = "contextWindowExceeded"
	CyberPolicy                                     CodexErrorInfoEnum = "cyberPolicy"
	InternalServerError                             CodexErrorInfoEnum = "internalServerError"
	MisalignmentPolicyViolation                     CodexErrorInfoEnum = "misalignmentPolicyViolation"
	SandboxError                                    CodexErrorInfoEnum = "sandboxError"
	ServerOverloaded                                CodexErrorInfoEnum = "serverOverloaded"
	SessionBudgetExceeded                           CodexErrorInfoEnum = "sessionBudgetExceeded"
	ThreadRollbackFailed                            CodexErrorInfoEnum = "threadRollbackFailed"
	Unauthorized                                    CodexErrorInfoEnum = "unauthorized"
)

type PatchChangeKindType string

const (
	Add    PatchChangeKindType = "add"
	Delete PatchChangeKindType = "delete"
	Update PatchChangeKindType = "update"
)

type PlanType string

const (
	Business                    PlanType = "business"
	Edu                         PlanType = "edu"
	Ent26                       PlanType = "ent26"
	Enterprise                  PlanType = "enterprise"
	EnterpriseCbpAutomation     PlanType = "enterprise_cbp_automation"
	EnterpriseCbpUsageBased     PlanType = "enterprise_cbp_usage_based"
	Free                        PlanType = "free"
	Go                          PlanType = "go"
	PlanTypeUnknown             PlanType = "unknown"
	Plus                        PlanType = "plus"
	Pro                         PlanType = "pro"
	Prolite                     PlanType = "prolite"
	SelfServeBusinessProlite    PlanType = "self_serve_business_prolite"
	SelfServeBusinessUsageBased PlanType = "self_serve_business_usage_based"
	Team                        PlanType = "team"
)

type AccountType string

const (
	AccountTypeAPIKey  AccountType = "apiKey"
	AccountTypeChatgpt AccountType = "chatgpt"
)

type WebSearchActionType string

const (
	FindInPage                WebSearchActionType = "findInPage"
	OpenPage                  WebSearchActionType = "openPage"
	WebSearchActionTypeOther  WebSearchActionType = "other"
	WebSearchActionTypeSearch WebSearchActionType = "search"
)

type AgentsStateStatus string

const (
	Errored           AgentsStateStatus = "errored"
	FluffyNotFound    AgentsStateStatus = "notFound"
	PendingInit       AgentsStateStatus = "pendingInit"
	PurpleCompleted   AgentsStateStatus = "completed"
	PurpleInterrupted AgentsStateStatus = "interrupted"
	Running           AgentsStateStatus = "running"
	Shutdown          AgentsStateStatus = "shutdown"
)

type DetailEnum string

const (
	FluffyAuto DetailEnum = "auto"
	FluffyHigh DetailEnum = "high"
	FluffyLow  DetailEnum = "low"
	Original   DetailEnum = "original"
)

type UserInputType string

const (
	LocalAudio         UserInputType = "localAudio"
	LocalImage         UserInputType = "localImage"
	Mention            UserInputType = "mention"
	Skill              UserInputType = "skill"
	UserInputTypeAudio UserInputType = "audio"
	UserInputTypeImage UserInputType = "image"
	UserInputTypeText  UserInputType = "text"
)

type InputDynamicToolCallOutputContentItemType string

const (
	InputAudio InputDynamicToolCallOutputContentItemType = "inputAudio"
	InputImage InputDynamicToolCallOutputContentItemType = "inputImage"
	InputText  InputDynamicToolCallOutputContentItemType = "inputText"
)

type UsageLimitExceededImageGenerationFailureType string

const (
	UsageLimitExceededImageGenerationFailureTypeUsageLimitExceeded UsageLimitExceededImageGenerationFailureType = "usageLimitExceeded"
)

type ItemKind string

const (
	Interacted      ItemKind = "interacted"
	KindInterrupted ItemKind = "interrupted"
	Started         ItemKind = "started"
)

// Mid-turn assistant text (for example preamble/progress narration).
//
// Additional tool calls or assistant output may follow before turn completion.
//
// The assistant's terminal answer text for the current turn.
type PhaseEnum string

const (
	Commentary  PhaseEnum = "commentary"
	FinalAnswer PhaseEnum = "final_answer"
)

type ItemSource string

const (
	Agent                  ItemSource = "agent"
	UnifiedExecInteraction ItemSource = "unifiedExecInteraction"
	UnifiedExecStartup     ItemSource = "unifiedExecStartup"
	UserShell              ItemSource = "userShell"
)

type ThreadItemType string

const (
	AgentMessage        ThreadItemType = "agentMessage"
	CollabAgentToolCall ThreadItemType = "collabAgentToolCall"
	CommandExecution    ThreadItemType = "commandExecution"
	ContextCompaction   ThreadItemType = "contextCompaction"
	DynamicToolCall     ThreadItemType = "dynamicToolCall"
	EnteredReviewMode   ThreadItemType = "enteredReviewMode"
	ExitedReviewMode    ThreadItemType = "exitedReviewMode"
	FileChange          ThreadItemType = "fileChange"
	HookPrompt          ThreadItemType = "hookPrompt"
	ImageGeneration     ThreadItemType = "imageGeneration"
	ImageView           ThreadItemType = "imageView"
	MCPToolCall         ThreadItemType = "mcpToolCall"
	Reasoning           ThreadItemType = "reasoning"
	Sleep               ThreadItemType = "sleep"
	SubAgentActivity    ThreadItemType = "subAgentActivity"
	UserMessage         ThreadItemType = "userMessage"
	WebSearch           ThreadItemType = "webSearch"
)

type AppBrandEnum string

const (
	Codex         AppBrandEnum = "codex"
	FluffyChatgpt AppBrandEnum = "chatgpt"
)

type Type string

const (
	ChatgptAuthTokens Type = "chatgptAuthTokens"
	ChatgptDeviceCode Type = "chatgptDeviceCode"
	TypeAPIKey        Type = "apiKey"
	TypeChatgpt       Type = "chatgpt"
)

type Mode string

const (
	Form       Mode = "form"
	OpenaiForm Mode = "openai/form"
	URL        Mode = "url"
)

type MCPServerElicitationAction string

const (
	MCPServerElicitationActionAccept  MCPServerElicitationAction = "accept"
	MCPServerElicitationActionCancel  MCPServerElicitationAction = "cancel"
	MCPServerElicitationActionDecline MCPServerElicitationAction = "decline"
)

type FailureReasonEnum string

const (
	ReauthenticationRequired FailureReasonEnum = "reauthenticationRequired"
)

type MCPServerStatusUpdatedNotificationStatus string

const (
	Cancelled    MCPServerStatusUpdatedNotificationStatus = "cancelled"
	PurpleFailed MCPServerStatusUpdatedNotificationStatus = "failed"
	Ready        MCPServerStatusUpdatedNotificationStatus = "ready"
	Starting     MCPServerStatusUpdatedNotificationStatus = "starting"
)

// Canonical user-input modality tags advertised by a model.
//
// Plain text turns and tool payloads.
//
// Image attachments included in user turns.
//
// Audio attachments included in user turns.
type InputModalityElement string

const (
	CodexAppServerProtocolSchemasAudio InputModalityElement = "audio"
	CodexAppServerProtocolSchemasImage InputModalityElement = "image"
	CodexAppServerProtocolSchemasText  InputModalityElement = "text"
)

// Multi-agent runtime supported by a model.
type MultiAgentVersionEnum string

const (
	FluffyDisabled MultiAgentVersionEnum = "disabled"
	V1             MultiAgentVersionEnum = "v1"
	V2             MultiAgentVersionEnum = "v2"
)

type Reason string

const (
	HighRiskCyberActivity Reason = "highRiskCyberActivity"
)

type Access string

const (
	AccessDeny Access = "deny"
	AccessRead Access = "read"
	Write      Access = "write"
)

type FileSystemPathType string

const (
	GlobPattern FileSystemPathType = "glob_pattern"
	Path        FileSystemPathType = "path"
	Special     FileSystemPathType = "special"
)

type ValueKind string

const (
	KindUnknown  ValueKind = "unknown"
	Minimal      ValueKind = "minimal"
	ProjectRoots ValueKind = "project_roots"
	Root         ValueKind = "root"
	SlashTmp     ValueKind = "slash_tmp"
	Tmpdir       ValueKind = "tmpdir"
)

type PermissionGrantScope string

const (
	Session PermissionGrantScope = "session"
	Turn    PermissionGrantScope = "turn"
)

type Scope string

const (
	Admin       Scope = "admin"
	Repo        Scope = "repo"
	ScopeSystem Scope = "system"
	ScopeUser   Scope = "user"
)

type SubAgentEnum string

const (
	CodexAppServerProtocolSchemasCompact SubAgentEnum = "compact"
	CodexAppServerProtocolSchemasReview  SubAgentEnum = "review"
	MemoryConsolidation                  SubAgentEnum = "memory_consolidation"
)

type SourceEnum string

const (
	AppServer                            SourceEnum = "appServer"
	CLI                                  SourceEnum = "cli"
	CodexAppServerProtocolSchemasUnknown SourceEnum = "unknown"
	Exec                                 SourceEnum = "exec"
	Vscode                               SourceEnum = "vscode"
)

type ActiveFlagElement string

const (
	WaitingOnApproval  ActiveFlagElement = "waitingOnApproval"
	WaitingOnUserInput ActiveFlagElement = "waitingOnUserInput"
)

type ThreadStatusType string

const (
	Active                    ThreadStatusType = "active"
	Idle                      ThreadStatusType = "idle"
	SystemError               ThreadStatusType = "systemError"
	ThreadStatusTypeNotLoaded ThreadStatusType = "notLoaded"
)

// Describes how much of `items` has been loaded for this turn.
//
// `items` was not loaded for this turn. The field is intentionally empty.
//
// `items` contains only a display summary for this turn.
//
// `items` contains every ThreadItem available from persisted app-server history for this
// turn.
type ItemsView string

const (
	Full               ItemsView = "full"
	ItemsViewNotLoaded ItemsView = "notLoaded"
	Summary            ItemsView = "summary"
)

type TurnStatus string

const (
	FluffyCompleted   TurnStatus = "completed"
	Failed            TurnStatus = "failed"
	FluffyInterrupted TurnStatus = "interrupted"
	PurpleInProgress  TurnStatus = "inProgress"
)

type PersonalityEnum string

const (
	FluffyNone PersonalityEnum = "none"
	Friendly   PersonalityEnum = "friendly"
	Pragmatic  PersonalityEnum = "pragmatic"
)

type NetworkAccess string

const (
	Enabled    NetworkAccess = "enabled"
	Restricted NetworkAccess = "restricted"
)

type SandboxPolicyType string

const (
	ExternalSandbox                   SandboxPolicyType = "externalSandbox"
	SandboxPolicyTypeDangerFullAccess SandboxPolicyType = "dangerFullAccess"
	SandboxPolicyTypeReadOnly         SandboxPolicyType = "readOnly"
	SandboxPolicyTypeWorkspaceWrite   SandboxPolicyType = "workspaceWrite"
)

type SessionStartSourceEnum string

const (
	Clear   SessionStartSourceEnum = "clear"
	Startup SessionStartSourceEnum = "startup"
)

type ThreadUnsubscribeResponseStatus string

const (
	NotSubscribed   ThreadUnsubscribeResponseStatus = "notSubscribed"
	StatusNotLoaded ThreadUnsubscribeResponseStatus = "notLoaded"
	Unsubscribed    ThreadUnsubscribeResponseStatus = "unsubscribed"
)

type PlanStatus string

const (
	FluffyInProgress   PlanStatus = "inProgress"
	Pending            PlanStatus = "pending"
	TentacledCompleted PlanStatus = "completed"
)

type CommandExecutionApprovalDecision struct {
	Enum                                            *FileChangeApprovalDecision
	PolicyAmendmentCommandExecutionApprovalDecision *PolicyAmendmentCommandExecutionApprovalDecision
}

func (x *CommandExecutionApprovalDecision) UnmarshalJSON(data []byte) error {
	x.PolicyAmendmentCommandExecutionApprovalDecision = nil
	x.Enum = nil
	var c PolicyAmendmentCommandExecutionApprovalDecision
	object, err := unmarshalUnion(data, nil, nil, nil, nil, false, nil, true, &c, false, nil, true, &x.Enum, false)
	if err != nil {
		return err
	}
	if object {
		x.PolicyAmendmentCommandExecutionApprovalDecision = &c
	}
	return nil
}

func (x *CommandExecutionApprovalDecision) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.PolicyAmendmentCommandExecutionApprovalDecision != nil, x.PolicyAmendmentCommandExecutionApprovalDecision, false, nil, x.Enum != nil, x.Enum, false)
}

type ApprovalPolicy struct {
	Enum                   *ApprovalPolicyEnum
	GranularAskForApproval *GranularAskForApproval
}

func (x *ApprovalPolicy) UnmarshalJSON(data []byte) error {
	x.GranularAskForApproval = nil
	x.Enum = nil
	var c GranularAskForApproval
	object, err := unmarshalUnion(data, nil, nil, nil, nil, false, nil, true, &c, false, nil, true, &x.Enum, true)
	if err != nil {
		return err
	}
	if object {
		x.GranularAskForApproval = &c
	}
	return nil
}

func (x *ApprovalPolicy) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.GranularAskForApproval != nil, x.GranularAskForApproval, false, nil, x.Enum != nil, x.Enum, true)
}

type ForcedChatgptWorkspaceID struct {
	String      *string
	StringArray []string
}

func (x *ForcedChatgptWorkspaceID) UnmarshalJSON(data []byte) error {
	x.StringArray = nil
	object, err := unmarshalUnion(data, nil, nil, nil, &x.String, true, &x.StringArray, false, nil, false, nil, false, nil, true)
	if err != nil {
		return err
	}
	if object {
	}
	return nil
}

func (x *ForcedChatgptWorkspaceID) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, x.String, x.StringArray != nil, x.StringArray, false, nil, false, nil, false, nil, true)
}

type CodexErrorInfoUnion struct {
	CodexErrorInfo *CodexErrorInfo
	Enum           *CodexErrorInfoEnum
}

func (x *CodexErrorInfoUnion) UnmarshalJSON(data []byte) error {
	x.CodexErrorInfo = nil
	x.Enum = nil
	var c CodexErrorInfo
	object, err := unmarshalUnion(data, nil, nil, nil, nil, false, nil, true, &c, false, nil, true, &x.Enum, true)
	if err != nil {
		return err
	}
	if object {
		x.CodexErrorInfo = &c
	}
	return nil
}

func (x *CodexErrorInfoUnion) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.CodexErrorInfo != nil, x.CodexErrorInfo, false, nil, x.Enum != nil, x.Enum, true)
}

type ContentElement struct {
	String    *string
	UserInput *UserInput
}

func (x *ContentElement) UnmarshalJSON(data []byte) error {
	x.UserInput = nil
	var c UserInput
	object, err := unmarshalUnion(data, nil, nil, nil, &x.String, false, nil, true, &c, false, nil, false, nil, false)
	if err != nil {
		return err
	}
	if object {
		x.UserInput = &c
	}
	return nil
}

func (x *ContentElement) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, x.String, false, nil, x.UserInput != nil, x.UserInput, false, nil, false, nil, false)
}

type Result struct {
	ResultClass *ResultClass
	String      *string
}

func (x *Result) UnmarshalJSON(data []byte) error {
	x.ResultClass = nil
	var c ResultClass
	object, err := unmarshalUnion(data, nil, nil, nil, &x.String, false, nil, true, &c, false, nil, false, nil, true)
	if err != nil {
		return err
	}
	if object {
		x.ResultClass = &c
	}
	return nil
}

func (x *Result) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, x.String, false, nil, x.ResultClass != nil, x.ResultClass, false, nil, false, nil, true)
}

type RequestID struct {
	Integer *int64
	String  *string
}

func (x *RequestID) UnmarshalJSON(data []byte) error {
	object, err := unmarshalUnion(data, &x.Integer, nil, nil, &x.String, false, nil, false, nil, false, nil, false, nil, false)
	if err != nil {
		return err
	}
	if object {
	}
	return nil
}

func (x *RequestID) MarshalJSON() ([]byte, error) {
	return marshalUnion(x.Integer, nil, nil, x.String, false, nil, false, nil, false, nil, false, nil, false)
}

// Origin of the thread (CLI, VSCode, codex exec, codex app-server, etc.).
type SourceUnion struct {
	Enum          *SourceEnum
	SessionSource *SessionSource
}

func (x *SourceUnion) UnmarshalJSON(data []byte) error {
	x.SessionSource = nil
	x.Enum = nil
	var c SessionSource
	object, err := unmarshalUnion(data, nil, nil, nil, nil, false, nil, true, &c, false, nil, true, &x.Enum, false)
	if err != nil {
		return err
	}
	if object {
		x.SessionSource = &c
	}
	return nil
}

func (x *SourceUnion) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.SessionSource != nil, x.SessionSource, false, nil, x.Enum != nil, x.Enum, false)
}

type SubAgent struct {
	Enum           *SubAgentEnum
	SubAgentSource *SubAgentSource
}

func (x *SubAgent) UnmarshalJSON(data []byte) error {
	x.SubAgentSource = nil
	x.Enum = nil
	var c SubAgentSource
	object, err := unmarshalUnion(data, nil, nil, nil, nil, false, nil, true, &c, false, nil, true, &x.Enum, false)
	if err != nil {
		return err
	}
	if object {
		x.SubAgentSource = &c
	}
	return nil
}

func (x *SubAgent) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.SubAgentSource != nil, x.SubAgentSource, false, nil, x.Enum != nil, x.Enum, false)
}

type ApprovalPolicyUnion struct {
	Enum                   *ApprovalPolicyEnum
	GranularAskForApproval *GranularAskForApproval
}

func (x *ApprovalPolicyUnion) UnmarshalJSON(data []byte) error {
	x.GranularAskForApproval = nil
	x.Enum = nil
	var c GranularAskForApproval
	object, err := unmarshalUnion(data, nil, nil, nil, nil, false, nil, true, &c, false, nil, true, &x.Enum, false)
	if err != nil {
		return err
	}
	if object {
		x.GranularAskForApproval = &c
	}
	return nil
}

func (x *ApprovalPolicyUnion) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.GranularAskForApproval != nil, x.GranularAskForApproval, false, nil, x.Enum != nil, x.Enum, false)
}

type NetworkAccessUnion struct {
	Bool *bool
	Enum *NetworkAccess
}

func (x *NetworkAccessUnion) UnmarshalJSON(data []byte) error {
	x.Enum = nil
	object, err := unmarshalUnion(data, nil, nil, &x.Bool, nil, false, nil, false, nil, false, nil, true, &x.Enum, false)
	if err != nil {
		return err
	}
	if object {
	}
	return nil
}

func (x *NetworkAccessUnion) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, x.Bool, nil, false, nil, false, nil, false, nil, x.Enum != nil, x.Enum, false)
}

func unmarshalUnion(data []byte, pi **int64, pf **float64, pb **bool, ps **string, haveArray bool, pa interface{}, haveObject bool, pc interface{}, haveMap bool, pm interface{}, haveEnum bool, pe interface{}, nullable bool) (bool, error) {
	if pi != nil {
		*pi = nil
	}
	if pf != nil {
		*pf = nil
	}
	if pb != nil {
		*pb = nil
	}
	if ps != nil {
		*ps = nil
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}

	switch v := tok.(type) {
	case json.Number:
		if pi != nil {
			i, err := v.Int64()
			if err == nil {
				*pi = &i
				return false, nil
			}
		}
		if pf != nil {
			f, err := v.Float64()
			if err == nil {
				*pf = &f
				return false, nil
			}
			return false, errors.New("Unparsable number")
		}
		return false, errors.New("Union does not contain number")
	case float64:
		return false, errors.New("Decoder should not return float64")
	case bool:
		if pb != nil {
			*pb = &v
			return false, nil
		}
		return false, errors.New("Union does not contain bool")
	case string:
		if haveEnum {
			return false, json.Unmarshal(data, pe)
		}
		if ps != nil {
			*ps = &v
			return false, nil
		}
		return false, errors.New("Union does not contain string")
	case nil:
		if nullable {
			return false, nil
		}
		return false, errors.New("Union does not contain null")
	case json.Delim:
		if v == '{' {
			if haveObject {
				return true, json.Unmarshal(data, pc)
			}
			if haveMap {
				return false, json.Unmarshal(data, pm)
			}
			return false, errors.New("Union does not contain object")
		}
		if v == '[' {
			if haveArray {
				return false, json.Unmarshal(data, pa)
			}
			return false, errors.New("Union does not contain array")
		}
		return false, errors.New("Cannot handle delimiter")
	}
	return false, errors.New("Cannot unmarshal union")
}

func marshalUnion(pi *int64, pf *float64, pb *bool, ps *string, haveArray bool, pa interface{}, haveObject bool, pc interface{}, haveMap bool, pm interface{}, haveEnum bool, pe interface{}, nullable bool) ([]byte, error) {
	if pi != nil {
		return json.Marshal(*pi)
	}
	if pf != nil {
		return json.Marshal(*pf)
	}
	if pb != nil {
		return json.Marshal(*pb)
	}
	if ps != nil {
		return json.Marshal(*ps)
	}
	if haveArray {
		return json.Marshal(pa)
	}
	if haveObject {
		return json.Marshal(pc)
	}
	if haveMap {
		return json.Marshal(pm)
	}
	if haveEnum {
		return json.Marshal(pe)
	}
	if nullable {
		return json.Marshal(nil)
	}
	return nil, errors.New("Union must not be null")
}
