package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

const (
	cursorUsageFileLimit    = 16 << 10
	cursorUsageFileCount    = 128
	cursorUsageCounterLimit = 1<<53 - 1
)

var cursorUsageUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validCursorUsageID(value string) bool { return cursorUsageUUID.MatchString(value) }

// cursorUsageEvent is the allowlisted usage record emitted for one native run by the private ACP process.
type cursorUsageEvent struct {
	// Schema identifies the verified event format.
	Schema int `json:"schema"`
	// Source identifies native TurnEnded counters.
	Source string `json:"source"`
	// Version is the native release whose artifact passed verification.
	Version string `json:"version"`
	// Nonce binds the record to the reserved host prompt.
	Nonce string `json:"nonce"`
	// Session is the native ACP session identity.
	Session string `json:"session_id"`
	// Request is the turn identity recorded by the native session's authoritative root.
	Request string `json:"request_id"`
	// User identifies the input carried by native userMessageAction.
	User string `json:"user_message_id"`
	// Run is a random identity for one native execution within the prompt.
	Run string `json:"run_id"`
	// Index orders native executions within the same prompt.
	Index int64 `json:"run_index"`
	// Model identifies the model actually passed to this native run, or remains unknown when absent.
	Model string `json:"model"`
	// Input is the native input total including cache reads and writes.
	Input *int64 `json:"input_tokens"`
	// Output is the native output counter.
	Output *int64 `json:"output_tokens"`
	// CacheRead is the native cache-read counter.
	CacheRead *int64 `json:"cache_read_tokens"`
	// CacheWrite is the native cache-write counter.
	CacheWrite *int64 `json:"cache_write_tokens"`
}

// captureCursorUsageJournal freezes native identity before submission and settles only usage proven to belong to that turn.
func captureCursorUsageJournal(ctx context.Context, scope acpclient.PromptJournalScope, configRoot, directory, nonce string) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	// When official ACP omits agentInfo, the shared layer supplies the independently probed launcher version.
	if scope.Version == "" {
		scope.Version = scope.LauncherVersion
	}
	if ctx.Err() != nil || scope.Version == "" || !validCursorUsageID(string(scope.SessionID)) || !validCursorUsageID(nonce) || !filepath.IsAbs(directory) {
		return nil
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil
	}
	defer root.Close()
	if !cursorUsageHealth(root, scope.Version) {
		return nil
	}
	names, err := cursorUsageNames(root)
	if err != nil {
		return nil
	}
	before, err := readCursorACPUsageIdentity(ctx, configRoot, scope.SessionID)
	if err != nil {
		return nil
	}
	baseline := make(map[string]bool, len(names))
	for _, name := range names {
		baseline[name] = true
	}
	return func(ctx context.Context, receipt acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
		if ctx.Err() != nil || !cursorUsageMatchedReceipt(receipt.StopReason) {
			return acpclient.PromptJournalEvidence{}
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			return acpclient.PromptJournalEvidence{}
		}
		defer root.Close()
		if !cursorUsageHealth(root, scope.Version) {
			return acpclient.PromptJournalEvidence{}
		}
		events := readCursorUsageEvents(ctx, root, scope, nonce, baseline)
		if len(events) == 0 {
			return acpclient.PromptJournalEvidence{}
		}
		after, err := readCursorACPUsageIdentity(ctx, configRoot, scope.SessionID)
		if err != nil || !validCursorUsageID(after.requestID) || !validCursorUsageID(after.userID) ||
			after.requestID == before.requestID || after.userID == before.userID {
			return acpclient.PromptJournalEvidence{}
		}
		usage := sumCursorUsageEvents(events, after)
		if usage == nil {
			return acpclient.PromptJournalEvidence{}
		}
		return acpclient.PromptJournalEvidence{NativeTurnID: after.requestID, Usage: usage}
	}
}

func cursorUsageMatchedReceipt(reason *acp.StopReason) bool {
	if reason == nil {
		return false
	}
	switch *reason {
	case acp.StopReasonEndTurn, acp.StopReasonMaxTokens, acp.StopReasonMaxTurnRequests, acp.StopReasonRefusal, acp.StopReasonCancelled:
		return true
	default:
		return false
	}
}

func cursorUsageHealth(root *os.Root, version string) bool {
	data, err := readCursorUsageFile(root, "health.json")
	if err != nil {
		return false
	}
	var health struct {
		// Schema identifies the artifact-verification result format.
		Schema int `json:"schema"`
		// Status is ready only after artifact verification and usage instrumentation succeed.
		Status string `json:"status"`
		// Version must match the version confirmed for this ACP connection.
		Version string `json:"version"`
	}
	return json.Unmarshal(data, &health) == nil && health.Schema == 1 && health.Status == "ready" && health.Version == version
}

