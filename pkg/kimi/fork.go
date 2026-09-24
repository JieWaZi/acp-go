package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/JieWaZi/acp-go/internal/sessionfiles"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// UnstableForkSession 先调用原生 fork，再把完整分支移植到原生创建的目标工作区会话。
// Kimi ACP 忽略 fork.cwd；目标会话由 session/new 建立正确的工作区索引，不能修改父会话。
func (a *Agent) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(request) {
		id, err := acpmeta.ReadForkReceipt(request)
		if err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
		servers, err := acpmeta.ForkMCPServers(request.McpServers)
		if err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
		// 收据只能证明身份，旧版绑定错误等损坏状态必须通过原生恢复校验后才能发布。
		_, err = a.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: id, Cwd: request.Cwd, McpServers: servers, AdditionalDirectories: request.AdditionalDirectories})
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}

	if !a.kimiCodeCLI.Load() || acpmeta.ForkPosition(request.Meta) != "" {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "Kimi supports latest native fork only"})
	}
	if _, active := a.usagePrompts.LoadOrStore(request.SessionId, true); active {
		return acp.UnstableForkSessionResponse{}, errors.New("Kimi source session is active")
	}
	defer a.usagePrompts.Delete(request.SessionId)
	sourceCwd, _ := request.Meta["sourceCwd"].(string)
	if sourceCwd == "" {
		sourceCwd = request.Cwd
	}
	servers, err := acpmeta.ForkMCPServers(request.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.BeginForkReceipt(request); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	native, err := a.Agent.UnstableForkSession(ctx, request)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if sourceCwd == request.Cwd {
		a.rememberWire(native.SessionId, request.Cwd)
		return native, acpmeta.WriteForkReceipt(request, native.SessionId)
	}
	target, err := a.NewSession(ctx, acp.NewSessionRequest{Cwd: request.Cwd, AdditionalDirectories: request.AdditionalDirectories, McpServers: servers})
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	sourceWire := indexedWirePath(a.codeDirectory, native.SessionId, sourceCwd)
	targetWire := indexedWirePath(a.codeDirectory, target.SessionId, request.Cwd)
	if sourceWire == "" || targetWire == "" {
		return acp.UnstableForkSessionResponse{}, errors.New("Kimi fork native session index is unavailable")
	}
	for _, id := range []acp.SessionId{native.SessionId, target.SessionId} {
		if _, err = a.Agent.CloseSession(ctx, acp.CloseSessionRequest{SessionId: id}); err != nil {
			return acp.UnstableForkSessionResponse{}, err
		}
	}
	sourceDir := filepath.Dir(filepath.Dir(filepath.Dir(sourceWire)))
	targetDir := filepath.Dir(filepath.Dir(filepath.Dir(targetWire)))
	if err = rebindNativeFork(sourceDir, targetDir, string(target.SessionId), request.Cwd); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, target.SessionId); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	resumed, err := a.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: target.SessionId, Cwd: request.Cwd, McpServers: servers, AdditionalDirectories: request.AdditionalDirectories})
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, target.SessionId); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(request.SessionId, acp.NewSessionResponse{SessionId: target.SessionId, ConfigOptions: resumed.ConfigOptions, Modes: resumed.Modes})
}

// rebindNativeFork 保留全部 wire、压缩和工具状态，只替换子会话的原生归属元数据。
func rebindNativeFork(source, target, id, cwd string) error {
	if source == target || !filepath.IsAbs(cwd) {
		return errors.New("invalid Kimi fork destination")
	}
	temporary, err := os.MkdirTemp(filepath.Dir(target), ".fork-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	copied := filepath.Join(temporary, "session")
	if err = sessionfiles.CopyTree(source, copied); err != nil {
		return err
	}
	metadataPath := filepath.Join(copied, "state.json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		return err
	}
	var metadata map[string]json.RawMessage
	if err = json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	metadata["id"], _ = json.Marshal(id)
	metadata["cwd"], _ = json.Marshal(cwd)
	var agents map[string]map[string]json.RawMessage
	if err = json.Unmarshal(metadata["agents"], &agents); err != nil {
		return err
	}
	for agentID, agent := range agents {
		if filepath.Base(agentID) != agentID || agentID == ".." {
			return errors.New("invalid Kimi agent identity")
		}
		agent["homedir"], _ = json.Marshal(filepath.Join(target, "agents", agentID))
	}
	if err = rebindForkRuntime(copied, target, agents); err != nil {
		return err
	}
	metadata["agents"], _ = json.Marshal(agents)
	raw, err = json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err = sessionfiles.WriteFile(metadataPath, raw); err != nil {
		return err
	}
	backup := filepath.Join(temporary, "empty-session")
	if err = os.Rename(target, backup); err != nil {
		return err
	}
	if err = os.Rename(copied, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	return sessionfiles.SyncDirectory(filepath.Dir(target))
}

// rebindForkRuntime 追加目标会话原生绑定事件，恢复时覆盖父工作区绑定而不改写历史。
func rebindForkRuntime(copied, target string, agents map[string]map[string]json.RawMessage) error {
	targetWire, err := os.ReadFile(filepath.Join(target, "agents", "main", "wire.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var binding map[string]json.RawMessage
	for _, line := range bytes.Split(targetWire, []byte{'\n'}) {
		var record map[string]json.RawMessage
		if json.Unmarshal(line, &record) == nil && string(record["type"]) == `"runtime.set_binding"` {
			binding = record
		}
	}
	sourceMainWire, err := os.ReadFile(filepath.Join(copied, "agents", "main", "wire.jsonl"))
	if err != nil {
		return err
	}
	sourceMainRuntimeID := latestForkRuntimeID(sourceMainWire)
	for id := range agents {
		wirePath := filepath.Join(copied, "agents", id, "wire.jsonl")
		wire, err := os.ReadFile(wirePath)
		if err != nil {
			return err
		}
		runtimeID := latestForkRuntimeID(wire)
		if binding == nil {
			if bytes.Contains(wire, []byte(`"runtime.set_binding"`)) {
				return errors.New("Kimi target runtime binding is unavailable")
			}
			continue
		}
		childBinding := make(map[string]json.RawMessage, len(binding))
		for key, value := range binding {
			childBinding[key] = value
		}
		childBinding["agentId"], _ = json.Marshal(id)
		if len(runtimeID) > 0 && !bytes.Equal(runtimeID, sourceMainRuntimeID) {
			childBinding["runtimeId"] = runtimeID
		}
		record, err := json.Marshal(childBinding)
		if err != nil {
			return err
		}
		if len(wire) > 0 && wire[len(wire)-1] != '\n' {
			wire = append(wire, '\n')
		}
		wire = append(append(wire, record...), '\n')
		if err = sessionfiles.WriteFile(wirePath, wire); err != nil {
			return err
		}
	}
	return nil
}

// latestForkRuntimeID 提取一个 Agent 最后生效的原生运行时标识。
func latestForkRuntimeID(wire []byte) json.RawMessage {
	var runtimeID json.RawMessage
	for _, line := range bytes.Split(wire, []byte{'\n'}) {
		var record map[string]json.RawMessage
		if json.Unmarshal(line, &record) == nil && string(record["type"]) == `"runtime.set_binding"` {
			runtimeID = record["runtimeId"]
		}
	}
	return runtimeID
}
