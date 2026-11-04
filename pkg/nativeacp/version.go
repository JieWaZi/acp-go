package nativeacp

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// RuntimeVersion probes the same launcher with bounded output and duration; failures remain unknown.
func RuntimeVersion(ctx context.Context, command string, args, environment []string, cwd string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd, err := CommandContext(ctx, command, args, environment)
	if err != nil {
		return ""
	}
	cmd.Dir = cwd
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

// versionOutput bounds memory consumed by a misbehaving version probe.
type versionOutput struct {
	// Buffer retains the bounded version output.
	bytes.Buffer
}

// Write rejects output beyond the version information budget.
func (b *versionOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 1024 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(data)
}

// completeRuntimeVersion fills missing runtime metadata without replacing native observations.
func (agent *Agent) completeRuntimeVersion(ctx context.Context, response *acp.InitializeResponse) {
	if len(agent.config.VersionArgs) == 0 {
		return
	}
	if response.AgentInfo != nil && acpmeta.RuntimeVersion(response.AgentInfo.Meta) != "" {
		return
	}
	agent.versionOnce.Do(func() {
		agent.version = RuntimeVersion(ctx, agent.config.Command, agent.config.VersionArgs, agent.config.Environment, agent.config.WorkingDirectory)
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
