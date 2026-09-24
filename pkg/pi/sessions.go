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
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// NewSession 创建独立 Pi 原生会话及其 MCP 快照。
func (a *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := validateAdditionalDirectories(request.AdditionalDirectories); err != nil {
		return acp.NewSessionResponse{}, err
	}
	s, options, err := a.open(ctx, request.Cwd, "", request.McpServers)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	options["sessionId"] = s.id
	response, err := convert[acp.NewSessionResponse](options)
	if err != nil {
		a.closeFailedSession(s)
	}
	return response, err
}

// open 对照 pi-acp 的创建/加载语义，直接启动 Pi 并读取真实模型目录。
func (a *Agent) open(ctx context.Context, cwd, file string, servers []acp.McpServer) (*session, map[string]any, error) {
	if err := a.opening.Lock(ctx); err != nil {
		return nil, nil, err
	}
	defer a.opening.Unlock()
	if !filepath.IsAbs(cwd) {
		return nil, nil, acp.NewInvalidParams(map[string]any{"message": "cwd must be absolute"})
	}
	a.mutex.Lock()
	closed := a.closed
	a.mutex.Unlock()
	if closed {
		return nil, nil, errors.New("Pi agent closed")
	}
	if file != "" {
		a.mutex.Lock()
		var previous *session
		for id, candidate := range a.sessions {
			if candidate.file == file {
				previous = candidate
				delete(a.sessions, id)
				break
			}
		}
		a.mutex.Unlock()
		if previous != nil {
			if err := previous.close(ctx); err != nil {
				return nil, nil, err
			}
		}
	}
	directory, err := os.MkdirTemp(a.directory, "session-")
	if err != nil {
		return nil, nil, err
	}
	extension := filepath.Join(directory, "extension.ts")
	if err = writeExtension(extension, a.modulePath, a.config.PermissionMode, servers); err != nil {
		_ = os.RemoveAll(directory)
		return nil, nil, err
	}
	p, err := startRPC(a.config, cwd, extension, file)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, nil, err
	}
	failed := true
	defer func() {
		if failed {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = p.close(cleanup)
			_ = os.RemoveAll(directory)
		}
	}()
	var state map[string]any
	if err = p.call(ctx, "get_state", nil, &state); err != nil {
		return nil, nil, err
	}
	startupEvents, err := p.awaitExtensionReady(ctx)
	if err != nil {
		return nil, nil, err
	}
	id := text(state["sessionId"])
	file = text(state["sessionFile"])
	if id == "" || file == "" {
		return nil, nil, errors.New("Pi did not return a session identity")
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return nil, nil, err
	}
	s := &session{id: acp.SessionId(id), cwd: cwd, file: file, directory: directory, process: p, startupEvents: startupEvents, tools: map[string]string{}, snapshots: map[string]fileSnapshot{}, bashOutput: map[string]bashOutputState{}, eventsDone: make(chan struct{})}
	options, err := s.configuration(ctx)
	if err != nil {
		return nil, nil, err
	}
	a.mutex.Lock()
	if a.closed {
		a.mutex.Unlock()
		return nil, nil, errors.New("Pi agent closed")
	}
	previous := a.sessions[s.id]
	a.sessions[s.id] = s
	a.mutex.Unlock()
	go a.events(s)
	if previous != nil && previous != s {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = previous.close(cleanup)
		cancel()
	}
	failed = false

	if err = a.commands(ctx, s); err != nil {
		a.closeFailedSession(s)
		return nil, nil, err
	}
	return s, options, nil
}

