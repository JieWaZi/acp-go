package standardacp

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

const (
	kimiWireVersion       = "2.0.2"
	kimiPythonWireVersion = "1.52.0"
	kimiIndexLimit        = 16 << 20
	kimiJournalLimit      = 64 << 20
)

func kimiProfileHome(environment []string, cwd, variable, defaultName string) string {
	home := environmentValue(environment, variable)
	if home == "" {
		userHome := environmentValue(environment, "HOME")
		if userHome == "" {
			userHome = environmentValue(environment, "USERPROFILE")
		}
		if userHome == "" {
			return ""
		}
		home = filepath.Join(userHome, defaultName)
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(cwd, home)
	}
	return home
}

func captureKimiJournal(ctx context.Context, scope acpclient.PromptJournalScope, home, pythonHome string) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	// Discover native layouts from the selected session, independently of release labels.
	if capture := captureKimiJournalLayout(ctx, scope, home, false); capture != nil {
		return capture
	}
	return captureKimiJournalLayout(ctx, scope, pythonHome, true)
}

func captureKimiJournalLayout(ctx context.Context, scope acpclient.PromptJournalScope, home string, python bool) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	if home == "" || ctx.Err() != nil {
		return nil
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil
	}
	defer root.Close()
	var relative string
	if python {
		id := string(scope.SessionID)
		if !filepath.IsAbs(scope.Cwd) || id == "." || !filepath.IsLocal(id) || filepath.Base(id) != id {
			return nil
		}
		// WorkDirMeta.sessions_dir uses MD5 to name the exact native workspace.
		relative = filepath.Join("sessions", fmt.Sprintf("%x", md5.Sum([]byte(scope.Cwd))), id, "wire.jsonl")
		if info, err := root.Stat(filepath.Dir(relative)); err != nil || !info.IsDir() {
			return nil
		}
	} else {
		relative = kimiJournalPath(ctx, root, home, scope)
	}
	if relative == "" {
		return nil
	}
	file, err := openKimiJournalFile(root, relative)
	var baseline os.FileInfo
	var offset int64
	switch {
	case err == nil:
		defer file.Close()
		baseline, err = file.Stat()
		if err != nil || !baseline.Mode().IsRegular() {
			return nil
		}
		offset = baseline.Size()
		if offset > 0 {
			last := []byte{0}
			if _, err := file.ReadAt(last, offset-1); err != nil || last[0] != '\n' {
				return nil
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil
	}
	return func(ctx context.Context, receipt acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
		return settleKimiJournal(ctx, receipt.StopReason, home, relative, baseline, offset, python)
	}
}

func kimiJournalPath(ctx context.Context, root *os.Root, home string, scope acpclient.PromptJournalScope) string {
	file, err := openKimiJournalFile(root, "session_index.jsonl")
	if err != nil {
		return ""
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > kimiIndexLimit {
		return ""
	}
	scanner := bufio.NewScanner(io.LimitReader(file, kimiIndexLimit))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var result string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ""
		}
		var row struct {
			// ID identifies the indexed native session.
			ID string `json:"sessionId"`
			// Cwd binds the indexed session to its original workspace.
			Cwd string `json:"workDir"`
			// Directory is the absolute session path recorded by Kimi.
			Directory string `json:"sessionDir"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.ID != string(scope.SessionID) ||
			!filepath.IsAbs(row.Cwd) || filepath.Clean(row.Cwd) != filepath.Clean(scope.Cwd) || !filepath.IsAbs(row.Directory) {
			continue
		}
		relative, err := filepath.Rel(filepath.Join(home, "sessions"), row.Directory)
		if err != nil || relative == "." || !filepath.IsLocal(relative) {
			return ""
		}
		result = filepath.Join("sessions", relative, "agents", "main", "wire.jsonl")
	}
	if scanner.Err() != nil {
		return ""
	}
	return result
}

func settleKimiJournal(ctx context.Context, reason *acp.StopReason, home, relative string, baseline os.FileInfo, offset int64, python bool) acpclient.PromptJournalEvidence {
	root, err := os.OpenRoot(home)
	if err != nil {
		return acpclient.PromptJournalEvidence{}
	}
	defer root.Close()
	if reason == nil || *reason != acp.StopReasonEndTurn {
		evidence, _, _ := scanKimiJournal(ctx, root, relative, baseline, offset, python)
		if reason != nil && *reason == acp.StopReasonCancelled {
			evidence.Failure = nil
		}
		return evidence
	}
	// The ACP response may precede the asynchronous journal flush. A slash
	// command need not open a main-agent turn.
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var latest acpclient.PromptJournalEvidence
	for {
		evidence, started, terminal := scanKimiJournal(wait, root, relative, baseline, offset, python)
		if terminal {
			return evidence
		}
		if started {
			latest = evidence
		}
		select {
		case <-wait.Done():
			latest.UnverifiedTerminal = latest.NativeTurnID != ""
			return latest
		case <-ticker.C:
		}
	}
}

type kimiWireCounters struct {
	// Input excludes cache read and creation input.
	Input *int64 `json:"inputOther"`
	// Output reports the native generation total.
	Output *int64 `json:"output"`
	// CacheRead reports input served from cache.
	CacheRead *int64 `json:"inputCacheRead"`
	// CacheWrite reports input written to cache.
	CacheWrite *int64 `json:"inputCacheCreation"`
}

type kimiWireEvent struct {
	// Type is the exact native record discriminator.
	Type string `json:"type"`
	// Agent identifies the main agent for prompt/terminal records.
	Agent string `json:"agentId"`
	// Turn identifies the started or ended main agent turn.
	Turn *int64 `json:"turnId"`
	// Reason is the native terminal state.
	Reason string `json:"reason"`
	// Error is present for native provider failures.
	Error struct {
		// Code is the stable native error identifier.
		Code string `json:"code"`
		// Details carries structured status evidence.
		Details struct {
			// StatusCode is the optional provider HTTP status.
			StatusCode int `json:"statusCode"`
		} `json:"details"`
	} `json:"error"`
	// Scope identifies usage for the current prompt.
	Scope string `json:"usageScope"`
	// Model is the model that actually performed the recorded request.
	Model string `json:"model"`
	// Usage contains explicit mutually exclusive native token counts.
	Usage *kimiWireCounters `json:"usage"`
}

func scanKimiJournal(ctx context.Context, root *os.Root, relative string, baseline os.FileInfo, offset int64, python bool) (acpclient.PromptJournalEvidence, bool, bool) {
	var evidence acpclient.PromptJournalEvidence
	file, err := openKimiJournalFile(root, relative)
	if err != nil {
		return evidence, false, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || baseline != nil && !os.SameFile(baseline, info) ||
		info.Size() < offset || info.Size()-offset > kimiJournalLimit {
		return evidence, false, false
	}
	scanner := bufio.NewScanner(io.NewSectionReader(file, offset, info.Size()-offset))
	scanner.Buffer(make([]byte, 4096), 8<<20)
	var turn *int64
	var calls int
	source := "kimi.wire.usage"
	if python {
		source = "kimi.python.wire.status"
	}
	for scanner.Scan() && ctx.Err() == nil {
		event, valid := decodeKimiWireEvent(scanner.Bytes(), python)
		if !valid {
			break
		}
		switch {
		case event.Type == "turn.prompt" && event.Agent == "main" && event.Turn != nil:
			if turn != nil {
				// The next main prompt would make the offset ambiguous.
				return acpclient.PromptJournalEvidence{}, false, false
			}
			turn = event.Turn
			if !python {
				evidence.NativeTurnID = strconv.FormatInt(*turn, 10)
			}
		case turn == nil || event.Turn != nil && *event.Turn != *turn:
		case event.Type == "usage.record" && event.Agent == "main" && event.Scope == "turn" && event.Usage != nil:
			if evidence.Usage == nil {
				evidence.Usage = &acpmeta.TurnUsage{Source: source, Coverage: "observed_requests", Model: event.Model}
			} else if evidence.Usage.Model != event.Model {
				evidence.Usage.Model = ""
			}
			addKimiUsage(evidence.Usage, *event.Usage, calls == 0)
			calls++
		case event.Type == "turn.ended" && event.Agent == "main" && event.Turn != nil:
			if event.Reason == "failed" {
				evidence.Failure = kimiWireFailure(event.Error.Code, event.Error.Details.StatusCode)
			}
			return evidence, true, true
		}
	}
	return evidence, turn != nil, false
}

func decodeKimiWireEvent(raw []byte, python bool) (kimiWireEvent, bool) {
	var event kimiWireEvent
	if !python {
		err := json.Unmarshal(raw, &event)
		return event, err == nil
	}
	var record struct {
		// Message is the native WireMessage envelope.
		Message struct {
			// Type identifies a durable start, step usage, or end.
			Type string `json:"type"`
			// Payload retains counters supplied by a completed native request.
			Payload struct {
				// Usage contains explicitly reported independent token classes.
				Usage *struct {
					// Input excludes cache reads and creation.
					Input *int64 `json:"input_other"`
					// Output is native output for this request.
					Output *int64 `json:"output"`
					// CacheRead counts cache hit input.
					CacheRead *int64 `json:"input_cache_read"`
					// CacheWrite counts cache creation input.
					CacheWrite *int64 `json:"input_cache_creation"`
				} `json:"token_usage"`
			} `json:"payload"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return event, false
	}
	// Python has no native turn ID. The sentinel is private to the one
	// append range and is never projected as a durable native identity.
	event.Agent, event.Turn = "main", acp.Ptr(int64(-1))
	switch record.Message.Type {
	case "TurnBegin":
		event.Type = "turn.prompt"
	case "StatusUpdate":
		if usage := record.Message.Payload.Usage; usage != nil {
			event.Type, event.Scope = "usage.record", "turn"
			event.Usage = &kimiWireCounters{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite}
		}
	case "TurnEnd":
		event.Type = "turn.ended"
	}
	return event, true
}

func openKimiJournalFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("Kimi journal entry is not a regular file")
	}
	return root.Open(name)
}

