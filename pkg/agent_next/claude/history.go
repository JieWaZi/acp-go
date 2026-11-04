package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const maxHistoryBytes = 64 << 20

var localCommandMarkup = regexp.MustCompile(`(?s)<(?:command-name|command-message|command-args|local-command-stdout|local-command-stderr)>.*?</(?:command-name|command-message|command-args|local-command-stdout|local-command-stderr)>`)

type transcriptRow struct {
	Kind           string  `json:"type"`
	ID             string  `json:"uuid"`
	SessionID      string  `json:"sessionId"`
	Sidechain      bool    `json:"isSidechain"`
	Meta           bool    `json:"isMeta"`
	Parent         *string `json:"parent_tool_use_id"`
	CompactSummary bool    `json:"isCompactSummary"`
	Message        struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (c *Client) transcript(id, cwd string) (string, error) {
	if !isUUID(id) {
		return "", errors.New("invalid Claude transcript session ID")
	}
	dir := c.environment("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home := firstString(c.environment("HOME"), c.environment("USERPROFILE"))
		if home == "" {
			return "", errors.New("Claude account home directory is unavailable")
		}
		dir = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.options.WorkingDirectory, dir)
	}
	project := filepath.Join(dir, "projects", projectKey(cwd))
	path := filepath.Join(project, id+".jsonl")
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("Claude transcript is unavailable for this workspace and account")
	}
	if !info.Mode().IsRegular() || info.Size() > maxHistoryBytes {
		return "", errors.New("Claude transcript exceeds verified read boundary")
	}
	return path, nil
}

// projectKey follows the CLI's persisted workspace key, including its JavaScript
// UTF-16 hash for paths exceeding the native key length limit.
func projectKey(cwd string) string {
	var out strings.Builder
	var hash int32
	for _, unit := range utf16.Encode([]rune(cwd)) {
		hash = 31*hash + int32(unit)
		if unit >= 'a' && unit <= 'z' || unit >= 'A' && unit <= 'Z' || unit >= '0' && unit <= '9' {
			out.WriteByte(byte(unit))
		} else {
			out.WriteByte('-')
		}
	}
	key := out.String()
	if len(key) <= 200 {
		return key
	}
	n := int64(hash)
	if n < 0 {
		n = -n
	}
	return key[:200] + "-" + strconv.FormatInt(n, 36)
}

func scanTranscript(ctx context.Context, path, id string, visit func(transcriptRow) error) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("could not open Claude transcript")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxHistoryBytes {
		return errors.New("Claude transcript exceeds verified read boundary")
	}
	reader := &io.LimitedReader{R: f, N: maxHistoryBytes + 1}
	scan := bufio.NewScanner(reader)
	scan.Buffer(make([]byte, 64<<10), maxNativeFrame)
	for scan.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := decodeOptional[transcriptRow](scan.Bytes())
		if row == nil {
			return errors.New("invalid Claude transcript row")
		}
		if row.Sidechain || row.Parent != nil || row.Meta || row.Kind != "user" && row.Kind != "assistant" || len(row.Message.Content) == 0 {
			continue
		}
		if row.SessionID != id {
			return errors.New("Claude transcript row has an inconsistent session ID")
		}
		if err := visit(*row); err != nil {
			return err
		}
	}
	if scan.Err() != nil || reader.N == 0 {
		return errors.New("Claude transcript exceeds verified read boundary")
	}
	return ctx.Err()
}

