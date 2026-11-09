package qoder

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
	"unicode/utf16"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

const (
	qoderHistoryLimit = 16 << 20
	qoderHistoryLine  = 4 << 20
	qoderHistoryRows  = 4096
)

var qoderHistoryUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func readPromptEvidence(_ acp.StopReason, user string, _ map[string]any) acpclient.PromptEvidence {
	if !qoderHistoryUUID.MatchString(user) {
		return acpclient.PromptEvidence{}
	}
	return acpclient.PromptEvidence{NativeTurnID: user, InputAccepted: true}
}

func qoderHistoryHome(options Options) string {
	value := func(name string) string {
		for i := len(options.Environment) - 1; i >= 0; i-- {
			key, v, ok := strings.Cut(options.Environment[i], "=")
			if ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
				return v
			}
		}
		return ""
	}
	config := value("QODER_CONFIG_DIR")
	folder := ".qoder"
	if options.RuntimeID == "qoder-cn" {
		config = value("QODERCN_CONFIG_DIR")
		folder = ".qoder-cn"
	}
	for i, arg := range options.PrefixArgs {
		if strings.HasPrefix(arg, "--config-dir=") {
			config = strings.TrimPrefix(arg, "--config-dir=")
		} else if arg == "--config-dir" && i+1 < len(options.PrefixArgs) {
			config = options.PrefixArgs[i+1]
		}
	}
	if config == "" {
		home := value("HOME")
		if runtime.GOOS == "windows" {
			home = value("USERPROFILE")
		}
		if !filepath.IsAbs(home) {
			return ""
		}
		return filepath.Join(home, folder)
	}
	if !filepath.IsAbs(config) {
		config = filepath.Join(options.WorkingDirectory, config)
	}
	return filepath.Clean(config)
}

// qoderProjectSlug follows Qoder 1.1.83's UTF-16 Ir/Te path encoder.
func qoderProjectSlug(cwd string) string {
	units := utf16.Encode([]rune(cwd))
	slug := make([]byte, len(units))
	var hash uint32 = 5381
	for i, c := range units {
		hash = hash*33 ^ uint32(c)
		slug[i] = '-'
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			slug[i] = byte(c)
		}
	}
	if len(slug) <= 200 {
		return string(slug)
	}
	magnitude := int64(int32(hash))
	if magnitude < 0 {
		magnitude = -magnitude
	}
	suffix := strconv.FormatInt(magnitude, 36)
	return string(slug[:200]) + "-" + suffix
}

func captureQoderHistory(ctx context.Context, scope acpclient.PromptJournalScope, home string) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	if home == "" || !qoderHistoryUUID.MatchString(string(scope.SessionID)) || !filepath.IsAbs(scope.Cwd) || ctx.Err() != nil {
		return nil
	}
	cwd, err := filepath.EvalSymlinks(scope.Cwd)
	if err != nil {
		return nil
	}
	name := filepath.Join("projects", qoderProjectSlug(cwd), string(scope.SessionID)+".jsonl")
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
		if receipt.StopReason == nil || *receipt.StopReason != acp.StopReasonEndTurn || !qoderHistoryUUID.MatchString(receipt.NativeTurnID) || ctx.Err() != nil {
			return acpclient.PromptJournalEvidence{}
		}
		return acpclient.PromptJournalEvidence{Usage: readQoderHistory(ctx, home, name, cwd, string(scope.SessionID), receipt.NativeTurnID, baseline)}
	}
}

type qoderHistoryUsage struct {
	// Input preserves native input without assuming its cache accounting convention.
	Input *int64 `json:"input_tokens"`
	// Output preserves the native completion count.
	Output *int64 `json:"output_tokens"`
	// CacheRead preserves positive native cache reads; zero may be a default.
	CacheRead *int64 `json:"cache_read_input_tokens"`
	// CacheWrite preserves positive native cache writes; zero may be a default.
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
	// Request identifies the native model request that owns these counters.
	Request string `json:"request_id"`
}

