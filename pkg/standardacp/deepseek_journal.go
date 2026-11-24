package standardacp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/klauspost/compress/zstd"
)

const (
	deepSeekHeaderLimit      = 64 << 10
	deepSeekJournalLimit     = 16 << 20
	deepSeekBaselineTail     = 4096
	deepSeekJournalBatchWait = 275 * time.Millisecond
)

type deepSeekJournalBaseline struct {
	// Path is the one v4 artifact within the frozen DSH home.
	path string
	// File identifies the same inode and its offset at prompt submission.
	file os.FileInfo
	// Tail checks that the pre-prompt end of the file was preserved.
	tail []byte
}

func captureDeepSeekJournal(ctx context.Context, scope acpclient.PromptJournalScope, home string) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	if ctx.Err() != nil || !filepath.IsAbs(home) || !filepath.IsAbs(scope.Cwd) || len(scope.SessionID) == 0 || len(scope.SessionID) > 250 {
		return nil
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil
	}
	defer root.Close()
	physical, err := filepath.EvalSymlinks(scope.Cwd)
	if err != nil {
		return nil
	}
	paths := []string{scope.Cwd, physical}
	// Darwin's native realpath can omit the /private spelling retained by Go.
	if runtime.GOOS == "darwin" {
		for _, path := range []string{scope.Cwd, physical} {
			if path != "/private" && strings.HasPrefix(path, "/private/") {
				paths = append(paths, strings.TrimPrefix(path, "/private"))
			}
		}
	}
	var baseline *deepSeekJournalBaseline
	visited := make(map[string]bool, len(paths))
	for _, path := range paths {
		directory := deepSeekSessionDirectory(path, string(scope.SessionID))
		if visited[directory] {
			continue
		}
		visited[directory] = true
		for _, name := range []string{"session.v4.jsonl.zstd", "session.v4.jsonl"} {
			if candidate := readDeepSeekBaseline(root, filepath.Join(directory, name), scope); candidate != nil {
				if baseline != nil {
					return nil // The transport did not identify which artifact it owns.
				}
				baseline = candidate
			}
		}
	}
	if baseline == nil {
		return nil
	}
	return func(ctx context.Context, receipt acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
		var empty acpclient.PromptJournalEvidence
		if ctx.Err() != nil || receipt.StopReason == nil {
			return empty
		}
		evidence, settled := baseline.read(ctx, home, scope, receipt)
		if settled {
			return evidence
		}
		// ACP settles after committed events, which the shipped persistence backend
		// writes in a 200 ms batch. Give a pending terminal batch one bounded read.
		timer := time.NewTimer(deepSeekJournalBatchWait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return empty
		case <-timer.C:
			evidence, _ := baseline.read(ctx, home, scope, receipt)
			return evidence
		}
	}
}

func deepSeekSessionDirectory(cwd, id string) string {
	project := strings.TrimLeft(deepSeekPathSegment(cwd, true), "-")
	if project == "" {
		project = "root"
	}
	project = project[:min(len(project), 251)]
	if id == "." || id == ".." {
		id = strings.ReplaceAll(id, ".", "~002E")
	} else {
		id = deepSeekPathSegment(id, false)
	}
	return filepath.Join("sessions", "--"+project+"--", id)
}

func deepSeekPathSegment(value string, project bool) string {
	var escaped strings.Builder
	separator := false
	for _, unit := range utf16.Encode([]rune(value)) {
		if project && (unit == '/' || unit == '\\' || unit == ':') {
			if !separator {
				escaped.WriteByte('-')
			}
			separator = true
			continue
		}
		separator = false
		if unit >= 'a' && unit <= 'z' || unit >= 'A' && unit <= 'Z' || unit >= '0' && unit <= '9' || unit == '.' || unit == '_' || unit == '-' {
			escaped.WriteByte(byte(unit))
		} else {
			fmt.Fprintf(&escaped, "~%04X", unit)
		}
	}
	return escaped.String()
}

