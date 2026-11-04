package claude_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureEnvironment(dir, mode string) []string {
	return append(os.Environ(), "CLAUDE_CANDIDATE_FIXTURE=1", "CLAUDE_FIXTURE_MODE="+mode, "CLAUDE_FIXTURE_DIR="+dir,
		"CLAUDE_CONFIG_DIR="+filepath.Join(dir, "config"), "ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=")
}

func TestClaudeFixture(t *testing.T) {
	if os.Getenv("CLAUDE_CANDIDATE_FIXTURE") != "1" {
		return
	}
	f := fixture{dir: os.Getenv("CLAUDE_FIXTURE_DIR"), mode: os.Getenv("CLAUDE_FIXTURE_MODE")}
	for i, arg := range os.Args {
		switch {
		case arg == "--version":
			fmt.Fprintln(os.Stdout, "2.1.309 (Claude Code)")
			os.Exit(0)
		case arg == "status":
			if f.mode == "subscription-identity" {
				f.emit(map[string]any{
					"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty", "email": "local@example.test",
					"orgId": "c58fe389-3a47-4d19-aa06-104f4450dff8", "subscriptionType": "pro",
					"configDirectory": os.Getenv("CLAUDE_CONFIG_DIR"),
				})
				os.Exit(0)
			}
			if f.mode == "legacy-auth-token" {
				f.emit(map[string]any{"loggedIn": true, "authMethod": "oauth_token", "apiProvider": "firstParty"})
				os.Exit(0)
			}
			f.emit(map[string]any{"loggedIn": true, "authMethod": "claude.ai", "email": "local@example.test", "subscriptionType": "pro"})
			os.Exit(0)
		case arg == "login" || arg == "logout":
			os.Exit(0)
		case strings.HasPrefix(arg, "--session-id="):
			f.id = strings.TrimPrefix(arg, "--session-id=")
		case strings.HasPrefix(arg, "--resume="):
			f.source = strings.TrimPrefix(arg, "--resume=")
			if f.id == "" {
				f.id = f.source
			}
		case arg == "--settings" && i+1 < len(os.Args):
			_ = json.Unmarshal([]byte(os.Args[i+1]), &f.workspace)
		case strings.HasPrefix(arg, "--plugin-dir-no-mcp="):
			f.pluginArgv = append(f.pluginArgv, strings.TrimPrefix(arg, "--plugin-dir-no-mcp="))
		case arg == "--permission-mode" && i+1 < len(os.Args):
			f.permissionMode = os.Args[i+1]
		case arg == "--await-initialize":
			f.awaitInitialize = true
		case arg == "--fork-session":
			f.fork = true
		case arg == "--strict-mcp-config":
			f.strict = true
		case arg == "--system-prompt-snapshot":
			f.snapshotOff = true
		}
	}
	if f.fork {
		cwd, _ := os.Getwd()
		dir := fixtureProject(f.dir, cwd)
		source, err := os.ReadFile(filepath.Join(dir, f.source+".jsonl"))
		if err != nil {
			os.Exit(73)
		}
		if err := os.WriteFile(filepath.Join(dir, f.id+".jsonl"), []byte(strings.ReplaceAll(string(source), f.source, f.id)), 0600); err != nil {
			os.Exit(74)
		}
	}
	f.input = bufio.NewReader(os.Stdin)
	f.model = "sonnet"
	if f.mode == "legacy-model-aliases" {
		f.model = "claude-opus-4-8[1m]"
		if os.Getenv("ANTHROPIC_MODEL") == "sonnet" {
			f.model = "claude-sonnet-4-6"
		}
	}
	for {
		line, err := f.input.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var frame fixtureFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			os.Exit(71)
		}
		switch frame.Type {
		case "user":
			f.user(frame)
		case "control_request":
			f.control(frame)
		case "control_response":
			f.controlReply(frame)
		}
	}
}

type fixtureFrame struct {
	Type     string
	ID       string `json:"request_id"`
	UUID     string
	Priority string
	Message  struct{ Content []struct{ Text string } }
	Response struct {
		RequestID string `json:"request_id"`
		Response  map[string]any
	}
	Request struct {
		Subtype string
		UUID    string `json:"message_uuid"`
		Model   string
		Mode    string
		Hooks   map[string][]struct {
			IDs []string `json:"hookCallbackIds"`
		}
		Append  *string `json:"appendSystemPrompt"`
		Plugins []struct {
			Type             string
			Path             string
			SkipMCPDiscovery bool `json:"skipMcpDiscovery"`
		}
		Settings map[string]any
	}
}