type qoderHistoryRow struct {
	// Type distinguishes conversation messages from metadata.
	Type string `json:"type"`
	// UUID identifies this native transcript entry.
	UUID string `json:"uuid"`
	// Parent identifies this entry's predecessor in the native conversation chain.
	Parent string `json:"parentUuid"`
	// ToolParent identifies a native tool-result user entry, not another user prompt.
	ToolParent string `json:"sourceToolAssistantUUID"`
	// Session binds the entry to the native ACP session.
	Session string `json:"sessionId"`
	// Cwd binds the entry to the original workspace despite slug collisions.
	Cwd string `json:"cwd"`
	// Version pins the persisted schema independently of the running CLI.
	Version string `json:"version"`
	// Sidechain excludes independently persisted subagent work.
	Sidechain bool `json:"isSidechain"`
	// ModelSource distinguishes custom-provider responses from first-party responses.
	ModelSource string `json:"modelSource"`
	// CustomProvider identifies a custom provider without reading its configuration.
	CustomProvider string `json:"customProviderId"`
	// Anchor preserves the request identity even on message fragments without usage.
	Anchor struct {
		// Request identifies the same native request as usage.request_id.
		Request string `json:"requestId"`
	} `json:"requestTokenAnchor"`
	// Message contains only accounting fields; content is deliberately not decoded.
	Message struct {
		// ID identifies a provider response across multiple assistant fragments.
		ID string `json:"id"`
		// Model identifies the actual model used for this response.
		Model string `json:"model"`
		// Usage contains this response's native counters when the final fragment reports them.
		Usage *qoderHistoryUsage `json:"usage"`
	} `json:"message"`
}

