package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// persistForkHistory 对齐 Agent SDK 0.3.232 forkSession 的原生 transcript 变换。
// 独立复制完整原生消息、工具结果、压缩及替换记录，不生成历史 Prompt。
func (a *Agent) persistForkHistory(sourceID, targetID, cwd string) error {
	source, err := findHistoryPath(sourceID, a.environment)
	if err != nil {
		return err
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxHistoryFileBytes {
		return errors.New("Claude fork transcript exceeds size limit")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxHistoryLineBytes)
	entries := []map[string]any{}
	historySuppressed := false
	replacements := []any{}
	ids := map[string]string{}
	byID := map[string]map[string]any{}
	for scanner.Scan() {
		var entry map[string]any
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		if err = decoder.Decode(&entry); err != nil {
			return fmt.Errorf("decoding native Claude fork: %w", err)
		}
		kind, _ := entry["type"].(string)
		id, _ := entry["uuid"].(string)
		sidechain, _ := entry["isSidechain"].(bool)
		switch kind {
		case "user", "assistant", "attachment", "system", "progress":
			if id == "" || sidechain {
				continue
			}
			replacement, idErr := a.idGenerator()
			if idErr != nil {
				return idErr
			}
			ids[id] = replacement
			byID[id] = entry
			entries = append(entries, entry)
		case "history-suppression":
			if entry["sessionId"] == sourceID {
				historySuppressed = true
			}
		case "content-replacement":
			if entry["sessionId"] == sourceID {
				if values, ok := entry["replacements"].([]any); ok {
					replacements = append(replacements, values...)
				}
			}
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("Claude session has no native history to fork")
	}
	output := []map[string]any{}
	if historySuppressed {
		output = append(output, map[string]any{"type": "history-suppression", "sessionId": targetID, "cause": "fork_inherit", "ts": time.Now().UTC().Format(time.RFC3339Nano)})
	}
	for _, entry := range entries {
		if entry["type"] == "progress" {
			continue
		}
		original := entry["uuid"].(string)
		parent, _ := entry["parentUuid"].(string)
		seen := map[string]bool{}
		for parent != "" && byID[parent] != nil && byID[parent]["type"] == "progress" {
			if seen[parent] {
				return errors.New("Claude transcript parent cycle")
			}
			seen[parent] = true
			parent, _ = byID[parent]["parentUuid"].(string)
		}
		entry["uuid"] = ids[original]
		entry["parentUuid"] = nil
		if ids[parent] != "" {
			entry["parentUuid"] = ids[parent]
		}
		if logical, ok := entry["logicalParentUuid"].(string); ok {
			entry["logicalParentUuid"] = nil
			if ids[logical] != "" {
				entry["logicalParentUuid"] = ids[logical]
			}
		}
		entry["sessionId"] = targetID
		entry["isSidechain"] = false
		entry["forkedFrom"] = map[string]any{"sessionId": sourceID, "messageUuid": original}
		if entry["type"] == "system" && entry["subtype"] == "model_refusal_fallback" {
			entry["neutralizedByFork"] = true
		}
		for _, key := range []string{"teamName", "agentName", "sessionKind", "slug", "sourceToolAssistantUUID"} {
			delete(entry, key)
		}
		output = append(output, entry)
	}
	if len(output) == 0 {
		return errors.New("Claude session has no native messages to fork")
	}
	output[len(output)-1]["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	if len(replacements) > 0 {
		id, idErr := a.idGenerator()
		if idErr != nil {
			return idErr
		}
		output = append(output, map[string]any{"type": "content-replacement", "sessionId": targetID, "replacements": replacements, "uuid": id, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
	}
	output = append(output, map[string]any{"type": "relocated", "sessionId": targetID, "relocatedCwd": cwd})
	destination, err := forkHistoryDirectory(source, cwd)
	if err != nil {
		return err
	}
	target := filepath.Join(destination, targetID+".jsonl")
	temp, err := os.CreateTemp(destination, ".fork-*.jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	encoder := json.NewEncoder(temp)
	for _, entry := range output {
		if err = encoder.Encode(entry); err != nil {
			_ = temp.Close()
			return err
		}
	}
	if err = errors.Join(temp.Sync(), temp.Close()); err != nil {
		return err
	}
	// 独占链接把已经完整同步的文件公开，不能覆盖既有原生身份。
	if err = os.Link(temp.Name(), target); err != nil {
		return err
	}
	directory, err := os.Open(destination)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// forkHistoryDirectory 按 CLI 的当前 cwd 项目键绑定子历史；relocated 记录只影响展示定位，不能替代文件位置。
func forkHistoryDirectory(source, cwd string) (string, error) {
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(filepath.Dir(filepath.Dir(source)), claudeProjectKey(canonical))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	return directory, nil
}

// claudeProjectKey 对齐 Agent SDK 的 Xo：UTF-16 单元转连字符，长路径截取 200 字符并附加 31 倍哈希。
func claudeProjectKey(cwd string) string {
	var key strings.Builder
	var hash int32
	for _, unit := range utf16.Encode([]rune(cwd)) {
		hash = hash*31 + int32(unit)
		if unit >= 'a' && unit <= 'z' || unit >= 'A' && unit <= 'Z' || unit >= '0' && unit <= '9' {
			key.WriteByte(byte(unit))
		} else {
			key.WriteByte('-')
		}
	}
	value := key.String()
	if len(value) <= 200 {
		return value
	}
	absolute := int64(hash)
	if absolute < 0 {
		absolute = -absolute
	}
	return value[:200] + "-" + strconv.FormatInt(absolute, 36)
}

// reconcileForkHistory 修复原生收据已确认、但跨工作区恢复尚未成功的子会话文件位置。
func (a *Agent) reconcileForkHistory(id, cwd string) error {
	source, err := findHistoryPath(id, a.environment)
	if err != nil {
		return err
	}
	directory, err := forkHistoryDirectory(source, cwd)
	if err != nil {
		return err
	}
	target := filepath.Join(directory, id+".jsonl")
	if source == target {
		return nil
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		return errors.New("Claude fork destination already exists")
	}
	if err := os.Rename(source, target); err != nil {
		return err
	}
	for _, path := range []string{filepath.Dir(source), directory} {
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
			return err
		}
	}
	return nil
}
