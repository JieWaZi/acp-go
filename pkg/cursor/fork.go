package cursor

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/JieWaZi/acp-go/internal/sessionfiles"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// UnstableForkSession 在受管终端执行原生 /fork，绝不调用模型 Prompt 接口。
func (a *Agent) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(request) {
		id, err := acpmeta.ReadForkReceipt(request)
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}

	if acpmeta.ForkPosition(request.Meta) != "" {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "Cursor supports latest fork only"})
	}
	sourceCwd, _ := request.Meta["sourceCwd"].(string)
	if sourceCwd == "" {
		sourceCwd = request.Cwd
	}
	servers, err := acpmeta.ForkMCPServers(request.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	s, err := a.session(request.SessionId)
	if err != nil {
		if _, err = a.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: request.SessionId, Cwd: sourceCwd, McpServers: servers, AdditionalDirectories: request.AdditionalDirectories}); err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
		s, err = a.session(request.SessionId)
		if err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
	}
	a.lifecycleMutex.Lock()
	defer a.lifecycleMutex.Unlock()
	if !s.mutex.TryLock() {
		return acp.UnstableForkSessionResponse{}, errors.New("Cursor source session is active")
	}
	defer s.mutex.Unlock()
	if err = a.start(ctx, s); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.BeginForkReceipt(request); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	child, err := forkTerminalSession(ctx, s)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	root := filepath.Dir(filepath.Dir(s.store.path))
	if err = s.stop(); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	targetState := a.state
	if a.config.StateDirectoryForWorkspace != nil {
		targetState = a.config.StateDirectoryForWorkspace(request.Cwd)
	}
	if err = publishForkStore(filepath.Join(root, child), targetState, child, request.Cwd); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(child)); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	// ACP 配置进程仍使用当前连接的索引；持久恢复副本已保存在目标状态目录。
	control := filepath.Join(a.state, "acp-sessions", child)
	if targetState != a.state {
		if err = sessionfiles.CopyTree(filepath.Join(targetState, "acp-sessions", child), control); err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
	}
	loaded, err := a.Agent.LoadSessionConfiguration(ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(child), Cwd: request.Cwd, McpServers: servers, AdditionalDirectories: request.AdditionalDirectories})
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	owner, err := a.claim(acp.SessionId(child), request.Cwd)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = a.register(acp.SessionId(child), request.Cwd, servers, request.AdditionalDirectories, owner); err != nil {
		_ = owner.Unlock()
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(child)); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(request.SessionId, acp.NewSessionResponse{SessionId: acp.SessionId(child), Modes: loaded.Modes, ConfigOptions: loaded.ConfigOptions})
}

// publishForkStore 保留官方不可变 blob 图，在目标工作区建立独立 ACP 冷恢复索引。
func publishForkStore(source, state, id, cwd string) error {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(resolved) || filepath.Base(id) != id || id == ".." {
		return errors.New("invalid Cursor fork destination")
	}
	sum := md5.Sum([]byte(resolved)) // 官方 CLI 按真实 cwd 的 MD5 定位聊天目录。
	parent := filepath.Join(state, "chats", hex.EncodeToString(sum[:]))
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	target := filepath.Join(parent, id)
	if target != source {
		if err = sessionfiles.CopyTree(source, target); err != nil {
			return err
		}
	}
	control := filepath.Join(state, "acp-sessions", id)
	if err = os.MkdirAll(control, 0700); err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]any{"schemaVersion": 1, "cwd": resolved})
	if err != nil {
		return err
	}
	if err = sessionfiles.WriteFile(filepath.Join(control, "meta.json"), metadata); err != nil {
		return err
	}
	adapter := &Agent{state: state}
	return adapter.snapshotControlStore(context.Background(), acp.SessionId(id), resolved)
}

// stateForWorkspace 让每个会话始终使用其自身的受管持久状态目录。
func (a *Agent) stateForWorkspace(cwd string) string {
	if a.config.StateDirectoryForWorkspace != nil {
		return a.config.StateDirectoryForWorkspace(cwd)
	}
	return a.state
}

// forkTerminalSession 等待原生命令产生完整原生状态，避免用旧终端标题判断完成。
func forkTerminalSession(ctx context.Context, s *interactiveSession) (string, error) {
	root := filepath.Dir(filepath.Dir(s.store.path))
	previous, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	known := map[string]bool{}
	for _, entry := range previous {
		known[entry.Name()] = true
	}
	if err = s.terminal.submit(ctx, "/fork"); err != nil {
		return "", err
	}
	// 原生命令完成后才出现带独立 agentId 的完整 store；关闭终端后再移动其冷状态。
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var child string
	for child == "" {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return "", readErr
		}
		for _, entry := range entries {
			if known[entry.Name()] || !entry.IsDir() {
				continue
			}
			if nativeForkStoreReady(ctx, s.store.path, filepath.Join(root, entry.Name())) {
				if child != "" {
					return "", errors.New("Cursor fork produced ambiguous native sessions")
				}
				child = entry.Name()
			}
		}
		if child != "" {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-s.terminal.done:
			return "", errors.New("Cursor exited before native fork completed")
		case <-ticker.C:
		}
	}

	return child, nil
}

// nativeForkStoreReady 使用上游完成时写出的 sidecar 和逐 blob 字节校验确认上下文完整。
func nativeForkStoreReady(ctx context.Context, source, target string) bool {
	data, err := os.ReadFile(filepath.Join(target, "meta.json"))
	if err != nil {
		return false
	}
	var meta map[string]any
	if json.Unmarshal(data, &meta) != nil {
		return false
	}
	if meta["hasConversation"] != true {
		return false
	}
	parent, err := newStoreCursor(source).open(ctx)
	if err != nil {
		return false
	}
	defer parent.Close()
	child, err := newStoreCursor(filepath.Join(target, "store.db")).open(ctx)
	if err != nil {
		return false
	}
	defer child.Close()
	rows, _, err := parent.Prepare("SELECT id,data FROM blobs")
	if err != nil {
		return false
	}
	defer rows.Close()
	lookup, _, err := child.Prepare("SELECT data FROM blobs WHERE id=?")
	if err != nil {
		return false
	}
	defer lookup.Close()
	for rows.Step() {
		if lookup.BindText(1, rows.ColumnText(0)) != nil || !lookup.Step() || !bytes.Equal(rows.ColumnBlob(1, nil), lookup.ColumnBlob(0, nil)) {
			return false
		}
		if lookup.Reset() != nil {
			return false
		}
	}
	return rows.Err() == nil
}