func readQoderHistory(ctx context.Context, home, name, cwd, session, turn string, baseline os.FileInfo) *acpmeta.TurnUsage {
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil
	}
	defer root.Close()
	entry, err := root.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() {
		return nil
	}
	file, err := root.OpenFile(name, os.O_RDONLY|qoderHistoryOpenFlags, 0)
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
	if size <= offset || size-offset > qoderHistoryLimit || !qoderHistoryBoundary(file, offset) || !qoderHistoryBoundary(file, size) {
		return nil
	}
	scanner := bufio.NewScanner(io.NewSectionReader(file, offset, size-offset))
	scanner.Buffer(make([]byte, 4096), qoderHistoryLine)
	rows := make(map[string]qoderHistoryRow)
	order := []string{}
	for scanner.Scan() && ctx.Err() == nil {
		var row qoderHistoryRow
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			return nil
		}
		if row.Session != session || row.Cwd != cwd || row.Sidechain || (row.Type != "user" && row.Type != "assistant" && row.Type != "system" && row.Type != "attachment") {
			continue
		}
		if !qoderHistoryID(row.UUID) || len(rows) >= qoderHistoryRows {
			return nil
		}
		if previous, exists := rows[row.UUID]; exists {
			a, _ := json.Marshal(previous)
			b, _ := json.Marshal(row)
			if string(a) != string(b) {
				return nil
			}
			continue
		}
		rows[row.UUID] = row
		order = append(order, row.UUID)
	}
	if scanner.Err() != nil || ctx.Err() != nil {
		return nil
	}
	start, ok := rows[turn]
	if !ok || start.Type != "user" {
		return nil
	}
	// Only descendants of this receipt's user entry can contribute. A different
	// user prompt ends attribution. Native tool-result entries retain their
	// source assistant UUID and can continue the same invocation chain.
	belongs := func(row qoderHistoryRow) bool {
		for steps := 0; steps < len(rows); steps++ {
			if row.Parent == turn {
				return true
			}
			parent, ok := rows[row.Parent]
			if !ok {
				return false
			}
			if parent.Type == "user" {
				assistant, exists := rows[parent.Parent]
				if parent.ToolParent == "" || parent.ToolParent != parent.Parent || !exists || assistant.Type != "assistant" {
					return false
				}
			}
			row = parent
		}
		return false
	}
	seen := make(map[string]qoderHistoryRow)
	for _, id := range order {
		row := rows[id]
		if row.Type != "assistant" || !belongs(row) || row.Message.Usage == nil {
			continue
		}
		u := row.Message.Usage
		if !qoderValidOptional(u.Input) || !qoderValidOptional(u.Output) || !qoderValidOptional(u.CacheRead) || !qoderValidOptional(u.CacheWrite) {
			return nil
		}
		if !qoderPositive(u.Input) && !qoderPositive(u.Output) && !qoderPositive(u.CacheRead) && !qoderPositive(u.CacheWrite) {
			continue
		}
		if !qoderHistoryID(row.Message.ID) || !qoderHistoryID(row.Message.Model) || row.Message.Model == "unknown" {
			return nil
		}
		request := u.Request
		if request == "" {
			request = row.Anchor.Request
		}
		if !qoderHistoryID(request) || u.Request != "" && row.Anchor.Request != "" && u.Request != row.Anchor.Request {
			return nil
		}
		key := request + "\x00" + row.Message.ID
		if previous, exists := seen[key]; exists {
			a, _ := json.Marshal(previous.Message.Usage)
			b, _ := json.Marshal(u)
			if previous.Message.Model != row.Message.Model || previous.ModelSource != row.ModelSource || previous.CustomProvider != row.CustomProvider || string(a) != string(b) {
				return nil
			}
			continue
		}
		seen[key] = row
	}
	if len(seen) == 0 {
		return nil
	}
	report := &acpmeta.TurnUsage{Source: "qoder.history.usage", Coverage: "observed_requests", Models: make(map[string]acpmeta.ModelUsage)}
	var input, output, read, write int64
	for _, row := range seen {
		u := row.Message.Usage
		part := report.Models[row.Message.Model]
		if !qoderAddPositive(&input, u.Input) || !qoderAddPositive(&output, u.Output) || !qoderAddPositive(&read, u.CacheRead) || !qoderAddPositive(&write, u.CacheWrite) || !qoderAddModel(&part, u) {
			return nil
		}
		report.Models[row.Message.Model] = part
	}
	if len(report.Models) == 1 {
		for model := range report.Models {
			report.Model = model
		}
	}
	if output > 0 {
		report.OutputTokens = &output
	}
	if read > 0 {
		report.CacheReadTokens = &read
	}
	if write > 0 {
		report.CacheWriteTokens = &write
	}
	// Missing availability flags and protocol identity forbid an uncached-input
	// normalization. Preserve the raw subtotal without claiming full coverage.
	observation := acpmeta.UsageObservation{Source: report.Source, Coverage: "observed_requests_raw", Tokens: acpmeta.ModelUsage{OutputTokens: report.OutputTokens, CacheReadTokens: report.CacheReadTokens, CacheWriteTokens: report.CacheWriteTokens}, Models: report.Models}
	if input > 0 {
		observation.InputTotalTokens = &input
	}
	report.Observations = []acpmeta.UsageObservation{observation}
	return report
}

func qoderHistoryBoundary(file *os.File, offset int64) bool {
	if offset == 0 {
		return true
	}
	var marker [1]byte
	_, err := file.ReadAt(marker[:], offset-1)
	return err == nil && marker[0] == '\n'
}
func qoderHistoryID(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
func qoderValidOptional(value *int64) bool { return value == nil || validQoderTokenCount(value) }
func qoderPositive(value *int64) bool      { return value != nil && *value > 0 }
func qoderAddPositive(total *int64, value *int64) bool {
	if !qoderPositive(value) {
		return true
	}
	if *value > maxNativeTokenCounter-*total {
		return false
	}
	*total += *value
	return true
}
func qoderAddModel(part *acpmeta.ModelUsage, u *qoderHistoryUsage) bool {
	for _, pair := range []struct {
		// Target receives the known positive model subtotal.
		Target **int64
		// Value is the measured native component.
		Value *int64
	}{{&part.OutputTokens, u.Output}, {&part.CacheReadTokens, u.CacheRead}, {&part.CacheWriteTokens, u.CacheWrite}} {
		if !qoderPositive(pair.Value) {
			continue
		}
		if *pair.Target == nil {
			*pair.Target = acp.Ptr(int64(0))
		}
		if !qoderAddPositive(*pair.Target, pair.Value) {
			return false
		}
	}
	return true
}
