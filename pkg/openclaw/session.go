package openclaw

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

var ownedKey = regexp.MustCompile(`^(agent:[a-z0-9][a-z0-9_-]{0,63}:)neuxbot-acp:(session|fork)-[a-f0-9]{32}$`)

// binding identifies the physical Gateway session behind one bridge session handle.
type binding struct {
	// Gateway is the frozen loopback origin selected for this session.
	Gateway string `json:"gateway"`
	// Key is the canonical Gateway key and explicitly identifies its agent.
	Key string `json:"key"`
	// PhysicalID is the transcript generation held under the canonical key.
	PhysicalID string `json:"sessionId"`
}

// sessionState retains Gateway settings alongside the bridge's native controls.
type sessionState struct {
	// binding identifies the active stable Gateway session and transcript.
	binding binding
	// options are the most recent confirmed ACP or Gateway settings.
	options []acp.SessionConfigOption
	// model is the Gateway-observed model shown alongside native ACP controls.
	model acp.SessionConfigValueId
	// selectedModel is an explicit Gateway setter acknowledgment.
	selectedModel acp.SessionConfigValueId
}

type gatewaySession struct {
	// Key is the canonical native routing key.
	Key string `json:"key"`
	// PhysicalID is the Gateway transcript generation.
	PhysicalID string `json:"sessionId"`
	// Cwd is the persisted Gateway execution directory.
	Cwd string `json:"spawnedCwd"`
	// ExecHost is present when the Gateway binds exec to a different host.
	ExecHost string `json:"execHost"`
	// ExecNode identifies an explicitly selected exec node when present.
	ExecNode string `json:"execNode"`
	// ExecCwd is the independent working directory used by exec when configured.
	ExecCwd string `json:"execCwd"`
	// Placement is the Gateway's native execution placement for a managed session.
	Placement *struct {
		// Kind identifies the projected target class.
		Kind string `json:"kind"`
		// State identifies the current placement lifecycle.
		State string `json:"state"`
	} `json:"placement"`
	// Model is the native model ID, relative to Provider.
	Model string `json:"model"`
	// Provider is the observed model provider selected by the Gateway.
	Provider string `json:"modelProvider"`
}

func (c *Client) updateOptions(id acp.SessionId, options []acp.SessionConfigOption) []acp.SessionConfigOption {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	state, exists := c.sessions[id]
	if !exists {
		return options
	}
	state.options = withModelOption(options, state.model)
	c.sessions[id] = state
	return state.options
}

func physicalDirectory(dir string) (string, error) {
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errors.New("cannot verify OpenClaw working directory")
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return "", errors.New("cannot verify OpenClaw working directory")
	}
	return filepath.Clean(physical), nil
}

func (c *Client) validateWorkspace(cwd string, additionalCount, serverCount int) error {
	switch {
	case serverCount != 0:
		return acp.NewInvalidParams("OpenClaw Gateway does not support per-session MCP")
	case additionalCount != 0:
		return acp.NewInvalidParams("OpenClaw Gateway supports one authorized session directory")
	case !filepath.IsAbs(cwd):
		return acp.NewInvalidParams("OpenClaw Gateway requires an absolute working directory")
	}
	physical, err := physicalDirectory(cwd)
	if err != nil || physical != c.cwd {
		return acp.NewInvalidParams("OpenClaw Gateway working directory does not match the authorized directory")
	}
	return nil
}

func (b binding) id() acp.SessionId {
	value, _ := json.Marshal(b)
	return acp.SessionId("openclaw:" + base64.RawURLEncoding.EncodeToString(value))
}

func (c *Client) parseSessionID(id acp.SessionId) (binding, error) {
	return parseBinding(id, c.gateway.url)
}

