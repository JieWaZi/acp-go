package acpclient

import (
	"context"
	"path/filepath"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	restoreStartupWait    = 2 * time.Second
	restoreListingWait    = 500 * time.Millisecond
	restoreListingTimeout = 3 * time.Second
	restoreListingPages   = 50
)

func (c *Client) recoverRestore(ctx context.Context, conn *acp.ClientSideConnection, p *opening, request setupRequest, peer *acp.RequestError) (bool, error) {
	classify := c.opts.Protocol.RecoverRestore
	if classify == nil || peer == nil || request.method != acp.AgentMethodSessionResume && request.method != acp.AgentMethodSessionLoad {
		return false, nil
	}
	action := classify(request.method, peer)
	if action == RestoreNoRecovery {
		return false, nil
	}
	c.mu.Lock()
	// Only a refusal without replay, config, or a bound response is safe to retry.
	if c.closed || c.opening != p || p.matched || p.err != nil || p.overflow || len(p.observations)+len(p.pending) != 0 {
		c.mu.Unlock()
		return false, nil
	}
	p.started = false
	c.mu.Unlock()
	delay := restoreStartupWait
	if action == RestoreCheckListing {
		if c.initialized.AgentCapabilities.SessionCapabilities.List != nil &&
			nativeListingProvesAbsence(ctx, conn, request.cwd, request.id) {
			return false, &acp.RequestError{Code: -32002, Message: "ACP agent session is absent from its native directory",
				Data: map[string]any{"method": request.method, "classification": "session_missing"}}
		}
		delay = restoreListingWait
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-conn.Done():
		return false, ErrRestartRequired
	}
}

func nativeListingProvesAbsence(ctx context.Context, conn *acp.ClientSideConnection, cwd string, id acp.SessionId) bool {
	op, cancel := context.WithTimeout(ctx, restoreListingTimeout)
	defer cancel()
	request := acp.ListSessionsRequest{Cwd: &cwd}
	seen := make(map[string]struct{})
	for range restoreListingPages {
		r := send[acp.ListSessionsResponse](conn, op, acp.AgentMethodSessionList, request)
		if receiptFailure(r, acp.AgentMethodSessionList) != nil || r.Result.Sessions == nil {
			return false
		}
		for _, row := range r.Result.Sessions {
			if row.SessionId == "" || row.SessionId == id || filepath.Clean(row.Cwd) != filepath.Clean(cwd) {
				return false
			}
		}
		if r.Result.NextCursor == nil {
			return true
		}
		cursor := *r.Result.NextCursor
		if _, duplicate := seen[cursor]; duplicate || cursor == "" {
			return false
		}
		seen[cursor] = struct{}{}
		request.Cursor = &cursor
	}
	return false
}
