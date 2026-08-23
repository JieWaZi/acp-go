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

// Source: Codex 0.148.0 default stable app-server schema
// (codex_app_server_protocol.schemas.json, generated without --experimental).
type Protocol struct {
	CancelLoginAccountParams                *CancelLoginAccountParams                `json:"cancelLoginAccountParams,omitempty"`
	CancelLoginAccountResponse              *CancelLoginAccountResponse              `json:"cancelLoginAccountResponse,omitempty"`
	ClientNotification                      *ClientNotificationClass                 `json:"clientNotification,omitempty"`
	ClientRequest                           *ClientRequest                           `json:"clientRequest,omitempty"`
	CommandExecutionRequestApprovalParams   *CommandExecutionRequestApprovalParams   `json:"commandExecutionRequestApprovalParams,omitempty"`
	CommandExecutionRequestApprovalResponse *CommandExecutionRequestApprovalResponse `json:"commandExecutionRequestApprovalResponse,omitempty"`
	ConfigReadParams                        *ConfigReadParams                        `json:"configReadParams,omitempty"`
	ConfigReadResponse                      *ConfigReadResponse                      `json:"configReadResponse,omitempty"`
	FileChangeRequestApprovalParams         *FileChangeRequestApprovalParams         `json:"fileChangeRequestApprovalParams,omitempty"`
	FileChangeRequestApprovalResponse       *FileChangeRequestApprovalResponse       `json:"fileChangeRequestApprovalResponse,omitempty"`
	GetAccountParams                        *GetAccountParams                        `json:"getAccountParams,omitempty"`
	GetAccountResponse                      *GetAccountResponse                      `json:"getAccountResponse,omitempty"`
	InitializeParams                        *InitializeParams                        `json:"initializeParams,omitempty"`
	InitializeResponse                      *InitializeResponse                      `json:"initializeResponse,omitempty"`
	LoginAccountParams                      *LoginAccountParams                      `json:"loginAccountParams,omitempty"`
	LoginAccountResponse                    *LoginAccountResponse                    `json:"loginAccountResponse,omitempty"`
	LogoutAccountResponse                   map[string]interface{}                   `json:"logoutAccountResponse,omitempty"`
	ModelListParams                         *ModelListParams                         `json:"modelListParams,omitempty"`
	ModelListResponse                       *ModelListResponse                       `json:"modelListResponse,omitempty"`
	PermissionsRequestApprovalParams        *PermissionsRequestApprovalParams        `json:"permissionsRequestApprovalParams,omitempty"`
	PermissionsRequestApprovalResponse      *PermissionsRequestApprovalResponse      `json:"permissionsRequestApprovalResponse,omitempty"`
	ServerNotification                      *ServerNotification                      `json:"serverNotification,omitempty"`
	ServerRequest                           *ServerRequest                           `json:"serverRequest,omitempty"`
	ThreadReadParams                        *ThreadReadParams                        `json:"threadReadParams,omitempty"`
	ThreadReadResponse                      *ThreadReadResponse                      `json:"threadReadResponse,omitempty"`
	ThreadResumeParams                      *ThreadResumeParams                      `json:"threadResumeParams,omitempty"`
	ThreadResumeResponse                    *ThreadResumeResponse                    `json:"threadResumeResponse,omitempty"`
	ThreadStartParams                       *ThreadStartParams                       `json:"threadStartParams,omitempty"`
	ThreadStartResponse                     *ThreadStartResponse                     `json:"threadStartResponse,omitempty"`
	ThreadUnsubscribeParams                 *ThreadUnsubscribeParams                 `json:"threadUnsubscribeParams,omitempty"`
	ThreadUnsubscribeResponse               *ThreadUnsubscribeResponse               `json:"threadUnsubscribeResponse,omitempty"`
	TurnInterruptParams                     *TurnInterruptParams                     `json:"turnInterruptParams,omitempty"`
	TurnInterruptResponse                   map[string]interface{}                   `json:"turnInterruptResponse,omitempty"`
	TurnStartParams                         *TurnStartParams                         `json:"turnStartParams,omitempty"`
	TurnStartResponse                       *TurnStartResponse                       `json:"turnStartResponse,omitempty"`
	TurnSteerParams                         *TurnSteerParams                         `json:"turnSteerParams,omitempty"`
	TurnSteerResponse                       *TurnSteerResponse                       `json:"turnSteerResponse,omitempty"`
}

type CancelLoginAccountParams struct {
	LoginID string `json:"loginId"`
}

type CancelLoginAccountResponse struct {
	Status CancelLoginAccountResponseStatus `json:"status"`
}

type ClientNotificationClass struct {
	Method InitializedNotificationMethod `json:"method"`
}

