package openclaw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	sqlite "github.com/ncruces/go-sqlite3"
)

// The supported native database shape was observed in OpenClaw 2026.11.7.
const (
	usageStateSchema = 36
	usageAgentSchema = 38
	usageReadLimit   = 256
	usageReadTimeout = time.Second
)

var gatewayRunID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

// usageJournal reads authoritative local Gateway records without running any management RPC.
type usageJournal struct {
	// root is the known selected native state directory, or empty when unproven.
	root string
	// gateway is the exact loopback origin encoded in the reserved ACP session.
	gateway string
}

type usageCheckpoint struct {
	// file fences SQLite replacements between the before and after snapshots.
	file os.FileInfo
	// sequence excludes old entries, including copied fork history.
	sequence int64
	// generation fences destructive native rewrites and ledger re-creation.
	generation string
	// lastID detects changes to the boundary record under the same native ID.
	lastID string
}

const (
	replayFile = "openclaw.sqlite"
	agentFile  = "openclaw-agent.sqlite"
)

func usageRoot(environment, prefix []string) string {
	values := make(map[string]string, 4)
	for _, record := range environment {
		key, value, _ := strings.Cut(record, "=")
		values[strings.ToUpper(key)] = strings.TrimSpace(value)
	}
	if selected := values["OPENCLAW_STATE_DIR"]; filepath.IsAbs(selected) {
		return filepath.Clean(selected)
	}
	if values["OPENCLAW_STATE_DIR"] != "" || values["OPENCLAW_CONFIG_PATH"] != "" ||
		values["OPENCLAW_HOME"] != "" || values["OPENCLAW_PROFILE"] != "" {
		return ""
	}
	for _, arg := range prefix {
		if arg == "--dev" || arg == "--profile" || strings.HasPrefix(arg, "--profile=") {
			return ""
		}
	}
	home := values["HOME"]
	if home == "" {
		home = values["USERPROFILE"]
	}
	if !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, ".openclaw")
}

func (j usageJournal) capture(ctx context.Context, scope acpclient.PromptJournalScope) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	if j.root == "" {
		return nil
	}
	bound, err := parseBinding(scope.SessionID, j.gateway)
	if err != nil {
		return nil
	}
	agent := strings.SplitN(bound.Key, ":", 3)[1]
	replay := filepath.Join(j.root, "state", replayFile)
	transcript := filepath.Join(j.root, "agents", agent, "agent", agentFile)

	op, cancel := context.WithTimeout(ctx, usageReadTimeout)
	defer cancel()
	beforeReplay, err := checkpoint(op, replay, usageStateSchema, func(db *sqlite.Conn) (usageCheckpoint, error) {
		return replayCheckpoint(db, scope, bound)
	})
	if err != nil {
		return nil
	}
	beforeNative, err := checkpoint(op, transcript, usageAgentSchema, func(db *sqlite.Conn) (usageCheckpoint, error) {
		return nativeCheckpoint(db, bound.PhysicalID)
	})
	if err != nil {
		return nil
	}

	return func(ctx context.Context, receipt acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
		if receipt.StopReason == nil || *receipt.StopReason != acp.StopReasonEndTurn && *receipt.StopReason != acp.StopReasonMaxTokens {
			return acpclient.PromptJournalEvidence{}
		}
		op, cancel := context.WithTimeout(ctx, usageReadTimeout)
		defer cancel()
		var run string
		err := readUsageDB(op, replay, usageStateSchema, beforeReplay.file, func(db *sqlite.Conn) error {
			now, err := replayCheckpoint(db, scope, bound)
			if err != nil || now.generation != beforeReplay.generation || now.sequence <= beforeReplay.sequence {
				return errUsageProof
			}
			last, err := replayIdentity(db, scope.SessionID, beforeReplay.sequence)
			if err != nil || last != beforeReplay.lastID {
				return errUsageProof
			}
			run, err = readReplayRun(db, scope.SessionID, bound.Key, beforeReplay.sequence)
			return err
		})
		if err != nil || !gatewayRunID.MatchString(run) || receipt.NativeTurnID != "" && receipt.NativeTurnID != run {
			return acpclient.PromptJournalEvidence{}
		}
		var report *acpmeta.TurnUsage
		err = readUsageDB(op, transcript, usageAgentSchema, beforeNative.file, func(db *sqlite.Conn) error {
			now, err := nativeCheckpoint(db, bound.PhysicalID)
			if err != nil || now.generation != beforeNative.generation || now.sequence <= beforeNative.sequence {
				return errUsageProof
			}
			last, err := nativeIdentity(db, bound.PhysicalID, beforeNative.sequence)
			if err != nil || last != beforeNative.lastID {
				return errUsageProof
			}
			report, err = readUsageResponses(db, bound.PhysicalID, run, beforeNative.sequence)
			return err
		})
		if err != nil || report == nil {
			return acpclient.PromptJournalEvidence{}
		}
		return acpclient.PromptJournalEvidence{NativeTurnID: run, Usage: report}
	}
}

