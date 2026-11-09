package workbuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

const (
	workBuddyHistoryLimit    = 16 << 20
	workBuddyHistoryLine     = 4 << 20
	workBuddyHistoryRequests = 1024
	maxWorkBuddyTokenCount   = 1<<53 - 1
)

var (
	workBuddyHistorySession = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
	workBuddySlugSeparators = regexp.MustCompile(`[-/\\:]+`)
)

func workBuddyHistoryHome(environment []string, cwd string) string {
	value := func(name string) string {
		for i := len(environment) - 1; i >= 0; i-- {
			key, v, ok := strings.Cut(environment[i], "=")
			if ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
				return v
			}
		}
		return ""
	}
	config := value("CODEBUDDY_CONFIG_DIR")
	if strings.TrimSpace(config) == "" {
		home := value("HOME")
		if runtime.GOOS == "windows" {
			home = value("USERPROFILE")
		}
		if !filepath.IsAbs(home) {
			return ""
		}
		return filepath.Join(home, ".codebuddy")
	}
	if filepath.IsAbs(config) {
		return config
	}
	path, err := filepath.Abs(filepath.Join(cwd, config))
	if err != nil {
		return ""
	}
	return path
}

// workBuddyProjectSlug follows CodeBuddy 2.180.0's compressWorkspacePathName.
func workBuddyProjectSlug(cwd string) string {
	const slugLimit, prefixLimit = 255, 180
	slug := strings.Trim(workBuddySlugSeparators.ReplaceAllString(cwd, "-"), "-")
	if len(slug) <= slugLimit {
		return slug
	}
	var hash uint32 = 5381
	for i := range len(slug) {
		hash = 33*hash ^ uint32(slug[i])
	}
	end := prefixLimit
	for !utf8.RuneStart(slug[end]) {
		end--
	}
	return slug[:end] + "-" + strconv.FormatUint(uint64(hash), 36)
}

func captureWorkBuddyHistory(ctx context.Context, scope acpclient.PromptJournalScope, home string) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	id := string(scope.SessionID)
	if home == "" || !workBuddyHistorySession.MatchString(id) ||
		!filepath.IsAbs(scope.Cwd) || ctx.Err() != nil {
		return nil
	}
	cwd, err := filepath.EvalSymlinks(scope.Cwd)
	if err != nil || !utf8.ValidString(cwd) {
		return nil
	}
	slug := workBuddyProjectSlug(cwd)
	if !filepath.IsLocal(slug) || filepath.Base(slug) != slug || slug == "." {
		return nil
	}
	name := filepath.Join("projects", slug, id+".jsonl")
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil
	}
	defer root.Close()
	baseline, err := root.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) || baseline != nil && !baseline.Mode().IsRegular() {
		return nil
	}
	return func(ctx context.Context, receipt acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
		if receipt.StopReason == nil || !conversationID.MatchString(receipt.NativeTurnID) || ctx.Err() != nil {
			return acpclient.PromptJournalEvidence{}
		}
		return acpclient.PromptJournalEvidence{Usage: readWorkBuddyHistory(ctx, home, name, cwd, id, receipt.NativeTurnID, baseline)}
	}
}

type workBuddyHistoryRow struct {
	// Type identifies an assistant message or function-call response record.
	Type string `json:"type"`
	// Role distinguishes assistant messages from user messages.
	Role string `json:"role"`
	// Cwd binds the entry to the original workspace, even if two slugs collide.
	Cwd string `json:"cwd"`
	// Session binds the entry to the native ACP session.
	Session string `json:"sessionId"`
	// Data contains the provider metadata used to match the request and its output.
	Data struct {
		// Request identifies the conversation turn recorded in the native prompt response.
		Request string `json:"conversationRequestId"`
		// Message identifies a single provider response across duplicate records.
		Message string `json:"messageId"`
		// Model identifies the model reported for that response.
		Model string `json:"model"`
		// RawUsage keeps the provider's original representation for the matched row.
		RawUsage json.RawMessage `json:"rawUsage"`
		// Usage keeps CodeBuddy's normalized usage for corroborating the raw output.
		Usage json.RawMessage `json:"usage"`
	} `json:"providerData"`
}

