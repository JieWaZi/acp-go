package cursor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// cursorPermissionState 只读取官方保存的权限字段，不复制身份或加密材料。
type cursorPermissionState struct {
	// ApprovalMode 是当前原生审批策略。
	ApprovalMode string `json:"approvalMode"`
	// RunEverything 兼容旧聊天中尚无审批策略字段的全权限标识。
	RunEverything bool `json:"isRunEverything"`
}

// approvalMode 按官方 meta 编码读取生效权限，数据无法确认时阻止提交用户消息。
func (s *storeCursor) approvalMode(ctx context.Context) (string, error) {
	db, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	defer db.Close()
	row, _, err := db.Prepare("SELECT value FROM meta WHERE key = '0'")
	if err != nil {
		return "", err
	}
	defer row.Close()
	if !row.Step() {
		if err = row.Err(); err != nil {
			return "", err
		}
		return "", errors.New("Cursor approval state is missing")
	}
	encoded := row.ColumnText(0)
	if len(encoded) > 1024*1024 {
		return "", errors.New("Cursor approval state exceeds size limit")
	}
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return "", errors.New("invalid Cursor approval state encoding")
	}
	var state cursorPermissionState
	if json.Unmarshal(data, &state) != nil {
		return "", errors.New("invalid Cursor approval state")
	}
	switch state.ApprovalMode {
	case "allowlist", "auto-review", "unrestricted":
		return state.ApprovalMode, nil
	case "":
		if state.RunEverything {
			return "unrestricted", nil
		}
		return "allowlist", nil
	default:
		return "", errors.New("unknown Cursor approval mode")
	}
}

// ensurePermission 使用官方本地命令更新聊天中持久化的权限，并只读核对实际回执；不会直接改写聊天数据库。
func (a *Agent) ensurePermission(ctx context.Context, s *interactiveSession) error {
	expected := "allowlist"
	switch s.mode {
	case "auto":
		expected = "auto-review"
	case "full-access":
		expected = "unrestricted"
	}
	actual, err := s.store.approvalMode(ctx)
	if err != nil {
		return err
	}
	if actual == expected {
		return nil
	}
	// 先撤销可能遗留的最大权限，自动审查不可用时也不能落回全权限。
	if actual == "unrestricted" && expected != "unrestricted" {
		if err = a.permissionCommand(ctx, s, "/run-everything off", "allowlist", false); err != nil {
			return err
		}
		actual = "allowlist"
	}
	if expected == "allowlist" && actual == "auto-review" {
		return a.permissionCommand(ctx, s, "/auto-review off", expected, false)
	}
	if expected == "auto-review" {
		return a.permissionCommand(ctx, s, "/auto-review on", expected, true)
	}
	if expected == "unrestricted" {
		return a.permissionCommand(ctx, s, "/run-everything on", expected, false)
	}
	return nil
}

// permissionCommand 等待本地权限命令与数据库状态一致，不把命令文字或配置文件当作生效证明。
func (a *Agent) permissionCommand(ctx context.Context, s *interactiveSession, command, expected string, allowFallback bool) error {
	change, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := s.terminal.submit(change, command); err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		actual, err := s.store.approvalMode(change)
		if err != nil {
			return err
		}
		if actual == expected && terminalReady(s.terminal.text()) {
			return nil
		}
		if allowFallback && actual == "allowlist" && strings.Contains(s.terminal.text(), "Auto-review is unavailable") && terminalReady(s.terminal.text()) {
			return nil
		}
		select {
		case <-change.Done():
			return errors.New("Cursor did not confirm the requested approval mode")
		case <-s.terminal.done:
			return errors.New("Cursor exited while changing approval mode")
		case <-ticker.C:
		}
	}
}
