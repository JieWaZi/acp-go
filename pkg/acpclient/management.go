package acpclient

import (
	"context"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// Executable returns the resolved launcher shared by this client's native operations.
func (c *Client) Executable() string { return c.proc.Executable() }

// VersionProbe reads bounded version evidence from the selected executable.
type VersionProbe struct {
	// Args is the complete argument list for the one-shot native command.
	Args []string
	// Required rejects startup when the executable does not prove its version.
	Required bool
	// Parse handles the provider's version layout, including a native manifest fallback.
	Parse func(executable string, stdout []byte) string
}

var semanticVersion = regexp.MustCompile(`\b[vV]?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.]+)?\b`)

func (c *Client) probeVersion(ctx context.Context) (version string, err error) {
	if c.probe == nil {
		return "", nil
	}
	options := c.opts.Version
	op, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if cleanupErr := c.probe.Close(cleanup); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	if err = c.probe.Start(op); err == nil {
		_ = c.probe.Stdin().Close()
		stop := context.AfterFunc(op, func() { _ = c.probe.Stdout().Close() })
		defer stop()
		var stdout []byte
		stdout, err = io.ReadAll(io.LimitReader(c.probe.Stdout(), 4097))
		if err == nil && len(stdout) <= 4096 {
			exit, waitErr := c.probe.Wait(op)
			if waitErr != nil || exit.Code != 0 || exit.Err != nil {
				stdout = nil
			}
		} else {
			stdout = nil
		}
		if options.Parse != nil {
			version = options.Parse(c.proc.Executable(), stdout)
		} else {
			version = strings.TrimLeft(semanticVersion.FindString(string(stdout)), "vV")
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if options.Required && version == "" {
		return "", errors.New("selected ACP executable did not report its expected version")
	}
	return version, nil
}

func (c *Client) Authenticate(ctx context.Context, request acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.AuthenticateResponse{}, err
	}
	defer c.end()
	auth := c.opts.Protocol.Authentication
	if auth.Disabled || auth.Method != "" && request.MethodId != auth.Method {
		return acp.AuthenticateResponse{}, invalid("native authentication is unavailable for this selection")
	}
	advertised := slices.ContainsFunc(c.initialized.AuthMethods, func(method acp.AuthMethod) bool {
		return method.Agent != nil && method.Agent.Id == request.MethodId
	})
	if !advertised {
		return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
	}
	op, cancel := c.operationContext(ctx, 5*time.Minute)
	defer cancel()
	r := send[acp.AuthenticateResponse](conn, op, acp.AgentMethodAuthenticate, request)
	return r.Result, receiptFailure(r, acp.AgentMethodAuthenticate)
}

func (c *Client) Logout(ctx context.Context, request acp.LogoutRequest) (acp.LogoutResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.LogoutResponse{}, err
	}
	defer c.end()
	auth := c.opts.Protocol.Authentication
	if auth.Disabled {
		return acp.LogoutResponse{}, invalid("selected credential does not support logout")
	}
	if auth.RetireOnLogout {
		defer c.invalidate()
	}
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	if c.initialized.AgentCapabilities.Auth.Logout == nil {
		if auth.LogoutFallback != nil {
			return acp.LogoutResponse{}, auth.LogoutFallback(op)
		}
		return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
	}
	r := send[acp.LogoutResponse](conn, op, acp.AgentMethodLogout, request)
	return r.Result, receiptFailure(r, acp.AgentMethodLogout)
}

// RetireAuthentication fences an independent CLI login before it can change
// cached credentials. The caller owns that management command and its cleanup.
func (c *Client) RetireAuthentication(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed || c.unusable {
		return ErrRestartRequired
	}
	if c.busy {
		return ErrBusy
	}
	c.unusable = true
	return nil
}

func (c *Client) invalidate() {
	c.mu.Lock()
	c.unusable = true
	c.mu.Unlock()
}