var errUsageProof = errors.New("OpenClaw native usage identity is unavailable")

func checkpoint(ctx context.Context, path string, schema int64, read func(*sqlite.Conn) (usageCheckpoint, error)) (usageCheckpoint, error) {
	file, err := os.Lstat(path)
	if err != nil || !file.Mode().IsRegular() {
		return usageCheckpoint{}, errUsageProof
	}
	var mark usageCheckpoint
	err = readUsageDB(ctx, path, schema, file, func(db *sqlite.Conn) error {
		observed, err := read(db)
		mark = observed
		return err
	})
	mark.file = file
	return mark, err
}

func sameUsageFile(path string, original os.FileInfo) bool {
	current, err := os.Lstat(path)
	return err == nil && current.Mode().IsRegular() && os.SameFile(current, original)
}

func readUsageDB(ctx context.Context, path string, schema int64, file os.FileInfo, read func(*sqlite.Conn) error) error {
	if !sameUsageFile(path, file) {
		return errUsageProof
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sqlite.OpenContext(ctx, u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetInterrupt(ctx)
	if err := db.BusyTimeout(0); err != nil {
		return err
	}
	if err := db.Exec("BEGIN"); err != nil {
		return err
	}
	defer db.Exec("ROLLBACK")
	var version int64
	err = usageRow(db, "PRAGMA user_version", nil, func(s *sqlite.Stmt) { version = s.ColumnInt64(0) })
	if err != nil || version != schema {
		return errUsageProof
	}
	if err := read(db); err != nil {
		return err
	}
	if !sameUsageFile(path, file) {
		return errUsageProof
	}
	return nil
}

func usageRow(db *sqlite.Conn, query string, bind func(*sqlite.Stmt), read func(*sqlite.Stmt)) error {
	s, _, err := db.Prepare(query)
	if err != nil {
		return err
	}
	defer s.Close()
	if bind != nil {
		bind(s)
	}
	if !s.Step() {
		if err := s.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	read(s)
	return s.Err()
}

func replayCheckpoint(db *sqlite.Conn, scope acpclient.PromptJournalScope, bound binding) (usageCheckpoint, error) {
	var mark usageCheckpoint
	var key, cwd string
	err := usageRow(db, `
		SELECT s.session_key, s.cwd, CAST(s.created_at AS TEXT),
			coalesce((SELECT max(seq) FROM acp_replay_events WHERE session_id=s.session_id),0)
		FROM acp_replay_sessions AS s WHERE s.session_id=?`, func(s *sqlite.Stmt) {
		_ = s.BindText(1, string(scope.SessionID))
	}, func(s *sqlite.Stmt) {
		key, cwd, mark.generation, mark.sequence = s.ColumnText(0), s.ColumnText(1), s.ColumnText(2), s.ColumnInt64(3)
	})
	if err != nil || key != bound.Key || cwd != scope.Cwd || mark.generation == "" || mark.sequence < 0 {
		return usageCheckpoint{}, errUsageProof
	}
	mark.lastID, err = replayIdentity(db, scope.SessionID, mark.sequence)
	return mark, err
}

func replayIdentity(db *sqlite.Conn, id acp.SessionId, sequence int64) (string, error) {
	if sequence == 0 {
		return "", nil
	}
	var marker string
	err := usageRow(db, `
		SELECT coalesce(run_id,'') || ':' || cast(length(update_json) as TEXT)
		FROM acp_replay_events WHERE session_id=? AND seq=?`, func(s *sqlite.Stmt) {
		_ = s.BindText(1, string(id))
		_ = s.BindInt64(2, sequence)
	}, func(s *sqlite.Stmt) { marker = s.ColumnText(0) })
	return marker, err
}

func readReplayRun(db *sqlite.Conn, id acp.SessionId, key string, after int64) (string, error) {
	s, _, err := db.Prepare(`
		SELECT session_key, coalesce(run_id,''), json_extract(update_json,'$.sessionUpdate')
		FROM acp_replay_events WHERE session_id=? AND seq>? ORDER BY seq LIMIT ?`)
	if err != nil {
		return "", err
	}
	defer s.Close()
	_ = s.BindText(1, string(id))
	_ = s.BindInt64(2, after)
	_ = s.BindInt64(3, usageReadLimit+1)
	var run string
	hasUser := false
	for count := 0; s.Step(); count++ {
		if count >= usageReadLimit || s.ColumnText(0) != key {
			return "", errUsageProof
		}
		kind, id := s.ColumnText(2), s.ColumnText(1)
		if kind == "user_message_chunk" {
			if !gatewayRunID.MatchString(id) {
				return "", errUsageProof
			}
			hasUser = true
		}
		if id != "" {
			if run != "" && id != run {
				return "", errUsageProof
			}
			run = id
		}
	}
	if !hasUser {
		return "", errUsageProof
	}
	return run, s.Err()
}

func nativeCheckpoint(db *sqlite.Conn, id string) (usageCheckpoint, error) {
	var mark usageCheckpoint
	err := usageRow(db, `
		SELECT coalesce((SELECT max(seq) FROM transcript_events WHERE session_id=?1),0),
			coalesce((SELECT generation FROM transcript_rewrite_watermarks WHERE session_id=?1),'')`,
		func(s *sqlite.Stmt) { _ = s.BindText(1, id) }, func(s *sqlite.Stmt) {
			mark.sequence, mark.generation = s.ColumnInt64(0), s.ColumnText(1)
		})
	if err != nil || mark.sequence < 0 {
		return usageCheckpoint{}, errUsageProof
	}
	mark.lastID, err = nativeIdentity(db, id, mark.sequence)
	return mark, err
}

func nativeIdentity(db *sqlite.Conn, id string, sequence int64) (string, error) {
	if sequence == 0 {
		return "", nil
	}
	var marker string
	err := usageRow(db, `
		SELECT coalesce((SELECT event_id FROM transcript_event_identities
			WHERE session_id=?1 AND seq=?2 ORDER BY event_id LIMIT 1),'')
		FROM transcript_events WHERE session_id=?1 AND seq=?2`, func(s *sqlite.Stmt) {
		_ = s.BindText(1, id)
		_ = s.BindInt64(2, sequence)
	}, func(s *sqlite.Stmt) { marker = s.ColumnText(0) })
	if err == nil && marker == "" {
		return "", errUsageProof
	}
	return marker, err
}

type transcriptUsageMessage struct {
	// Role identifies the persisted user admission or one assistant reply.
	Role string `json:"role"`
	// IdempotencyKey is the Gateway's exact user admission identity.
	IdempotencyKey string `json:"idempotencyKey"`
	// OpenClaw assigns the run identity to persisted assistant responses.
	OpenClaw struct {
		// RunID links this response to the accepted Gateway execution.
		RunID string `json:"runId"`
	} `json:"__openclaw"`
	// Provider identifies the actual native provider for this response.
	Provider string `json:"provider"`
	// Model names the actual native model for this response.
	Model string `json:"model"`
	// ResponseID is the native provider response identifier used to detect duplicate records.
	ResponseID string `json:"responseId"`
	// Usage contains only the provider counters verified by this adapter.
	Usage *struct {
		// Output is the reported output count including any reasoning.
		Output *int64 `json:"output"`
		// Report preserves the native distinction between a measured count and a placeholder.
		Report struct {
			// State is "available" only when the count has native accounting evidence.
			State string `json:"state"`
		} `json:"usageReport"`
	} `json:"usage"`
}

func readUsageResponses(db *sqlite.Conn, physicalID, run string, after int64) (*acpmeta.TurnUsage, error) {
	// A copied or otherwise preexisting user admission cannot own a new turn.
	err := usageRow(db, `
		SELECT 1 FROM transcript_event_identities
		WHERE session_id=? AND message_idempotency_key=? AND seq<=? LIMIT 1`,
		func(s *sqlite.Stmt) {
			_ = s.BindText(1, physicalID)
			_ = s.BindText(2, run+":user")
			_ = s.BindInt64(3, after)
		}, func(*sqlite.Stmt) {})
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, errUsageProof
	}
	s, _, err := db.Prepare(`
		SELECT CASE WHEN length(event_json)<=262144 THEN event_json ELSE NULL END
		FROM transcript_events WHERE session_id=? AND seq>? AND message_role IN ('user','assistant')
		ORDER BY seq LIMIT ?`)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_ = s.BindText(1, physicalID)
	_ = s.BindInt64(2, after)
	_ = s.BindInt64(3, usageReadLimit+1)
	report := acpmeta.TurnUsage{Source: "openclaw-native-transcript", Coverage: "matched_assistant_responses",
		Models: make(map[string]acpmeta.ModelUsage)}
	seenResponses := make(map[string]bool)
	hasUser := false
	var total int64
	for count := 0; s.Step(); count++ {
		if count >= usageReadLimit {
			return nil, errUsageProof
		}
		var record struct {
			// Message is the JSON payload of one native transcript event.
			Message transcriptUsageMessage `json:"message"`
		}
		if json.Unmarshal([]byte(s.ColumnText(0)), &record) != nil {
			return nil, errUsageProof
		}
		msg := record.Message
		if msg.Role == "user" && msg.IdempotencyKey == run+":user" {
			if hasUser {
				return nil, errUsageProof
			}
			hasUser = true
		}
		if msg.Role != "assistant" || msg.OpenClaw.RunID != run {
			continue
		}
		if !hasUser {
			return nil, errUsageProof
		}
		if msg.Usage == nil || msg.Usage.Report.State != "available" || msg.Usage.Output == nil || *msg.Usage.Output < 0 {
			continue
		}
		model := string(modelID(msg.Provider, msg.Model))
		if model == "" || msg.ResponseID == "" || seenResponses[msg.ResponseID] || *msg.Usage.Output > math.MaxInt64-total {
			return nil, errUsageProof
		}
		seenResponses[msg.ResponseID] = true
		total += *msg.Usage.Output
		perModel := report.Models[model]
		if perModel.OutputTokens == nil {
			perModel.OutputTokens = acp.Ptr(int64(0))
		}
		*perModel.OutputTokens += *msg.Usage.Output
		report.Models[model] = perModel
	}
	if err := s.Err(); err != nil || !hasUser || len(seenResponses) == 0 {
		return nil, err
	}
	report.OutputTokens = &total
	if len(report.Models) == 1 {
		for name := range report.Models {
			report.Model = name
		}
	}
	return &report, nil
}