type fixture struct {
	input           *bufio.Reader
	dir             string
	mode            string
	source          string
	fork            bool
	id              string
	model           string
	active          string
	index           int
	strict          bool
	snapshotOff     bool
	awaitInitialize bool
	// permissionMode captures the mode explicitly selected at launch.
	permissionMode string
	// mcpStatusReads tracks a server still connecting after initialize.
	mcpStatusReads int
	pluginArgv     []string
	settings       map[string]any
	workspace      map[string]any
	hookID         string
}

func (f *fixture) emit(frame map[string]any) {
	if frame["type"] != "control_response" {
		frame["session_id"] = f.id
	}
	_ = json.NewEncoder(os.Stdout).Encode(frame)
}

func (f *fixture) mark(name, value string) {
	_ = os.WriteFile(filepath.Join(f.dir, name), []byte(value), 0600)
}

func (f *fixture) assistant(id, text string, provider bool) {
	frame := map[string]any{
		"type": "assistant", "parent_tool_use_id": nil,
		"message": map[string]any{"id": fmt.Sprintf("message-%d-%s", f.index, text), "model": f.model,
			"content": []map[string]any{{"type": "text", "text": text}},
			"usage":   map[string]any{"input_tokens": 43, "output_tokens": 7, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
		},
	}
	if id != "" {
		frame["user_message_uuid"] = id
	}
	if provider {
		frame["resume_reason"] = "interrupted_turn"
	}
	f.emit(frame)
}

func (f *fixture) result(id, text, terminal string) {
	frame := map[string]any{
		"type": "result", "subtype": "success", "uuid": fmt.Sprintf("terminal-%d", f.index), "result_index": f.index,
		"terminal_reason": terminal, "result": text, "is_error": false,
		"usage": map[string]any{"input_tokens": 43, "output_tokens": 7, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
	}
	if id != "" {
		frame["user_message_uuid"] = id
	}
	if terminal == "api_error" {
		frame["subtype"] = "error_during_execution"
		frame["is_error"] = true
		frame["api_error_status"] = 429
	}
	if f.mode == "pricing-basis" {
		basis := []string{"list", "managed", "managed", "unknown", "unknown", "list", "list"}[f.index]
		used := (f.index + 1) * 20
		frame["modelUsage"] = map[string]any{f.model: map[string]any{
			"inputTokens": used, "outputTokens": used, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0,
			"costUSD": f.index + 1, "costBasis": basis,
		}}
		frame["total_cost_usd"] = f.index + 1
	}
	if f.mode == "usage-gap" && f.index != 1 {
		used := 20
		if f.index >= 2 {
			used = 55
		}
		frame["modelUsage"] = map[string]any{f.model: map[string]any{
			"inputTokens": used, "outputTokens": 7, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0,
		}}
	}
	f.index++
	f.emit(frame)
	if f.mode == "foreign-duplicate" {
		f.emit(frame)
	}
}

func (f *fixture) control(frame fixtureFrame) {
	var response any = map[string]any{}
	switch frame.Request.Subtype {
	case "initialize":
		f.settings = map[string]any{}
		cwd, _ := os.Getwd()
		init := map[string]any{"type": "system", "subtype": "init", "cwd": cwd, "model": f.model, "permissionMode": "default", "claude_code_version": "2.1.309", "capabilities": []string{"cancel_queued"}}
		if f.mode == "rereview-workspace-cwd" || f.mode == "rereview-hook-cwd" {
			forbidden := filepath.Join(f.dir, "blocked")
			_ = os.Mkdir(forbidden, 0700)
			if f.mode == "rereview-workspace-cwd" {
				init["cwd"] = forbidden
			}
		}
		f.emit(init)
		f.mark("launch", fmt.Sprintf("strict=%t snapshot=%t instructions=%v mode=%s", f.strict, f.snapshotOff, optionalString(frame.Request.Append), f.permissionMode))
		if hooks := frame.Request.Hooks["PreToolUse"]; len(hooks) > 0 && len(hooks[0].IDs) > 0 {
			f.hookID = hooks[0].IDs[0]
		}
		response = map[string]any{"hooks_applied": f.hookID != "", "models": []map[string]any{
			{"value": "sonnet", "displayName": "Sonnet", "supportsEffort": true, "supportedEffortLevels": []string{"low", "high"}, "supportsFastMode": true},
			{"value": "opus", "displayName": "Opus", "supportsEffort": false, "supportsFastMode": false},
		}}
		if f.mode == "legacy-model-aliases" {
			response.(map[string]any)["fast_mode_disabled_reason"] = "extra_usage_disabled"
			response.(map[string]any)["models"] = []map[string]any{
				{"value": "default", "displayName": "Default (recommended)", "supportsEffort": true, "supportedEffortLevels": []string{"low", "medium", "high", "xhigh", "max"}, "supportsFastMode": true},
				{"value": "sonnet", "displayName": "Sonnet", "supportsEffort": true, "supportedEffortLevels": []string{"low", "medium", "high", "max"}, "supportsAutoMode": true},
			}
		}
		paths := make([]string, 0, len(frame.Request.Plugins))
		for _, p := range frame.Request.Plugins {
			if p.Type == "local" && p.SkipMCPDiscovery {
				paths = append(paths, p.Path)
			}
		}
		f.mark("plugin-delivery", fmt.Sprintf("await=%t argv=%v payload=%v", f.awaitInitialize, f.pluginArgv, paths))
		if f.mode == "rereview-plugin-reject" {
			response.(map[string]any)["plugins_applied"] = false
		}
		if f.mode == "rereview-plugin-ack" && len(frame.Request.Plugins) == 1 {
			p := frame.Request.Plugins[0]
			response.(map[string]any)["plugins_applied"] = p.Type == "local" && p.SkipMCPDiscovery && p.Path != ""
		}
	case "get_settings":
		effective := map[string]any{"fastMode": f.settings["fastMode"]}
		if f.workspace != nil {
			effective["sandbox"] = f.workspace["sandbox"]
			effective["allowManagedHooksOnly"] = true
		}
		response = map[string]any{"applied": map[string]any{"model": f.model, "effort": f.settings["effortLevel"]}, "effective": effective}
	case "set_model":
		f.model = frame.Request.Model
		if f.mode == "legacy-model-aliases" {
			switch f.model {
			case "default":
				f.model = "claude-opus-4-8[1m]"
			case "sonnet":
				f.model = "claude-sonnet-4-6"
			}
		}
	case "apply_flag_settings":
		for key, value := range frame.Request.Settings {
			f.settings[key] = value
		}
	case "mcp_status":
		if f.mode == "mcp-pending" {
			status := "pending"
			if f.mcpStatusReads > 0 {
				status = "connected"
			}
			f.mcpStatusReads++
			response = map[string]any{"mcpServers": []map[string]string{{"name": "local", "status": status}}}
		}
	case "get_context_usage":
		response = map[string]any{"totalTokens": 17, "rawMaxTokens": 1000}
	case "get_usage":
		response = map[string]any{"rate_limits_available": true, "rate_limits": map[string]any{"five_hour": map[string]any{
			"utilization": 0, "resets_at": "2026-11-04T00:00:00Z",
		}}}
	case "interrupt":
		f.mark("interrupt", f.active)
		if f.mode == "provider-cancel-fifo" {
			f.waitMarker("allow-interrupt")
		}
		if f.mode == "watchdog" && f.index == 0 {
			f.result(f.active, "A completed before interrupt acknowledgement", "completed")
			f.active = ""
			f.assistant("", "provider successor", true)
			return
		}
		if f.mode == "review-running-before-uuid" {
			f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "own-cancel", "user_message_uuid": f.active, "terminal_reason": "aborted_streaming", "result_index": f.index, "origin": map[string]any{"kind": "human"}})
			f.index++
		} else {
			f.result(f.active, "", "aborted_streaming")
		}
		f.active = ""
	case "cancel_async_message":
		if f.mode == "review-cancel-fold" {
			f.assistant(frame.Request.UUID, "provider folded cancelled host", true)
			f.mark("folded", "true")
		}
		f.mark("recall", frame.Request.UUID)
		response = map[string]any{"cancelled": f.mode == "recall" && frame.Request.UUID != "" && frame.Request.UUID == f.active}
	}
	f.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": frame.ID, "response": response}})
	if frame.Request.Subtype == "cancel_async_message" && f.mode == "review-cancel-fold" {
		f.waitMarker("finish-review-provider")
		f.result(frame.Request.UUID, "provider answer", "completed")
	}
	if frame.Request.Subtype == "initialize" {
		switch f.mode {
		case "result-gap":
			f.result("", "earlier provider", "completed")
		case "provider", "provider-cancel-fifo", "review-cancel-fold":
			f.assistant("", "provider started", true)
		case "pages":
			for i := 0; i < 20; i++ {
				f.result("", fmt.Sprintf("provider result %d", i), "completed")
			}
			f.mark("produced", "done")
		}
	}
	if frame.Request.Subtype == "get_settings" && f.mode == "overflow" {
		f.waitMarker("produce")
		for i := 0; i < 129; i++ {
			f.result("", fmt.Sprintf("provider result %d", i), "completed")
		}
		f.mark("produced", "done")
	}
}