// LoadSession 使用真实历史文件恢复，并重建本次 MCP 快照及历史消息。
func (a *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if err := validateAdditionalDirectories(request.AdditionalDirectories); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	file, err := a.findSession(request.SessionId, request.Cwd)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	s, options, err := a.open(ctx, request.Cwd, file, request.McpServers)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	loaded := false
	defer func() {
		if !loaded {
			a.closeFailedSession(s)
		}
	}()
	var history map[string]any
	if err = s.process.call(ctx, "get_messages", nil, &history); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	for _, entry := range list(history["messages"]) {
		message := object(entry)
		role := text(message["role"])
		kind := "agent_message_chunk"
		if role == "user" {
			kind = "user_message_chunk"
		}
		if role == "assistant" || role == "user" {
			for _, block := range contentBlocks(message["content"]) {
				if err = a.emit(ctx, s, map[string]any{"sessionUpdate": kind, "content": block}); err != nil {
					return acp.LoadSessionResponse{}, err
				}
			}
		}
		if role == "toolResult" {
			id := text(message["toolCallId"])
			if id != "" {
				_ = a.emit(ctx, s, map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": text(message["toolName"]), "status": historyToolStatus(message), "content": toolContents(message["content"])})
			}
		}
	}
	response, err := convert[acp.LoadSessionResponse](options)
	loaded = err == nil
	return response, err
}

// ResumeSession 与 load 使用相同的配置刷新，但不回放历史。
func (a *Agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if err := validateAdditionalDirectories(request.AdditionalDirectories); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	file, err := a.findSession(request.SessionId, request.Cwd)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	s, options, err := a.open(ctx, request.Cwd, file, request.McpServers)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	response, err := convert[acp.ResumeSessionResponse](options)
	if err != nil {
		a.closeFailedSession(s)
	}
	return response, err
}

// closeFailedSession 只移除仍指向失败实例的注册项，避免并发恢复覆盖新 Session。
func (a *Agent) closeFailedSession(s *session) {
	a.mutex.Lock()
	if a.sessions[s.id] != s {
		a.mutex.Unlock()
		return
	}
	delete(a.sessions, s.id)
	a.mutex.Unlock()
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.close(cleanup)
}

// storedSession 是读取 Pi 原生文件所得的索引，不复制或维护第二份历史。
type storedSession struct {
	// id 是首行 session.id。
	id string
	// cwd 是原生记录所属工作目录。
	cwd string
	// file 是实际 JSONL 文件路径。
	file string
	// title 是 Pi 会话名称或最近用户消息。
	title string
	// updated 是文件修改时间。
	updated time.Time
}

// storedCacheEntry 缓存文件摘要；加载会话时仍重新核对原生文件头。
type storedCacheEntry struct {
	// item 是一次完整扫描取得的列表摘要。
	item storedSession
	// size 与 item.updated 一起判定原生文件是否变化。
	size int64
}

// errHistoryLineTooLarge 表示单条历史记录过大，但读取器已跳到下一行。
var errHistoryLineTooLarge = errors.New("Pi history line exceeds 16 MiB")

// readHistoryLine 有界读取一行，并跳过超大记录以继续扫描后续会话元数据。
func readHistoryLine(reader *bufio.Reader) ([]byte, error) {
	const maxHistoryLineBytes = 16 << 20
	var line []byte
	tooLarge := false
	for {
		part, err := reader.ReadSlice('\n')
		if !tooLarge {
			if len(line)+len(part) > maxHistoryLineBytes {
				tooLarge = true
			} else {
				line = append(line, part...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(part) == 0 && len(line) == 0 && !tooLarge {
				return nil, io.EOF
			}
			if tooLarge {
				return nil, errHistoryLineTooLarge
			}
			return line, nil
		}
		if err != nil {
			return nil, err
		}
		if tooLarge {
			return nil, errHistoryLineTooLarge
		}
		return line, nil
	}
}

// sessionRoot 对照 pi-acp 读取 Pi 原生目录和官方 sessionDir 设置。
func (a *Agent) sessionRoot() string {
	root := nativeacp.EnvironmentValue(a.config.Environment, "PI_CODING_AGENT_DIR")
	if root == "" {
		home := nativeacp.EnvironmentValue(a.config.Environment, "HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		root = filepath.Join(home, ".pi", "agent")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(a.config.WorkingDirectory, root)
	}
	data, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err == nil {
		var settings map[string]any
		if json.Unmarshal(data, &settings) == nil {
			if dir := text(settings["sessionDir"]); dir != "" {
				if filepath.IsAbs(dir) {
					return dir
				}
				return filepath.Join(root, dir)
			}
		}
	}
	return filepath.Join(root, "sessions")
}

// stored 只扫描原生 session 头，不把任意文件名当作可加载路径。
func (a *Agent) stored() ([]storedSession, error) {
	result := []storedSession{}
	seen := map[string]bool{}
	err := filepath.WalkDir(a.sessionRoot(), func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		seen[path] = true
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			a.historyMu.Lock()
			cached, ok := a.historyCache[path]
			a.historyMu.Unlock()
			if ok && cached.size == info.Size() && cached.item.updated.Equal(info.ModTime()) {
				result = append(result, cached.item)
				return nil
			}
		}
		a.historyMu.Lock()
		delete(a.historyCache, path)
		a.historyMu.Unlock()
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		reader := bufio.NewReaderSize(f, 64*1024)
		first, err := readHistoryLine(reader)
		if errors.Is(err, io.EOF) || errors.Is(err, errHistoryLineTooLarge) {
			return nil
		}
		if err != nil {
			return err
		}
		var header map[string]any
		if json.Unmarshal(first, &header) != nil || header["type"] != "session" {
			return nil
		}
		item := storedSession{id: text(header["id"]), cwd: text(header["cwd"]), file: path, updated: info.ModTime()}
		for {
			line, err := readHistoryLine(reader)
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, errHistoryLineTooLarge) {
				continue
			}
			if err != nil {
				return err
			}
			var value map[string]any
			if json.Unmarshal(line, &value) != nil {
				continue
			}
			if value["type"] == "session_info" && text(value["name"]) != "" {
				item.title = text(value["name"])
			}
			if item.title == "" {
				m := object(value["message"])
				if m["role"] == "user" {
					item.title = messageText(m["content"])
					if len([]rune(item.title)) > 120 {
						item.title = string([]rune(item.title)[:120])
					}
				}
			}
		}
		if item.id != "" && filepath.IsAbs(item.cwd) {
			result = append(result, item)
			if entry.Type()&os.ModeSymlink == 0 {
				a.historyMu.Lock()
				if a.historyCache == nil {
					a.historyCache = map[string]storedCacheEntry{}
				}
				a.historyCache[path] = storedCacheEntry{item: item, size: info.Size()}
				a.historyMu.Unlock()
			}
		}
		return nil
	})
	if err == nil {
		a.historyMu.Lock()
		for path := range a.historyCache {
			if !seen[path] {
				delete(a.historyCache, path)
			}
		}
		a.historyMu.Unlock()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].updated.After(result[j].updated) })
	return result, err
}