func addKimiUsage(report *acpmeta.TurnUsage, value kimiWireCounters, first bool) {
	targets := []**int64{&report.InputTokens, &report.OutputTokens, &report.CacheReadTokens, &report.CacheWriteTokens}
	for i, count := range []*int64{value.Input, value.Output, value.CacheRead, value.CacheWrite} {
		target := targets[i]
		switch {
		case count == nil || *count < 0:
			*target = nil
		case first:
			*target = count
		case *target == nil:
		case **target > math.MaxInt64-*count:
			*target = nil
		default:
			*target = acp.Ptr(**target + *count)
		}
	}
}

func kimiWireFailure(code string, status int) *acp.RequestError {
	kind := ""
	switch code {
	case "provider.rate_limit":
		kind = "rate_limit"
	case "provider.auth_error":
		kind = "authentication_failed"
	case "provider.overloaded":
		kind = "overloaded"
	case "provider.connection_error":
		kind = "network_error"
	case "provider.api_error":
		switch {
		case status == 401:
			kind = "authentication_failed"
		case status == 429:
			kind = "rate_limit"
		case status == 408 || status == 504:
			kind = "timeout"
		case status >= 500 && status <= 599:
			kind = "server_error"
		}
	}
	data := map[string]any{"method": acp.AgentMethodSessionPrompt}
	if kind != "" {
		data["errorKind"] = kind
	}
	if kind == "authentication_failed" {
		return acp.NewAuthRequired(data)
	}
	return &acp.RequestError{Code: -32603, Message: "Kimi provider request failed", Data: data}
}

func environmentValue(environment []string, key string) string {
	for i := len(environment) - 1; i >= 0; i-- {
		if name, value, ok := strings.Cut(environment[i], "="); ok && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value
		}
	}
	return ""
}