type workBuddyHistoryOutput struct {
	// model identifies the provider response's actual model.
	model string
	// tokens holds the output reported for this distinct response.
	tokens int64
	// reasoning holds the separately reported subset of generated output.
	reasoning int64
	// reasoningKnown requires explicit valid reasoning counters on every duplicate.
	reasoningKnown bool
	// inputTotal retains the native input without treating it as an additive component.
	inputTotal *int64
	// input is the uncached portion of a corroborated inclusive prompt counter.
	input *int64
	// cacheRead is the measured cached portion of that same provider response.
	cacheRead *int64
	// cacheWrite is the explicitly reported cache-creation portion of that response.
	cacheWrite *int64
}

func readWorkBuddyHistory(ctx context.Context, home, name, cwd, session, request string, baseline os.FileInfo) *acpmeta.TurnUsage {
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil
	}
	defer root.Close()
	entry, err := root.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() {
		return nil
	}
	file, err := root.OpenFile(name, os.O_RDONLY|workBuddyHistoryOpenFlags, 0)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, entry) {
		return nil
	}
	var offset int64
	if baseline != nil {
		if !os.SameFile(info, baseline) {
			return nil
		}
		offset = baseline.Size()
	}
	size := info.Size()
	if size <= offset || size-offset > workBuddyHistoryLimit || !workBuddyLineBoundary(file, offset) || !workBuddyLineBoundary(file, size) {
		return nil
	}
	scanner := bufio.NewScanner(io.NewSectionReader(file, offset, size-offset))
	scanner.Buffer(make([]byte, 4096), workBuddyHistoryLine)
	seen := make(map[string]workBuddyHistoryOutput)
	var total int64
	for scanner.Scan() && ctx.Err() == nil {
		var row workBuddyHistoryRow
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			return nil
		}
		if (row.Type != "function_call" && (row.Type != "message" || row.Role != "assistant")) || row.Session != session || row.Cwd != cwd ||
			row.Data.Request != request || len(row.Data.RawUsage) == 0 || string(row.Data.RawUsage) == "null" {
			continue
		}
		tokens := row.outputTokens()
		if tokens == nil || !workBuddyUsageID(row.Data.Message) || !workBuddyUsageID(row.Data.Model) || row.Data.Model == "unknown" {
			return nil
		}
		output := workBuddyHistoryOutput{model: row.Data.Model, tokens: *tokens}
		output.inputTotal, output.input, output.cacheRead, output.cacheWrite = row.inputTokens(*tokens)
		if reasoning := row.reasoningTokens(*tokens); reasoning != nil {
			output.reasoning, output.reasoningKnown = *reasoning, true
		}
		if previous, duplicate := seen[row.Data.Message]; duplicate {
			if previous.model != output.model || previous.tokens != output.tokens {
				return nil
			}
			if !output.reasoningKnown || previous.reasoning != output.reasoning {
				previous.reasoningKnown = false
			}
			// A duplicate cannot supply missing evidence or select one of two
			// contradictory cache breakdowns for the same provider response.
			if !sameHistoryCounter(previous.inputTotal, output.inputTotal) {
				previous.inputTotal, previous.input, previous.cacheRead, previous.cacheWrite = nil, nil, nil, nil
			} else {
				if !sameHistoryCounter(previous.input, output.input) {
					previous.input = nil
				}
				if !sameHistoryCounter(previous.cacheRead, output.cacheRead) {
					previous.cacheRead = nil
				}
				if !sameHistoryCounter(previous.cacheWrite, output.cacheWrite) {
					previous.cacheWrite = nil
				}
			}
			seen[row.Data.Message] = previous
			continue
		}
		if len(seen) >= workBuddyHistoryRequests || output.tokens > maxWorkBuddyTokenCount-total {
			return nil
		}
		seen[row.Data.Message] = output
		total += output.tokens
	}
	if scanner.Err() != nil || ctx.Err() != nil || len(seen) == 0 {
		return nil
	}
	report := &acpmeta.TurnUsage{Source: "codebuddy.history.usage", Coverage: "observed_requests", OutputTokens: &total,
		Models: make(map[string]acpmeta.ModelUsage)}
	var reasoningTotal int64
	reasoningKnown := true
	inputTotal := acp.Ptr(int64(0))
	report.InputTokens, report.CacheReadTokens, report.CacheWriteTokens = acp.Ptr(int64(0)), acp.Ptr(int64(0)), acp.Ptr(int64(0))
	for _, output := range seen {
		part, exists := report.Models[output.model]
		if !exists {
			part.OutputTokens = acp.Ptr(int64(0))
			part.ReasoningTokens = acp.Ptr(int64(0))
			part.InputTokens, part.CacheReadTokens, part.CacheWriteTokens = acp.Ptr(int64(0)), acp.Ptr(int64(0)), acp.Ptr(int64(0))
		}
		inputTotal = addHistoryCounter(inputTotal, output.inputTotal)
		report.InputTokens = addHistoryCounter(report.InputTokens, output.input)
		report.CacheReadTokens = addHistoryCounter(report.CacheReadTokens, output.cacheRead)
		report.CacheWriteTokens = addHistoryCounter(report.CacheWriteTokens, output.cacheWrite)
		part.InputTokens = addHistoryCounter(part.InputTokens, output.input)
		part.CacheReadTokens = addHistoryCounter(part.CacheReadTokens, output.cacheRead)
		part.CacheWriteTokens = addHistoryCounter(part.CacheWriteTokens, output.cacheWrite)
		*part.OutputTokens += output.tokens
		if !output.reasoningKnown {
			reasoningKnown, part.ReasoningTokens = false, nil
		} else {
			// Each reasoning count is bounded by its output, whose checked sum
			// already bounds the invocation and every model subtotal.
			reasoningTotal += output.reasoning
			if part.ReasoningTokens != nil {
				*part.ReasoningTokens += output.reasoning
			}
		}
		report.Models[output.model] = part
	}
	if reasoningKnown {
		report.ReasoningTokens = &reasoningTotal
	}
	if len(report.Models) == 1 {
		for model := range report.Models {
			report.Model = model
		}
	}
	if inputTotal != nil {
		report.Observations = []acpmeta.UsageObservation{{Source: report.Source, Coverage: report.Coverage, InputTotalTokens: inputTotal}}
	}
	return report
}

