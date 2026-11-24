package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

// gateway owns the local route and the native management children.
type gateway struct {
	// url pins the literal loopback endpoint for both native processes.
	url string
	// process launches the selected executable under the original native profile.
	process agentclient.ProcessSpec
	// pinnedEnv uses the same pinned local configuration as native ACP.
	pinnedEnv []string
	// mu protects process ownership and admission during shutdown.
	mu sync.Mutex
	// closed rejects new CLI launches once shutdown starts.
	closed bool
	// active retains children until their cleanup has been confirmed.
	active map[*agentclient.Process]struct{}
}

func (g *gateway) call(ctx context.Context, method string, params map[string]any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	output, code, err := g.run(ctx, g.pinnedEnv, "gateway", "call", method,
		"--expect-url", g.url, "--params", string(body), "--json", "--timeout", "20000")
	if err != nil {
		return fmt.Errorf("OpenClaw Gateway %s: %w", method, err)
	}
	if code != 0 {
		if code == 1 {
			if classified := gatewayFailure(output); classified != nil {
				return classified
			}
		}
		return fmt.Errorf("OpenClaw Gateway %s failed", method)
	}
	if err := json.Unmarshal(output, result); err != nil {
		return fmt.Errorf("OpenClaw Gateway %s returned an invalid response", method)
	}
	return nil
}

// gatewayFailure consumes the native CLI's --json failure envelope without
// publishing stderr, message bodies, endpoint details, or recovery metadata.
func gatewayFailure(output []byte) *acp.RequestError {
	var report struct {
		// OK is explicitly false for a native CLI failure.
		OK *bool `json:"ok"`
		// Error contains the protocol evidence for a failed Gateway command.
		Error struct {
			// Type distinguishes local credential errors, Gateway RPC errors and transport errors.
			Type string `json:"type"`
			// Code is a Gateway RPC error identifier or WebSocket close code.
			Code json.RawMessage `json:"code"`
			// Kind identifies a closed or timed-out transport.
			Kind string `json:"kind"`
			// Reason is the native close reason, compared only against fixed values.
			Reason string `json:"reason"`
			// TimeoutMS is the budget in a native timeout report.
			TimeoutMS int64 `json:"timeoutMs"`
			// Details includes structured Gateway connect error discriminants.
			Details struct {
				// Code is the native structured reason, when supplied.
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(output, &report) != nil || report.OK == nil || *report.OK {
		return nil
	}
	failure := report.Error
	switch failure.Type {
	case "gateway_credentials_required":
		return acp.NewAuthRequired(map[string]any{"classification": "authentication"})
	case "gateway_request_error":
		var code string
		if json.Unmarshal(failure.Code, &code) != nil {
			return nil
		}
		if code == "UNAVAILABLE" {
			return acp.NewInternalError(map[string]any{"classification": "runtime_unavailable"})
		}
		if code == "INVALID_REQUEST" {
			switch failure.Details.Code {
			case "AUTH_REQUIRED", "AUTH_UNAUTHORIZED", "AUTH_TOKEN_MISSING", "AUTH_TOKEN_MISMATCH", "AUTH_TOKEN_NOT_CONFIGURED",
				"AUTH_PASSWORD_MISSING", "AUTH_PASSWORD_MISMATCH", "AUTH_PASSWORD_NOT_CONFIGURED":
				return acp.NewAuthRequired(map[string]any{"classification": "authentication"})
			case "AUTH_RATE_LIMITED":
				return acp.NewInternalError(map[string]any{"classification": "rate_limit"})
			}
		}
	case "gateway_transport_error":
		if failure.Kind == "timeout" && failure.TimeoutMS > 0 {
			return acp.NewInternalError(map[string]any{"classification": "runtime_unavailable"})
		}
		if failure.Kind != "closed" {
			return nil
		}
		if len(failure.Code) == 0 && failure.Reason != "" {
			return acp.NewInternalError(map[string]any{"classification": "runtime_unavailable"})
		}
		var code int
		if json.Unmarshal(failure.Code, &code) != nil {
			return nil
		}
		switch code {
		case 1006, 1012:
			return acp.NewInternalError(map[string]any{"classification": "runtime_unavailable"})
		case 1008:
			// Shared-token/password denial can be downgraded by the native CLI
			// to a close receipt. Code 1008 alone means a policy refusal.
			switch failure.Reason {
			case "unauthorized: gateway token missing (use this gateway's gateway.auth.token or pair the device)",
				"unauthorized: gateway token mismatch (use this gateway's gateway.auth.token or pair the device)",
				"unauthorized: gateway token not configured on gateway (set gateway.auth.token)",
				"unauthorized: gateway password missing (use this gateway's gateway.auth.password)",
				"unauthorized: gateway password mismatch (use this gateway's gateway.auth.password)",
				"unauthorized: gateway password not configured on gateway (set gateway.auth.password)":
				return acp.NewAuthRequired(map[string]any{"classification": "authentication"})
			}
		}
	}
	return nil
}

func (g *gateway) run(ctx context.Context, env []string, args ...string) (output []byte, code int, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	spec := g.process
	spec.Args = append(slices.Clone(spec.Args), args...)
	spec.Env = env
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, 0, context.Canceled
	}
	process, err := agentclient.NewProcess(spec)
	if err != nil {
		g.mu.Unlock()
		return nil, 0, err
	}
	g.active[process] = struct{}{}
	g.mu.Unlock()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		err = errors.Join(err, g.release(cleanup, process))
	}()
	op, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	if err := process.Start(op); err != nil {
		return nil, 0, err
	}
	_ = process.Stdin().Close()
	stdout := process.Stdout()
	stopRead := context.AfterFunc(op, func() { _ = stdout.Close() })
	defer stopRead()
	const maxOutput = 8 << 20
	output, err = io.ReadAll(io.LimitReader(stdout, maxOutput+1))
	if op.Err() != nil {
		return nil, 0, op.Err()
	}
	if err != nil || len(output) > maxOutput {
		return nil, 0, errors.New("native CLI output unavailable")
	}
	exit, err := process.Wait(op)
	return output, exit.Code, err
}

func (g *gateway) release(ctx context.Context, process *agentclient.Process) error {
	if err := process.Close(ctx); err != nil {
		return err
	}
	g.mu.Lock()
	delete(g.active, process)
	g.mu.Unlock()
	return nil
}

func (g *gateway) close(ctx context.Context) error {
	g.mu.Lock()
	g.closed = true
	children := make([]*agentclient.Process, 0, len(g.active))
	for p := range g.active {
		children = append(children, p)
	}
	g.mu.Unlock()
	var err error
	for _, p := range children {
		err = errors.Join(err, g.release(ctx, p))
	}
	return err
}
