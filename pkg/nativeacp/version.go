package nativeacp

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// RuntimeVersion 读取同一 CLI 的版本，不执行模型请求；失败保持未知，输出和等待均有界。
func RuntimeVersion(ctx context.Context, command string, args, environment []string, cwd string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	prepareProcess(cmd)
	cmd.Env, cmd.Dir = environment, cwd
	cmd.WaitDelay = time.Second
	output := &versionOutput{}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	if cmd.Run() != nil {
		return ""
	}
	version := strings.TrimSpace(output.String())
	if strings.ContainsAny(version, "\n\r\x1b") {
		return ""
	}
	return version
}

// versionOutput 限制异常 CLI 输出，避免版本探测占用无限内存。
type versionOutput struct {
	// Buffer 保存受限的单行版本输出。
	bytes.Buffer
}

// Write 拒绝超过版本信息上限的输出。
func (b *versionOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 1024 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(data)
}

// completeRuntimeVersion 补齐上游握手省略的运行时版本；不覆盖原生元数据。
func (agent *Agent) completeRuntimeVersion(ctx context.Context, response *acp.InitializeResponse) {
	if len(agent.config.VersionArgs) == 0 {
		return
	}
	if response.AgentInfo != nil && acpmeta.RuntimeVersion(response.AgentInfo.Meta) != "" {
		return
	}
	agent.versionOnce.Do(func() {
		agent.version = RuntimeVersion(ctx, agent.command.Path, agent.config.VersionArgs, agent.config.Environment, agent.config.WorkingDirectory)
	})
	if agent.version == "" {
		return
	}
	if response.AgentInfo == nil {
		response.AgentInfo = &acp.Implementation{Name: agent.config.RuntimeName, Version: agent.version}
	}
	if response.AgentInfo.Meta == nil {
		response.AgentInfo.Meta = map[string]any{}
	}
	runtimeMeta := map[string]any{}
	if existing, ok := response.AgentInfo.Meta["runtime"].(map[string]any); ok {
		for key, value := range existing {
			runtimeMeta[key] = value
		}
	}
	runtimeMeta["version"] = agent.version
	response.AgentInfo.Meta["runtime"] = runtimeMeta
}
