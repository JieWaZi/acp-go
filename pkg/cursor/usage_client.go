package cursor

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

const cursorUsageNonceKey = "acp-go/cursor-usage-nonce"

// cursorUsageState associates the current ACP input with allowlisted usage files without duplicating execution ownership.
type cursorUsageState struct {
	// directory is the connection-private usage path planned at construction and created during initialization.
	directory string
	// nonces associates inputs already reserved by shared ACP and is protected by Client.mu.
	nonces map[acp.SessionId]string
	// directoryMu serializes directory creation and cleanup without enclosing native operations.
	directoryMu sync.Mutex
}

func newCursorUsageNonce() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}

// Initialize prepares the optional usage directory and delegates capability, streaming, and failure semantics to official ACP.
func (c *Client) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	if usage := c.usage; usage != nil && context.Cause(ctx) == nil {
		usage.directoryMu.Lock()
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if !closed {
			_ = os.MkdirAll(usage.directory, 0700)
		}
		usage.directoryMu.Unlock()
	}
	return c.Client.Initialize(ctx, request)
}

// ReservePrompt reserves through shared ACP and adds a unique usage association for that input.
func (c *Client) ReservePrompt(ctx context.Context, request acp.PromptRequest, options agentclient.PromptOptions) (func() agentclient.PromptResult, error) {
	if c.usage == nil {
		return c.Client.ReservePrompt(ctx, request, options)
	}
	nonce := newCursorUsageNonce()
	meta := make(map[string]any, len(request.Meta)+1)
	for key, value := range request.Meta {
		meta[key] = value
	}
	meta[cursorUsageNonceKey] = nonce
	request.Meta = meta
	if options.HostInvocationID == "" {
		options.HostInvocationID = newCursorUsageNonce()
	}
	run, err := c.Client.ReservePrompt(ctx, request, options)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.usage.nonces[request.SessionId] = nonce
	c.mu.Unlock()
	var continued atomic.Bool
	return func() agentclient.PromptResult {
		if !continued.CompareAndSwap(false, true) {
			return agentclient.PromptResult{Receipt: agentclient.PromptReceipt{
				Input:      agentclient.PromptSubmission{SessionID: request.SessionId, HostInvocationID: options.HostInvocationID},
				Submission: agentclient.SubmissionNotSent, Admission: agentclient.AdmissionNotAccepted,
				OperationError: errors.New("Cursor prompt continuation was already consumed"),
			}}
		}
		defer c.clearUsageNonce(request.SessionId, nonce)
		return run()
	}, nil
}

func (c *Client) captureUsageJournal(ctx context.Context, scope acpclient.PromptJournalScope) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	if c.usage == nil {
		return nil
	}
	c.mu.Lock()
	nonce := c.usage.nonces[scope.SessionID]
	c.mu.Unlock()
	if nonce == "" {
		return nil
	}
	return captureCursorUsageJournal(ctx, scope, c.usageConfigRoot(), c.usage.directory, nonce)
}

func (c *Client) usageConfigRoot() string {
	root := envValue(c.opts.Environment, "CURSOR_CONFIG_DIR")
	if root == "" {
		if xdg := envValue(c.opts.Environment, "XDG_CONFIG_HOME"); xdg != "" {
			root = filepath.Join(xdg, "cursor")
		}
	}
	if root == "" {
		home := envValue(c.opts.Environment, "HOME")
		if home == "" {
			home = envValue(c.opts.Environment, "USERPROFILE")
		}
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return ""
			}
		}
		root = filepath.Join(home, ".cursor")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(c.opts.WorkingDirectory, root)
	}
	return filepath.Clean(root)
}

func (c *Client) clearUsageNonce(sessionID acp.SessionId, nonce string) {
	c.mu.Lock()
	if c.usage.nonces[sessionID] == nonce {
		delete(c.usage.nonces, sessionID)
	}
	c.mu.Unlock()
	// The cleanup budget covers only this input's event records, preserving health and other turns.
	root, err := os.OpenRoot(c.usage.directory)
	if err != nil {
		return
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return
	}
	defer directory.Close()
	entries, _ := directory.ReadDir(256)
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), nonce+"-") && strings.HasSuffix(entry.Name(), ".json") {
			_ = root.Remove(entry.Name())
		}
	}
}

func (c *Client) closeUsage(ctx context.Context) error {
	if c.usage == nil {
		return nil
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if !c.usage.directoryMu.TryLock() {
		return ErrBusy
	}
	defer c.usage.directoryMu.Unlock()
	return os.RemoveAll(c.usage.directory)
}