func parseBinding(id acp.SessionId, endpoint string) (binding, error) {
	var bound binding
	encoded, ok := strings.CutPrefix(string(id), "openclaw:")
	if !ok || len(encoded) > 1500 {
		return bound, missingSession()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(raw, &bound) != nil || bound.id() != id ||
		bound.Gateway != endpoint || !ownedKey.MatchString(bound.Key) || !validPhysicalID(bound.PhysicalID) {
		return binding{}, missingSession()
	}
	return bound, nil
}

func validPhysicalID(id string) bool {
	return id != "" && len(id) <= 256 && strings.TrimSpace(id) == id
}

func missingSession() *acp.RequestError {
	return &acp.RequestError{Code: -32002, Message: "OpenClaw Gateway session is missing or no longer matches its bound identity",
		Data: map[string]any{"classification": "session_missing"}}
}

func (c *Client) describe(ctx context.Context, bound binding) (gatewaySession, error) {
	var result struct {
		// Session is null when no visible session matches the exact canonical key.
		Session *gatewaySession `json:"session"`
	}
	if err := c.gateway.call(ctx, "sessions.describe", map[string]any{"key": bound.Key}, &result); err != nil {
		return gatewaySession{}, err
	}
	if result.Session == nil || result.Session.Key != bound.Key || result.Session.PhysicalID != bound.PhysicalID {
		return gatewaySession{}, missingSession()
	}
	row := *result.Session
	if row.ExecNode != "" || row.ExecHost != "" && row.ExecHost != "gateway" && row.ExecHost != "sandbox" ||
		row.Placement != nil && (row.Placement.State != "local" || row.Placement.Kind != "" && row.Placement.Kind != "local") {
		return gatewaySession{}, acp.NewInvalidParams("OpenClaw session is not assigned to the local Gateway")
	}
	if err := c.validateWorkspace(row.Cwd, 0, 0); err != nil {
		return gatewaySession{}, acp.NewInvalidParams("OpenClaw Gateway did not confirm the authorized execution directory")
	}
	if row.ExecCwd != "" {
		if err := c.validateWorkspace(row.ExecCwd, 0, 0); err != nil {
			return gatewaySession{}, acp.NewInvalidParams("OpenClaw Gateway reported a different exec directory")
		}
	}
	return row, nil
}

// NewSession creates a unique Gateway-owned conversation, then binds it to ACP.
func (c *Client) NewSession(ctx context.Context, request acp.NewSessionRequest) (_ acp.NewSessionResponse, err error) {
	if err := c.admit(ctx); err != nil {
		return acp.NewSessionResponse{}, err
	}
	defer c.op.Unlock()
	if err := c.validateWorkspace(request.Cwd, len(request.AdditionalDirectories), len(request.McpServers)); err != nil {
		return acp.NewSessionResponse{}, err
	}
	op, cancel := c.operationContext(ctx)
	defer cancel()
	bound, err := c.create(op)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	c.unopened[bound] = struct{}{}
	defer func() {
		if err == nil {
			delete(c.unopened, bound)
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, c.discardUnopened(cleanup, bound))
	}()
	row, err := c.describe(op, bound)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	opened, err := c.resume(op, bound, row, request.Meta)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{SessionId: bound.id(), ConfigOptions: opened.ConfigOptions, Meta: opened.Meta}, nil
}

func (c *Client) create(ctx context.Context) (binding, error) {
	var suffix [16]byte
	rand.Read(suffix[:])
	key := "neuxbot-acp:session-" + hex.EncodeToString(suffix[:])
	params := map[string]any{"cwd": c.cwd, "key": key}

	var result struct {
		// OK acknowledges successful native session creation.
		OK bool `json:"ok"`
		// Key is the canonical session key issued by the same Gateway.
		Key string `json:"key"`
		// PhysicalID identifies the actual persisted transcript generation.
		PhysicalID string `json:"sessionId"`
	}
	if err := c.gateway.call(ctx, "sessions.create", params, &result); err != nil {
		return binding{}, err
	}
	if !result.OK || !strings.HasSuffix(result.Key, ":"+key) || !ownedKey.MatchString(result.Key) || !validPhysicalID(result.PhysicalID) {
		return binding{}, errors.New("OpenClaw Gateway did not confirm the new physical session")
	}
	return binding{Gateway: c.gateway.url, Key: result.Key, PhysicalID: result.PhysicalID}, nil
}

func (c *Client) discardUnopened(ctx context.Context, bound binding) error {
	var receipt struct {
		// OK reports that the Gateway completed the delete request.
		OK bool `json:"ok"`
		// Key confirms the canonical session deleted by the Gateway.
		Key string `json:"key"`
		// Deleted reports that the native row was removed.
		Deleted bool `json:"deleted"`
	}
	err := c.gateway.call(ctx, "sessions.delete", map[string]any{
		"key": bound.Key, "expectedSessionId": bound.PhysicalID, "deleteTranscript": true,
	}, &receipt)
	if err == nil && receipt.OK && receipt.Key == bound.Key && receipt.Deleted {
		delete(c.unopened, bound)
		return nil
	}
	// The prior request can have completed with a lost receipt, or the
	// Gateway can have replaced the session. Only the original ID is ours.
	var status struct {
		// Session is null when the exact canonical key has disappeared.
		Session json.RawMessage `json:"session"`
	}
	readErr := c.gateway.call(ctx, "sessions.describe", map[string]any{"key": bound.Key}, &status)
	var current gatewaySession
	replaced := json.Unmarshal(status.Session, &current) == nil && current.Key == bound.Key &&
		validPhysicalID(current.PhysicalID) && current.PhysicalID != bound.PhysicalID
	if readErr == nil && (string(status.Session) == "null" || replaced) {
		delete(c.unopened, bound)
		return nil
	}
	return errors.Join(errors.New("OpenClaw Gateway has not confirmed cleanup of the failed new session"), err, readErr)
}

// ResumeSession verifies the persisted physical session before binding without replay.
func (c *Client) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if err := c.admit(ctx); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	defer c.op.Unlock()
	if err := c.validateWorkspace(request.Cwd, len(request.AdditionalDirectories), len(request.McpServers)); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	bound, err := c.parseSessionID(request.SessionId)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	op, cancel := c.operationContext(ctx)
	defer cancel()
	row, err := c.describe(op, bound)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return c.resume(op, bound, row, request.Meta)
}

