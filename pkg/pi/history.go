package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/JieWaZi/acp-go/internal/sessionfiles"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type header struct {
	// Type distinguishes the native session header from a journal entry.
	Type string `json:"type"`
	// ID is the Pi identity of this transcript.
	ID string `json:"id"`
	// Cwd is the workspace persisted by Pi for resumption.
	Cwd string `json:"cwd"`
}

func (c *Client) sessionRoot(cwd string) string {
	override := c.getenv("PI_CODING_AGENT_SESSION_DIR")
	for i, arg := range c.options.PrefixArgs {
		if arg == "--session-dir" && i+1 < len(c.options.PrefixArgs) {
			override = c.options.PrefixArgs[i+1]
		}
	}
	root := c.getenv("PI_CODING_AGENT_DIR")
	if root == "" {
		home := c.getenv("HOME")
		if home == "" {
			home = c.getenv("USERPROFILE")
		}
		root = filepath.Join(home, ".pi", "agent")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(cwd, root)
	}
	if override == "" {
		for _, path := range []string{filepath.Join(root, "settings.json"), filepath.Join(cwd, ".pi", "settings.json")} {
			var settings struct {
				// SessionDir overrides the account transcript directory.
				SessionDir string `json:"sessionDir"`
			}
			if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &settings) == nil && settings.SessionDir != "" {
				override = settings.SessionDir
			}
		}
	}
	if override == "" {
		return filepath.Join(root, "sessions")
	}
	if !filepath.IsAbs(override) {
		return filepath.Join(cwd, override)
	}
	return filepath.Clean(override)
}

// containsHistory checks native-returned paths before opening or creating
// any Go-owned file. Missing path components are allowed for a new session.
func (c *Client) containsHistory(cwd, file string) bool {
	if !filepath.IsAbs(file) || filepath.Ext(file) != ".jsonl" {
		return false
	}
	root, err := resolveHistoryPath(c.sessionRoot(cwd))
	if err != nil {
		return false
	}
	resolved, err := resolveHistoryPath(file)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, resolved)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

func resolveHistoryPath(path string) (string, error) {
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return filepath.EvalSymlinks(path)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", os.ErrNotExist
	}
	resolved, err := resolveHistoryPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func sessionHeader(path string) (header, error) {
	var item header
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return item, missingSession()
	}
	file, err := os.Open(path)
	if err != nil {
		return item, missingSession()
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	if !scan.Scan() || json.Unmarshal(scan.Bytes(), &item) != nil || item.Type != "session" || item.ID == "" || !filepath.IsAbs(item.Cwd) {
		return item, missingSession()
	}
	return item, nil
}

func (c *Client) findSession(ctx context.Context, id acp.SessionId, cwd string) (string, error) {
	if id == "" || !filepath.IsAbs(cwd) {
		return "", acp.NewInvalidParams(nil)
	}
	root := c.sessionRoot(cwd)
	if filepath.IsAbs(string(id)) {
		file := filepath.Clean(string(id))
		if !c.containsHistory(cwd, file) {
			return "", missingSession()
		}
		item, err := sessionHeader(file)
		if err != nil || !samePath(item.Cwd, cwd) {
			return "", missingSession()
		}
		return file, nil
	}
	var found string
	err := walkSessionFiles(ctx, root, func(path string, item header, _ fs.FileInfo) error {
		if item.ID == string(id) && samePath(item.Cwd, cwd) {
			if found != "" {
				return errors.New("Pi legacy session ID is ambiguous")
			}
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", missingSession()
	}
	return found, nil
}

func walkSessionFiles(ctx context.Context, root string, visit func(string, header, fs.FileInfo) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("Pi native history could not be read")
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		item, err := sessionHeader(path)
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		return visit(path, item, info)
	})
}

