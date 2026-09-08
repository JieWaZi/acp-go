package pi

// 文件差异和 Bash 终端映射移植自 pi-acp src/acp/session.ts，许可见 UPSTREAM-LICENSE。
import (
	"os"
	"path/filepath"
	"strings"
)

// fileSnapshot 保留一次文件工具调用前的真实文件内容。
type fileSnapshot struct {
	// path 是解析到会话目录的绝对文件路径。
	path string
	// oldText 为 nil 表示调用前文件不存在。
	oldText *string
}

// snapshotFile 在 Pi 执行写入前保存旧内容；无法读取时保持文本输出回退。
func (s *session) snapshotFile(event map[string]any) {
	name := text(event["toolName"])
	if name != "edit" && name != "write" {
		return
	}
	path := text(object(event["args"])["path"])
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.cwd, path)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	snapshot := fileSnapshot{path: path}
	if err == nil {
		value := string(data)
		snapshot.oldText = &value
	}
	s.snapshots[text(event["toolCallId"])] = snapshot
}

// decorateTool 保留 pi-acp 的结构化差异、终端增量和退出码。
func (s *session) decorateTool(update map[string]any, id, name, status string, input, output any, final bool) {
	if name == "bash" && input != nil {
		if command := text(object(input)["command"]); command != "" {
			update["title"] = command
		}
		if _, exists := s.bashOutput[id]; !exists {
			s.bashOutput[id] = ""
			update["content"] = []any{map[string]any{"type": "terminal", "terminalId": id}}
			update["_meta"] = map[string]any{"terminal_info": map[string]any{"terminal_id": id, "cwd": s.cwd}}
		}
	}
	if previous, bash := s.bashOutput[id]; bash && output != nil {
		result := object(output)
		next := messageText(result["content"])
		if next == "" {
			for _, field := range []string{"stdout", "output", "stderr"} {
				if value := text(object(result["details"])[field]); value != "" {
					next += value
				}
			}
		}
		delta := next
		if strings.HasPrefix(next, previous) {
			delta = strings.TrimPrefix(next, previous)
		}
		s.bashOutput[id] = next
		meta := map[string]any{}
		if delta != "" {
			meta["terminal_output"] = map[string]any{"terminal_id": id, "data": delta}
		}
		if final {
			code := 0
			if status == "failed" {
				code = 1
			}
			for _, fields := range []map[string]any{object(result["details"]), result} {
				for _, key := range []string{"exitCode", "code"} {
					if value, ok := fields[key].(float64); ok {
						code = int(value)
					}
				}
			}
			meta["terminal_exit"] = map[string]any{"terminal_id": id, "exit_code": code, "signal": nil}
		}
		update["_meta"] = meta
		// 保留文本内容，未实现终端元数据的 ACP 客户端仍能看到输出。
	}
	if snapshot, ok := s.snapshots[id]; ok {
		if input != nil && snapshot.oldText != nil {
			needle := text(object(input)["oldText"])
			if needle != "" && strings.Count(*snapshot.oldText, needle) == 1 {
				line := 1 + strings.Count((*snapshot.oldText)[:strings.Index(*snapshot.oldText, needle)], "\n")
				update["locations"] = []any{map[string]any{"path": snapshot.path, "line": line}}
			}
		}
		if final && status == "completed" {
			if data, err := os.ReadFile(snapshot.path); err == nil && (snapshot.oldText == nil || *snapshot.oldText != string(data)) {
				update["content"] = []any{map[string]any{"type": "diff", "path": snapshot.path, "oldText": snapshot.oldText, "newText": string(data)}}
				delete(update, "rawOutput")
			}
		}
	}
}