func readDeepSeekBaseline(root *os.Root, path string, scope acpclient.PromptJournalScope) *deepSeekJournalBaseline {
	file, info := openDeepSeekJournal(root, path)
	if file == nil {
		return nil
	}
	defer file.Close()
	if !verifyDeepSeekHeader(file, path, info.Size(), scope) {
		return nil
	}
	tail := make([]byte, min(info.Size(), deepSeekBaselineTail))
	if _, err := file.ReadAt(tail, info.Size()-int64(len(tail))); err != nil {
		return nil
	}
	if !strings.HasSuffix(path, ".zstd") && tail[len(tail)-1] != '\n' {
		return nil
	}
	return &deepSeekJournalBaseline{path: path, file: info, tail: tail}
}

func openDeepSeekJournal(root *os.Root, path string) (*os.File, os.FileInfo) {
	if info, err := root.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return nil, nil
	}
	file, err := root.OpenFile(path, os.O_RDONLY|journalReadOpenFlags, 0)
	if err != nil {
		return nil, nil
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		file.Close()
		return nil, nil
	}
	return file, info
}

func verifyDeepSeekHeader(file *os.File, path string, size int64, scope acpclient.PromptJournalScope) bool {
	reader, closeReader := deepSeekJournalReader(file, path, 0, min(size, deepSeekHeaderLimit))
	if reader == nil {
		return false
	}
	defer closeReader()
	line, err := bufio.NewReaderSize(reader, deepSeekHeaderLimit).ReadSlice('\n')
	if err != nil {
		return false
	}
	var header struct {
		// Type distinguishes the session header from an ordinary event.
		Type string `json:"type"`
		// Version selects the released physical session log schema.
		Version int `json:"version"`
		// ID is the native persisted session identity.
		ID string `json:"id"`
		// Cwd is the native physical workspace for this session.
		Cwd string `json:"cwd"`
		// Origin excludes logs belonging to managed descendants.
		Origin string `json:"origin"`
	}
	if json.Unmarshal(line, &header) != nil || header.Type != "session" || header.Version != 4 ||
		header.ID != string(scope.SessionID) || header.Origin != "" || !filepath.IsAbs(header.Cwd) {
		return false
	}
	// The project key is intentionally lossy. Verify the header's physical
	// directory in addition to matching the selected session ID and artifact.
	return sameDeepSeekDirectory(scope.Cwd, header.Cwd)
}

func deepSeekJournalReader(file *os.File, path string, offset, size int64) (io.Reader, func()) {
	section := io.NewSectionReader(file, offset, size)
	if !strings.HasSuffix(path, ".zstd") {
		return section, func() {}
	}
	reader, err := zstd.NewReader(section, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(deepSeekJournalLimit))
	if err != nil {
		return nil, nil
	}
	return reader, reader.Close
}

func (baseline deepSeekJournalBaseline) read(ctx context.Context, home string, scope acpclient.PromptJournalScope, receipt acpclient.PromptJournalReceipt) (acpclient.PromptJournalEvidence, bool) {
	var empty acpclient.PromptJournalEvidence
	if ctx.Err() != nil {
		return empty, true
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return empty, true
	}
	defer root.Close()
	file, info := openDeepSeekJournal(root, baseline.path)
	if file == nil {
		return empty, true
	}
	defer file.Close()
	offset := baseline.file.Size()
	if !os.SameFile(baseline.file, info) || info.Size() < offset || info.Size()-offset > deepSeekJournalLimit ||
		!verifyDeepSeekHeader(file, baseline.path, info.Size(), scope) {
		return empty, true
	}
	previous := make([]byte, len(baseline.tail))
	if _, err := file.ReadAt(previous, offset-int64(len(previous))); err != nil || !bytes.Equal(previous, baseline.tail) {
		return empty, true
	}
	if info.Size() == offset {
		return empty, false
	}
	reader, closeReader := deepSeekJournalReader(file, baseline.path, offset, info.Size()-offset)
	if reader == nil {
		return empty, true
	}
	defer closeReader()
	suffix, err := io.ReadAll(io.LimitReader(reader, deepSeekJournalLimit+1))
	if err != nil || len(suffix) == 0 || suffix[len(suffix)-1] != '\n' {
		return empty, false // A read may have caught a partially written frame.
	}
	if len(suffix) > deepSeekJournalLimit {
		return empty, true
	}
	return readDeepSeekTurn(ctx, suffix, receipt)
}