// Request from the client to the server.
//
// # NEW APIs
//
// Append raw Responses API items to the thread history without starting a user turn.
//
// Execute a standalone command (argv vector) under the server's sandbox.
//
// Write stdin bytes to a running `command/exec` session or close stdin.
//
// Terminate a running `command/exec` session by client-supplied `processId`.
//
// Resize a running PTY-backed `command/exec` session by client-supplied `processId`.
type ClientRequest struct {
	ID     *ID                 `json:"id"`
	Method ClientRequestMethod `json:"method"`
	Params *Params             `json:"params,omitempty"`
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
//
// There are two ways to fork a thread: 1. By thread_id: load the thread from disk by
// thread_id and fork it into a new thread. 2. By path: load the thread from disk by path
// and fork it into a new thread.
//
// If using a non-empty path, the thread_id param will be ignored. Empty string path values
// are treated as absent.
//
// Prefer using thread_id whenever possible.
//
// Parameters for moving a thread within a server-owned section ordering.
//
// DEPRECATED: `thread/rollback` will be removed soon.
//
// Parameters for listing independently persisted thread sections.
//
// Parameters for creating an independently persisted thread section.
//
// Parameters for updating an independently persisted thread section.
//
// Parameters for deleting an independently persisted thread section.
//
// EXPERIMENTAL - read metadata for specific apps/connectors.
//
// EXPERIMENTAL - list available apps/connectors.
//
// Read the committed installed connector runtime snapshot.
//
// Read a file from the host filesystem.
//
// Write a file on the host filesystem.
//
// Create a directory on the host filesystem.
//
// Request metadata for an absolute path.
//
// List direct child names for a directory.
//
// Remove a file or directory tree from the host filesystem.
//
// Copy a file or directory tree on the host filesystem.
//
// Start filesystem watch notifications for an absolute path.
//
// Stop filesystem watch notifications for a prior `fs/watch`.
//
// [UNSTABLE] FOR OPENAI INTERNAL USE ONLY - DO NOT USE. The access token must contain the
// same scopes that Codex-managed ChatGPT auth tokens have.
//
// [UNSTABLE] Managed Amazon Bedrock login is experimental.
//
// Run a standalone command (argv vector) in the server sandbox without creating a thread or
// turn.
//
// The final `command/exec` response is deferred until the process exits and is sent only
// after all `command/exec/outputDelta` notifications for that connection have been
// emitted.
//
// Write stdin bytes to a running `command/exec` session, close stdin, or both.
//
// Terminate a running `command/exec` session.
//
// Resize a running PTY-backed `command/exec` session.
type Params struct {
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
	ClientInfo   *ClientInfo             `json:"clientInfo,omitempty"`
	// Override the approval policy for this turn and subsequent turns.
	ApprovalPolicy *ApprovalPolicy `json:"approvalPolicy,omitempty"`
	// Override where approval requests are routed for review on this thread and subsequent
	// turns.
	//
	// Override where approval requests are routed for review on this turn and subsequent turns.
	ApprovalsReviewer *ApprovalsReviewerEnum `json:"approvalsReviewer,omitempty"`
	BaseInstructions  *string                `json:"baseInstructions,omitempty"`
	Config            map[string]interface{} `json:"config,omitempty"`
	// Optional cwd filter or filters; when set, only threads whose session cwd exactly matches
	// one of these paths are returned.
	//
	// Override the working directory for this turn and subsequent turns.
	//
	// Optional working directory to resolve project config layers.
	//
	// Optional working directory. Defaults to the server cwd.
	//
	// Optional working directory to resolve project config layers. If specified, return the
	// effective config as seen from that directory (i.e., including any project layers between
	// `cwd` and the project/repo root).
	Cwd                   *ForcedChatgptWorkspaceID `json:"cwd,omitempty"`
	DeveloperInstructions *string                   `json:"developerInstructions,omitempty"`
	Ephemeral             *bool                     `json:"ephemeral,omitempty"`
	// Configuration overrides for the resumed thread, if any.
	//
	// Configuration overrides for the forked thread, if any.
	//
	// Override the model for this turn and subsequent turns.
	Model         *string `json:"model,omitempty"`
	ModelProvider *string `json:"modelProvider,omitempty"`
	// Override the personality for this turn and subsequent turns.
	Personality *PersonalityEnum `json:"personality,omitempty"`
	Sandbox     *SandboxEnum     `json:"sandbox,omitempty"`
	ServiceName *string          `json:"serviceName,omitempty"`
	// Override the service tier for this turn and subsequent turns.
	ServiceTier        *string                 `json:"serviceTier,omitempty"`
	SessionStartSource *SessionStartSourceEnum `json:"sessionStartSource,omitempty"`
	// Optional client-supplied analytics source classification for this thread.
	//
	// Optional client-supplied analytics source classification for this forked thread.
	ThreadSource *string `json:"threadSource,omitempty"`
	// Thread to move into, within, or out of a section.
	//
	// Optional loaded thread id used to evaluate effective app configuration.
	//
	// Optional thread id used to evaluate app feature gating from that thread's config.
	//
	// Optional loaded thread id. Pass this when showing feature state for an existing thread so
	// enablement is computed from that thread's refreshed config, including project-local
	// config for the thread's cwd.
	//
	// When present, read estimated usage for this thread instead of account-wide token activity.
	ThreadID *string `json:"threadId,omitempty"`
	// Optional last turn id to fork through, inclusive.
	//
	// When specified, turns after `last_turn_id` are omitted from the fork. The referenced turn
	// cannot be in progress.
	LastTurnID *string `json:"lastTurnId,omitempty"`
	// The user-visible name of the section.
	//
	// The updated user-visible name of the section.
	//
	// Name-based selector.
	Name        *string     `json:"name,omitempty"`
	Objective   *string     `json:"objective,omitempty"`
	Status      *StatusEnum `json:"status,omitempty"`
	TokenBudget *int64      `json:"tokenBudget,omitempty"`
	// Patch the stored Git metadata for this thread. Omit a field to leave it unchanged, set it
	// to `null` to clear it, or provide a string to replace the stored value.
	GitInfo *InitializeParamsGitInfo `json:"gitInfo,omitempty"`
	// Existing thread to insert before; omission or null appends to the section.
	BeforeThreadID *string `json:"beforeThreadId,omitempty"`
	// Destination section, or `null` to remove the thread from its section.
	//
	// Omit to include every section, set to `null` for unsectioned threads, or provide a
	// section ID to return only threads in that section.
	//
	// The stable, server-generated identity of the section to update.
	//
	// The stable, server-generated identity of the section to delete.
	SectionID *string `json:"sectionId,omitempty"`
	// Shell command string evaluated by the thread's configured shell. Unlike `command/exec`,
	// this intentionally preserves shell syntax such as pipes, redirects, and quoting. This
	// runs unsandboxed with full access rather than inheriting the thread sandbox policy.
	//
	// Command argv vector. Empty arrays are rejected.
	Command *Command `json:"command,omitempty"`
	// Serialized `codex_protocol::protocol::GuardianAssessmentEvent`.
	Event interface{} `json:"event,omitempty"`
	// The number of turns to drop from the end of the thread. Must be >= 1.
	//
	// This only modifies the thread's history and does not revert local file changes that have
	// been made by the agent. Clients are responsible for reverting these changes.
	NumTurns *int64 `json:"numTurns,omitempty"`
	// Optional archived filter; when set to true, only archived threads are returned. If false
	// or null, only non-archived threads are returned.
	Archived *bool `json:"archived,omitempty"`
	// Opaque pagination cursor returned by a previous call.
	Cursor *string `json:"cursor,omitempty"`
	// Optional page size; defaults to a reasonable server-side value.
	//
	// Maximum number of sections to return.
	//
	// Optional page size; defaults to no limit.
	//
	// Optional page size; defaults to the full result set.
	//
	// Optional page size; defaults to a server-defined value.
	Limit *int64 `json:"limit,omitempty"`
	// Optional provider filter; when set, only sessions recorded under these providers are
	// returned. When present but empty, includes all providers.
	ModelProviders []string `json:"modelProviders,omitempty"`
	// Optional substring filter for the extracted thread title.
	SearchTerm *string `json:"searchTerm,omitempty"`
	// Optional sort direction; defaults to descending (newest first).
	SortDirection *SortDirectionEnum `json:"sortDirection,omitempty"`
	// Optional sort key; defaults to created_at.
	SortKey *SortKeyEnum `json:"sortKey,omitempty"`
	// Optional source filter; when set, only sessions from these source kinds are returned.
	// When omitted or empty, defaults to interactive sources.
	SourceKinds []SourceKindElement `json:"sourceKinds,omitempty"`
	// If true, return from the state DB without scanning JSONL rollouts to repair thread
	// metadata. Omitted or false preserves scan-and-repair behavior.
	UseStateDBOnly *bool `json:"useStateDbOnly,omitempty"`
	// Omit to preserve appearance, use `null` to clear it, or provide a replacement.
	Appearance *AppearanceClass `json:"appearance,omitempty"`
	// When true, include turns and their items from rollout history.
	IncludeTurns *bool `json:"includeTurns,omitempty"`
	// Raw Responses API items to append to the thread's model-visible history.
	Items []interface{} `json:"items,omitempty"`
	// When empty, defaults to the current session working directory.
	//
	// Optional working directories used to discover repo marketplaces. When omitted, only
	// home-scoped marketplaces and the official curated marketplace are considered.
	//
	// Optional working directories used to discover repo marketplaces.
	//
	// Zero or more working directories to include for repo-scoped detection.
	Cwds []string `json:"cwds,omitempty"`
	// When true, bypass the skills cache and re-scan skills from disk.
	ForceReload *bool    `json:"forceReload,omitempty"`
	ExtraRoots  []string `json:"extraRoots,omitempty"`
	RefName     *string  `json:"refName,omitempty"`
	// Deprecated field retained for compatibility. This field is ignored; use `migrationSource`
	// to select the migration source.
	//
	// Optional identifier for the product that initiated the import.
	Source          *string  `json:"source,omitempty"`
	SparsePaths     []string `json:"sparsePaths,omitempty"`
	MarketplaceName *string  `json:"marketplaceName,omitempty"`
	// Whether the client requests a fresh remote plugin catalog fetch.
	//
	// When true, bypass app caches and fetch the latest data from sources.
	ForceRefetch *bool `json:"forceRefetch,omitempty"`
	// Optional marketplace kind filter. When omitted, only local marketplaces are queried, plus
	// the default remote catalog when enabled by feature flag.
	MarketplaceKinds []MarketplaceKindElement `json:"marketplaceKinds,omitempty"`
	// Additional uninstalled plugin names that should be returned when present locally. This is
	// used by mention surfaces that intentionally expose install entrypoints.
	InstallSuggestionPluginNames []string             `json:"installSuggestionPluginNames,omitempty"`
	MarketplacePath              *string              `json:"marketplacePath,omitempty"`
	PluginName                   *string              `json:"pluginName,omitempty"`
	RemoteMarketplaceName        *string              `json:"remoteMarketplaceName,omitempty"`
	RemotePluginID               *string              `json:"remotePluginId,omitempty"`
	SkillName                    *string              `json:"skillName,omitempty"`
	Discoverability              *DiscoverabilityEnum `json:"discoverability,omitempty"`
	PluginPath                   *string              `json:"pluginPath,omitempty"`
	ShareTargets                 []ShareTargetElement `json:"shareTargets,omitempty"`
	// App ids to read. The server accepts at most 100 ids and deduplicates repeated ids while
	// preserving their first-request order.
	AppIDS []string `json:"appIds,omitempty"`
	// When true, include display-only public tool summaries in the returned metadata.
	IncludeTools *bool `json:"includeTools,omitempty"`
	// When true and Apps are permitted, refresh and publish the hosted connector runtime tool
	// snapshot first.
	ForceRefresh *bool `json:"forceRefresh,omitempty"`
	// Absolute path to read.
	//
	// Absolute path to write.
	//
	// Absolute directory path to create.
	//
	// Absolute path to inspect.
	//
	// Absolute directory path to read.
	//
	// Absolute path to remove.
	//
	// Absolute file or directory path to watch.
	//
	// Path-based selector.
	Path *string `json:"path,omitempty"`
	// File contents encoded as base64.
	DataBase64 *string `json:"dataBase64,omitempty"`
	// Whether parent directories should also be created. Defaults to `true`.
	//
	// Whether directory removal should recurse. Defaults to `true`.
	//
	// Required for directory copies; ignored for file copies.
	Recursive *bool `json:"recursive,omitempty"`
	// Whether missing paths should be ignored. Defaults to `true`.
	Force *bool `json:"force,omitempty"`
	// Absolute destination path.
	DestinationPath *string `json:"destinationPath,omitempty"`
	// Absolute source path.
	SourcePath *string `json:"sourcePath,omitempty"`
	// Connection-scoped watch identifier used for `fs/unwatch` and `fs/changed`.
	//
	// Watch identifier previously provided to `fs/watch`.
	WatchID *string `json:"watchId,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	// Client-generated identifier used to correlate one installation attempt.
	InstallAttemptID    *string `json:"installAttemptId,omitempty"`
	PluginID            *string `json:"pluginId,omitempty"`
	ClientUserMessageID *string `json:"clientUserMessageId,omitempty"`
	// Override the reasoning effort for this turn and subsequent turns.
	Effort *string        `json:"effort,omitempty"`
	Input  []InputElement `json:"input,omitempty"`
	// Optional JSON Schema used to constrain the final assistant message for this turn.
	OutputSchema interface{} `json:"outputSchema,omitempty"`
	// Override the sandbox policy for this turn and subsequent turns.
	//
	// Optional sandbox policy for this command.
	//
	// Uses the same shape as thread/turn execution sandbox configuration and defaults to the
	// user's configured policy when omitted. Cannot be combined with `permissionProfile`.
	SandboxPolicy *SandboxPolicy `json:"sandboxPolicy,omitempty"`
	// Override the reasoning summary for this turn and subsequent turns.
	Summary *SummaryEnum `json:"summary,omitempty"`
	// Required active turn id precondition. The request fails when it does not match the
	// currently active turn.
	ExpectedTurnID *string `json:"expectedTurnId,omitempty"`
	TurnID         *string `json:"turnId,omitempty"`
	// Where to run the review: inline (default) on the current thread or detached on a new
	// thread (returned in `reviewThreadId`).
	Delivery *DeliveryEnum `json:"delivery,omitempty"`
	Target   *ReviewTarget `json:"target,omitempty"`
	// When true, include models that are hidden from the default picker list.
	IncludeHidden *bool `json:"includeHidden,omitempty"`
	// Process-wide runtime feature enablement keyed by canonical feature name.
	//
	// Only named features are updated. Omitted features are left unchanged. Send an empty map
	// for a no-op.
	Enablement map[string]bool `json:"enablement,omitempty"`
	// Registration strategy for this login only; omission selects automatic discovery.
	ClientRegistration *ClientRegistrationEnum `json:"clientRegistration,omitempty"`
	Scopes             []string                `json:"scopes,omitempty"`
	TimeoutSecs        *int64                  `json:"timeoutSecs,omitempty"`
	// Controls how much MCP inventory data to fetch for each server. Defaults to `Full` when
	// omitted.
	Detail                    *InitializeParamsDetail `json:"detail,omitempty"`
	Server                    *string                 `json:"server,omitempty"`
	URI                       *string                 `json:"uri,omitempty"`
	Meta                      interface{}             `json:"_meta,omitempty"`
	Arguments                 interface{}             `json:"arguments,omitempty"`
	Tool                      *string                 `json:"tool,omitempty"`
	Mode                      *InitializeParamsMode   `json:"mode,omitempty"`
	APIKey                    *string                 `json:"apiKey,omitempty"`
	Type                      *InitializeParamsType   `json:"type,omitempty"`
	AppBrand                  *AppBrandEnum           `json:"appBrand,omitempty"`
	CodexStreamlinedLogin     *bool                   `json:"codexStreamlinedLogin,omitempty"`
	UseHostedLoginSuccessPage *bool                   `json:"useHostedLoginSuccessPage,omitempty"`
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
	Region          *string `json:"region,omitempty"`
	LoginID         *string `json:"loginId,omitempty"`
	// Opaque reset-credit identifier to redeem. When omitted, the backend selects the next
	// available credit.
	CreditID *string `json:"creditId,omitempty"`
	// Identifies one logical reset attempt. A UUID is recommended; reuse the same value when
	// retrying that attempt.
	IdempotencyKey *string           `json:"idempotencyKey,omitempty"`
	CreditType     *CreditType       `json:"creditType,omitempty"`
	Classification *string           `json:"classification,omitempty"`
	ExtraLogFiles  []string          `json:"extraLogFiles,omitempty"`
	IncludeLogs    *bool             `json:"includeLogs,omitempty"`
	Reason         *string           `json:"reason,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	// Disable stdout/stderr capture truncation for this request.
	//
	// Cannot be combined with `outputBytesCap`.
	DisableOutputCap *bool `json:"disableOutputCap,omitempty"`
	// Disable the timeout entirely for this request.
	//
	// Cannot be combined with `timeoutMs`.
	DisableTimeout *bool `json:"disableTimeout,omitempty"`
	// Optional environment overrides merged into the server-computed environment.
	//
	// Matching names override inherited values. Set a key to `null` to unset an inherited
	// variable.
	Env map[string]*string `json:"env,omitempty"`
	// Optional per-stream stdout/stderr capture cap in bytes.
	//
	// When omitted, the server default applies. Cannot be combined with `disableOutputCap`.
	OutputBytesCap *int64 `json:"outputBytesCap,omitempty"`
	// Optional client-supplied, connection-scoped process id.
	//
	// Required for `tty`, `streamStdin`, `streamStdoutStderr`, and follow-up
	// `command/exec/write`, `command/exec/resize`, and `command/exec/terminate` calls. When
	// omitted, buffered execution gets an internal id that is not exposed to the client.
	//
	// Client-supplied, connection-scoped `processId` from the original `command/exec` request.
	ProcessID *string `json:"processId,omitempty"`
	// Optional initial PTY size in character cells. Only valid when `tty` is true.
	//
	// New PTY size in character cells.
	Size *SizeClass `json:"size,omitempty"`
	// Allow follow-up `command/exec/write` requests to write stdin bytes.
	//
	// Requires a client-supplied `processId`.
	StreamStdin *bool `json:"streamStdin,omitempty"`
	// Stream stdout/stderr via `command/exec/outputDelta` notifications.
	//
	// Streamed bytes are not duplicated into the final response and require a client-supplied
	// `processId`.
	StreamStdoutStderr *bool `json:"streamStdoutStderr,omitempty"`
	// Optional timeout in milliseconds.
	//
	// When omitted, the server default applies. Cannot be combined with `disableTimeout`.
	TimeoutMS *int64 `json:"timeoutMs,omitempty"`
	// Enable PTY mode.
	//
	// This implies `streamStdin` and `streamStdoutStderr`.
	TTY *bool `json:"tty,omitempty"`
	// Close stdin after writing `deltaBase64`, if present.
	CloseStdin *bool `json:"closeStdin,omitempty"`
	// Optional base64-encoded stdin bytes to write.
	DeltaBase64   *string `json:"deltaBase64,omitempty"`
	IncludeLayers *bool   `json:"includeLayers,omitempty"`
	// If true, include detection under the user's home directory.
	IncludeHome *bool `json:"includeHome,omitempty"`
	// Maximum age in days for detected sessions. Missing values use the default limit.
	MaxSessionAgeDays *int64 `json:"maxSessionAgeDays,omitempty"`
	// Maximum number of sessions to detect. Missing values use the default limit.
	MaxSessions *int64 `json:"maxSessions,omitempty"`
	// Optional migration-source selector. Missing or unrecognized values use the default
	// source.
	//
	// Migration-source selector used to produce the migration items. Pass the same value to
	// detection and import; missing or unrecognized values use the default source.
	MigrationSource *string                `json:"migrationSource,omitempty"`
	MigrationItems  []MigrationItemElement `json:"migrationItems,omitempty"`
	// Opaque provider identifier supplied by the caller for analytics attribution and import
	// history display. This does not select the migration source.
	//
	// Opaque provider identifier for the externally completed import.
	ProviderID *string `json:"providerId,omitempty"`
	// Completed results grouped by imported item type.
	ItemTypeResults []InitializeParamsItemTypeResult `json:"itemTypeResults,omitempty"`
	ExpectedVersion *string                          `json:"expectedVersion,omitempty"`
	// Path to the config file to write; defaults to the user's `config.toml` when omitted.
	FilePath      *string        `json:"filePath,omitempty"`
	KeyPath       *string        `json:"keyPath,omitempty"`
	MergeStrategy *MergeStrategy `json:"mergeStrategy,omitempty"`
	Value         interface{}    `json:"value,omitempty"`
	Edits         []EditElement  `json:"edits,omitempty"`
	// When true, hot-reload updated runtime settings into loaded threads after writing.
	// Session-static model, reasoning-effort, Plan-mode reasoning-effort, service-tier, and
	// personality defaults are not reloaded.
	ReloadUserConfig *bool `json:"reloadUserConfig,omitempty"`
	// When `true`, requests a proactive token refresh before returning.
	//
	// In managed auth mode this triggers the normal refresh-token flow. In external auth mode
	// this flag is ignored. Clients should refresh tokens themselves and call
	// `account/login/start` with `chatgptAuthTokens`.
	RefreshToken      *bool    `json:"refreshToken,omitempty"`
	CancellationToken *string  `json:"cancellationToken,omitempty"`
	Query             *string  `json:"query,omitempty"`
	Roots             []string `json:"roots,omitempty"`
}

// Extensible visual presentation for a custom thread section.
type AppearanceClass struct {
	Color *string `json:"color,omitempty"`
	Icon  *string `json:"icon,omitempty"`
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

// Client-declared capabilities negotiated during initialize.
type InitializeCapabilities struct {
	// Opt into receiving experimental API methods and fields.
	ExperimentalAPI *bool `json:"experimentalApi,omitempty"`
	// MCP extension settings declared by the app-server client.
	Extensions map[string]interface{} `json:"extensions,omitempty"`
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

type EditElement struct {
	KeyPath       string        `json:"keyPath"`
	MergeStrategy MergeStrategy `json:"mergeStrategy"`
	Value         interface{}   `json:"value"`
}

type InitializeParamsGitInfo struct {
	// Omit to leave the stored branch unchanged, set to `null` to clear it, or provide a
	// non-empty string to replace it.
	Branch *string `json:"branch,omitempty"`
	// Omit to leave the stored origin URL unchanged, set to `null` to clear it, or provide a
	// non-empty string to replace it.
	OriginURL *string `json:"originUrl,omitempty"`
	// Omit to leave the stored commit unchanged, set to `null` to clear it, or provide a
	// non-empty string to replace it.
	SHA *string `json:"sha,omitempty"`
}

type InputElement struct {
	Text *string `json:"text,omitempty"`
	// UI-defined spans within `text` used to render or persist special elements.
	TextElements []TextElementElement `json:"text_elements,omitempty"`
	Type         UserInputType        `json:"type"`
	Detail       *InputDetail         `json:"detail,omitempty"`
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

type InitializeParamsItemTypeResult struct {
	Failures  []FailureElement                      `json:"failures"`
	ItemType  ItemType                              `json:"itemType"`
	Successes []PurpleCodexAppServerProtocolSchemas `json:"successes"`
}

type FailureElement struct {
	Cwd          *string  `json:"cwd,omitempty"`
	ErrorType    *string  `json:"errorType,omitempty"`
	FailureStage string   `json:"failureStage"`
	ItemType     ItemType `json:"itemType"`
	Message      string   `json:"message"`
	Source       *string  `json:"source,omitempty"`
	SubErrorType *string  `json:"subErrorType,omitempty"`
}

type PurpleCodexAppServerProtocolSchemas struct {
	Cwd      *string  `json:"cwd,omitempty"`
	ItemType ItemType `json:"itemType"`
	Source   *string  `json:"source,omitempty"`
	Target   *string  `json:"target,omitempty"`
	// Original title for an imported session, when available.
	Title *string `json:"title,omitempty"`
}

type MigrationItemElement struct {
	// Null or empty means home-scoped migration; non-empty means repo-scoped migration.
	Cwd         *string       `json:"cwd,omitempty"`
	Description string        `json:"description"`
	Details     *DetailsClass `json:"details,omitempty"`
	ItemType    ItemType      `json:"itemType"`
}

type DetailsClass struct {
	Commands   []CommandElement   `json:"commands,omitempty"`
	Hooks      []HookElement      `json:"hooks,omitempty"`
	MCPServers []MCPServerElement `json:"mcpServers,omitempty"`
	Memory     []string           `json:"memory,omitempty"`
	Plugins    []PluginElement    `json:"plugins,omitempty"`
	Sessions   []SessionElement   `json:"sessions,omitempty"`
	Skills     []SkillElement     `json:"skills,omitempty"`
	Subagents  []SubagentElement  `json:"subagents,omitempty"`
}

type CommandElement struct {
	Name string `json:"name"`
}

type HookElement struct {
	Name string `json:"name"`
}

type MCPServerElement struct {
	Name string `json:"name"`
}

type PluginElement struct {
	MarketplaceName string   `json:"marketplaceName"`
	PluginNames     []string `json:"pluginNames"`
}

type SessionElement struct {
	Cwd   string  `json:"cwd"`
	Path  string  `json:"path"`
	Title *string `json:"title,omitempty"`
}

type SkillElement struct {
	Name string `json:"name"`
}

type SubagentElement struct {
	Name string `json:"name"`
}

type SandboxPolicy struct {
	Type                SandboxPolicyType   `json:"type"`
	NetworkAccess       *NetworkAccessUnion `json:"networkAccess,omitempty"`
	ExcludeSlashTmp     *bool               `json:"excludeSlashTmp,omitempty"`
	ExcludeTmpdirEnvVar *bool               `json:"excludeTmpdirEnvVar,omitempty"`
	WritableRoots       []string            `json:"writableRoots,omitempty"`
}

type ShareTargetElement struct {
	PrincipalID   string        `json:"principalId"`
	PrincipalType PrincipalType `json:"principalType"`
	Role          Role          `json:"role"`
}

// PTY size in character cells for `command/exec` PTY sessions.
//
// New PTY size in character cells.
type SizeClass struct {
	// Terminal width in character cells.
	Cols int64 `json:"cols"`
	// Terminal height in character cells.
	Rows int64 `json:"rows"`
}

// Review the working tree: staged, unstaged, and untracked files.
//
// Review changes between the current branch and the given base branch.
//
// Review the changes introduced by a specific commit.
//
// Arbitrary instructions, equivalent to the old free-form prompt.
type ReviewTarget struct {
	Type   ReviewTargetType `json:"type"`
	Branch *string          `json:"branch,omitempty"`
	SHA    *string          `json:"sha,omitempty"`
	// Optional human-readable label (e.g., commit subject) for UIs.
	Title        *string `json:"title,omitempty"`
	Instructions *string `json:"instructions,omitempty"`
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
	Config  ConfigClass            `json:"config"`
	Layers  []LayerElement         `json:"layers,omitempty"`
	Origins map[string]OriginValue `json:"origins"`
}

type ConfigClass struct {
	Analytics      *AnalyticsClass `json:"analytics,omitempty"`
	ApprovalPolicy *ApprovalPolicy `json:"approval_policy,omitempty"`
	// [UNSTABLE] Optional default for where approval requests are routed for review.
	ApprovalsReviewer               *ApprovalsReviewerEnum               `json:"approvals_reviewer,omitempty"`
	CompactPrompt                   *string                              `json:"compact_prompt,omitempty"`
	Desktop                         map[string]interface{}               `json:"desktop,omitempty"`
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
	Config         interface{}       `json:"config"`
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

type FileChangeRequestApprovalParams struct {
	// [UNSTABLE] When set, the agent is asking the user to allow writes under this root for the
	// remainder of the session (unclear if this is honored today).
	GrantRoot *string `json:"grantRoot,omitempty"`
	ItemID    string  `json:"itemId"`
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

// [UNSTABLE] FOR OPENAI INTERNAL USE ONLY - DO NOT USE. The access token must contain the
// same scopes that Codex-managed ChatGPT auth tokens have.
//
// [UNSTABLE] Managed Amazon Bedrock login is experimental.
type LoginAccountParams struct {
	APIKey                    *string              `json:"apiKey,omitempty"`
	Type                      InitializeParamsType `json:"type"`
	AppBrand                  *AppBrandEnum        `json:"appBrand,omitempty"`
	CodexStreamlinedLogin     *bool                `json:"codexStreamlinedLogin,omitempty"`
	UseHostedLoginSuccessPage *bool                `json:"useHostedLoginSuccessPage,omitempty"`
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
	Region          *string `json:"region,omitempty"`
}

type LoginAccountResponse struct {
	Type InitializeParamsType `json:"type"`
	// URL the client should open in a browser to initiate the OAuth flow.
	AuthURL *string `json:"authUrl,omitempty"`
	LoginID *string `json:"loginId,omitempty"`
	// One-time code the user must enter after signing in.
	UserCode *string `json:"userCode,omitempty"`
	// URL the client should open in a browser to complete device code authorization.
	VerificationURL *string `json:"verificationUrl,omitempty"`
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
	StrictAutoReview *bool `json:"strictAutoReview,omitempty"`
}

type GrantedPermissionProfile struct {
	FileSystem *FileSystemClass `json:"fileSystem,omitempty"`
	Network    *NetworkClass    `json:"network,omitempty"`
}

// Notification sent from the server to the client.
//
// # NEW NOTIFICATIONS
//
// EXPERIMENTAL - proposed plan streaming deltas for plan items.
//
// Stream base64-encoded stdout/stderr chunks for a running `command/exec` session.
//
// Stream base64-encoded stdout/stderr chunks for a running `process/spawn` session.
//
// Final exit notification for a `process/spawn` session.
//
// Deprecated legacy apply_patch output stream notification.
//
// Deprecated: Use `ContextCompaction` item type instead.
//
// Notifies the user of world-writable directories on Windows, which cannot be protected by
// the sandbox.
type ServerNotification struct {
	// Unix timestamp (in milliseconds) when app-server emitted this notification.
	EmittedAtMS *int64             `json:"emittedAtMs,omitempty"`
	Method      NotificationMethod `json:"method"`
	Params      ParamsClass        `json:"params"`
}

// Notification emitted when watched local skill files change.
//
// Treat this as an invalidation signal and re-run `skills/list` with the client's current
// parameters when refreshed skill metadata is needed.
//
// Notification that the turn-level unified diff has changed. Contains the latest aggregated
// diff across all file changes in the turn.
//
// [UNSTABLE] Temporary notification payload for approval auto-review. This shape is
// expected to change soon.
//
// EXPERIMENTAL - proposed plan streaming deltas for plan items. Clients should not assume
// concatenated deltas match the completed plan item content.
//
// Base64-encoded output chunk emitted for a streaming `command/exec` request.
//
// These notifications are connection-scoped. If the originating connection closes, the
// server terminates the process.
//
// Base64-encoded output chunk emitted for a streaming `process/spawn` request.
//
// Final process exit notification for `process/spawn`.
//
// Deprecated legacy notification for `apply_patch` textual output.
//
// The server no longer emits this notification.
//
// Sparse rolling rate-limit update.
//
// Clients should merge available values into the most recent `account/rateLimits/read`
// response or refetch that snapshot. Nullable account metadata may be unavailable in a
// rolling update and does not clear a previously observed value.
//
// EXPERIMENTAL - notification emitted when the app list changes.
//
// Current remote-control connection status and remote identity exposed to clients.
//
// Filesystem watch notification emitted for `fs/watch` subscribers.
//
// Deprecated: Use `ContextCompaction` item type instead.
//
// EXPERIMENTAL - emitted when thread realtime startup is accepted.
//
// EXPERIMENTAL - raw non-audio thread realtime item emitted by the backend.
//
// EXPERIMENTAL - flat transcript delta emitted whenever realtime transcript text changes.
//
// EXPERIMENTAL - final transcript text emitted when realtime completes a transcript part.
//
// EXPERIMENTAL - streamed output audio emitted by thread realtime.
//
// EXPERIMENTAL - emitted with the remote SDP for a WebRTC realtime session.
//
// EXPERIMENTAL - emitted when thread realtime encounters an error.
//
// EXPERIMENTAL - emitted when thread realtime transport closes.
type ParamsClass struct {
	Error *Title `json:"error,omitempty"`
	// Optional thread target when the warning applies to a specific thread.
	//
	// Thread target for the guardian warning.
	ThreadID       *string         `json:"threadId,omitempty"`
	TurnID         *string         `json:"turnId,omitempty"`
	WillRetry      *bool           `json:"willRetry,omitempty"`
	Thread         *ThreadClass    `json:"thread,omitempty"`
	Status         *StatusUnion    `json:"status,omitempty"`
	ThreadName     *string         `json:"threadName,omitempty"`
	Goal           *Goal           `json:"goal,omitempty"`
	EnvironmentID  *string         `json:"environmentId,omitempty"`
	ThreadSettings *ThreadSettings `json:"threadSettings,omitempty"`
	TokenUsage     *TokenUsage     `json:"tokenUsage,omitempty"`
	Turn           *TurnElement    `json:"turn,omitempty"`
	Run            *Run            `json:"run,omitempty"`
	Diff           *string         `json:"diff,omitempty"`
	Explanation    *string         `json:"explanation,omitempty"`
	Plan           []PlanElement   `json:"plan,omitempty"`
	Item           interface{}     `json:"item,omitempty"`
	// Unix timestamp (in milliseconds) when this item lifecycle started.
	//
	// Unix timestamp (in milliseconds) when this review started.
	StartedAtMS *int64                        `json:"startedAtMs,omitempty"`
	Action      *GuardianApprovalReviewAction `json:"action,omitempty"`
	Review      *Review                       `json:"review,omitempty"`
	// Stable identifier for this review.
	ReviewID *string `json:"reviewId,omitempty"`
	// Identifier for the reviewed item or tool call when one exists.
	//
	// In most cases, one review maps to one target item. The exceptions are - execve reviews,
	// where a single command may contain multiple execve calls to review (only possible when
	// using the shell_zsh_fork feature) - network policy reviews, where there is no target
	// item
	//
	// A network call is triggered by a CommandExecution item, so having a target_item_id set to
	// the CommandExecution item would be misleading because the review is about the network
	// call, not the command execution. Therefore, target_item_id is set to None for network
	// policy reviews.
	TargetItemID *string `json:"targetItemId,omitempty"`
	// Unix timestamp (in milliseconds) when this review completed.
	//
	// Unix timestamp (in milliseconds) when this item lifecycle completed.
	CompletedAtMS  *int64          `json:"completedAtMs,omitempty"`
	DecisionSource *DecisionSource `json:"decisionSource,omitempty"`
	// Live transcript delta from the realtime event.
	Delta  *string `json:"delta,omitempty"`
	ItemID *string `json:"itemId,omitempty"`
	// `true` on the final streamed chunk for a stream when `outputBytesCap` truncated later
	// output on that stream.
	//
	// True on the final streamed chunk for this stream when output was truncated by
	// `outputBytesCap`.
	CapReached *bool `json:"capReached,omitempty"`
	// Base64-encoded output bytes.
	DeltaBase64 *string `json:"deltaBase64,omitempty"`
	// Client-supplied, connection-scoped `processId` from the original `command/exec` request.
	ProcessID *string `json:"processId,omitempty"`
	// Output stream for this chunk.
	//
	// Output stream this chunk belongs to.
	Stream *Stream `json:"stream,omitempty"`
	// Client-supplied, connection-scoped `processHandle` from `process/spawn`.
	ProcessHandle *string `json:"processHandle,omitempty"`
	// Process exit code.
	ExitCode *int64 `json:"exitCode,omitempty"`
	// Buffered stderr capture.
	//
	// Empty when stderr was streamed via `process/outputDelta`.
	Stderr *string `json:"stderr,omitempty"`
	// Whether stderr reached `outputBytesCap`.
	//
	// In streaming mode, stderr is empty and cap state is also reported on the final stderr
	// `process/outputDelta` notification.
	StderrCapReached *bool `json:"stderrCapReached,omitempty"`
	// Buffered stdout capture.
	//
	// Empty when stdout was streamed via `process/outputDelta`.
	Stdout *string `json:"stdout,omitempty"`
	// Whether stdout reached `outputBytesCap`.
	//
	// In streaming mode, stdout is empty and cap state is also reported on the final stdout
	// `process/outputDelta` notification.
	StdoutCapReached *bool           `json:"stdoutCapReached,omitempty"`
	Stdin            *string         `json:"stdin,omitempty"`
	Changes          []ChangeElement `json:"changes,omitempty"`
	RequestID        *ID             `json:"requestId,omitempty"`
	// Concise warning message for the user.
	//
	// Concise guardian warning message for the user.
	Message         *string                `json:"message,omitempty"`
	Name            *string                `json:"name,omitempty"`
	Success         *bool                  `json:"success,omitempty"`
	FailureReason   *FailureReasonEnum     `json:"failureReason,omitempty"`
	AuthMode        *AuthModeEnum          `json:"authMode,omitempty"`
	PlanType        *PlanType              `json:"planType,omitempty"`
	RateLimits      *RateLimits            `json:"rateLimits,omitempty"`
	Data            []ParamsDatum          `json:"data,omitempty"`
	InstallationID  *string                `json:"installationId,omitempty"`
	ServerName      *string                `json:"serverName,omitempty"`
	ImportID        *string                `json:"importId,omitempty"`
	ItemTypeResults []ParamsItemTypeResult `json:"itemTypeResults,omitempty"`
	// File or directory paths associated with this event.
	ChangedPaths []string `json:"changedPaths,omitempty"`
	// Watch identifier previously provided to `fs/watch`.
	WatchID         *string               `json:"watchId,omitempty"`
	SummaryIndex    *int64                `json:"summaryIndex,omitempty"`
	ContentIndex    *int64                `json:"contentIndex,omitempty"`
	FromModel       *string               `json:"fromModel,omitempty"`
	Reason          *string               `json:"reason,omitempty"`
	ToModel         *string               `json:"toModel,omitempty"`
	Verifications   []VerificationElement `json:"verifications,omitempty"`
	Metadata        interface{}           `json:"metadata,omitempty"`
	FasterModel     *string               `json:"fasterModel,omitempty"`
	Model           *string               `json:"model,omitempty"`
	Reasons         []string              `json:"reasons,omitempty"`
	ShowBufferingUI *bool                 `json:"showBufferingUi,omitempty"`
	UseCases        []string              `json:"useCases,omitempty"`
	// Optional extra guidance, such as migration steps or rationale.
	//
	// Optional extra guidance or error details.
	Details *string `json:"details,omitempty"`
	// Concise summary of what is deprecated.
	//
	// Concise summary of the warning.
	Summary *string `json:"summary,omitempty"`
	// Optional path to the config file that triggered the warning.
	Path *string `json:"path,omitempty"`
	// Optional range for the error location inside the config file.
	Range             *RangeClass             `json:"range,omitempty"`
	Files             []FuzzyFileSearchResult `json:"files,omitempty"`
	Query             *string                 `json:"query,omitempty"`
	SessionID         *string                 `json:"sessionId,omitempty"`
	RealtimeSessionID *string                 `json:"realtimeSessionId,omitempty"`
	Version           *Version                `json:"version,omitempty"`
	Role              *string                 `json:"role,omitempty"`
	// Final complete text for the transcript part.
	Text                 *string                   `json:"text,omitempty"`
	Audio                *Audio                    `json:"audio,omitempty"`
	SDP                  *string                   `json:"sdp,omitempty"`
	ExtraCount           *int64                    `json:"extraCount,omitempty"`
	FailedScan           *bool                     `json:"failedScan,omitempty"`
	SamplePaths          []string                  `json:"samplePaths,omitempty"`
	Mode                 *InitializeParamsMode     `json:"mode,omitempty"`
	LoginID              *string                   `json:"loginId,omitempty"`
	OnboardingEntrypoint *OnboardingEntrypointEnum `json:"onboardingEntrypoint,omitempty"`
}

type GuardianApprovalReviewAction struct {
	Command       *string                          `json:"command,omitempty"`
	Cwd           *string                          `json:"cwd,omitempty"`
	Source        *ActionSource                    `json:"source,omitempty"`
	Type          GuardianApprovalReviewActionType `json:"type"`
	Argv          []string                         `json:"argv,omitempty"`
	Program       *string                          `json:"program,omitempty"`
	Files         []string                         `json:"files,omitempty"`
	Host          *string                          `json:"host,omitempty"`
	Port          *int64                           `json:"port,omitempty"`
	Protocol      *ProtocolEnum                    `json:"protocol,omitempty"`
	Target        *string                          `json:"target,omitempty"`
	ConnectorID   *string                          `json:"connectorId,omitempty"`
	ConnectorName *string                          `json:"connectorName,omitempty"`
	Server        *string                          `json:"server,omitempty"`
	ToolName      *string                          `json:"toolName,omitempty"`
	ToolTitle     *string                          `json:"toolTitle,omitempty"`
	Permissions   *Permissions                     `json:"permissions,omitempty"`
	Reason        *string                          `json:"reason,omitempty"`
}

// EXPERIMENTAL - thread realtime audio chunk.
type Audio struct {
	Data              string  `json:"data"`
	ItemID            *string `json:"itemId,omitempty"`
	NumChannels       int64   `json:"numChannels"`
	SampleRate        int64   `json:"sampleRate"`
	SamplesPerChannel *int64  `json:"samplesPerChannel,omitempty"`
}

type ChangeElement struct {
	Diff string          `json:"diff"`
	Kind PatchChangeKind `json:"kind"`
	Path string          `json:"path"`
}

type PatchChangeKind struct {
	Type     KindType `json:"type"`
	MovePath *string  `json:"move_path,omitempty"`
}

// EXPERIMENTAL - app metadata returned by app-list APIs.
type ParamsDatum struct {
	AppMetadata         *AppMetadataClass `json:"appMetadata,omitempty"`
	Branding            *BrandingClass    `json:"branding,omitempty"`
	Description         *string           `json:"description,omitempty"`
	DistributionChannel *string           `json:"distributionChannel,omitempty"`
	IconAssets          map[string]string `json:"iconAssets,omitempty"`
	IconDarkAssets      map[string]string `json:"iconDarkAssets,omitempty"`
	ID                  string            `json:"id"`
	InstallURL          *string           `json:"installUrl,omitempty"`
	IsAccessible        *bool             `json:"isAccessible,omitempty"`
	// Whether this app is enabled in config.toml. Example: ```toml [apps.bad_app] enabled =
	// false ```
	IsEnabled          *bool             `json:"isEnabled,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	LogoURL            *string           `json:"logoUrl,omitempty"`
	LogoURLDark        *string           `json:"logoUrlDark,omitempty"`
	Name               string            `json:"name"`
	PluginDisplayNames []string          `json:"pluginDisplayNames,omitempty"`
}

type AppMetadataClass struct {
	Categories                 []string            `json:"categories,omitempty"`
	Developer                  *string             `json:"developer,omitempty"`
	FirstPartyRequiresInstall  *bool               `json:"firstPartyRequiresInstall,omitempty"`
	Review                     *ReviewClass        `json:"review,omitempty"`
	Screenshots                []ScreenshotElement `json:"screenshots,omitempty"`
	SEODescription             *string             `json:"seoDescription,omitempty"`
	ShowInComposerWhenUnlinked *bool               `json:"showInComposerWhenUnlinked,omitempty"`
	SubCategories              []string            `json:"subCategories,omitempty"`
	Version                    *string             `json:"version,omitempty"`
	VersionID                  *string             `json:"versionId,omitempty"`
	VersionNotes               *string             `json:"versionNotes,omitempty"`
}

type ReviewClass struct {
	Status string `json:"status"`
}

type ScreenshotElement struct {
	FileID     *string `json:"fileId,omitempty"`
	URL        *string `json:"url,omitempty"`
	UserPrompt string  `json:"userPrompt"`
}

// EXPERIMENTAL - app metadata returned by app-list APIs.
type BrandingClass struct {
	Category          *string `json:"category,omitempty"`
	Developer         *string `json:"developer,omitempty"`
	IsDiscoverableApp bool    `json:"isDiscoverableApp"`
	PrivacyPolicy     *string `json:"privacyPolicy,omitempty"`
	TermsOfService    *string `json:"termsOfService,omitempty"`
	Website           *string `json:"website,omitempty"`
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

// Superset of [`codex_file_search::FileMatch`]
type FuzzyFileSearchResult struct {
	FileName  string                   `json:"file_name"`
	Indices   []int64                  `json:"indices,omitempty"`
	MatchType FuzzyFileSearchMatchType `json:"match_type"`
	Path      string                   `json:"path"`
	Root      string                   `json:"root"`
	Score     int64                    `json:"score"`
}

type Goal struct {
	CreatedAt       int64      `json:"createdAt"`
	Objective       string     `json:"objective"`
	Status          StatusEnum `json:"status"`
	ThreadID        string     `json:"threadId"`
	TimeUsedSeconds int64      `json:"timeUsedSeconds"`
	TokenBudget     *int64     `json:"tokenBudget,omitempty"`
	TokensUsed      int64      `json:"tokensUsed"`
	UpdatedAt       int64      `json:"updatedAt"`
}

type ParamsItemTypeResult struct {
	Failures  []FailureElement                      `json:"failures"`
	ItemType  ItemType                              `json:"itemType"`
	Successes []FluffyCodexAppServerProtocolSchemas `json:"successes"`
}

type FluffyCodexAppServerProtocolSchemas struct {
	Cwd      *string  `json:"cwd,omitempty"`
	ItemType ItemType `json:"itemType"`
	Source   *string  `json:"source,omitempty"`
	Target   *string  `json:"target,omitempty"`
	// Original title for an imported session; null for other item types.
	Title *string `json:"title,omitempty"`
}

type PlanElement struct {
	Status PlanStatus `json:"status"`
	Step   string     `json:"step"`
}

type RangeClass struct {
	End   End `json:"end"`
	Start End `json:"start"`
}

type End struct {
	// 1-based column number (in Unicode scalar values).
	Column int64 `json:"column"`
	// 1-based line number.
	Line int64 `json:"line"`
}

type RateLimits struct {
	Credits              *CreditsClass             `json:"credits,omitempty"`
	IndividualLimit      *IndividualLimitClass     `json:"individualLimit,omitempty"`
	LimitID              *string                   `json:"limitId,omitempty"`
	LimitName            *string                   `json:"limitName,omitempty"`
	PlanType             *PlanType                 `json:"planType,omitempty"`
	Primary              *PrimaryClass             `json:"primary,omitempty"`
	RateLimitReachedType *RateLimitReachedTypeEnum `json:"rateLimitReachedType,omitempty"`
	Secondary            *PrimaryClass             `json:"secondary,omitempty"`
	// Backend-reported spend-control state. `None` is unavailable, not a sparse-update recovery.
	SpendControlReached *bool `json:"spendControlReached,omitempty"`
}

type CreditsClass struct {
	Balance    *string `json:"balance,omitempty"`
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
}

type IndividualLimitClass struct {
	Limit            string `json:"limit"`
	RemainingPercent int64  `json:"remainingPercent"`
	ResetsAt         int64  `json:"resetsAt"`
	Used             string `json:"used"`
}

type PrimaryClass struct {
	ResetsAt           *int64 `json:"resetsAt,omitempty"`
	UsedPercent        int64  `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins,omitempty"`
}

// [UNSTABLE] Temporary approval auto-review payload used by `item/autoApprovalReview/*`
// notifications. This shape is expected to change soon.
type Review struct {
	Rationale         *string                `json:"rationale,omitempty"`
	RiskLevel         *RiskLevelEnum         `json:"riskLevel,omitempty"`
	Status            ReviewStatus           `json:"status"`
	UserAuthorization *UserAuthorizationEnum `json:"userAuthorization,omitempty"`
}

type Run struct {
	CompletedAt   *int64        `json:"completedAt,omitempty"`
	DisplayOrder  int64         `json:"displayOrder"`
	DurationMS    *int64        `json:"durationMs,omitempty"`
	Entries       []RunEntry    `json:"entries"`
	EventName     EventName     `json:"eventName"`
	ExecutionMode ExecutionMode `json:"executionMode"`
	HandlerType   HandlerType   `json:"handlerType"`
	ID            string        `json:"id"`
	Scope         Scope         `json:"scope"`
	Source        *RunSource    `json:"source,omitempty"`
	SourcePath    string        `json:"sourcePath"`
	StartedAt     int64         `json:"startedAt"`
	Status        RunStatus     `json:"status"`
	StatusMessage *string       `json:"statusMessage,omitempty"`
}

type RunEntry struct {
	Kind EntryKind `json:"kind"`
	Text string    `json:"text"`
}

type ThreadStatus struct {
	Type        ThreadStatusType    `json:"type"`
	ActiveFlags []ActiveFlagElement `json:"activeFlags,omitempty"`
}

type ThreadClass struct {
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
	GitInfo *ThreadGitInfo `json:"gitInfo,omitempty"`
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
	Status StatusClass `json:"status"`
	// Optional analytics source classification for this thread.
	ThreadSource *string `json:"threadSource,omitempty"`
	// Only populated on `thread/resume`, `thread/rollback`, `thread/fork`, and `thread/read`
	// (when `includeTurns` is true) responses. For all other responses and notifications
	// returning a Thread, the turns field will be an empty list.
	Turns []TurnElement `json:"turns"`
	// Unix timestamp (in seconds) when the thread was last updated.
	UpdatedAt int64 `json:"updatedAt"`
}

type ThreadGitInfo struct {
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

type SessionSource struct {
	Custom   *string        `json:"custom,omitempty"`
	SubAgent *SubAgentUnion `json:"subAgent,omitempty"`
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
type StatusClass struct {
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

// EXPERIMENTAL - proposed plan item content. The completed plan item is authoritative and
// may not match the concatenation of `PlanDelta` text.
//
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
	Arguments  interface{}      `json:"arguments,omitempty"`
	Error      *ErrorClass      `json:"error,omitempty"`
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
	Results               []interface{}                             `json:"results,omitempty"`
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
	Detail       *InputDetail         `json:"detail,omitempty"`
	URL          *string              `json:"url,omitempty"`
	Path         *string              `json:"path,omitempty"`
	Name         *string              `json:"name,omitempty"`
}

type InputDynamicToolCallOutputContentItem struct {
	Text     *string                                   `json:"text,omitempty"`
	Type     InputDynamicToolCallOutputContentItemType `json:"type"`
	ImageURL *string                                   `json:"imageUrl,omitempty"`
	AudioURL *string                                   `json:"audioUrl,omitempty"`
}

type ErrorClass struct {
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
	Meta              interface{}   `json:"_meta,omitempty"`
	Content           []interface{} `json:"content"`
	StructuredContent interface{}   `json:"structuredContent,omitempty"`
}

type ThreadSettings struct {
	ActivePermissionProfile *ActivePermissionProfileClass `json:"activePermissionProfile,omitempty"`
	ApprovalPolicy          *ApprovalPolicyUnion          `json:"approvalPolicy"`
	ApprovalsReviewer       ApprovalsReviewerEnum         `json:"approvalsReviewer"`
	CollaborationMode       CollaborationMode             `json:"collaborationMode"`
	Cwd                     string                        `json:"cwd"`
	Effort                  *string                       `json:"effort,omitempty"`
	Model                   string                        `json:"model"`
	ModelProvider           string                        `json:"modelProvider"`
	Personality             *PersonalityEnum              `json:"personality,omitempty"`
	SandboxPolicy           SandboxClass                  `json:"sandboxPolicy"`
	ServiceTier             *string                       `json:"serviceTier,omitempty"`
	Summary                 *SummaryEnum                  `json:"summary,omitempty"`
}

type ActivePermissionProfileClass struct {
	// Parent profile identifier from the selected permissions profile's `extends` setting, when
	// present.
	Extends *string `json:"extends,omitempty"`
	// Identifier from `default_permissions` or the implicit built-in default, such as
	// `:workspace` or a user-defined `[permissions.<id>]` profile.
	ID string `json:"id"`
}

// Collaboration mode for a Codex session.
type CollaborationMode struct {
	Mode     CollaborationModeMode `json:"mode"`
	Settings Settings              `json:"settings"`
}

// Settings for a collaboration mode.
type Settings struct {
	DeveloperInstructions *string `json:"developer_instructions,omitempty"`
	Model                 string  `json:"model"`
	ReasoningEffort       *string `json:"reasoning_effort,omitempty"`
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

// Request initiated from the server and sent to the client.
//
// NEW APIs Sent when approval is requested for a specific command execution. This request
// is used for Turns started via turn/start.
//
// Sent when approval is requested for a specific file change. This request is used for
// Turns started via turn/start.
//
// EXPERIMENTAL - Request input from the user for a tool call.
//
// Request input for an MCP server elicitation.
//
// Request approval for additional permissions from the user.
//
// Execute a dynamic tool call on the client.
//
// Generate a fresh upstream attestation result on demand.
//
// DEPRECATED APIs below Request to approve a patch. This request is used for Turns started
// via the legacy APIs (i.e. SendUserTurn, SendUserMessage).
//
// Request to exec a command. This request is used for Turns started via the legacy APIs
// (i.e. SendUserTurn, SendUserMessage).
type ServerRequest struct {
	ID     *ID                               `json:"id"`
	Method ServerRequestMethod               `json:"method"`
	Params MCPServerElicitationRequestParams `json:"params"`
}

// EXPERIMENTAL. Params sent with a request_user_input event.
type MCPServerElicitationRequestParams struct {
	// Unique identifier for this specific approval callback.
	//
	// For regular shell/unified_exec approvals, this is null.
	//
	// For zsh-exec-bridge subcommand approvals, multiple callbacks can belong to one parent
	// `itemId`, so `approvalId` is a distinct opaque callback id (a UUID) used to disambiguate
	// routing.
	//
	// Identifier for this specific approval callback.
	ApprovalID *string `json:"approvalId,omitempty"`
	// The command to be executed.
	Command *ForcedChatgptWorkspaceID `json:"command,omitempty"`
	// Best-effort parsed command actions for friendly display.
	CommandActions []CommandAction `json:"commandActions,omitempty"`
	// The command's working directory.
	Cwd *string `json:"cwd,omitempty"`
	// Environment in which the command will run.
	EnvironmentID *string `json:"environmentId,omitempty"`
	ItemID        *string `json:"itemId,omitempty"`
	// Optional context for a managed-network approval prompt.
	NetworkApprovalContext *NetworkApprovalContext `json:"networkApprovalContext,omitempty"`
	// Optional proposed execpolicy amendment to allow similar commands without prompting.
	ProposedExecpolicyAmendment []string `json:"proposedExecpolicyAmendment,omitempty"`
	// Optional proposed network policy amendments (allow/deny host) for future requests.
	ProposedNetworkPolicyAmendments []NetworkPolicyAmendment `json:"proposedNetworkPolicyAmendments,omitempty"`
	// Optional explanatory reason (e.g. request for network access).
	//
	// Optional explanatory reason (e.g. request for extra write access).
	Reason *string `json:"reason,omitempty"`
	// Unix timestamp (in milliseconds) when this approval request started.
	StartedAtMS *int64  `json:"startedAtMs,omitempty"`
	ThreadID    *string `json:"threadId,omitempty"`
	// Active Codex turn when this elicitation was observed, if app-server could correlate one.
	//
	// This is nullable because MCP models elicitation as a standalone server-to-client request
	// identified by the MCP server request id. It may be triggered during a turn, but turn
	// context is app-server correlation rather than part of the protocol identity of the
	// elicitation itself.
	TurnID *string `json:"turnId,omitempty"`
	// [UNSTABLE] When set, the agent is asking the user to allow writes under this root for the
	// remainder of the session (unclear if this is honored today).
	//
	// When set, the agent is asking the user to allow writes under this root for the remainder
	// of the session (unclear if this is honored today).
	GrantRoot *string `json:"grantRoot,omitempty"`
	// @deprecated Use `isBlocking` to decide whether the request should block.
	AutoResolutionMS *int64                         `json:"autoResolutionMs,omitempty"`
	IsBlocking       *bool                          `json:"isBlocking,omitempty"`
	Questions        []ToolRequestUserInputQuestion `json:"questions,omitempty"`
	ServerName       *string                        `json:"serverName,omitempty"`
	Meta             interface{}                    `json:"_meta,omitempty"`
	Message          *string                        `json:"message,omitempty"`
	Mode             *PurpleMode                    `json:"mode,omitempty"`
	RequestedSchema  interface{}                    `json:"requestedSchema,omitempty"`
	ElicitationID    *string                        `json:"elicitationId,omitempty"`
	URL              *string                        `json:"url,omitempty"`
	Permissions      *Permissions                   `json:"permissions,omitempty"`
	Arguments        interface{}                    `json:"arguments,omitempty"`
	// Use to correlate this with [codex_protocol::protocol::PatchApplyBeginEvent] and
	// [codex_protocol::protocol::PatchApplyEndEvent].
	//
	// Use to correlate this with [codex_protocol::protocol::ExecCommandBeginEvent] and
	// [codex_protocol::protocol::ExecCommandEndEvent].
	CallID    *string `json:"callId,omitempty"`
	Namespace *string `json:"namespace,omitempty"`
	Tool      *string `json:"tool,omitempty"`
	// Workspace/account identifier that Codex was previously using.
	//
	// Clients that manage multiple accounts/workspaces can use this as a hint to refresh the
	// token for the correct workspace.
	//
	// This may be `null` when the prior auth state did not include a workspace identifier
	// (`chatgpt_account_id`).
	PreviousAccountID *string               `json:"previousAccountId,omitempty"`
	ConversationID    *string               `json:"conversationId,omitempty"`
	FileChanges       map[string]FileChange `json:"fileChanges,omitempty"`
	ParsedCmd         []ParsedCommand       `json:"parsedCmd,omitempty"`
}

type FileChange struct {
	Content     *string  `json:"content,omitempty"`
	Type        KindType `json:"type"`
	MovePath    *string  `json:"move_path,omitempty"`
	UnifiedDiff *string  `json:"unified_diff,omitempty"`
}

type ParsedCommand struct {
	Cmd  string  `json:"cmd"`
	Name *string `json:"name,omitempty"`
	// (Best effort) Path to the file being read by the command. When possible, this is an
	// absolute path, though when relative, it should be resolved against the `cwd`` that will
	// be used to run the command to derive the absolute path.
	Path  *string           `json:"path,omitempty"`
	Type  ParsedCommandType `json:"type"`
	Query *string           `json:"query,omitempty"`
}

// EXPERIMENTAL. Represents one request_user_input question and its required options.
type ToolRequestUserInputQuestion struct {
	Header   string                       `json:"header"`
	ID       string                       `json:"id"`
	IsOther  *bool                        `json:"isOther,omitempty"`
	IsSecret *bool                        `json:"isSecret,omitempty"`
	Options  []ToolRequestUserInputOption `json:"options,omitempty"`
	Question string                       `json:"question"`
}

// EXPERIMENTAL. Defines a single selectable option for request_user_input.
type ToolRequestUserInputOption struct {
	Description string `json:"description"`
	Label       string `json:"label"`
}

type ThreadReadParams struct {
	// When true, include turns and their items from rollout history.
	IncludeTurns *bool  `json:"includeTurns,omitempty"`
	ThreadID     string `json:"threadId"`
}

type ThreadReadResponse struct {
	Thread ThreadClass `json:"thread"`
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
	ApprovalsReviewer     *ApprovalsReviewerEnum `json:"approvalsReviewer,omitempty"`
	BaseInstructions      *string                `json:"baseInstructions,omitempty"`
	Config                map[string]interface{} `json:"config,omitempty"`
	Cwd                   *string                `json:"cwd,omitempty"`
	DeveloperInstructions *string                `json:"developerInstructions,omitempty"`
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
	Thread      ThreadClass  `json:"thread"`
}

type ThreadStartParams struct {
	ApprovalPolicy *ApprovalPolicy `json:"approvalPolicy,omitempty"`
	// Override where approval requests are routed for review on this thread and subsequent
	// turns.
	ApprovalsReviewer     *ApprovalsReviewerEnum  `json:"approvalsReviewer,omitempty"`
	BaseInstructions      *string                 `json:"baseInstructions,omitempty"`
	Config                map[string]interface{}  `json:"config,omitempty"`
	Cwd                   *string                 `json:"cwd,omitempty"`
	DeveloperInstructions *string                 `json:"developerInstructions,omitempty"`
	Ephemeral             *bool                   `json:"ephemeral,omitempty"`
	Model                 *string                 `json:"model,omitempty"`
	ModelProvider         *string                 `json:"modelProvider,omitempty"`
	Personality           *PersonalityEnum        `json:"personality,omitempty"`
	Sandbox               *SandboxEnum            `json:"sandbox,omitempty"`
	ServiceName           *string                 `json:"serviceName,omitempty"`
	ServiceTier           *string                 `json:"serviceTier,omitempty"`
	SessionStartSource    *SessionStartSourceEnum `json:"sessionStartSource,omitempty"`
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
	Thread      ThreadClass  `json:"thread"`
}

type ThreadUnsubscribeParams struct {
	ThreadID string `json:"threadId"`
}

type ThreadUnsubscribeResponse struct {
	Status ThreadUnsubscribeResponseStatus `json:"status"`
}

type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
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
	OutputSchema interface{} `json:"outputSchema,omitempty"`
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

type TurnStartResponse struct {
	Turn TurnElement `json:"turn"`
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

type CancelLoginAccountResponseStatus string

const (
	Canceled       CancelLoginAccountResponseStatus = "canceled"
	PurpleNotFound CancelLoginAccountResponseStatus = "notFound"
)

type InitializedNotificationMethod string

const (
	Initialized InitializedNotificationMethod = "initialized"
)

type ClientRequestMethod string

const (
	AccountLoginCancel                     ClientRequestMethod = "account/login/cancel"
	AccountLoginStart                      ClientRequestMethod = "account/login/start"
	AccountLogout                          ClientRequestMethod = "account/logout"
	AccountRateLimitResetCreditConsume     ClientRequestMethod = "account/rateLimitResetCredit/consume"
	AccountRateLimitsRead                  ClientRequestMethod = "account/rateLimits/read"
	AccountRead                            ClientRequestMethod = "account/read"
	AccountSendAddCreditsNudgeEmail        ClientRequestMethod = "account/sendAddCreditsNudgeEmail"
	AccountUsageRead                       ClientRequestMethod = "account/usage/read"
	AccountWorkspaceMessagesRead           ClientRequestMethod = "account/workspaceMessages/read"
	AppInstalled                           ClientRequestMethod = "app/installed"
	AppList                                ClientRequestMethod = "app/list"
	AppRead                                ClientRequestMethod = "app/read"
	CommandExec                            ClientRequestMethod = "command/exec"
	CommandExecResize                      ClientRequestMethod = "command/exec/resize"
	CommandExecTerminate                   ClientRequestMethod = "command/exec/terminate"
	CommandExecWrite                       ClientRequestMethod = "command/exec/write"
	ConfigBatchWrite                       ClientRequestMethod = "config/batchWrite"
	ConfigMCPServerReload                  ClientRequestMethod = "config/mcpServer/reload"
	ConfigRead                             ClientRequestMethod = "config/read"
	ConfigRequirementsRead                 ClientRequestMethod = "configRequirements/read"
	ConfigValueWrite                       ClientRequestMethod = "config/value/write"
	ExperimentalFeatureEnablementSet       ClientRequestMethod = "experimentalFeature/enablement/set"
	ExperimentalFeatureList                ClientRequestMethod = "experimentalFeature/list"
	ExternalAgentConfigDetect              ClientRequestMethod = "externalAgentConfig/detect"
	ExternalAgentConfigImport              ClientRequestMethod = "externalAgentConfig/import"
	ExternalAgentConfigImportReadHistories ClientRequestMethod = "externalAgentConfig/import/readHistories"
	ExternalAgentConfigImportRecordHistory ClientRequestMethod = "externalAgentConfig/import/recordHistory"
	FSCopy                                 ClientRequestMethod = "fs/copy"
	FSCreateDirectory                      ClientRequestMethod = "fs/createDirectory"
	FSGetMetadata                          ClientRequestMethod = "fs/getMetadata"
	FSReadDirectory                        ClientRequestMethod = "fs/readDirectory"
	FSReadFile                             ClientRequestMethod = "fs/readFile"
	FSRemove                               ClientRequestMethod = "fs/remove"
	FSUnwatch                              ClientRequestMethod = "fs/unwatch"
	FSWatch                                ClientRequestMethod = "fs/watch"
	FSWriteFile                            ClientRequestMethod = "fs/writeFile"
	FeedbackUpload                         ClientRequestMethod = "feedback/upload"
	FuzzyFileSearch                        ClientRequestMethod = "fuzzyFileSearch"
	HooksList                              ClientRequestMethod = "hooks/list"
	Initialize                             ClientRequestMethod = "initialize"
	MCPServerOauthLogin                    ClientRequestMethod = "mcpServer/oauth/login"
	MCPServerResourceRead                  ClientRequestMethod = "mcpServer/resource/read"
	MCPServerStatusList                    ClientRequestMethod = "mcpServerStatus/list"
	MCPServerToolCall                      ClientRequestMethod = "mcpServer/tool/call"
	MarketplaceAdd                         ClientRequestMethod = "marketplace/add"
	MarketplaceRemove                      ClientRequestMethod = "marketplace/remove"
	MarketplaceUpgrade                     ClientRequestMethod = "marketplace/upgrade"
	ModelList                              ClientRequestMethod = "model/list"
	ModelProviderCapabilitiesRead          ClientRequestMethod = "modelProvider/capabilities/read"
	PermissionProfileList                  ClientRequestMethod = "permissionProfile/list"
	PluginInstall                          ClientRequestMethod = "plugin/install"
	PluginInstalled                        ClientRequestMethod = "plugin/installed"
	PluginList                             ClientRequestMethod = "plugin/list"
	PluginRead                             ClientRequestMethod = "plugin/read"
	PluginShareCheckout                    ClientRequestMethod = "plugin/share/checkout"
	PluginShareDelete                      ClientRequestMethod = "plugin/share/delete"
	PluginShareList                        ClientRequestMethod = "plugin/share/list"
	PluginShareSave                        ClientRequestMethod = "plugin/share/save"
	PluginShareUpdateTargets               ClientRequestMethod = "plugin/share/updateTargets"
	PluginSkillRead                        ClientRequestMethod = "plugin/skill/read"
	PluginUninstall                        ClientRequestMethod = "plugin/uninstall"
	ReviewStart                            ClientRequestMethod = "review/start"
	SkillsConfigWrite                      ClientRequestMethod = "skills/config/write"
	SkillsExtraRootsSet                    ClientRequestMethod = "skills/extraRoots/set"
	SkillsList                             ClientRequestMethod = "skills/list"
	ThreadApproveGuardianDeniedAction      ClientRequestMethod = "thread/approveGuardianDeniedAction"
	ThreadArchive                          ClientRequestMethod = "thread/archive"
	ThreadCompactStart                     ClientRequestMethod = "thread/compact/start"
	ThreadDelete                           ClientRequestMethod = "thread/delete"
	ThreadFork                             ClientRequestMethod = "thread/fork"
	ThreadGoalClear                        ClientRequestMethod = "thread/goal/clear"
	ThreadGoalGet                          ClientRequestMethod = "thread/goal/get"
	ThreadGoalSet                          ClientRequestMethod = "thread/goal/set"
	ThreadInjectItems                      ClientRequestMethod = "thread/inject_items"
	ThreadList                             ClientRequestMethod = "thread/list"
	ThreadLoadedList                       ClientRequestMethod = "thread/loaded/list"
	ThreadMetadataUpdate                   ClientRequestMethod = "thread/metadata/update"
	ThreadNameSet                          ClientRequestMethod = "thread/name/set"
	ThreadRead                             ClientRequestMethod = "thread/read"
	ThreadResume                           ClientRequestMethod = "thread/resume"
	ThreadRollback                         ClientRequestMethod = "thread/rollback"
	ThreadSectionCreate                    ClientRequestMethod = "threadSection/create"
	ThreadSectionDelete                    ClientRequestMethod = "threadSection/delete"
	ThreadSectionList                      ClientRequestMethod = "threadSection/list"
	ThreadSectionMove                      ClientRequestMethod = "thread/section/move"
	ThreadSectionUpdate                    ClientRequestMethod = "threadSection/update"
	ThreadShellCommand                     ClientRequestMethod = "thread/shellCommand"
	ThreadStart                            ClientRequestMethod = "thread/start"
	ThreadUnarchive                        ClientRequestMethod = "thread/unarchive"
	ThreadUnsubscribe                      ClientRequestMethod = "thread/unsubscribe"
	TurnInterrupt                          ClientRequestMethod = "turn/interrupt"
	TurnStart                              ClientRequestMethod = "turn/start"
	TurnSteer                              ClientRequestMethod = "turn/steer"
	WindowsSandboxReadiness                ClientRequestMethod = "windowsSandbox/readiness"
	WindowsSandboxSetupStart               ClientRequestMethod = "windowsSandbox/setupStart"
)

type AppBrandEnum string

const (
	Codex         AppBrandEnum = "codex"
	PurpleChatgpt AppBrandEnum = "chatgpt"
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

type ClientRegistrationEnum string

const (
	Cimd       ClientRegistrationEnum = "cimd"
	Dcr        ClientRegistrationEnum = "dcr"
	PurpleAuto ClientRegistrationEnum = "auto"
)

type CreditType string

const (
	Credits    CreditType = "credits"
	UsageLimit CreditType = "usage_limit"
)

type DeliveryEnum string

const (
	Detached DeliveryEnum = "detached"
	Inline   DeliveryEnum = "inline"
)

type InitializeParamsDetail string

const (
	CodexAppServerProtocolSchemasFull InitializeParamsDetail = "full"
	ToolsAndAuthOnly                  InitializeParamsDetail = "toolsAndAuthOnly"
)

type DiscoverabilityEnum string

const (
	Listed   DiscoverabilityEnum = "LISTED"
	Private  DiscoverabilityEnum = "PRIVATE"
	Unlisted DiscoverabilityEnum = "UNLISTED"
)

type MergeStrategy string

const (
	Replace MergeStrategy = "replace"
	Upsert  MergeStrategy = "upsert"
)

type InputDetail string

const (
	FluffyAuto InputDetail = "auto"
	Original   InputDetail = "original"
	PurpleHigh InputDetail = "high"
	PurpleLow  InputDetail = "low"
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

type ItemType string

const (
	AgentsMd        ItemType = "AGENTS_MD"
	Commands        ItemType = "COMMANDS"
	Config          ItemType = "CONFIG"
	Hooks           ItemType = "HOOKS"
	MCPServerConfig ItemType = "MCP_SERVER_CONFIG"
	Memory          ItemType = "MEMORY"
	Plugins         ItemType = "PLUGINS"
	Sessions        ItemType = "SESSIONS"
	Skills          ItemType = "SKILLS"
	Subagents       ItemType = "SUBAGENTS"
)

type MarketplaceKindElement string

const (
	CreatedByMeRemote  MarketplaceKindElement = "created-by-me-remote"
	Local              MarketplaceKindElement = "local"
	SharedWithMe       MarketplaceKindElement = "shared-with-me"
	Vertical           MarketplaceKindElement = "vertical"
	WorkspaceDirectory MarketplaceKindElement = "workspace-directory"
)

type InitializeParamsMode string

const (
	Elevated   InitializeParamsMode = "elevated"
	Unelevated InitializeParamsMode = "unelevated"
)

type PersonalityEnum string

const (
	Friendly   PersonalityEnum = "friendly"
	Pragmatic  PersonalityEnum = "pragmatic"
	PurpleNone PersonalityEnum = "none"
)

type SandboxEnum string

const (
	DangerFullAccess SandboxEnum = "danger-full-access"
	ReadOnly         SandboxEnum = "read-only"
	WorkspaceWrite   SandboxEnum = "workspace-write"
)

type NetworkAccessEnum string

const (
	Enabled    NetworkAccessEnum = "enabled"
	Restricted NetworkAccessEnum = "restricted"
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

type PrincipalType string

const (
	Group             PrincipalType = "group"
	PrincipalTypeUser PrincipalType = "user"
	Workspace         PrincipalType = "workspace"
)

type Role string

const (
	Editor Role = "editor"
	Reader Role = "reader"
)

type SortDirectionEnum string

const (
	Asc  SortDirectionEnum = "asc"
	Desc SortDirectionEnum = "desc"
)

type SortKeyEnum string

const (
	CreatedAt       SortKeyEnum = "created_at"
	RecencyAt       SortKeyEnum = "recency_at"
	SectionPosition SortKeyEnum = "section_position"
	UpdatedAt       SortKeyEnum = "updated_at"
)

type SourceKindElement string

const (
	PurpleAppServer     SourceKindElement = "appServer"
	PurpleCLI           SourceKindElement = "cli"
	PurpleExec          SourceKindElement = "exec"
	PurpleUnknown       SourceKindElement = "unknown"
	PurpleVscode        SourceKindElement = "vscode"
	SubAgent            SourceKindElement = "subAgent"
	SubAgentCompact     SourceKindElement = "subAgentCompact"
	SubAgentOther       SourceKindElement = "subAgentOther"
	SubAgentReview      SourceKindElement = "subAgentReview"
	SubAgentThreadSpawn SourceKindElement = "subAgentThreadSpawn"
)

type StatusEnum string

const (
	BudgetLimited                        StatusEnum = "budgetLimited"
	CodexAppServerProtocolSchemasActive  StatusEnum = "active"
	CodexAppServerProtocolSchemasBlocked StatusEnum = "blocked"
	Complete                             StatusEnum = "complete"
	Paused                               StatusEnum = "paused"
	UsageLimited                         StatusEnum = "usageLimited"
)

// Option to disable reasoning summaries.
type SummaryEnum string

const (
	Concise       SummaryEnum = "concise"
	Detailed      SummaryEnum = "detailed"
	FluffyNone    SummaryEnum = "none"
	TentacledAuto SummaryEnum = "auto"
)

type ReviewTargetType string

const (
	BaseBranch         ReviewTargetType = "baseBranch"
	Commit             ReviewTargetType = "commit"
	Custom             ReviewTargetType = "custom"
	UncommittedChanges ReviewTargetType = "uncommittedChanges"
)

type InitializeParamsType string

const (
	ChatgptDeviceCode     InitializeParamsType = "chatgptDeviceCode"
	TypeAPIKey            InitializeParamsType = "apiKey"
	TypeAmazonBedrock     InitializeParamsType = "amazonBedrock"
	TypeChatgpt           InitializeParamsType = "chatgpt"
	TypeChatgptAuthTokens InitializeParamsType = "chatgptAuthTokens"
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
	Accept           FileChangeApprovalDecision = "accept"
	AcceptForSession FileChangeApprovalDecision = "acceptForSession"
	Cancel           FileChangeApprovalDecision = "cancel"
	Decline          FileChangeApprovalDecision = "decline"
)

type ForcedLoginMethodEnum string

const (
	API           ForcedLoginMethodEnum = "api"
	FluffyChatgpt ForcedLoginMethodEnum = "chatgpt"
)

// Count the full active context against the limit.
//
// Count sampled output and later growth after the carried window prefix.
type ModelAutoCompactTokenLimitScopeEnum string

const (
	BodyAfterPrefix ModelAutoCompactTokenLimitScopeEnum = "body_after_prefix"
	Total           ModelAutoCompactTokenLimitScopeEnum = "total"
)

// Controls output length/detail on GPT-5 models via the Responses API. Serialized with
// lowercase values to match the OpenAI API.
type ModelVerbosityEnum string

const (
	FluffyHigh   ModelVerbosityEnum = "high"
	FluffyLow    ModelVerbosityEnum = "low"
	PurpleMedium ModelVerbosityEnum = "medium"
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
	ConfigLayerSourceTypeMdm          ConfigLayerSourceType = "mdm"
	ConfigLayerSourceTypeProject      ConfigLayerSourceType = "project"
	ConfigLayerSourceTypeSessionFlags ConfigLayerSourceType = "sessionFlags"
	ConfigLayerSourceTypeSystem       ConfigLayerSourceType = "system"
	ConfigLayerSourceTypeUser         ConfigLayerSourceType = "user"
	EnterpriseManaged                 ConfigLayerSourceType = "enterpriseManaged"
	LegacyManagedConfigTomlFromFile   ConfigLayerSourceType = "legacyManagedConfigTomlFromFile"
	LegacyManagedConfigTomlFromMdm    ConfigLayerSourceType = "legacyManagedConfigTomlFromMdm"
	PackagedDefaults                  ConfigLayerSourceType = "packagedDefaults"
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
	AccountTypeAPIKey        AccountType = "apiKey"
	AccountTypeAmazonBedrock AccountType = "amazonBedrock"
	AccountTypeChatgpt       AccountType = "chatgpt"
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
	CodexAppServerProtocolSchemasV1 MultiAgentVersionEnum = "v1"
	CodexAppServerProtocolSchemasV2 MultiAgentVersionEnum = "v2"
	FluffyDisabled                  MultiAgentVersionEnum = "disabled"
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
	PermissionGrantScopeTurn PermissionGrantScope = "turn"
	Session                  PermissionGrantScope = "session"
)

type NotificationMethod string

const (
	AccountLoginCompleted                   NotificationMethod = "account/login/completed"
	AccountRateLimitsUpdated                NotificationMethod = "account/rateLimits/updated"
	AccountUpdated                          NotificationMethod = "account/updated"
	AppListUpdated                          NotificationMethod = "app/list/updated"
	CommandExecOutputDelta                  NotificationMethod = "command/exec/outputDelta"
	ConfigWarning                           NotificationMethod = "configWarning"
	DeprecationNotice                       NotificationMethod = "deprecationNotice"
	ExternalAgentConfigImportCompleted      NotificationMethod = "externalAgentConfig/import/completed"
	ExternalAgentConfigImportProgress       NotificationMethod = "externalAgentConfig/import/progress"
	FSChanged                               NotificationMethod = "fs/changed"
	FuzzyFileSearchSessionCompleted         NotificationMethod = "fuzzyFileSearch/sessionCompleted"
	FuzzyFileSearchSessionUpdated           NotificationMethod = "fuzzyFileSearch/sessionUpdated"
	GuardianWarning                         NotificationMethod = "guardianWarning"
	HookCompleted                           NotificationMethod = "hook/completed"
	HookStarted                             NotificationMethod = "hook/started"
	ItemAgentMessageDelta                   NotificationMethod = "item/agentMessage/delta"
	ItemAutoApprovalReviewCompleted         NotificationMethod = "item/autoApprovalReview/completed"
	ItemAutoApprovalReviewStarted           NotificationMethod = "item/autoApprovalReview/started"
	ItemCommandExecutionOutputDelta         NotificationMethod = "item/commandExecution/outputDelta"
	ItemCommandExecutionTerminalInteraction NotificationMethod = "item/commandExecution/terminalInteraction"
	ItemCompleted                           NotificationMethod = "item/completed"
	ItemFileChangeOutputDelta               NotificationMethod = "item/fileChange/outputDelta"
	ItemFileChangePatchUpdated              NotificationMethod = "item/fileChange/patchUpdated"
	ItemMCPToolCallProgress                 NotificationMethod = "item/mcpToolCall/progress"
	ItemPlanDelta                           NotificationMethod = "item/plan/delta"
	ItemReasoningSummaryPartAdded           NotificationMethod = "item/reasoning/summaryPartAdded"
	ItemReasoningSummaryTextDelta           NotificationMethod = "item/reasoning/summaryTextDelta"
	ItemReasoningTextDelta                  NotificationMethod = "item/reasoning/textDelta"
	ItemStarted                             NotificationMethod = "item/started"
	MCPServerOauthLoginCompleted            NotificationMethod = "mcpServer/oauthLogin/completed"
	MCPServerStartupStatusUpdated           NotificationMethod = "mcpServer/startupStatus/updated"
	ModelRerouted                           NotificationMethod = "model/rerouted"
	ModelSafetyBufferingUpdated             NotificationMethod = "model/safetyBuffering/updated"
	ModelVerification                       NotificationMethod = "model/verification"
	NotificationMethodError                 NotificationMethod = "error"
	NotificationMethodWarning               NotificationMethod = "warning"
	ProcessExited                           NotificationMethod = "process/exited"
	ProcessOutputDelta                      NotificationMethod = "process/outputDelta"
	RemoteControlStatusChanged              NotificationMethod = "remoteControl/status/changed"
	ServerRequestResolved                   NotificationMethod = "serverRequest/resolved"
	SkillsChanged                           NotificationMethod = "skills/changed"
	ThreadArchived                          NotificationMethod = "thread/archived"
	ThreadClosed                            NotificationMethod = "thread/closed"
	ThreadCompacted                         NotificationMethod = "thread/compacted"
	ThreadDeleted                           NotificationMethod = "thread/deleted"
	ThreadEnvironmentConnected              NotificationMethod = "thread/environment/connected"
	ThreadEnvironmentDisconnected           NotificationMethod = "thread/environment/disconnected"
	ThreadGoalCleared                       NotificationMethod = "thread/goal/cleared"
	ThreadGoalUpdated                       NotificationMethod = "thread/goal/updated"
	ThreadNameUpdated                       NotificationMethod = "thread/name/updated"
	ThreadQueueChanged                      NotificationMethod = "thread/queue/changed"
	ThreadRealtimeClosed                    NotificationMethod = "thread/realtime/closed"
	ThreadRealtimeError                     NotificationMethod = "thread/realtime/error"
	ThreadRealtimeItemAdded                 NotificationMethod = "thread/realtime/itemAdded"
	ThreadRealtimeOutputAudioDelta          NotificationMethod = "thread/realtime/outputAudio/delta"
	ThreadRealtimeSDP                       NotificationMethod = "thread/realtime/sdp"
	ThreadRealtimeStarted                   NotificationMethod = "thread/realtime/started"
	ThreadRealtimeTranscriptDelta           NotificationMethod = "thread/realtime/transcript/delta"
	ThreadRealtimeTranscriptDone            NotificationMethod = "thread/realtime/transcript/done"
	ThreadReverted                          NotificationMethod = "thread/reverted"
	ThreadSettingsUpdated                   NotificationMethod = "thread/settings/updated"
	ThreadStarted                           NotificationMethod = "thread/started"
	ThreadStatusChanged                     NotificationMethod = "thread/status/changed"
	ThreadTokenUsageUpdated                 NotificationMethod = "thread/tokenUsage/updated"
	ThreadUnarchived                        NotificationMethod = "thread/unarchived"
	TurnCompleted                           NotificationMethod = "turn/completed"
	TurnDiffUpdated                         NotificationMethod = "turn/diff/updated"
	TurnModerationMetadata                  NotificationMethod = "turn/moderationMetadata"
	TurnPlanUpdated                         NotificationMethod = "turn/plan/updated"
	TurnStarted                             NotificationMethod = "turn/started"
	WindowsSandboxSetupCompleted            NotificationMethod = "windowsSandbox/setupCompleted"
	WindowsWorldWritableWarning             NotificationMethod = "windows/worldWritableWarning"
)

type ActionSource string

const (
	Shell       ActionSource = "shell"
	UnifiedExec ActionSource = "unifiedExec"
)

type GuardianApprovalReviewActionType string

const (
	ApplyPatch                                  GuardianApprovalReviewActionType = "applyPatch"
	Execve                                      GuardianApprovalReviewActionType = "execve"
	GuardianApprovalReviewActionTypeCommand     GuardianApprovalReviewActionType = "command"
	GuardianApprovalReviewActionTypeMCPToolCall GuardianApprovalReviewActionType = "mcpToolCall"
	NetworkAccess                               GuardianApprovalReviewActionType = "networkAccess"
	RequestPermissions                          GuardianApprovalReviewActionType = "requestPermissions"
)

// OpenAI API key provided by the caller and stored by Codex.
//
// ChatGPT OAuth managed by Codex (tokens persisted and refreshed by Codex).
//
// [UNSTABLE] FOR OPENAI INTERNAL USE ONLY - DO NOT USE.
//
// ChatGPT auth tokens are supplied by an external host app and are only stored in memory.
// Token refresh must be handled by the external host app.
//
// Backend auth supplied as request headers.
//
// Programmatic Codex auth backed by a registered Agent Identity.
//
// Programmatic Codex auth backed by a personal access token.
//
// Amazon Bedrock bearer token managed by Codex.
type AuthModeEnum string

const (
	AgentIdentity                                  AuthModeEnum = "agentIdentity"
	Apikey                                         AuthModeEnum = "apikey"
	BedrockAPIKey                                  AuthModeEnum = "bedrockApiKey"
	CodexAppServerProtocolSchemasChatgptAuthTokens AuthModeEnum = "chatgptAuthTokens"
	Headers                                        AuthModeEnum = "headers"
	PersonalAccessToken                            AuthModeEnum = "personalAccessToken"
	TentacledChatgpt                               AuthModeEnum = "chatgpt"
)

type KindType string

const (
	Add    KindType = "add"
	Delete KindType = "delete"
	Update KindType = "update"
)

// [UNSTABLE] Source that produced a terminal approval auto-review decision.
type DecisionSource string

const (
	DecisionSourceAgent DecisionSource = "agent"
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

type FailureReasonEnum string

const (
	ReauthenticationRequired FailureReasonEnum = "reauthenticationRequired"
)

type FuzzyFileSearchMatchType string

const (
	Directory FuzzyFileSearchMatchType = "directory"
	File      FuzzyFileSearchMatchType = "file"
)

type OnboardingEntrypointEnum string

const (
	LifeSciences OnboardingEntrypointEnum = "life_sciences"
)

type PlanStatus string

const (
	Pending          PlanStatus = "pending"
	PurpleCompleted  PlanStatus = "completed"
	PurpleInProgress PlanStatus = "inProgress"
)

type RateLimitReachedTypeEnum string

const (
	RateLimitReached                 RateLimitReachedTypeEnum = "rate_limit_reached"
	WorkspaceMemberCreditsDepleted   RateLimitReachedTypeEnum = "workspace_member_credits_depleted"
	WorkspaceMemberUsageLimitReached RateLimitReachedTypeEnum = "workspace_member_usage_limit_reached"
	WorkspaceOwnerCreditsDepleted    RateLimitReachedTypeEnum = "workspace_owner_credits_depleted"
	WorkspaceOwnerUsageLimitReached  RateLimitReachedTypeEnum = "workspace_owner_usage_limit_reached"
)

// [UNSTABLE] Risk level assigned by approval auto-review.
type RiskLevelEnum string

const (
	Critical      RiskLevelEnum = "critical"
	FluffyMedium  RiskLevelEnum = "medium"
	TentacledHigh RiskLevelEnum = "high"
	TentacledLow  RiskLevelEnum = "low"
)

// [UNSTABLE] Lifecycle state for an approval auto-review.
type ReviewStatus string

const (
	Aborted          ReviewStatus = "aborted"
	Approved         ReviewStatus = "approved"
	Denied           ReviewStatus = "denied"
	FluffyInProgress ReviewStatus = "inProgress"
	TimedOut         ReviewStatus = "timedOut"
)

// [UNSTABLE] Authorization level assigned by approval auto-review.
type UserAuthorizationEnum string

const (
	FluffyUnknown   UserAuthorizationEnum = "unknown"
	StickyHigh      UserAuthorizationEnum = "high"
	StickyLow       UserAuthorizationEnum = "low"
	TentacledMedium UserAuthorizationEnum = "medium"
)

type EntryKind string

const (
	Context     EntryKind = "context"
	Feedback    EntryKind = "feedback"
	KindError   EntryKind = "error"
	KindStop    EntryKind = "stop"
	KindWarning EntryKind = "warning"
)

type EventName string

const (
	EventNameStop     EventName = "stop"
	PermissionRequest EventName = "permissionRequest"
	PostCompact       EventName = "postCompact"
	PostToolUse       EventName = "postToolUse"
	PreCompact        EventName = "preCompact"
	PreToolUse        EventName = "preToolUse"
	SessionEnd        EventName = "sessionEnd"
	SessionStart      EventName = "sessionStart"
	SubagentStart     EventName = "subagentStart"
	SubagentStop      EventName = "subagentStop"
	UserPromptSubmit  EventName = "userPromptSubmit"
)

type ExecutionMode string

const (
	Async ExecutionMode = "async"
	Sync  ExecutionMode = "sync"
)

type HandlerType string

const (
	HandlerTypeAgent   HandlerType = "agent"
	HandlerTypeCommand HandlerType = "command"
	MCPTool            HandlerType = "mcpTool"
	Prompt             HandlerType = "prompt"
)

type Scope string

const (
	ScopeTurn Scope = "turn"
	Thread    Scope = "thread"
)

type RunSource string

const (
	CloudManagedConfig      RunSource = "cloudManagedConfig"
	CloudRequirements       RunSource = "cloudRequirements"
	LegacyManagedConfigFile RunSource = "legacyManagedConfigFile"
	LegacyManagedConfigMdm  RunSource = "legacyManagedConfigMdm"
	Plugin                  RunSource = "plugin"
	SourceMdm               RunSource = "mdm"
	SourceProject           RunSource = "project"
	SourceSessionFlags      RunSource = "sessionFlags"
	SourceSystem            RunSource = "system"
	SourceUnknown           RunSource = "unknown"
	SourceUser              RunSource = "user"
)

type RunStatus string

const (
	FluffyCompleted RunStatus = "completed"
	PurpleFailed    RunStatus = "failed"
	PurpleRunning   RunStatus = "running"
	StatusBlocked   RunStatus = "blocked"
	Stopped         RunStatus = "stopped"
)

type ActiveFlagElement string

const (
	WaitingOnApproval  ActiveFlagElement = "waitingOnApproval"
	WaitingOnUserInput ActiveFlagElement = "waitingOnUserInput"
)

type ThreadStatusType string

const (
	Idle                      ThreadStatusType = "idle"
	SystemError               ThreadStatusType = "systemError"
	ThreadStatusTypeActive    ThreadStatusType = "active"
	ThreadStatusTypeNotLoaded ThreadStatusType = "notLoaded"
)

type StatusStatus string

const (
	Cancelled      StatusStatus = "cancelled"
	Connected      StatusStatus = "connected"
	Connecting     StatusStatus = "connecting"
	FluffyFailed   StatusStatus = "failed"
	PurpleErrored  StatusStatus = "errored"
	Ready          StatusStatus = "ready"
	Starting       StatusStatus = "starting"
	StatusDisabled StatusStatus = "disabled"
)

// stdout stream. PTY mode multiplexes terminal output here.
//
// stderr stream.
type Stream string

const (
	Stderr Stream = "stderr"
	Stdout Stream = "stdout"
)

type SubAgentEnum string

const (
	CodexAppServerProtocolSchemasCompact SubAgentEnum = "compact"
	CodexAppServerProtocolSchemasReview  SubAgentEnum = "review"
	MemoryConsolidation                  SubAgentEnum = "memory_consolidation"
)

type SourceEnum string

const (
	FluffyAppServer  SourceEnum = "appServer"
	FluffyCLI        SourceEnum = "cli"
	FluffyExec       SourceEnum = "exec"
	FluffyVscode     SourceEnum = "vscode"
	TentacledUnknown SourceEnum = "unknown"
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
	FluffyErrored      AgentsStateStatus = "errored"
	FluffyNotFound     AgentsStateStatus = "notFound"
	FluffyRunning      AgentsStateStatus = "running"
	PendingInit        AgentsStateStatus = "pendingInit"
	PurpleInterrupted  AgentsStateStatus = "interrupted"
	Shutdown           AgentsStateStatus = "shutdown"
	TentacledCompleted AgentsStateStatus = "completed"
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
	SourceAgent            ItemSource = "agent"
	UnifiedExecInteraction ItemSource = "unifiedExecInteraction"
	UnifiedExecStartup     ItemSource = "unifiedExecStartup"
	UserShell              ItemSource = "userShell"
)

type ThreadItemType string

const (
	AgentMessage              ThreadItemType = "agentMessage"
	CollabAgentToolCall       ThreadItemType = "collabAgentToolCall"
	CommandExecution          ThreadItemType = "commandExecution"
	ContextCompaction         ThreadItemType = "contextCompaction"
	DynamicToolCall           ThreadItemType = "dynamicToolCall"
	EnteredReviewMode         ThreadItemType = "enteredReviewMode"
	ExitedReviewMode          ThreadItemType = "exitedReviewMode"
	HookPrompt                ThreadItemType = "hookPrompt"
	ImageGeneration           ThreadItemType = "imageGeneration"
	ImageView                 ThreadItemType = "imageView"
	Reasoning                 ThreadItemType = "reasoning"
	Sleep                     ThreadItemType = "sleep"
	SubAgentActivity          ThreadItemType = "subAgentActivity"
	ThreadItemTypeFileChange  ThreadItemType = "fileChange"
	ThreadItemTypeMCPToolCall ThreadItemType = "mcpToolCall"
	ThreadItemTypePlan        ThreadItemType = "plan"
	UserMessage               ThreadItemType = "userMessage"
	WebSearch                 ThreadItemType = "webSearch"
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
	ItemsViewFull      ItemsView = "full"
	ItemsViewNotLoaded ItemsView = "notLoaded"
	Summary            ItemsView = "summary"
)

type TurnStatus string

const (
	FluffyInterrupted   TurnStatus = "interrupted"
	StickyCompleted     TurnStatus = "completed"
	TentacledFailed     TurnStatus = "failed"
	TentacledInProgress TurnStatus = "inProgress"
)

// Initial collaboration mode to use when the TUI starts.
type CollaborationModeMode string

const (
	Default  CollaborationModeMode = "default"
	ModePlan CollaborationModeMode = "plan"
)

type VerificationElement string

const (
	TrustedAccessForCyber VerificationElement = "trustedAccessForCyber"
)

type Version string

const (
	V3        Version = "v3"
	VersionV1 Version = "v1"
	VersionV2 Version = "v2"
)

type ServerRequestMethod string

const (
	AccountChatgptAuthTokensRefresh     ServerRequestMethod = "account/chatgptAuthTokens/refresh"
	ApplyPatchApproval                  ServerRequestMethod = "applyPatchApproval"
	AttestationGenerate                 ServerRequestMethod = "attestation/generate"
	ExecCommandApproval                 ServerRequestMethod = "execCommandApproval"
	ItemCommandExecutionRequestApproval ServerRequestMethod = "item/commandExecution/requestApproval"
	ItemFileChangeRequestApproval       ServerRequestMethod = "item/fileChange/requestApproval"
	ItemPermissionsRequestApproval      ServerRequestMethod = "item/permissions/requestApproval"
	ItemToolCall                        ServerRequestMethod = "item/tool/call"
	ItemToolRequestUserInput            ServerRequestMethod = "item/tool/requestUserInput"
	MCPServerElicitationRequest         ServerRequestMethod = "mcpServer/elicitation/request"
)

type PurpleMode string

const (
	Form       PurpleMode = "form"
	OpenaiForm PurpleMode = "openai/form"
	URL        PurpleMode = "url"
)

type ParsedCommandType string

const (
	ParsedCommandTypeListFiles ParsedCommandType = "list_files"
	ParsedCommandTypeRead      ParsedCommandType = "read"
	ParsedCommandTypeSearch    ParsedCommandType = "search"
	ParsedCommandTypeUnknown   ParsedCommandType = "unknown"
)

type ThreadUnsubscribeResponseStatus string

const (
	NotSubscribed   ThreadUnsubscribeResponseStatus = "notSubscribed"
	StatusNotLoaded ThreadUnsubscribeResponseStatus = "notLoaded"
	Unsubscribed    ThreadUnsubscribeResponseStatus = "unsubscribed"
)

type ID struct {
	Integer *int64
	String  *string
}

func (x *ID) UnmarshalJSON(data []byte) error {
	object, err := unmarshalUnion(data, &x.Integer, nil, nil, &x.String, false, nil, false, nil, false, nil, false, nil, false)
	if err != nil {
		return err
	}
	if object {
	}
	return nil
}

func (x *ID) MarshalJSON() ([]byte, error) {
	return marshalUnion(x.Integer, nil, nil, x.String, false, nil, false, nil, false, nil, false, nil, false)
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

type Command struct {
	String      *string
	StringArray []string
}

func (x *Command) UnmarshalJSON(data []byte) error {
	x.StringArray = nil
	object, err := unmarshalUnion(data, nil, nil, nil, &x.String, true, &x.StringArray, false, nil, false, nil, false, nil, false)
	if err != nil {
		return err
	}
	if object {
	}
	return nil
}

func (x *Command) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, x.String, x.StringArray != nil, x.StringArray, false, nil, false, nil, false, nil, false)
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

type NetworkAccessUnion struct {
	Bool *bool
	Enum *NetworkAccessEnum
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

type Title struct {
	Error  *Error
	String *string
}

func (x *Title) UnmarshalJSON(data []byte) error {
	x.Error = nil
	var c Error
	object, err := unmarshalUnion(data, nil, nil, nil, &x.String, false, nil, true, &c, false, nil, false, nil, true)
	if err != nil {
		return err
	}
	if object {
		x.Error = &c
	}
	return nil
}

func (x *Title) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, x.String, false, nil, x.Error != nil, x.Error, false, nil, false, nil, true)
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

type StatusUnion struct {
	Enum         *StatusStatus
	ThreadStatus *ThreadStatus
}

func (x *StatusUnion) UnmarshalJSON(data []byte) error {
	x.ThreadStatus = nil
	x.Enum = nil
	var c ThreadStatus
	object, err := unmarshalUnion(data, nil, nil, nil, nil, false, nil, true, &c, false, nil, true, &x.Enum, false)
	if err != nil {
		return err
	}
	if object {
		x.ThreadStatus = &c
	}
	return nil
}

func (x *StatusUnion) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.ThreadStatus != nil, x.ThreadStatus, false, nil, x.Enum != nil, x.Enum, false)
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

type SubAgentUnion struct {
	Enum           *SubAgentEnum
	SubAgentSource *SubAgentSource
}

func (x *SubAgentUnion) UnmarshalJSON(data []byte) error {
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

func (x *SubAgentUnion) MarshalJSON() ([]byte, error) {
	return marshalUnion(nil, nil, nil, nil, false, nil, x.SubAgentSource != nil, x.SubAgentSource, false, nil, x.Enum != nil, x.Enum, false)
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
