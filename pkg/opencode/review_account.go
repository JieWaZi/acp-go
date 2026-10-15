package opencode

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

// reviewAccountConfiguration 只读检查组织配置来源，不复制数据库或组织 token。
func reviewAccountConfiguration(environment []string) error {
	root, err := reviewProfilePath(environment, "XDG_DATA_HOME", ".local/share")
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(root, "opencode*.db"))
	if err != nil {
		return err
	}
	if custom := nativeacp.EnvironmentValue(environment, "OPENCODE_DB"); custom != "" {
		if custom == ":memory:" {
			return errors.New("permission review cannot inspect in-memory account configuration")
		}
		if !filepath.IsAbs(custom) {
			custom = filepath.Join(root, custom)
		}
		paths = []string{custom}
	}
	for _, path := range paths {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := reviewAccountDatabase(path); err != nil {
			return err
		}
	}
	return nil
}

// reviewAccountDatabase 使用当前官方表只读判定远程组织覆盖，未知数据库安全回退人工。
func reviewAccountDatabase(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	database, err := sqlite3.OpenContext(ctx, uri.String())
	if err != nil {
		return err
	}
	defer database.Close()
	database.SetInterrupt(ctx)
	statement, _, err := database.Prepare(`SELECT active_org_id FROM account_state WHERE active_account_id IS NOT NULL`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for statement.Step() {
		if statement.ColumnText(0) != "" {
			return errors.New("permission review requires manual approval for active organization configuration")
		}
	}
	return statement.Err()
}
