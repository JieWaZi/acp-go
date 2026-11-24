package standardacp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	sqlite "github.com/ncruces/go-sqlite3"
)

const (
	hermesGatewayVersion      = "0.23.3"
	hermesGatewaySchema       = 33
	hermesGatewayURLVariable  = "HERMES_TUI_GATEWAY_URL"
	hermesUsageSource         = "hermes/gateway-admission"
	hermesUsageReadTimeout    = 500 * time.Millisecond
	hermesUsageAdmissionLimit = 256
	hermesUsageHistoryLimit   = 4096
	hermesUsageInputLimit     = 1 << 20
	hermesUsageResultLimit    = 8 << 20
)

var (
	hermesGatewaySessionID   = regexp.MustCompile(`^local-[0-9a-f]{64}$`)
	hermesGatewayAdmissionID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hermesUsageModelID       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:-]{0,249}$`)
)

type hermesGatewayJournal struct {
	// home is the selected local Hermes profile directory.
	home string
}

type hermesUsageMark struct {
	// sequence is the committed high-water mark of the admission ledger.
	sequence int64
	// epoch and owner identify the Gateway process that owns the conversation.
	epoch int64
	owner string
	// started identifies the original native session across the two reads.
	started string
	// principal is the native local session owner.
	principal string
	// profile is the profile attached to the native session.
	profile string
	// main is the independently persisted provider request total excluding auxiliary tasks.
	main hermesUsageCounts
}

type hermesUsageCounts struct {
	// input is native gross input, including reads and writes of cached prompt tokens.
	input int64
	// output is the native output total.
	output int64
	// calls counts model calls with a persisted main-loop usage record.
	calls int64
}

// hermesUsageHome limits receipt reads to the configured local profile.
func hermesUsageHome(environment []string) string {
	if strings.TrimSpace(environmentValue(environment, hermesGatewayURLVariable)) != "" {
		return ""
	}
	home := strings.TrimSpace(environmentValue(environment, "HERMES_HOME"))
	if !filepath.IsAbs(home) {
		return ""
	}
	physical, err := filepath.EvalSymlinks(home)
	if err != nil || !localHermesLock(physical) {
		return ""
	}
	managed := strings.TrimSpace(environmentValue(environment, "HERMES_MANAGED_DIR"))
	if managed == "" {
		managed = "/etc/hermes"
	}
	sources := []string{
		filepath.Join(physical, ".env"), filepath.Join(physical, ".op.env"),
		filepath.Join(physical, "hermes-agent", ".env"), filepath.Join(managed, ".env"),
	}
	for _, root := range filepath.SplitList(environmentValue(environment, "PYTHONPATH")) {
		if !filepath.IsAbs(root) {
			return ""
		}
		sources = append(sources, filepath.Join(root, ".env"))
	}
	for _, path := range sources {
		if hermesDotenvMayRedirect(path) {
			return ""
		}
	}
	return physical
}

// localHermesLock excludes private overlay profiles whose lock names another owner.
func localHermesLock(home string) bool {
	info, err := os.Lstat(filepath.Join(home, "gateway.lock"))
	return errors.Is(err, os.ErrNotExist) || err == nil && info.Mode().IsRegular()
}

// hermesDotenvMayRedirect conservatively excludes authored remote routes.
func hermesDotenvMayRedirect(path string) bool {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil || !info.Mode().IsRegular() {
		return true
	}
	file, err := os.OpenFile(path, os.O_RDONLY|journalReadOpenFlags, 0)
	if err != nil {
		return true
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return true
	}
	const maxEnv = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxEnv+1))
	defer clear(data)
	return err != nil || len(data) > maxEnv || bytes.IndexByte(data, 0) >= 0 ||
		bytes.Contains(data, []byte(hermesGatewayURLVariable)) || bytes.Contains(data, []byte("HERMES_HOME"))
}

// capture reads committed native receipts only. It leaves unsupported or ambiguous
// reports to the standard ACP layer as unattributed observations.
func (j hermesGatewayJournal) capture(ctx context.Context, scope acpclient.PromptJournalScope) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
	id := string(scope.SessionID)
	if j.home == "" || strings.TrimPrefix(scope.Version, "v") != hermesGatewayVersion ||
		!hermesGatewaySessionID.MatchString(id) || !filepath.IsAbs(scope.Cwd) || !localHermesLock(j.home) {
		return nil
	}
	input, ok := hermesTextInput(scope.Prompt)
	if !ok {
		return nil
	}
	path := filepath.Join(j.home, "state.db")
	file, err := os.Lstat(path)
	if err != nil || !file.Mode().IsRegular() {
		return nil
	}
	var before hermesUsageMark
	if !readHermesUsageDB(ctx, path, file, func(db *sqlite.Conn) bool {
		before, ok = readHermesUsageMark(db, id, scope.Cwd, j.home)
		return ok && verifyHermesUsageHistory(db, id, before)
	}) {
		return nil
	}
	return func(ctx context.Context, receipt acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
		var evidence acpclient.PromptJournalEvidence
		if !receipt.UsageReported || receipt.StopReason == nil || !localHermesLock(j.home) {
			return evidence
		}
		outcome := ""
		switch *receipt.StopReason {
		case acp.StopReasonEndTurn:
			outcome = "completed"
		case acp.StopReasonCancelled:
			outcome = "interrupted"
		default:
			return evidence
		}
		// This Gateway ACP version attaches Usage only after its own submission's
		// terminal (commands and voice-stop shortcuts return no Usage). It does
		// not expose its admission ID. Establish that exactly one new native
		// admission can be the matched submission; never choose by arrival order.
		if !readHermesUsageDB(ctx, path, file, func(db *sqlite.Conn) bool {
			after, ok := readHermesUsageMark(db, id, scope.Cwd, j.home)
			if !ok || after.epoch != before.epoch || after.owner != before.owner ||
				after.started != before.started || after.principal != before.principal || after.profile != before.profile {
				return false
			}
			delta := after.sequence - before.sequence
			if delta <= 0 || delta > hermesUsageAdmissionLimit {
				return false
			}
			count, ok := hermesUsageInteger(db,
				"SELECT count(*) FROM session_admissions WHERE seq>? AND seq<=?", before.sequence, after.sequence)
			if !ok || count != delta {
				return false // A deleted/pruned admission could have been ours.
			}
			if !verifyHermesMainLoop(db, id, before, after) {
				return false
			}
			admission, ok := findHermesAdmission(db, id, input, before, after, outcome)
			if !ok {
				return false
			}
			evidence = readHermesAdmissionUsage(db, admission, id)
			return evidence.Usage != nil
		}) {
			return acpclient.PromptJournalEvidence{}
		}
		return evidence
	}
}

func hermesTextInput(blocks []acp.ContentBlock) (string, bool) {
	var parts []string
	size := 0
	for _, block := range blocks {
		if block.Text == nil {
			return "", false
		}
		if text := block.Text.Text; text != "" {
			if len(text)+1 > hermesUsageInputLimit-size {
				return "", false
			}
			size += len(text) + 1
			parts = append(parts, text)
		}
	}
	text := strings.Join(parts, "\n")
	// Native ACP resolves slash commands before submitting and some commands
	// rewrite the admitted input. Python lstrip also accepts U+001C–U+001F.
	trimmed := strings.TrimLeftFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r >= '\x1c' && r <= '\x1f'
	})
	return text, trimmed != "" && !strings.HasPrefix(trimmed, "/") && utf8.ValidString(text)
}

func readHermesUsageDB(ctx context.Context, path string, file os.FileInfo, read func(*sqlite.Conn) bool) bool {
	sameFile := func() bool {
		current, err := os.Lstat(path)
		return err == nil && current.Mode().IsRegular() && os.SameFile(file, current)
	}
	if !sameFile() {
		return false
	}
	op, cancel := context.WithTimeout(ctx, hermesUsageReadTimeout)
	defer cancel()
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sqlite.OpenContext(op, uri.String())
	if err != nil {
		return false
	}
	defer db.Close()
	db.SetInterrupt(op)
	if db.BusyTimeout(0) != nil || db.Exec("BEGIN") != nil {
		return false
	}
	defer db.Exec("ROLLBACK")
	version, ok := hermesUsageInteger(db, "SELECT version FROM schema_version")
	return ok && version == hermesGatewaySchema && read(db) && sameFile()
}

func readHermesUsageMark(db *sqlite.Conn, id, cwd, home string) (hermesUsageMark, bool) {
	var mark hermesUsageMark
	stmt, _, err := db.Prepare(`
		SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='session_admissions'), 0),
			r.epoch, r.instance_id, CAST(s.started_at AS TEXT), s.cwd,
			json_extract(p.value, '$.principal_id'), json_extract(p.value, '$.profile_id'),
			json_extract(p.value, '$.session_id'), json_extract(p.value, '$.entry.session_id'),
			json_extract(p.value, '$.policy.source'), json_extract(p.value, '$.policy.cwd')
		FROM sessions AS s JOIN runtime_epoch AS r ON r.singleton=1
		JOIN state_meta AS p ON p.key=?
		WHERE s.id=? AND NOT EXISTS (
			SELECT 1 FROM session_admissions WHERE target_session_id=s.id
				AND status IN ('queued','started','unknown')
		)`)
	if err != nil {
		return mark, false
	}
	defer stmt.Close()
	if stmt.BindText(1, "gateway.local_policy.v1:"+id) != nil || stmt.BindText(2, id) != nil || !stmt.Step() {
		return mark, false
	}
	mark = hermesUsageMark{
		sequence: stmt.ColumnInt64(0), epoch: stmt.ColumnInt64(1), owner: stmt.ColumnText(2),
		started: stmt.ColumnText(3), principal: stmt.ColumnText(5), profile: stmt.ColumnText(6),
	}
	if mark.sequence < 0 || mark.epoch <= 0 || mark.owner == "" || mark.started == "" || mark.principal == "" ||
		!sameHermesUsageDirectory(mark.profile, home) || stmt.ColumnText(7) != id || stmt.ColumnText(8) != id || stmt.ColumnText(9) != "acp" ||
		!sameHermesUsageDirectory(stmt.ColumnText(4), cwd) || !sameHermesUsageDirectory(stmt.ColumnText(10), cwd) {
		return mark, false
	}
	if stmt.Step() || stmt.Err() != nil {
		return mark, false
	}
	main, ok := readHermesUsageCounts(db, `
		SELECT coalesce(sum(input_tokens+cache_read_tokens+cache_write_tokens),0),
			coalesce(sum(output_tokens),0), coalesce(sum(api_call_count),0)
		FROM session_model_usage WHERE session_id=? AND task=''`, func(s *sqlite.Stmt) bool {
		return s.BindText(1, id) == nil
	})
	mark.main = main
	return mark, ok
}

func sameHermesUsageDirectory(root, cwd string) bool {
	if !filepath.IsAbs(root) {
		return false
	}
	selected, err := os.Stat(cwd)
	if err != nil || !selected.IsDir() {
		return false
	}
	actual, err := os.Stat(root)
	return err == nil && actual.IsDir() && os.SameFile(selected, actual)
}

func findHermesAdmission(db *sqlite.Conn, id, input string, before, after hermesUsageMark, outcome string) (string, bool) {
	stmt, _, err := db.Prepare(`
		SELECT admission_id, request_id, principal_id, intent, status, outcome, owner_epoch, generation
		FROM session_admissions NOT INDEXED
		WHERE seq>? AND seq<=? AND target_session_id=? AND json_extract(payload_json, '$.text')=?
		ORDER BY seq LIMIT 2`)
	if err != nil {
		return "", false
	}
	defer stmt.Close()
	if stmt.BindInt64(1, before.sequence) != nil || stmt.BindInt64(2, after.sequence) != nil ||
		stmt.BindText(3, id) != nil || stmt.BindText(4, input) != nil || !stmt.Step() {
		return "", false
	}
	admission := stmt.ColumnText(0)
	if !hermesGatewayAdmissionID.MatchString(admission) || !hermesGatewayAdmissionID.MatchString(stmt.ColumnText(1)) ||
		stmt.ColumnText(2) != before.principal || stmt.ColumnText(3) != "queue" || stmt.ColumnText(4) != "terminal" ||
		stmt.ColumnText(5) != outcome || stmt.ColumnInt64(6) != before.epoch || stmt.ColumnInt64(7) <= 0 {
		return "", false
	}
	return admission, !stmt.Step() && stmt.Err() == nil
}

// verifyHermesUsageHistory excludes delayed writes from earlier admissions.
// Hermes can mark a receipt terminal even when its async ledger flush times
// out. Compare the prior receipt call count before measuring any new delta.
func verifyHermesUsageHistory(db *sqlite.Conn, id string, mark hermesUsageMark) bool {
	stmt, _, err := db.Prepare(`
		WITH history AS (
			SELECT admission_id, outcome, generation
			FROM session_admissions WHERE target_session_id=? AND status='terminal' AND seq<=?
			ORDER BY seq DESC LIMIT ?
		), calls AS (
			SELECT CASE
				WHEN h.generation IS NULL AND h.outcome IN ('cancelled','rejected') THEN 0
				WHEN json_type(m.value,'$.result.api_calls')='integer'
					AND json_extract(m.value,'$.result.api_calls')>=0
					THEN json_extract(m.value,'$.result.api_calls')
				END AS count
			FROM history AS h
			LEFT JOIN state_meta AS m ON m.key='gateway.admission.result.v1.'||h.admission_id
				AND length(CAST(m.value AS BLOB))<=?
		)
		SELECT coalesce(sum(count),0), count(*), count(count) FROM calls`)
	if err != nil {
		return false
	}
	defer stmt.Close()
	if stmt.BindText(1, id) != nil || stmt.BindInt64(2, mark.sequence) != nil ||
		stmt.BindInt(3, hermesUsageHistoryLimit+1) != nil || stmt.BindInt(4, hermesUsageResultLimit) != nil || !stmt.Step() {
		return false
	}
	total, records, verified := stmt.ColumnInt64(0), stmt.ColumnInt64(1), stmt.ColumnInt64(2)
	return total == mark.main.calls && records == verified && records <= hermesUsageHistoryLimit &&
		!stmt.Step() && stmt.Err() == nil
}

// verifyHermesMainLoop reconciles every admission in the bounded window with
// the separately persisted main-loop counters. A late cache warm can inflate
// the owner's admission delta while being recorded under its own task.
func verifyHermesMainLoop(db *sqlite.Conn, id string, before, after hermesUsageMark) bool {
	delta := hermesUsageCounts{
		input: after.main.input - before.main.input, output: after.main.output - before.main.output,
		calls: after.main.calls - before.main.calls,
	}
	totals, ok := readHermesUsageCounts(db, `
		SELECT sum(json_extract(m.value,'$.usage.input_tokens')),
			sum(json_extract(m.value,'$.usage.output_tokens')),
			sum(json_extract(m.value,'$.result.api_calls'))
		FROM session_admissions AS a NOT INDEXED
		LEFT JOIN state_meta AS m ON m.key='gateway.admission.result.v1.'||a.admission_id
			AND length(CAST(m.value AS BLOB))<=?
		WHERE a.seq>? AND a.seq<=? AND a.target_session_id=?
		HAVING count(*)>0 AND count(*)=sum(CASE WHEN
			a.status='terminal' AND a.owner_epoch=?
			AND json_extract(m.value,'$.result.session_id')=a.target_session_id
			AND json_type(m.value,'$.usage.input_tokens')='integer'
			AND json_type(m.value,'$.usage.output_tokens')='integer'
			AND json_type(m.value,'$.usage.total_tokens')='integer'
			AND json_type(m.value,'$.result.api_calls')='integer'
			AND json_extract(m.value,'$.usage.input_tokens')>=0
			AND json_extract(m.value,'$.usage.output_tokens')>=0
			AND json_extract(m.value,'$.usage.total_tokens')=
				json_extract(m.value,'$.usage.input_tokens')+json_extract(m.value,'$.usage.output_tokens')
			AND json_extract(m.value,'$.result.api_calls')>=0
			THEN 1 ELSE 0 END)`, func(s *sqlite.Stmt) bool {
		return s.BindInt(1, hermesUsageResultLimit) == nil && s.BindInt64(2, before.sequence) == nil &&
			s.BindInt64(3, after.sequence) == nil && s.BindText(4, id) == nil && s.BindInt64(5, before.epoch) == nil
	})
	return ok && totals == delta && totals.calls > 0
}

func readHermesUsageCounts(db *sqlite.Conn, query string, bind func(*sqlite.Stmt) bool) (hermesUsageCounts, bool) {
	stmt, _, err := db.Prepare(query)
	if err != nil {
		return hermesUsageCounts{}, false
	}
	defer stmt.Close()
	if !bind(stmt) || !stmt.Step() {
		return hermesUsageCounts{}, false
	}
	for i := range 3 {
		if stmt.ColumnType(i) != sqlite.INTEGER || stmt.ColumnInt64(i) < 0 {
			return hermesUsageCounts{}, false
		}
	}
	counts := hermesUsageCounts{input: stmt.ColumnInt64(0), output: stmt.ColumnInt64(1), calls: stmt.ColumnInt64(2)}
	return counts, !stmt.Step() && stmt.Err() == nil
}

func readHermesAdmissionUsage(db *sqlite.Conn, admission, session string) acpclient.PromptJournalEvidence {
	var evidence acpclient.PromptJournalEvidence
	stmt, _, err := db.Prepare(`
		SELECT json_extract(value, '$.usage.input_tokens'), json_extract(value, '$.usage.output_tokens'),
			json_extract(value, '$.usage.total_tokens'), json_extract(value, '$.result.session_id'),
			json_extract(value, '$.result.api_calls'), json_extract(value, '$.result.model'),
			json_extract(value, '$.result.served_model'), json_extract(value, '$.result.provider')
		FROM state_meta WHERE key=? AND length(CAST(value AS BLOB))<=?`)
	if err != nil {
		return evidence
	}
	defer stmt.Close()
	if stmt.BindText(1, "gateway.admission.result.v1."+admission) != nil ||
		stmt.BindInt(2, hermesUsageResultLimit) != nil || !stmt.Step() {
		return evidence
	}
	for _, column := range []int{0, 1, 2, 4} {
		if stmt.ColumnType(column) != sqlite.INTEGER {
			return evidence
		}
	}
	input, output, total, calls := stmt.ColumnInt64(0), stmt.ColumnInt64(1), stmt.ColumnInt64(2), stmt.ColumnInt64(4)
	if input < 0 || output < 0 || input > math.MaxInt64-output || total != input+output ||
		total == 0 || calls <= 0 || stmt.ColumnText(3) != session {
		return evidence
	}
	model := ""
	if calls == 1 && stmt.ColumnText(7) != "moa" {
		model = stmt.ColumnText(6)
		if model == "" {
			model = stmt.ColumnText(5)
		}
		if !hermesUsageModelID.MatchString(model) || strings.Contains(model, "://") {
			model = ""
		}
	}
	// Input here is the gross prompt count. The receipt supplies no per-turn
	// cache breakdown, so retain it separately from verified output.
	return acpclient.PromptJournalEvidence{NativeTurnID: admission, Usage: &acpmeta.TurnUsage{
		Source: hermesUsageSource, Coverage: "native_turn", Model: model, OutputTokens: acp.Ptr(output),
		Observations: []acpmeta.UsageObservation{{
			Source: hermesUsageSource, Coverage: "provider_input_total",
			InputTotalTokens: acp.Ptr(input), TotalTokens: acp.Ptr(total),
		}},
	}}
}

func hermesUsageInteger(db *sqlite.Conn, query string, args ...int64) (int64, bool) {
	stmt, _, err := db.Prepare(query)
	if err != nil {
		return 0, false
	}
	defer stmt.Close()
	for i, value := range args {
		if stmt.BindInt64(i+1, value) != nil {
			return 0, false
		}
	}
	if !stmt.Step() || stmt.ColumnType(0) != sqlite.INTEGER {
		return 0, false
	}
	value := stmt.ColumnInt64(0)
	return value, !stmt.Step() && stmt.Err() == nil
}
