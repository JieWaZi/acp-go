package gemini

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// sessionRecord 保留公共身份到真实持久会话的映射，不存储会话内容或凭据。
type sessionRecord struct {
	// NativeID 是官方 CLI 生成的真实会话标识。
	NativeID acp.SessionId `json:"nativeId"`
	// Model 是已成功应用的规范模型标识。
	Model string `json:"model"`
	// Reasoning 是已成功应用的思考选择。
	Reasoning string `json:"reasoning"`
	// Cwd 固定官方项目注册表使用的真实路径。
	Cwd string `json:"cwd"`
	// Mode 是成功应用的官方权限模式。
	Mode acp.SessionModeId `json:"mode,omitempty"`
	// HasHistory 记录是否已确认真正可恢复的原生内容，确认后不因失败而清除。
	HasHistory bool `json:"hasHistory"`
}

// recordName 使用散列文件名阻止外部 sessionId 路径穿越。
func recordName(id acp.SessionId) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join("acp-go-sessions", fmt.Sprintf("%x.json", digest))
}

// saveRecord 原子提交可重启恢复的配置与身份。
func (state *profileState) saveRecord(id acp.SessionId, record sessionRecord) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return state.write(recordName(id), data)
}

// loadRecord 读取受管映射，缺失时保留官方会话标识用于原生恢复。
func (state *profileState) loadRecord(id acp.SessionId, cwd string) (sessionRecord, error) {
	path := filepath.Join(state.root, recordName(id))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return sessionRecord{NativeID: id, Cwd: cwd, Reasoning: "default", HasHistory: true}, nil
	}
	if err != nil {
		return sessionRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return sessionRecord{}, errors.New("unsafe Gemini session mapping")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionRecord{}, err
	}
	var record sessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return sessionRecord{}, err
	}
	if record.NativeID == "" || record.Cwd == "" {
		return sessionRecord{}, errors.New("invalid Gemini session mapping")
	}
	return record, nil
}

// transcript 保存真正原生文件的候选元数据与原始字节。
type transcript struct {
	// path 是受管 tmp 目录中的真实普通文件。
	path string
	// data 保存逐字节原始记录。
	data []byte
	// updated 是日志中最后一个有效的更新时间。
	updated time.Time
	// modified 在更新时间相同时确定最新原生写入。
	modified time.Time
}

// inspectTranscript 只读取官方日志字段选择候选，不重新生成或修改对话。
func inspectTranscript(path string, data []byte, id acp.SessionId) (transcript, bool, error) {
	metadata := map[string]any{}
	messages := map[string]map[string]any{}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal(line, &record); err != nil {
			return transcript{}, false, fmt.Errorf("invalid Gemini transcript %s: %w", path, err)
		}
		if patch, ok := record["$set"].(map[string]any); ok {
			for key, value := range patch {
				metadata[key] = value
			}
			if values, ok := patch["messages"].([]any); ok {
				messages = map[string]map[string]any{}
				for _, value := range values {
					if message, ok := value.(map[string]any); ok {
						key, _ := message["id"].(string)
						messages[key] = message
					}
				}
			}
		} else if key, ok := record["id"].(string); ok {
			messages[key] = record
		} else {
			for key, value := range record {
				metadata[key] = value
			}
			if values, ok := record["messages"].([]any); ok {
				for _, value := range values {
					if message, ok := value.(map[string]any); ok {
						key, _ := message["id"].(string)
						messages[key] = message
					}
				}
			}
		}
	}
	if metadata["sessionId"] != string(id) {
		return transcript{}, false, nil
	}
	resumable := false
	for _, message := range messages {
		kind, _ := message["type"].(string)
		content := strings.TrimSpace(transcriptText(message["content"]))
		if kind == "user" {
			internal := strings.HasPrefix(content, "<session_context>") || strings.HasPrefix(content, "<hook_context>")
			command := strings.HasPrefix(content, "/") || strings.HasPrefix(content, "?")
			ignored := content == "" || internal || command
			if !ignored {
				resumable = true
			}
		}
		if kind == "gemini" {
			tools, _ := message["toolCalls"].([]any)
			thoughts, _ := message["thoughts"].([]any)
			if content != "" || len(tools) > 0 || len(thoughts) > 0 {
				resumable = true
			}
		}
	}
	if !resumable {
		return transcript{}, false, nil
	}
	value, _ := metadata["lastUpdated"].(string)
	updated, _ := time.Parse(time.RFC3339Nano, value)
	return transcript{path: path, data: data, updated: updated}, true, nil
}

