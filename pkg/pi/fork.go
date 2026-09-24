package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/JieWaZi/acp-go/internal/sessionfiles"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// UnstableForkSession 使用隔离 RPC 进程执行 clone，不切换来源连接的当前会话。
func (a *Agent) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(request) {
		id, err := acpmeta.ReadForkReceipt(request)
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}

	if err := validateAdditionalDirectories(request.AdditionalDirectories); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if acpmeta.ForkPosition(request.Meta) != "" {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "Pi adapter supports latest fork only"})
	}
	if source, getErr := a.get(request.SessionId); getErr == nil {
		if !source.operation.TryLock() {
			return acp.UnstableForkSessionResponse{}, errors.New("Pi source session is active")
		}
		defer source.operation.Unlock()
	}
	sourceCwd, _ := request.Meta["sourceCwd"].(string)
	if sourceCwd == "" {
		sourceCwd = request.Cwd
	}
	file, err := a.findSession(request.SessionId, sourceCwd)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	servers, err := acpmeta.ForkMCPServers(request.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	directory, err := os.MkdirTemp(a.directory, "fork-")
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	defer os.RemoveAll(directory)
	extension := filepath.Join(directory, "extension.ts")
	if err = writeExtension(extension, a.modulePath, a.config.PermissionMode, servers); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	p, err := startRPC(a.config, sourceCwd, extension, file)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.close(cleanup)
	}()
	if _, err = p.awaitExtensionReady(ctx); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	// 无提示的管理进程仍可能产生扩展通知，持续排空以免阻塞命令响应。
	go func() {
		for range p.events {
		}
	}()
	if err = acpmeta.BeginForkReceipt(request); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	var cloned map[string]any
	if err = p.call(ctx, "clone", nil, &cloned); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if cloned["cancelled"] == true {
		return acp.UnstableForkSessionResponse{}, errors.New("Pi fork cancelled by extension")
	}
	var state map[string]any
	if err = p.call(ctx, "get_state", nil, &state); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	id, childFile := text(state["sessionId"]), text(state["sessionFile"])
	if id == "" || id == string(request.SessionId) || childFile == "" || childFile == file {
		return acp.UnstableForkSessionResponse{}, errors.New("Pi clone did not produce an independent session")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	err = p.close(cleanup)
	cancel()
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = rebindForkFile(childFile, id, request.Cwd); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(id)); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	s, options, err := a.open(ctx, request.Cwd, childFile, servers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	options["sessionId"] = s.id
	response, err := convert[acp.NewSessionResponse](options)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, s.id); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(request.SessionId, response)
}

// rebindForkFile 只重写 clone 新文件的会话头；消息树、压缩和工具上下文保持原始字节。
func rebindForkFile(path, id, cwd string) error {
	if !filepath.IsAbs(cwd) {
		return errors.New("Pi fork cwd must be absolute")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	newline := bytes.IndexByte(content, '\n')
	if newline < 0 {
		return errors.New("Pi fork has no persisted session header")
	}
	var header map[string]json.RawMessage
	if json.Unmarshal(content[:newline], &header) != nil {
		return errors.New("Pi fork has invalid session header")
	}
	var actualID string
	if json.Unmarshal(header["id"], &actualID) != nil || actualID != id {
		return errors.New("Pi fork header identity mismatch")
	}
	header["cwd"], _ = json.Marshal(cwd)
	encoded, err := json.Marshal(header)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".fork-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(append(append(encoded, '\n'), content[newline+1:]...))
	err = errors.Join(writeErr, temp.Sync(), temp.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return err
	}
	return sessionfiles.SyncDirectory(filepath.Dir(path))
}