type deepSeekJournalEvent struct {
	// Seq is the persisted event position within this session.
	Seq *int64 `json:"seq"`
	// Type selects the event envelope.
	Type string `json:"type"`
	// Data preserves the different schemas behind each native event type.
	Data json.RawMessage `json:"data"`
}

type deepSeekUsageEventData struct {
	// Turn names the native loop turn.
	Turn int64 `json:"turn"`
	// Step names the native model step within the turn.
	Step int64 `json:"step"`
	// ID identifies a directly admitted message.
	ID string `json:"id"`
	// Role identifies the outer user/message record.
	Role string `json:"role"`
	// Source distinguishes user input from other producers.
	Source deepSeekJournalSource `json:"source"`
	// Reason carries the persisted native terminal.
	Reason struct {
		// Kind maps to the closest ACP stop reason.
		Kind string `json:"kind"`
	} `json:"reason"`
	// Message identifies the committed model-produced assistant message.
	Message struct {
		// ID identifies one assistant result.
		ID string `json:"id"`
		// Source records the exact native model route used.
		Source deepSeekJournalSource `json:"source"`
	} `json:"message"`
	// Usage contains authoritative counters for this one committed request.
	Usage deepSeekJournalUsage `json:"usage"`
}

type deepSeekJournalSource struct {
	// Kind identifies the event producer.
	Kind string `json:"kind"`
	// Model identifies the model used by a committed assistant request.
	Model string `json:"model"`
}

type deepSeekJournalUsage struct {
	// Input counts uncached input tokens.
	Input *int64 `json:"inputTokens"`
	// Output includes any separately reported reasoning count.
	Output *int64 `json:"outputTokens"`
	// Read counts input served from cache.
	Read *int64 `json:"cacheReadTokens"`
	// Write counts input written to cache.
	Write *int64 `json:"cacheWriteTokens"`
	// Reasoning is an optional subset of output.
	Reasoning *int64 `json:"reasoningTokens"`
	// Total checks the disjoint counters when all are present.
	Total *int64 `json:"totalTokens"`
}