func (c *Client) ListSessions(ctx context.Context, r acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	cwd := c.options.WorkingDirectory
	if r.Cwd != nil {
		cwd = *r.Cwd
		if !filepath.IsAbs(cwd) {
			return acp.ListSessionsResponse{}, acp.NewInvalidParams(nil)
		}
	}
	items := make([]acp.SessionInfo, 0)
	err := walkSessionFiles(ctx, c.sessionRoot(cwd), func(path string, item header, info fs.FileInfo) error {
		if r.Cwd == nil || samePath(item.Cwd, cwd) {
			items = append(items, acp.SessionInfo{
				SessionId: acp.SessionId(path), Cwd: item.Cwd,
				UpdatedAt: pointer(info.ModTime().UTC().Format(time.RFC3339Nano)),
			})
		}
		return nil
	})
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}
	sort.Slice(items, func(i, j int) bool {
		if *items[i].UpdatedAt == *items[j].UpdatedAt {
			return items[i].SessionId < items[j].SessionId
		}
		a, _ := time.Parse(time.RFC3339Nano, *items[i].UpdatedAt)
		b, _ := time.Parse(time.RFC3339Nano, *items[j].UpdatedAt)
		return a.After(b)
	})
	offset := 0
	if r.Cursor != nil {
		offset, err = strconv.Atoi(*r.Cursor)
		if err != nil || offset < 0 || offset > len(items) {
			return acp.ListSessionsResponse{}, acp.NewInvalidParams(nil)
		}
	}
	end := min(offset+100, len(items))
	result := acp.ListSessionsResponse{Sessions: items[offset:end]}
	if end < len(items) {
		result.NextCursor = pointer(strconv.Itoa(end))
	}
	return result, nil
}

