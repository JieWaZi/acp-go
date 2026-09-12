package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"

	sqlite3 "github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// storePart 保留官方消息中与宿主展示有关的内容。
type storePart struct {
	// kind 区分正文、思考、工具调用与结果。
	kind string
	// text 是未丢失 Unicode 的正文。
	text string
	// id 是原生工具调用身份。
	id string
	// name 是原生工具名称。
	name string
	// args 是完整结构化工具参数。
	args map[string]any
	// result 是原生工具执行结果。
	result any
}

// storeMessage 是已提交的官方消息，不包含二进制检查点的历史副本。
type storeMessage struct {
	// id 是不可变 blob 身份，供消息去重。
	id string
	// role 是官方消息角色。
	role string
	// content 保留消息内容顺序。
	content []storePart
}

// pendingCall 是官方检查点确认正在等待审批或回答的工具。
type pendingCall struct {
	// id 是原生调用标识。
	id string
	// name 是原生工具名称。
	name string
	// args 保存审批与表单需要的完整输入。
	args map[string]any
}

// storeBatch 是一次增量读取的消息和仍有效的待处理请求。
type storeBatch struct {
	// messages 只包含本次新提交的消息。
	messages []storeMessage
	// pending 排除已经提交执行或已有结果的调用。
	pending []pendingCall
}

// storeCursor 仅跟踪一个确定会话的 SQLite WAL 增量。
type storeCursor struct {
	// path 是受管会话的唯一数据库。
	path string
	// row 是已读取的最后 rowid。
	row int64
	// waiting 按调用身份保存检查点中的待处理请求。
	waiting map[string]pendingCall
	// committed 防止已执行工具的旧检查点重现为审批。
	committed map[string]bool
	// order 保持审批首次出现的顺序。
	order []string
}

// newStoreCursor 创建只读增量游标，跟踪该会话的待审批和已提交消息。
func newStoreCursor(path string) *storeCursor {
	return &storeCursor{path: path, waiting: map[string]pendingCall{}, committed: map[string]bool{}}
}

// open 只读打开官方会话库，保留 WAL 可见性，不创建或修改库。
func (s *storeCursor) open(ctx context.Context) (*sqlite3.Conn, error) {
	if _, err := os.Stat(s.path); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: s.path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sqlite3.OpenContext(ctx, u.String())
	if err == nil {
		db.SetInterrupt(ctx)
	}
	return db, err
}

// seed 恢复前记住当前日志边界，避免重放历史成为新回合输出。
func (s *storeCursor) seed(ctx context.Context) error { _, err := s.read(ctx); return err }

// read 借鉴 Omnigent 的 rowid 增量与 pending/committed/resolved 判据。
func (s *storeCursor) read(ctx context.Context) (storeBatch, error) {
	var batch storeBatch
	db, err := s.open(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return batch, nil
	}
	if err != nil {
		return batch, err
	}
	defer db.Close()
	rows, _, err := db.Prepare(`SELECT rowid,id,data FROM blobs WHERE rowid > ? ORDER BY rowid`)
	if err != nil {
		return batch, err
	}
	defer rows.Close()
	if err = rows.BindInt64(1, s.row); err != nil {
		return batch, err
	}
	for rows.Step() {
		row := rows.ColumnInt64(0)
		id := rows.ColumnText(1)
		data := rows.ColumnBlob(2, nil)
		var plain map[string]any
		isPlain := json.Unmarshal(data, &plain) == nil
		objects := embeddedObjects(data)
		for _, object := range objects {
			content, ok := object["content"].([]any)
			if !ok {
				continue
			}
			pending := false
			if provider, ok := object["providerOptions"].(map[string]any); ok {
				if cursor, ok := provider["cursor"].(map[string]any); ok {
					_, pending = cursor["pendingToolCallStartedAtMs"].(float64)
				}
			}
			message := storeMessage{id: id, role: stringValue(object["role"])}
			for _, raw := range content {
				part, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				p := storePart{kind: stringValue(part["type"]), text: stringValue(part["text"]), id: stringValue(part["toolCallId"]), name: stringValue(part["toolName"]), result: part["result"]}
				p.args, _ = part["args"].(map[string]any)
				if p.kind == "reasoning" && p.text == "" {
					p.text = stringValue(part["reasoning"])
				}
				if p.id != "" {
					switch p.kind {
					case "tool-call":
						if pending {
							if _, exists := s.waiting[p.id]; !exists {
								s.order = append(s.order, p.id)
							}
							s.waiting[p.id] = pendingCall{id: p.id, name: p.name, args: p.args}
						} else {
							s.committed[p.id] = true
						}
					case "tool-result":
						s.committed[p.id] = true
					}
				}
				message.content = append(message.content, p)
			}
			if isPlain && message.role != "" {
				batch.messages = append(batch.messages, message)
			}
		}
		s.row = row
	}
	if err = rows.Err(); err != nil {
		return batch, err
	}
	for _, id := range s.order {
		if !s.committed[id] {
			batch.pending = append(batch.pending, s.waiting[id])
		}
	}
	return batch, nil
}

// embeddedObjects 按括号与字符串转义提取检查点内的 JSON，不把 Unicode 字节转换为 Latin-1。
func embeddedObjects(data []byte) []map[string]any {
	result := []map[string]any{}
	for i := 0; i < len(data); i++ {
		if data[i] != '{' {
			continue
		}
		k := i + 1
		for k < len(data) && bytes.ContainsRune([]byte(" \r\n\t"), rune(data[k])) {
			k++
		}
		if k >= len(data) || data[k] != '"' {
			continue
		}
		depth := 0
		quoted, escape := false, false
		for j := i; j < len(data); j++ {
			c := data[j]
			if quoted {
				if escape {
					escape = false
				} else if c == '\\' {
					escape = true
				} else if c == '"' {
					quoted = false
				}
				continue
			}
			if c == '"' {
				quoted = true
			} else if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					var object map[string]any
					if json.Unmarshal(data[i:j+1], &object) == nil {
						result = append(result, object)
						i = j
					}
					break
				}
			}
		}
	}
	return result
}

// stringValue 仅提取协议中真实的字符串值。
func stringValue(v any) string { s, _ := v.(string); return s }