// resumableTranscript 查找官方可恢复的真实日志，调用方持有状态锁。
func (state *profileState) resumableTranscript(id acp.SessionId) (*transcript, error) {
	var best *transcript
	err := filepath.WalkDir(filepath.Join(state.root, "tmp"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe Gemini history symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasPrefix(entry.Name(), "session-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		candidate, match, err := inspectTranscript(path, data, id)
		if err != nil {
			return err
		}
		if !match {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		candidate.modified = info.ModTime()
		newer := best == nil || candidate.updated.After(best.updated)
		if best != nil && candidate.updated.Equal(best.updated) {
			newer = candidate.modified.After(best.modified) || candidate.modified.Equal(best.modified) && candidate.path < best.path
		}
		if newer {
			best = &candidate
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return best, nil
}

// hasTranscript 区分尚未执行的空会话与原生已经落盘的部分历史。
func (state *profileState) hasTranscript(id acp.SessionId) (bool, error) {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	candidate, err := state.resumableTranscript(id)
	return candidate != nil, err
}

// protectTranscript 在 load 启动前将最新真实记录复制到不会发生分钟碰撞的稳定文件。
func (state *profileState) protectTranscript(id acp.SessionId) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	best, err := state.resumableTranscript(id)
	if err != nil {
		return err
	}
	if best == nil {
		return errors.New("Gemini has no genuine resumable transcript")
	}
	digest := sha256.Sum256([]byte(id))
	name := filepath.Join(filepath.Dir(best.path), fmt.Sprintf("session-!acp-go-%x.jsonl", digest[:16]))
	if name == best.path {
		return nil
	}
	relative, err := filepath.Rel(state.root, name)
	if err != nil {
		return err
	}
	return state.write(relative, best.data)
}

// transcriptText 按官方 PartListUnion 提取用于空历史过滤的文本。
func transcriptText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []any:
		var result strings.Builder
		for _, part := range value {
			result.WriteString(transcriptText(part))
		}
		return result.String()
	case map[string]any:
		text, _ := value["text"].(string)
		return text
	default:
		return ""
	}
}

// historySnapshot 保存替换前真实文件字节，失败时恢复官方初始化可能追加的检查点。
func (state *profileState) historySnapshot(id acp.SessionId) (map[string][]byte, error) {
	result := map[string][]byte{}
	err := filepath.WalkDir(filepath.Join(state.root, "tmp"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unsafe Gemini history symlink")
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "session-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			value := map[string]any{}
			if json.Unmarshal(line, &value) != nil {
				continue
			}
			if value["sessionId"] == string(id) {
				relative, err := filepath.Rel(state.root, path)
				if err != nil {
					return err
				}
				result[relative] = data
				break
			}
		}
		return nil
	})
	return result, err
}

// restoreHistory 仅回滚该真实会话拥有的文件，绝不回写默认用户目录。
func (state *profileState) restoreHistory(id acp.SessionId, before map[string][]byte) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	current, err := state.historySnapshot(id)
	if err != nil {
		return err
	}
	for path := range current {
		if _, ok := before[path]; !ok {
			if err := os.Remove(filepath.Join(state.root, path)); err != nil {
				return err
			}
		}
	}
	for path, data := range before {
		if err := state.write(path, data); err != nil {
			return err
		}
	}
	return nil
}