func (f *fixture) controlReply(frame fixtureFrame) {
	f.mark("interaction-"+frame.Response.RequestID, fmt.Sprint(frame.Response.Response))
	if f.mode == "workspace-approval" || f.mode == "workspace-path-substitution" {
		if frame.Response.RequestID == "tool-request" {
			path := "safe.txt"
			if f.mode == "workspace-path-substitution" {
				path = "protected/entry"
			}
			f.emit(map[string]any{"type": "control_request", "request_id": "permission-followup", "request": map[string]any{
				"subtype": "can_use_tool", "tool_name": "Write", "tool_use_id": "workspace-tool", "input": map[string]any{"file_path": path},
			}})
		} else {
			f.result(f.active, "interaction complete", "completed")
			f.active = ""
		}
		return
	}
	if f.mode == "approval" || f.mode == "ask" || f.mode == "workspace" || f.mode == "review-stale-permission" || f.mode == "review-foreground-agent" || f.mode == "rereview-workspace-cwd" || f.mode == "rereview-hook-cwd" {
		f.result(f.active, "interaction complete", "completed")
		f.active = ""
	}
}

func optionalString(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func (f *fixture) user(frame fixtureFrame) {
	f.mark("input", frame.UUID)
	if frame.Priority == "now" && f.mode == "stale" {
		return
	}
	f.active = frame.UUID
	switch f.mode {
	case "steer-admission", "steer-target-completes":
		if frame.Priority == "" {
			f.active = frame.UUID
			f.assistant(frame.UUID, "opening", false)
			if f.mode == "steer-target-completes" {
				f.waitMarker("finish-opening")
				f.result(frame.UUID, "opening completed", "completed")
			}
			return
		}
		f.waitMarker("consume-steer")
		f.assistant(frame.UUID, "priority input consumed", false)
		f.result(f.active, "both inputs consumed", "completed")
	case "rereview-retired-rerun":
		f.assistant(frame.UUID, "original host", false)
		f.result(frame.UUID, "", "aborted_streaming")
		f.assistant(frame.UUID, "provider retry of interrupted send", true)
		f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "provider-rerun-terminal", "result_index": 1,
			"resume_reason": "interrupted_turn", "user_message_uuid": frame.UUID, "user_message_uuids": []string{frame.UUID},
			"result": "new provider answer", "usage": map[string]any{"input_tokens": 17, "output_tokens": 4}})
		f.mark("emitted-rerun", "true")
	case "closure-reset-capacity":
		if f.index == 1 {
			// One more unique reset than the native stream history can hold.
			for n := range 16385 {
				id := fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
				f.emit(map[string]any{"type": "conversation_reset", "new_conversation_id": id, "uuid": id})
			}
		}
		f.result(frame.UUID, "ordinary result", "")
	case "closure-missing-report", "closure-missing-components", "closure-missing-model", "closure-number-gap", "closure-late-reset-replay":
		mode := f.mode
		index := f.index
		input, output, cost := 100, 50, "1.0"
		if mode == "closure-late-reset-replay" {
			input, output, cost = []int{100, 80, 70, 90}[f.index], []int{50, 40, 35, 45}[f.index], []string{"1.0", "0.8", "0.7", "0.9"}[f.index]
			epochs := []string{"", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
			if epoch := epochs[f.index]; epoch != "" {
				f.emit(map[string]any{"type": "conversation_reset", "new_conversation_id": epoch, "uuid": epoch, "trigger": "clear"})
			}
		} else if mode == "closure-number-gap" {
			input, output, cost = []int{100, 80, 110, 120}[f.index], []int{50, 40, 55, 60}[f.index], []string{"1.0", "0.8", "1.1", "1.2"}[f.index]
			if index > 0 {
				index++
			}
		} else {
			input, output, cost = []int{100, 90, 80, 110, 120}[f.index], []int{50, 45, 40, 55, 60}[f.index], []string{"1.0", "0.9", "0.8", "1.1", "1.2"}[f.index]
		}
		row := map[string]any{"inputTokens": input, "outputTokens": output, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0, "costUSD": json.Number(cost)}
		result := map[string]any{"type": "result", "subtype": "success", "uuid": fmt.Sprintf("closure-%d", index),
			"result_index": index, "user_message_uuid": frame.UUID, "usage": map[string]any{"input_tokens": 7, "output_tokens": 4},
			"modelUsage": map[string]any{"sonnet": row}, "total_cost_usd": json.Number(cost)}
		if f.index == 1 && mode == "closure-missing-report" {
			delete(result, "modelUsage")
			delete(result, "total_cost_usd")
		}
		if f.index == 1 && mode == "closure-missing-components" {
			delete(row, "inputTokens")
			delete(row, "outputTokens")
			delete(row, "costUSD")
		}
		if f.index == 1 && mode == "closure-missing-model" {
			result["modelUsage"] = map[string]any{
				"opus": map[string]any{"inputTokens": 9, "outputTokens": 3, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0},
			}
		}
		f.emit(result)
		f.index++
	case "pipeline-dip", "pipeline-token-dip", "pipeline-reset", "pipeline-system-reset":
		if f.mode == "pipeline-reset" && f.index > 0 {
			f.emit(map[string]any{"type": "conversation_reset", "new_conversation_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
				"uuid": "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "trigger": "clear"})
		}
		if f.mode == "pipeline-system-reset" && f.index == 1 {
			f.emit(map[string]any{"type": "system", "subtype": "conversation_reset"})
		}
		input := []int{100, 80, 110}[f.index]
		output := []int{50, 40, 55}[f.index]
		cost := []json.Number{"1.0", "0.8", "1.1"}[f.index]
		if f.mode == "pipeline-token-dip" && f.index == 1 {
			cost = "1.0"
		}
		f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": fmt.Sprintf("pipeline-usage-%d", f.index),
			"result_index": f.index, "user_message_uuid": frame.UUID,
			"usage": map[string]any{"input_tokens": 7, "output_tokens": 4},
			"modelUsage": map[string]any{"sonnet": map[string]any{"inputTokens": input, "outputTokens": output,
				"cacheReadInputTokens": []int{20, 18, 24}[f.index], "cacheCreationInputTokens": []int{10, 11, 13}[f.index],
				"thinkingTokens": []int{10, 5, 12}[f.index], "costUSD": cost}}, "total_cost_usd": cost,
		})
		f.index++
	case "rereview-partial-counter":
		if f.index == 0 {
			f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "complete-baseline", "result_index": 0,
				"user_message_uuid": frame.UUID, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2},
				"modelUsage": map[string]any{"sonnet": map[string]any{"inputTokens": 3, "outputTokens": 2}}})
		} else {
			f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "partial-current", "result_index": 1,
				"user_message_uuid": frame.UUID, "usage": map[string]any{"input_tokens": 7, "output_tokens": 4},
				"modelUsage": map[string]any{"sonnet": map[string]any{"inputTokens": 10}}})
		}
		f.index++
	case "rereview-model-gap", "rereview-observation-budget":
		models := map[string]any{}
		if f.mode == "rereview-model-gap" {
			models["sonnet"] = map[string]any{"inputTokens": 10 + 7*f.index}
			if f.index != 1 {
				models["opus"] = map[string]any{"inputTokens": 20 + 5*f.index}
			}
		} else {
			models[strings.Repeat("m", 140<<10)] = map[string]any{"inputTokens": 7}
		}
		f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": fmt.Sprintf("model-evidence-%d", f.index),
			"result_index": f.index, "user_message_uuid": frame.UUID, "result": "known result", "modelUsage": models,
			"usage": map[string]any{"input_tokens": 7, "output_tokens": 4}})
		f.index++
	case "rereview-unstamped-provider":
		f.emit(map[string]any{"type": "system", "subtype": "session_state_changed", "state": "running"})
		f.emit(map[string]any{"type": "stream_event", "uuid": "p-first", "parent_tool_use_id": nil, "event": map[string]any{
			"type": "message_start", "message": map[string]any{"id": "p-tool-request", "role": "assistant", "model": "sonnet", "content": []any{}},
		}})
		f.emit(map[string]any{"type": "assistant", "uuid": "p-tool", "parent_tool_use_id": nil,
			"message": map[string]any{"id": "p-tool-request", "model": "sonnet", "content": []any{map[string]any{
				"type": "tool_use", "id": "p-tool-use", "name": "Bash", "input": map[string]any{"command": "printf provider"},
			}}}})
		f.assistant(frame.UUID, "provider folded host after tool", false)
	case "rereview-late-background", "rereview-ended-background", "rereview-restarted-background":
		if f.index == 0 {
			f.assistant(frame.UUID, "first host", false)
			f.emitTaskOwner(frame.UUID, false)
			f.result(frame.UUID, "first host", "completed")
			run := "run-1"
			if f.mode != "rereview-late-background" {
				f.emit(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "old-background-agent-A",
					"run_id": "run-1", "status": "stopped"})
				if f.mode == "rereview-restarted-background" {
					run = "run-2"
				}
			}
			f.emit(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": []any{map[string]any{
				"task_id": "old-background-agent-A", "task_type": "local_agent", "run_id": run,
			}}})
			return
		}
		f.assistant(frame.UUID, "second host", false)
		f.emit(map[string]any{"type": "assistant", "parent_tool_use_id": "spawn-tool", "agent_id": "old-background-agent-A", "uuid": "late-background-assistant",
			"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "late-background-tool", "name": "Bash", "input": map[string]any{"command": "printf late"}}}}})
		f.result(frame.UUID, "second host", "completed")
	case "rereview-fallback-supersedes":
		for i, text := range []string{"refused content", "replacement answer"} {
			part := map[string]any{"type": "assistant", "parent_tool_use_id": nil, "user_message_uuid": frame.UUID, "uuid": fmt.Sprintf("fallback-frame-%d", i),
				"message": map[string]any{"id": "fallback-message", "model": f.model, "content": []any{map[string]any{"type": "text", "text": text}}},
			}
			if i > 0 {
				part["supersedes"] = []string{"fallback-frame-0"}
			}
			f.emit(part)
		}
		f.result(frame.UUID, "", "completed")
	case "rereview-max-turns":
		f.emit(map[string]any{"type": "result", "subtype": "error_max_turns", "terminal_reason": "max_turns", "result_index": 0,
			"uuid": "max-turns-result", "user_message_uuid": frame.UUID, "is_error": true, "usage": map[string]any{"input_tokens": 43, "output_tokens": 7},
		})
	case "rereview-multiple-assistant-blocks":
		f.emit(map[string]any{"type": "stream_event", "parent_tool_use_id": nil, "user_message_uuid": frame.UUID,
			"event": map[string]any{"type": "message_start", "message": map[string]any{"id": "multi-message", "model": f.model}},
		})
		for i, text := range []string{"first ", "second"} {
			f.emit(map[string]any{"type": "stream_event", "parent_tool_use_id": nil,
				"event": map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": text}},
			})
			f.emit(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "user_message_uuid": frame.UUID, "uuid": fmt.Sprintf("complete-%d", i),
				"message": map[string]any{"id": "multi-message", "model": f.model, "content": []any{map[string]any{"type": "text", "text": text}}},
			})
		}
		f.result(frame.UUID, "", "completed")
	case "rereview-tool-narration":
		f.emit(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "user_message_uuid": frame.UUID, "uuid": "narration-frame",
			"message": map[string]any{"id": "narration-message", "model": f.model, "content": []any{
				map[string]any{"type": "text", "text": "I will use a tool."},
				map[string]any{"type": "tool_use", "name": "Bash", "id": "narration-tool", "input": map[string]any{"command": "printf sample"}},
			}},
		})
		f.result(frame.UUID, "", "completed")
	case "rereview-observation-prefix":
		for i := 0; i < 5; i++ {
			f.emit(map[string]any{"type": "system", "subtype": "commands_changed", "commands": []any{}})
		}
		f.assistant(frame.UUID, "complete despite slow session updates", false)
		f.result(frame.UUID, "complete despite slow session updates", "completed")
	case "rereview-false-compact":
		f.emit(map[string]any{"type": "system", "subtype": "session_state_changed", "state": "running"})
		f.emit(map[string]any{"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "auto", "pre_tokens": 100}})
		f.emit(map[string]any{"type": "system", "subtype": "status", "compact_result": "success"})
		f.result(frame.UUID, "local completion", "completed")
	case "review-cancel-fold":
		// The native producer has accepted stdin, while P owns the output stream.
	case "review-running-before-uuid":
		f.emit(map[string]any{"type": "system", "subtype": "session_state_changed", "state": "running"})
		f.assistant(frame.UUID, "exact host UUID", false)
	case "review-stale-permission":
		if f.index == 0 {
			f.assistant(frame.UUID, "first host", false)
			f.emitTaskOwner(frame.UUID, true)
			f.emit(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": []any{map[string]any{
				"task_id": "old-background-agent-A", "task_type": "local_agent", "run_id": "run-1",
			}}})
			f.result(frame.UUID, "first host", "completed")
			return
		}
		f.assistant(frame.UUID, "second host", false)
		f.emit(map[string]any{"type": "assistant", "parent_tool_use_id": "spawn-tool", "agent_id": "old-background-agent-A",
			"uuid": "old-tool-message", "message": map[string]any{"content": []any{map[string]any{
				"type": "tool_use", "id": "tool-first-seen-in-old-agent", "name": "Bash", "input": map[string]any{"command": "printf old-owner"},
			}}},
		})
		f.emit(map[string]any{"type": "control_request", "request_id": "background-A-tool", "request": map[string]any{
			"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "tool-first-seen-in-old-agent", "agent_id": "old-background-agent-A", "input": map[string]any{"command": "printf old-owner"},
		}})
	case "review-foreground-agent":
		f.assistant(frame.UUID, "foreground parent", false)
		f.emitTaskOwner(frame.UUID, false)
		f.emit(map[string]any{"type": "control_request", "request_id": "foreground-agent-tool", "request": map[string]any{
			"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "known-child-tool", "agent_id": "old-background-agent-A",
			"input": map[string]any{"command": "printf same-owner"},
		}})
	case "review-gap-anonymous":
		f.assistant(frame.UUID, "first host before lost terminal", false)
		f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "unattributed-successor", "result_index": 1, "result": "anonymous native completion", "usage": map[string]any{"input_tokens": 17, "output_tokens": 4}})
	case "review-repeated-assistant":
		f.assistant(frame.UUID, "duplicate native message", false)
		f.assistant(frame.UUID, "duplicate native message", false)
		f.result(frame.UUID, "duplicate native message", "completed")
	case "review-zero-failure":
		if f.index == 0 {
			f.emit(map[string]any{"type": "result", "subtype": "error_during_execution", "uuid": "zero-failure", "result_index": 0, "user_message_uuid": frame.UUID, "is_error": true,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0}, "modelUsage": map[string]any{}, "total_cost_usd": 0})
			f.index++
		} else {
			f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "after-zero-failure", "result_index": 1, "user_message_uuid": frame.UUID, "result": "second",
				"usage":          map[string]any{"input_tokens": 7, "output_tokens": 4, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
				"modelUsage":     map[string]any{f.model: map[string]any{"inputTokens": 20, "outputTokens": 4, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0}},
				"total_cost_usd": 0.02})
			f.index++
		}
	case "review-partial-models":
		for i, model := range []string{"sonnet", "opus"} {
			f.emit(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "user_message_uuid": frame.UUID,
				"message": map[string]any{"id": fmt.Sprintf("model-message-%d", i), "model": model,
					"content": []any{map[string]any{"type": "text", "text": model}}, "usage": map[string]any{"input_tokens": 3 + i}},
			})
		}
		f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "partial-models", "result_index": 0,
			"user_message_uuid": frame.UUID, "result": "two models", "usage": map[string]any{"input_tokens": 7},
		})
	case "review-usage-boundaries":
		text := frame.Message.Content[0].Text
		switch text {
		case "auto":
			f.emit(map[string]any{"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "auto"}})
		case "/compact":
			f.emit(map[string]any{"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "manual"}})
		case "clear":
			f.emit(map[string]any{"type": "conversation_reset", "new_conversation_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
				"uuid": "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "trigger": "clear"})
		}
		frameResult := map[string]any{"type": "result", "subtype": "success", "uuid": fmt.Sprintf("counter-%d", f.index),
			"result_index": f.index, "user_message_uuid": frame.UUID, "result": text, "usage": map[string]any{"input_tokens": 3, "output_tokens": 1},
		}
		if text != "auto" {
			used := (f.index + 1) * 10
			if text == "clear" {
				used = 5
			}
			frameResult["modelUsage"] = map[string]any{f.model: map[string]any{
				"inputTokens": used, "outputTokens": used, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0,
			}}
		}
		if text != "/compact" {
			cost := float64(f.index+1) * 0.01
			if text == "clear" {
				cost = 0.005
			}
			frameResult["total_cost_usd"] = cost
		}
		f.emit(frameResult)
		f.index++
	case "recall":
		f.emit(map[string]any{"type": "user", "uuid": frame.UUID, "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "queued"}}}})
	case "transport":
		f.assistant(frame.UUID, "partial", false)
		os.Exit(0)
	case "numbered-failure":
		f.result(frame.UUID, "", "api_error")
	case "result-gap":
		f.assistant(frame.UUID, "host before missing result", false)
		f.emit(map[string]any{"type": "result", "subtype": "success", "uuid": "after-gap", "result_index": 2,
			"resume_reason": "interrupted_turn", "result": "provider after gap", "usage": map[string]any{"input_tokens": 7, "output_tokens": 2},
		})
	case "foreign-duplicate":
		f.assistant(frame.UUID, "current", false)
		f.emit(map[string]any{"type": "result", "subtype": "error_during_execution", "uuid": "foreign-terminal",
			"user_message_uuid": "e5be7014-fb83-4fee-92d8-391ab87393c3", "is_error": true})
		f.result(frame.UUID, "complete answer", "completed")
	case "provider":
		f.result(frame.UUID, "provider consumed host", "completed")
	case "stale":
		f.assistant(frame.UUID, "foreground A", false)
		if _, err := f.input.Peek(1); err != nil {
			os.Exit(0)
		}
		f.mark("write-blocked", "writing")
		f.waitMarker("finish-old")
		f.result(frame.UUID, "foreground A", "completed")
		f.active = ""
		f.assistant("", "provider successor", true)
		f.waitMarker("resume-stdin")
	case "watchdog", "active":
		f.assistant(frame.UUID, "foreground A", false)
	case "approval", "ask":
		f.assistant(frame.UUID, "interaction started", false)
		tool := "Bash"
		payload := map[string]any{"command": "printf hello"}
		if f.mode == "ask" {
			tool = "AskUserQuestion"
			payload = map[string]any{"questions": []any{map[string]any{
				"question": "Select a color", "header": "Color", "options": []any{map[string]any{"label": "Red"}, map[string]any{"label": "Green"}},
			}}}
		}
		f.emit(map[string]any{"type": "control_request", "request_id": "tool-request", "request": map[string]any{
			"subtype": "can_use_tool", "tool_name": tool, "tool_use_id": "tool-1", "input": payload,
		}})
	case "rereview-workspace-cwd", "rereview-hook-cwd":
		f.assistant(frame.UUID, "check native relative write", false)
		f.emit(map[string]any{"type": "control_request", "request_id": "wrong-cwd-hook", "request": map[string]any{
			"subtype": "hook_callback", "callback_id": f.hookID, "input": map[string]any{
				"hook_event_name": "PreToolUse", "session_id": f.id, "cwd": filepath.Join(f.dir, "blocked"), "tool_use_id": "cwd-write", "tool_name": "Write", "tool_input": map[string]any{"file_path": "wrong-cwd-write.txt"},
			},
		}})
	case "workspace", "workspace-approval", "workspace-path-substitution":
		f.assistant(frame.UUID, "check native file write", false)
		cwd, _ := os.Getwd()
		path := "protected/entry"
		if f.mode != "workspace" {
			path = "safe.txt"
		}
		f.emit(map[string]any{"type": "control_request", "request_id": "tool-request", "request": map[string]any{
			"subtype": "hook_callback", "callback_id": f.hookID, "input": map[string]any{
				"hook_event_name": "PreToolUse", "session_id": f.id, "cwd": cwd, "tool_use_id": "workspace-tool", "tool_name": "Write", "tool_input": map[string]any{"file_path": path},
			},
		}})
	case "large":
		f.result(frame.UUID, strings.Repeat("large answer ", 64<<10), "completed")
	default:
		if len(frame.Message.Content) > 0 && frame.Message.Content[0].Text == "/compact" {
			f.emit(map[string]any{"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "manual"}})
		}
		f.assistant(frame.UUID, "complete answer", false)
		f.result(frame.UUID, "complete answer", "completed")
	}
}

func (f *fixture) waitMarker(name string) {
	for i := 0; i < 2000; i++ {
		if _, err := os.Stat(filepath.Join(f.dir, name)); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	os.Exit(72)
}

func fixtureProject(dir, cwd string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, cwd)
	return filepath.Join(dir, "config", "projects", name)
}

func (f *fixture) emitTaskOwner(input string, background bool) {
	f.emit(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "uuid": "spawn-message", "user_message_uuid": input,
		"message": map[string]any{"id": "spawn", "content": []any{map[string]any{"type": "tool_use", "id": "spawn-tool", "name": "Agent"}}},
	})
	f.emit(map[string]any{"type": "system", "subtype": "task_started", "task_id": "old-background-agent-A", "run_id": "run-1",
		"tool_use_id": "spawn-tool", "is_backgrounded": background, "task_type": "local_agent",
	})
}