func readDeepSeekTurn(ctx context.Context, suffix []byte, receipt acpclient.PromptJournalReceipt) (acpclient.PromptJournalEvidence, bool) {
	var empty acpclient.PromptJournalEvidence
	var sequence, turn, lastStep int64 = -1, 0, 0
	var userID string
	var ended bool
	var forkPosition string
	var totals acpmeta.ModelUsage
	models := make(map[string]acpmeta.ModelUsage)
	for len(suffix) > 0 {
		if ctx.Err() != nil {
			return empty, true
		}
		line, rest, _ := bytes.Cut(suffix, []byte{'\n'})
		suffix = rest
		var event deepSeekJournalEvent
		if json.Unmarshal(line, &event) != nil || !exactCounter(event.Seq) || event.Type == "" ||
			sequence >= 0 && sequence+1 != *event.Seq {
			return empty, true
		}
		sequence = *event.Seq
		var data deepSeekUsageEventData
		switch event.Type {
		case "turn/start", "user/message", "assistant/message", "turn/end":
			if json.Unmarshal(event.Data, &data) != nil {
				return empty, true
			}
		default:
			continue
		}
		switch event.Type {
		case "turn/start":
			if turn != 0 || data.Turn <= 0 || data.Turn > maxExactMetadataCounter {
				return empty, true
			}
			turn = data.Turn
		case "user/message":
			if data.Source.Kind == "user" {
				if userID != "" || turn == 0 || ended || data.Role != "user" || data.ID == "" {
					return empty, true
				}
				userID = data.ID
			}
		case "assistant/message":
			if userID == "" || ended || data.Turn != turn || data.Step <= lastStep || data.Step > maxExactMetadataCounter ||
				data.Message.ID == "" || data.Message.Source.Kind != "model" {
				return empty, true
			}
			lastStep = data.Step
			if sample, valid := data.Usage.tokens(); valid {
				model := data.Message.Source.Model
				attributed := models[model]
				if (len(models) >= 128 && attributed == (acpmeta.ModelUsage{})) ||
					!addDeepSeekCounters(&totals, sample) || !addDeepSeekCounters(&attributed, sample) {
					return empty, true
				}
				models[model] = attributed
			}
		case "turn/end":
			if userID == "" || ended || data.Turn != turn || !deepSeekTerminalMatches(data.Reason.Kind, *receipt.StopReason) {
				return empty, true
			}
			ended = true
			if data.Reason.Kind == "completed" || data.Reason.Kind == "max-tokens" {
				if validDeepSeekSessionID(userID) {
					forkPosition = fmt.Sprintf("%d:%d:%s", sequence, turn, userID)
				}
			}
		}
	}
	if !ended {
		return empty, false
	}
	id := strconv.FormatInt(turn, 10)
	if receipt.NativeTurnID != "" && receipt.NativeTurnID != id {
		return empty, true
	}
	result := acpclient.PromptJournalEvidence{NativeTurnID: id, ForkPosition: forkPosition}
	if totals == (acpmeta.ModelUsage{}) {
		return result, true
	}
	report := &acpmeta.TurnUsage{
		Source: "deepseek.session.journal", Coverage: "observed_requests",
		InputTokens: totals.InputTokens, OutputTokens: totals.OutputTokens, CacheReadTokens: totals.CacheReadTokens,
		CacheWriteTokens: totals.CacheWriteTokens, ReasoningTokens: totals.ReasoningTokens,
	}
	// Other attempts and descendants can consume tokens outside this ledger.
	if _, unknown := models[""]; !unknown {
		report.Models = models
		if len(models) == 1 {
			for model := range models {
				report.Model = model
			}
		}
	}
	result.Usage = report
	return result, true
}

func deepSeekTerminalMatches(native string, stop acp.StopReason) bool {
	switch native {
	case "completed", "blocked":
		return stop == acp.StopReasonEndTurn
	case "max-tokens":
		return stop == acp.StopReasonMaxTokens
	case "aborted", "interrupted":
		return stop == acp.StopReasonCancelled || native == "aborted" && stop == acp.StopReasonEndTurn
	}
	return false
}

func (usage deepSeekJournalUsage) tokens() (acpmeta.ModelUsage, bool) {
	if !exactCounter(usage.Input) || !exactCounter(usage.Output) {
		return acpmeta.ModelUsage{}, false
	}
	for _, value := range []*int64{usage.Read, usage.Write, usage.Reasoning, usage.Total} {
		if value != nil && !exactCounter(value) {
			return acpmeta.ModelUsage{}, false
		}
	}
	if usage.Reasoning != nil && *usage.Reasoning > *usage.Output {
		return acpmeta.ModelUsage{}, false
	}
	if usage.Total != nil && usage.Read != nil && usage.Write != nil &&
		*usage.Total != *usage.Input+*usage.Output+*usage.Read+*usage.Write {
		return acpmeta.ModelUsage{}, false
	}
	return acpmeta.ModelUsage{InputTokens: usage.Input, OutputTokens: usage.Output, CacheReadTokens: usage.Read,
		CacheWriteTokens: usage.Write, ReasoningTokens: usage.Reasoning}, true
}

func addDeepSeekCounters(total *acpmeta.ModelUsage, increment acpmeta.ModelUsage) bool {
	fields := []**int64{&total.InputTokens, &total.OutputTokens, &total.CacheReadTokens, &total.CacheWriteTokens, &total.ReasoningTokens}
	for index, count := range []*int64{increment.InputTokens, increment.OutputTokens, increment.CacheReadTokens, increment.CacheWriteTokens, increment.ReasoningTokens} {
		if count == nil {
			continue
		}
		field := fields[index]
		if *field == nil {
			*field = acp.Ptr(*count)
		} else {
			if **field > maxExactMetadataCounter-*count {
				return false
			}
			**field += *count
		}
	}
	return true
}