// findSession 只允许加载原生索引中与请求 cwd 相同的会话。
func (a *Agent) findSession(id acp.SessionId, cwd string) (string, error) {
	a.mutex.Lock()
	active := a.sessions[id]
	a.mutex.Unlock()
	if active != nil && sameDirectory(active.cwd, cwd) {
		return active.file, nil
	}
	var file string
	var newest time.Time
	err := filepath.WalkDir(a.sessionRoot(), func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		reader := bufio.NewReaderSize(f, 64*1024)
		first, readErr := readHistoryLine(reader)
		closeErr := f.Close()
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, errHistoryLineTooLarge) {
			return closeErr
		}
		if joined := errors.Join(readErr, closeErr); joined != nil {
			return joined
		}
		var header map[string]any
		_ = json.Unmarshal(first, &header)
		headerCWD := text(header["cwd"])
		if header["type"] == "session" && text(header["id"]) == string(id) && filepath.IsAbs(headerCWD) && sameDirectory(headerCWD, cwd) {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if file == "" || info.ModTime().After(newest) {
				file, newest = path, info.ModTime()
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if file != "" {
		return file, nil
	}
	return "", acp.NewInvalidParams(map[string]any{"message": "Pi session not found in this workspace"})
}

// ListSessions 对照 pi-acp 提供工作目录过滤与 50 条游标分页。
func (a *Agent) ListSessions(ctx context.Context, request acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	entries, err := a.stored()
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}
	params, _ := convert[map[string]any](request)
	cwd := text(params["cwd"])
	offset, _ := strconv.Atoi(text(params["cursor"]))
	if offset < 0 {
		offset = 0
	}
	filtered := []storedSession{}
	for _, entry := range entries {
		if cwd == "" || sameDirectory(entry.cwd, cwd) {
			filtered = append(filtered, entry)
		}
	}
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + 50
	if end > len(filtered) {
		end = len(filtered)
	}
	result := []any{}
	for _, entry := range filtered[offset:end] {
		result = append(result, map[string]any{"sessionId": entry.id, "cwd": entry.cwd, "title": entry.title, "updatedAt": entry.updated.UTC().Format(time.RFC3339)})
	}
	out := map[string]any{"sessions": result}
	if end < len(filtered) {
		out["nextCursor"] = strconv.Itoa(end)
	}
	return convert[acp.ListSessionsResponse](out)
}

// HandleExtensionMethod 实现 pi-acp 已有的会话删除和旧模型切换扩展。
func (a *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var request map[string]any
	if json.Unmarshal(params, &request) != nil {
		return nil, acp.NewInvalidParams(nil)
	}
	id := acp.SessionId(text(request["sessionId"]))
	switch method {
	case "session/delete":
		entries, err := a.stored()
		if err != nil {
			return nil, err
		}
		if _, err = a.CloseSession(ctx, acp.CloseSessionRequest{SessionId: id}); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.id == string(id) {
				if err = os.Remove(entry.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return nil, err
				}
			}
		}
		return map[string]any{}, nil
	case "session/set_model":
		s, err := a.get(id)
		if err != nil {
			return nil, err
		}
		if err := s.operation.Lock(ctx); err != nil {
			return nil, err
		}
		defer s.operation.Unlock()
		if err = s.setModel(ctx, text(request["modelId"])); err != nil {
			return nil, err
		}
		return map[string]any{}, a.configurationUpdate(ctx, s)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

// UnstableDeleteSession 接入 SDK 显式分发的删除方法，关闭进程并删除原生历史。
func (a *Agent) UnstableDeleteSession(ctx context.Context, request acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return acp.UnstableDeleteSessionResponse{}, err
	}
	_, err = a.HandleExtensionMethod(ctx, "session/delete", data)
	return acp.UnstableDeleteSessionResponse{}, err
}

// historyToolStatus 保留原生历史中的失败状态。
func historyToolStatus(message map[string]any) string {
	if failed, _ := message["isError"].(bool); failed {
		return "failed"
	}
	return "completed"
}

// sameDirectory 识别 macOS /var 与 /private/var 等同一目录的路径别名。
func sameDirectory(left, right string) bool {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	a, errA := os.Stat(left)
	b, errB := os.Stat(right)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// validateAdditionalDirectories 校验宿主冻结的项目范围，不伪造 Pi 不存在的沙箱参数。
// Pi 原生文件工具支持工作区外绝对路径，读写仍经过现有扩展审批。
func validateAdditionalDirectories(directories []string) error {
	for _, directory := range directories {
		if !filepath.IsAbs(directory) {
			return acp.NewInvalidParams(map[string]any{"message": "additional directory must be absolute"})
		}
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			return acp.NewInvalidParams(map[string]any{"message": "additional directory is unavailable"})
		}
	}
	return nil
}