func sameHistoryCounter(a, b *int64) bool {
	return a != nil && b != nil && *a == *b
}

func addHistoryCounter(a, b *int64) *int64 {
	if a == nil || b == nil || *b > maxWorkBuddyTokenCount-*a {
		return nil
	}
	return acp.Ptr(*a + *b)
}

func workBuddyLineBoundary(file *os.File, offset int64) bool {
	if offset == 0 {
		return true
	}
	var marker [1]byte
	_, err := file.ReadAt(marker[:], offset-1)
	return err == nil && marker[0] == '\n'
}

func workBuddyUsageID(id string) bool {
	return id != "" && len(id) <= 512 && strings.TrimSpace(id) == id && strings.IndexFunc(id, unicode.IsControl) < 0
}

func (row workBuddyHistoryRow) outputTokens() *int64 {
	var raw struct {
		// Output is the verified completion counter in this version's provider history.
		Output *int64 `json:"completion_tokens"`
	}
	var normalized struct {
		// Output is CodeBuddy's matching normalized completion counter.
		Output *int64 `json:"outputTokens"`
	}
	if json.Unmarshal(row.Data.RawUsage, &raw) != nil || raw.Output == nil || *raw.Output < 0 || *raw.Output > maxWorkBuddyTokenCount ||
		json.Unmarshal(row.Data.Usage, &normalized) != nil || normalized.Output == nil || *normalized.Output != *raw.Output {
		return nil
	}
	return raw.Output
}

func (row workBuddyHistoryRow) reasoningTokens(output int64) *int64 {
	var raw struct {
		// Details contains an optional measured subset of this response's output.
		Details struct {
			// Reasoning is already included in completion_tokens.
			Reasoning *int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if json.Unmarshal(row.Data.RawUsage, &raw) != nil || raw.Details.Reasoning == nil ||
		*raw.Details.Reasoning < 0 || *raw.Details.Reasoning > output {
		return nil
	}
	return raw.Details.Reasoning
}