func (c *Client) resume(ctx context.Context, bound binding, row gatewaySession, meta map[string]any) (acp.ResumeSessionResponse, error) {
	meta = maps.Clone(meta)
	if meta == nil {
		meta = make(map[string]any)
	}
	for _, alias := range []string{"session", "key", "sessionLabel", "label", "resetSession", "reset", "forkReceiptDirectory", "reconcileOnly"} {
		delete(meta, alias)
	}
	meta["sessionKey"] = bound.Key
	meta["requireExisting"] = true
	id := bound.id()
	c.sessionMu.Lock()
	previous, existed := c.sessions[id]
	active := previous
	active.binding = bound
	active.model = modelID(row.Provider, row.Model)
	c.sessions[id] = active
	c.sessionMu.Unlock()
	reply, err := c.bridge.Load().ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: id, Cwd: c.cwd, Meta: meta})
	if err != nil {
		c.sessionMu.Lock()
		if existed {
			c.sessions[id] = previous
		} else {
			delete(c.sessions, id)
		}
		c.sessionMu.Unlock()
		return acp.ResumeSessionResponse{}, err
	}
	reply.Modes = nil
	reply.ConfigOptions = c.updateOptions(id, reply.ConfigOptions)
	return reply, nil
}

// CloseSession releases the ACP binding and preserves the native Gateway history.
func (c *Client) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	if err := c.admit(ctx); err != nil {
		return acp.CloseSessionResponse{}, err
	}
	defer c.op.Unlock()
	c.sessionMu.Lock()
	_, exists := c.sessions[request.SessionId]
	c.sessionMu.Unlock()
	if !exists {
		return acp.CloseSessionResponse{}, missingSession()
	}
	response, err := c.bridge.Load().CloseSession(ctx, request)
	if err == nil {
		c.sessionMu.Lock()
		delete(c.sessions, request.SessionId)
		c.sessionMu.Unlock()
	}
	return response, err
}

// UnstableForkSession can reconcile historical receipts. The Gateway cannot
// atomically fork at the physical turn selected by the host.
func (c *Client) UnstableForkSession(_ context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(request) {
		id, err := acpmeta.ReadForkReceipt(request)
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}
	return acp.UnstableForkSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionFork)
}