func transcriptDigest(ctx context.Context, path, id string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	hash := sha256.New()
	count := 0
	err := scanTranscript(ctx, path, id, func(row transcriptRow) error {
		var b bytes.Buffer
		if err := json.Compact(&b, row.Message.Content); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s %s\n", row.Kind, b.Bytes())
		count++
		return nil
	})
	if err != nil {
		return sum, err
	}
	if count == 0 {
		return sum, errors.New("Claude transcript has no conversation context")
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func (s *session) replay(ctx context.Context) error {
	path, err := s.client.transcript(string(s.id), s.launch.cwd)
	if err != nil {
		return err
	}
	if s.client.host.Observe == nil {
		return nil
	}
	skippingCompact := false
	return scanTranscript(ctx, path, string(s.id), func(row transcriptRow) error {
		if row.CompactSummary {
			skippingCompact = true
			return nil
		}
		if row.Kind == "user" {
			skippingCompact = false
		}
		if skippingCompact {
			return nil
		}
		var blocks []protocol.ContentBlock
		if text := decodeOptional[string](row.Message.Content); text != nil {
			blocks = []protocol.ContentBlock{{Type: "text", Text: *text}}
		} else if values := decodeOptional[[]protocol.ContentBlock](row.Message.Content); values != nil {
			blocks = *values
		}
		for _, block := range blocks {
			update, ok := historyUpdate(row, block)
			if !ok {
				continue
			}
			if err := s.replayObservation(ctx, update); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *session) replayObservation(ctx context.Context, update acp.SessionUpdate) error {
	reply := make(chan error, 1)
	s.mu.Lock()
	s.taskLocked(func() {
		reply <- s.client.host.Observe(ctx, agentclient.Scope{SessionID: s.id}, acp.SessionNotification{SessionId: s.id, Update: update})
	})
	s.mu.Unlock()
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func historyUpdate(row transcriptRow, block protocol.ContentBlock) (acp.SessionUpdate, bool) {
	var id *string
	if isUUID(row.ID) {
		id = &row.ID
	}
	switch block.Type {
	case "text":
		text := strings.TrimSpace(localCommandMarkup.ReplaceAllString(block.Text, ""))
		if text == "" || text == "[Request interrupted by user]" || strings.HasPrefix(text, "<task-notification>") || strings.HasPrefix(text, "This session is being continued from a previous conversation") {
			return acp.SessionUpdate{}, false
		}
		if row.Kind == "user" {
			update := acp.UpdateUserMessageText(text)
			update.UserMessageChunk.MessageId = id
			return update, true
		}
		update := acp.UpdateAgentMessageText(text)
		update.AgentMessageChunk.MessageId = id
		return update, true
	case "image":
		if row.Kind == "user" && block.Source != nil && block.Source.Type == "base64" && block.Source.Data != "" && block.Source.MediaType != "" {
			update := acp.UpdateUserMessage(acp.ImageBlock(block.Source.Data, block.Source.MediaType))
			update.UserMessageChunk.MessageId = id
			return update, true
		}
	case "thinking":
		if row.Kind == "assistant" && block.Thinking != "" {
			update := acp.UpdateAgentThoughtText(block.Thinking)
			update.AgentThoughtChunk.MessageId = id
			return update, true
		}
	case "tool_use":
		if row.Kind == "assistant" && block.ID != "" {
			return acp.StartToolCall(acp.ToolCallId(block.ID), block.Name, acp.WithStartKind(toolKind(block.Name)),
				acp.WithStartRawInput(jsonValue(block.Input)), acp.WithStartLocations(toolLocations(block.Input))), true
		}
	case "tool_result":
		if row.Kind == "user" && block.ToolUseID != "" {
			status := acp.ToolCallStatusCompleted
			if block.IsError {
				status = acp.ToolCallStatusFailed
			}
			return acp.UpdateToolCall(acp.ToolCallId(block.ToolUseID), acp.WithUpdateStatus(status),
				acp.WithUpdateRawOutput(jsonValue(block.Content))), true
		}
	}
	return acp.SessionUpdate{}, false
}

func (c *Client) forkMode() string {
	return acpmeta.VerifiedForkModeInMinor(c.version, "2.1.309", acpmeta.ForkLatest)
}

// UnstableForkSession opens the native child and verifies its independent
// transcript. A confirmed child ID survives subsequent initialization or cleanup failure.
func (c *Client) UnstableForkSession(ctx context.Context, req acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if err := c.ready(); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	cwd, err := realDirectory(req.Cwd)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if acpmeta.ReconcileOnly(req) {
		id, err := acpmeta.ReadForkReceipt(req)
		if err == nil {
			var path string
			path, err = c.transcript(string(id), cwd)
			if err == nil {
				_, err = transcriptDigest(ctx, path, string(id))
			}
		}
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}
	if c.forkMode() != acpmeta.ForkLatest {
		return acp.UnstableForkSessionResponse{}, acp.NewMethodNotFound("session/fork")
	}
	if !isUUID(string(req.SessionId)) || acpmeta.ForkPosition(req.Meta) != "" {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "Claude requires a native UUID and supports latest fork only"})
	}
	sourceCwd := cwd
	if value, ok := req.Meta["sourceCwd"].(string); ok && value != "" {
		sourceCwd, err = realDirectory(value)
		if err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
	}
	servers, err := acpmeta.ForkMCPServers(req.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if _, err := acpmeta.DeveloperInstructions(req.Meta); err != nil {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(nil)
	}
	finish, err := c.beginSessionOperation(req.SessionId)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	defer finish()

	c.mu.Lock()
	source := c.sessions[req.SessionId]
	c.mu.Unlock()
	if source != nil {
		source.mu.Lock()
		if source.open && !source.closing && source.busyLocked() {
			source.mu.Unlock()
			return acp.UnstableForkSessionResponse{}, errBusy
		}
		if source.launch.cwd != sourceCwd {
			source.mu.Unlock()
			return acp.UnstableForkSessionResponse{}, errors.New("Claude fork source workspace differs from native generation")
		}
		source.frozen = true
		source.mu.Unlock()
		if err := c.release(ctx, source); err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
	}
	path, err := c.transcript(string(req.SessionId), sourceCwd)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	original, err := transcriptDigest(ctx, path, string(req.SessionId))
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	sourceInfo, err := os.Stat(path)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, acpmeta.ErrForkUnconfirmed
	}
	id, err := newUUID()
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err := acpmeta.BeginForkReceipt(req); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	confirmed := false
	s, err := c.open(ctx, openRequest{
		id: acp.SessionId(id), cwd: cwd, additional: req.AdditionalDirectories, mcp: servers, meta: req.Meta,
		resume: true, forkSource: string(req.SessionId), confirm: func(ctx context.Context, s *session) error {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				child, err := c.transcript(id, cwd)
				if err == nil {
					childInfo, statErr := os.Stat(child)
					if statErr == nil && !os.SameFile(sourceInfo, childInfo) {
						digest, digestErr := transcriptDigest(ctx, child, id)
						if digestErr == nil && original == digest {
							confirmed = true
							return acpmeta.WriteForkReceipt(req, acp.SessionId(id))
						}
					}
				}
				select {
				case <-ctx.Done():
					return errors.Join(acpmeta.ErrForkUnconfirmed, ctx.Err())
				case <-s.wire.done:
					return acpmeta.ErrForkUnconfirmed
				case <-timer.C:
					return acpmeta.ErrForkUnconfirmed
				case <-tick.C:
				}
			}
		},
	})
	if err != nil {
		if confirmed {
			return acp.UnstableForkSessionResponse{SessionId: acp.SessionId(id)}, err
		}
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(req.SessionId, s.response())
}