func cursorUsageNames(root *os.Root) ([]string, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(cursorUsageFileCount + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > cursorUsageFileCount {
		return nil, errors.New("Cursor usage journal exceeds event limit")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func readCursorUsageFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > cursorUsageFileLimit {
		return nil, errors.New("invalid Cursor usage journal file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return nil, errors.New("Cursor usage journal changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, cursorUsageFileLimit+1))
	if err != nil || len(data) > cursorUsageFileLimit {
		return nil, errors.New("Cursor usage journal exceeds record limit")
	}
	return data, nil
}

func readCursorUsageEvents(ctx context.Context, root *os.Root, scope acpclient.PromptJournalScope, nonce string, baseline map[string]bool) []cursorUsageEvent {
	names, err := cursorUsageNames(root)
	if err != nil {
		return nil
	}
	events := []cursorUsageEvent{}
	for _, name := range names {
		if ctx.Err() != nil {
			return nil
		}
		if baseline[name] || !strings.HasPrefix(name, nonce+"-") {
			continue
		}
		data, err := readCursorUsageFile(root, name)
		if err != nil {
			return nil
		}
		var event cursorUsageEvent
		if json.Unmarshal(data, &event) != nil {
			return nil
		}
		if event.Schema != 1 || event.Source != "cursor.acp.turn_ended" || event.Version != scope.Version || event.Nonce != nonce || event.Session != string(scope.SessionID) {
			continue
		}
		if !validCursorUsageID(event.Request) || !validCursorUsageID(event.User) || !validCursorUsageID(event.Run) || event.Index < 1 || event.Index > cursorUsageCounterLimit {
			return nil
		}
		events = append(events, event)
	}
	return events
}

func sumCursorUsageEvents(events []cursorUsageEvent, identity cursorACPUsageIdentity) *acpmeta.TurnUsage {
	seen := map[string][]byte{}
	indices := map[int64]string{}
	usage := &acpmeta.TurnUsage{Source: "cursor.acp.turn_ended", Coverage: "observed_requests"}
	var rawInput *int64
	count := 0
	for _, event := range events {
		if event.Request != identity.requestID || event.User != identity.userID {
			continue
		}
		key := event.Request + "\x00" + event.Run
		normalized, _ := json.Marshal(event)
		if previous, exists := seen[key]; exists {
			if !bytes.Equal(previous, normalized) {
				return nil
			}
			continue
		}
		if previous, exists := indices[event.Index]; exists && previous != event.Run {
			return nil
		}
		seen[key], indices[event.Index] = normalized, event.Run
		if count == 0 && len(event.Model) <= 200 && strings.TrimSpace(event.Model) == event.Model {
			usage.Model = event.Model
		} else if usage.Model != event.Model {
			// Missing or mixed model identities cannot attribute the entire turn to one model.
			usage.Model = ""
		}
		for _, value := range []*int64{event.Input, event.Output, event.CacheRead, event.CacheWrite} {
			if value != nil && (*value < 0 || *value > cursorUsageCounterLimit) {
				return nil
			}
		}
		input := event.Input
		if input != nil && (event.CacheRead != nil && *event.CacheRead > *input || event.CacheWrite != nil && *event.CacheWrite > *input) {
			return nil
		}
		if input != nil && event.CacheRead != nil && event.CacheWrite != nil {
			if *event.CacheWrite > *input-*event.CacheRead {
				return nil
			}
			input = acp.Ptr(*input - *event.CacheRead - *event.CacheWrite)
		} else {
			input = nil
		}
		pairs := []struct {
			// total points to the currently known subtotal.
			total **int64
			// value is the component reported by this native execution.
			value *int64
		}{
			{&usage.InputTokens, input}, {&usage.OutputTokens, event.Output},
			{&usage.CacheReadTokens, event.CacheRead}, {&usage.CacheWriteTokens, event.CacheWrite},
			{&rawInput, event.Input},
		}
		for _, pair := range pairs {
			if !addCursorUsageCounter(pair.total, pair.value, count == 0) {
				return nil
			}
		}
		count++
	}
	if count == 0 || usage.InputTokens == nil && usage.OutputTokens == nil && usage.CacheReadTokens == nil && usage.CacheWriteTokens == nil && rawInput == nil {
		return nil
	}
	if usage.InputTokens == nil && rawInput != nil {
		usage.Observations = []acpmeta.UsageObservation{{Source: usage.Source, Coverage: "observed_requests_raw", InputTotalTokens: rawInput}}
	}
	return usage
}

func addCursorUsageCounter(total **int64, value *int64, first bool) bool {
	if value == nil || !first && *total == nil {
		*total = nil
		return true
	}
	if first {
		*total = acp.Ptr(*value)
		return true
	}
	if *value > cursorUsageCounterLimit-**total {
		return false
	}
	**total += *value
	return true
}
