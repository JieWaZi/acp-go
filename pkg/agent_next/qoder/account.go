package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
)

// LoginStatus is the installed CLI's local status, which may lack account identity.
type LoginStatus struct {
	// Authenticated is the explicit logged_in field.
	Authenticated bool `json:"logged_in"`
	// AuthSource describes the selected CLI's reported credential source.
	AuthSource string `json:"auth_source"`
	// AccountType identifies the native account class.
	AccountType string `json:"account_type"`
	// LoginMethod identifies the native sign-in method.
	LoginMethod string `json:"login_method"`
	// Username is the optional local display label.
	Username string `json:"username"`
	// Email is the optional local display label.
	Email string `json:"email"`
	// UserType is the optional subscription label.
	UserType string `json:"user_type"`
}

// NativeIdentity is account evidence from the selected Qoder SDK control channel.
type NativeIdentity struct {
	// UserID is the provider's opaque account identity, when reported.
	UserID string `json:"userId"`
	// Name is the optional display name.
	Name string `json:"name"`
	// Email is the optional provider label.
	Email string `json:"email"`
	// Plan is the native subscription type, when reported.
	Plan string `json:"subscriptionType"`
	// TokenSource is the native source label, never the token itself.
	TokenSource string `json:"tokenSource"`
	// Organization is the optional provider organization identifier.
	Organization string `json:"organization"`
}

// ReadLoginStatus reads the selected CLI without starting an ACP process.
func ReadLoginStatus(ctx context.Context, options Options) (LoginStatus, error) {
	options, err := freezeOptions(options)
	if err != nil {
		return LoginStatus{}, err
	}
	owner := management{options: options}
	return owner.readLoginStatus(ctx)
}

// ReadNativeIdentity reads the native account without opening an ACP session.
func ReadNativeIdentity(ctx context.Context, options Options) (NativeIdentity, error) {
	options, err := freezeOptions(options)
	if err != nil {
		return NativeIdentity{}, err
	}
	owner := management{options: options}
	return owner.readNativeIdentity(ctx)
}

// ReadLoginStatus reads the frozen CLI's local login status.
func (c *Client) ReadLoginStatus(ctx context.Context) (LoginStatus, error) {
	return c.management.readLoginStatus(ctx)
}

// ReadNativeIdentity reads the account selected by the frozen CLI launch.
func (c *Client) ReadNativeIdentity(ctx context.Context) (NativeIdentity, error) {
	return c.management.readNativeIdentity(ctx)
}

func (m *management) readLoginStatus(ctx context.Context) (LoginStatus, error) {
	var status LoginStatus
	// A leading flag routes regional command dispatchers to the CLI even if
	// the work directory contains a file named status.
	err := m.run(ctx, []string{"--cwd", ".", "status", "-o", "json"}, func(ctx context.Context, process *agentclient.Process) error {
		_ = process.Stdin().Close()
		const limit = 64 << 10
		raw, err := io.ReadAll(io.LimitReader(process.Stdout(), limit+1))
		if err != nil || len(raw) > limit {
			return errors.New("Qoder CLI account status output failed")
		}
		exit, err := process.Wait(ctx)
		if err != nil || exit.Code != 0 || exit.Err != nil {
			return errors.New("Qoder CLI account status command failed")
		}
		var response struct {
			LoginStatus
			// LoggedIn preserves explicit signed-out evidence.
			LoggedIn *bool `json:"logged_in"`
		}
		if json.Unmarshal(raw, &response) != nil || response.LoggedIn == nil {
			return errors.New("Qoder CLI account status is invalid")
		}
		if *response.LoggedIn {
			status = response.LoginStatus
			status.Authenticated = true
		}
		return nil
	})
	return status, err
}

func (m *management) readNativeIdentity(ctx context.Context) (NativeIdentity, error) {
	var identity NativeIdentity
	err := m.control(ctx, func(channel *controlChannel, bootstrap controlAccount) error {
		account, err := readControlIdentity(channel, bootstrap)
		if err != nil {
			return err
		}
		if err := m.verifyIdentity(account.UserID); err != nil {
			return err
		}
		identity = account
		return nil
	})
	return identity, err
}

func (m *management) verifyIdentity(id string) error {
	if m.options.ExpectedUserID != "" && m.options.ExpectedUserID != id {
		return errors.New("Qoder CLI account does not match the selected subject")
	}
	return nil
}

// ReadAccount reports verified native identity or local sign-in status.
func (c *Client) ReadAccount(ctx context.Context) (acpmeta.AccountInfo, error) {
	identity, err := c.ReadNativeIdentity(ctx)
	if err == nil && identity.UserID != "" {
		kind := "default_login"
		if selectedToken(c.management.options) != "" {
			kind = "personal_access_token"
		}
		return accountInfo(kind, identity.Email, identity.Name, identity.Plan), nil
	}
	if ctx.Err() != nil {
		return acpmeta.AccountInfo{}, ctx.Err()
	}
	if errors.Is(err, acpclient.ErrBusy) || errors.Is(err, acpclient.ErrRestartRequired) {
		return acpmeta.AccountInfo{}, err
	}
	if selectedToken(c.management.options) != "" || c.management.options.ExpectedUserID != "" {
		// Local status could belong to a different credential or have no subject.
		if err == nil {
			err = errors.New("Qoder CLI did not confirm the selected account")
		}
		return acpmeta.AccountInfo{}, err
	}
	status, err := c.ReadLoginStatus(ctx)
	if err != nil {
		return acpmeta.AccountInfo{}, err
	}
	if !status.Authenticated {
		return acpmeta.AccountInfo{RequiresAuthentication: true}, nil
	}
	return accountInfo("default_login", status.Email, status.Username, status.UserType), nil
}

func accountInfo(kind, email, name, plan string) acpmeta.AccountInfo {
	account := &acpmeta.AccountIdentity{Kind: kind}
	if label := strings.TrimSpace(email); label != "" {
		account.Label = &label
	} else if label := strings.TrimSpace(name); label != "" {
		account.Label = &label
	}
	if plan = strings.TrimSpace(plan); plan != "" {
		account.Plan = &plan
	}
	return acpmeta.AccountInfo{Account: account}
}

// HandleExtensionMethod projects the same account reads on public ACP.
func (c *Client) HandleExtensionMethod(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case acpmeta.AccountReadMethod:
		return c.ReadAccount(ctx)
	case acpmeta.AccountUsageMethod:
		return c.ReadAccountUsage(ctx)
	default:
		return c.Client.HandleExtensionMethod(ctx, method, raw)
	}
}

func readControlIdentity(channel *controlChannel, bootstrap controlAccount) (NativeIdentity, error) {
	// Initialize can establish identity on older SDK runtimes as well.
	if bootstrap.Account != nil && bootstrap.Account.UserID != "" {
		return *bootstrap.Account, nil
	}
	var current controlAccount
	if err := channel.request("account_info", &current); err != nil {
		return NativeIdentity{}, err
	}
	if current.Account == nil {
		return NativeIdentity{}, errors.New("Qoder CLI account response is missing")
	}
	return *current.Account, nil
}