func (s *session) replay(ctx context.Context) error {
	if s.client.host.Observe == nil {
		return nil
	}
	end, err := s.begin()
	if err != nil {
		return err
	}
	defer end()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var response struct {
		// Messages contains the visible messages of the active native branch.
		Messages []nativeMessage
	}
	if err := s.rpc.call(ctx, "get_messages", nil, &response); err != nil {
		return err
	}
	for _, msg := range response.Messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		for _, content := range messageContent(msg.Content) {
			update := acp.UpdateUserMessage(content)
			if msg.Role == "assistant" {
				update = acp.UpdateAgentMessage(content)
			}
			if err := s.client.host.Observe(ctx, agentclient.Scope{SessionID: s.id}, acp.SessionNotification{SessionId: s.id, Update: update}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) UnstableForkSession(ctx context.Context, r acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(r) {
		id, err := acpmeta.ReadForkReceipt(r)
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}
	if acpmeta.ForkPosition(r.Meta) != "" {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "Pi clone supports the current native branch position"})
	}
	unlock, err := c.lockOpen(ctx)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	defer unlock()
	if acpmeta.VerifiedForkMode(c.version, "0.85.1", acpmeta.ForkLatest) == acpmeta.ForkUnsupported {
		return acp.UnstableForkSessionResponse{}, acp.NewMethodNotFound("session/fork")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if source, err := c.session(r.SessionId); err == nil {
		end, err := source.begin()
		if err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
		defer end()
	}
	cwd, _ := r.Meta["sourceCwd"].(string)
	if cwd == "" {
		cwd = r.Cwd
	}
	file, err := c.findSession(ctx, r.SessionId, cwd)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	servers, err := acpmeta.ForkMCPServers(r.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	config, err := openResources(r.Meta, r.AdditionalDirectories, servers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err := validateRoots(r.Cwd, config.directories); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	clone, err := c.start(ctx, cwd, []string{"--no-tools", "--no-extensions"}, resources{probe: true}, file)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	defer func() { _ = c.retire(clone) }()
	source, err := clone.state(ctx)
	if err != nil || !samePath(file, source.SessionFile) || source.SessionID == "" {
		return acp.UnstableForkSessionResponse{}, errors.Join(err, errors.New("Pi did not confirm the source session"))
	}
	if err := acpmeta.BeginForkReceipt(r); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	var result struct {
		// Cancelled indicates that native policy refused the clone.
		Cancelled bool
	}
	if err := clone.rpc.call(ctx, "clone", nil, &result); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if result.Cancelled {
		return acp.UnstableForkSessionResponse{}, errors.New("Pi cancelled native clone")
	}
	child, err := clone.state(ctx)
	if err != nil || !c.containsHistory(cwd, child.SessionFile) || !c.containsHistory(r.Cwd, child.SessionFile) || samePath(child.SessionFile, file) || child.SessionID == "" || child.SessionID == source.SessionID {
		return acp.UnstableForkSessionResponse{}, errors.Join(err, errors.New("Pi did not confirm an independent clone"))
	}
	if err := c.retire(clone); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if !samePath(cwd, r.Cwd) {
		if err := rebindClone(child.SessionFile, child.SessionID, r.Cwd); err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
	}
	id := acp.SessionId(child.SessionFile)
	if err := acpmeta.WriteForkReceipt(r, id); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	_, options, err := c.open(ctx, r.Cwd, id, child.SessionFile, config)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(r.SessionId, acp.NewSessionResponse{SessionId: id, ConfigOptions: options})
}

// Only the clone's header is rebound. Its tree, entries and parent are kept intact.
func rebindClone(path, id, cwd string) error {
	item, err := sessionHeader(path)
	if err != nil || item.ID != id {
		return errors.New("Pi clone header could not be verified")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	read := bufio.NewReaderSize(file, 64<<10)
	line, err := read.ReadSlice('\n')
	if err != nil {
		return errors.New("Pi clone header exceeds its expected record budget")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return err
	}
	fields["cwd"], _ = json.Marshal(cwd)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".acp-pi-fork-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(fields)
	if err == nil {
		_, err = io.Copy(tmp, read)
	}
	err = errors.Join(err, tmp.Sync(), tmp.Close(), file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return sessionfiles.SyncDirectory(filepath.Dir(path))
}

// UnstableDeleteSession removes exactly the native history identified by the
// session request after preventing concurrent activity in this client.
func (c *Client) UnstableDeleteSession(ctx context.Context, r acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error) {
	unlock, err := c.lockOpen(ctx)
	if err != nil {
		return acp.UnstableDeleteSessionResponse{}, err
	}
	defer unlock()
	cwd, _ := r.Meta["cwd"].(string)
	if opened, err := c.session(r.SessionId); err == nil {
		cwd = opened.cwd
	}
	if cwd == "" && filepath.IsAbs(string(r.SessionId)) {
		item, err := sessionHeader(string(r.SessionId))
		if err != nil {
			return acp.UnstableDeleteSessionResponse{}, err
		}
		cwd = item.Cwd
	}
	if cwd == "" {
		cwd = c.options.WorkingDirectory
	}
	file, err := c.findSession(ctx, r.SessionId, cwd)
	if err != nil {
		return acp.UnstableDeleteSessionResponse{}, err
	}
	c.mu.Lock()
	var opened *session
	for _, s := range c.sessions {
		if samePath(s.file, file) {
			opened = s
			break
		}
	}
	c.mu.Unlock()
	if opened != nil {
		opened.mu.Lock()
		if opened.busy {
			opened.mu.Unlock()
			return acp.UnstableDeleteSessionResponse{}, errors.New("Pi session has an active operation")
		}
		opened.closing = true
		opened.mu.Unlock()
		if err := c.release(ctx, opened); err != nil {
			return acp.UnstableDeleteSessionResponse{}, err
		}
	}
	if err := context.Cause(ctx); err != nil {
		return acp.UnstableDeleteSessionResponse{}, err
	}
	if err := os.Remove(file); err != nil {
		return acp.UnstableDeleteSessionResponse{}, errors.New("Pi native session could not be deleted")
	}
	return acp.UnstableDeleteSessionResponse{}, sessionfiles.SyncDirectory(filepath.Dir(file))
}
