package cursor

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// outcomeCursor 只读取本进程启动后的官方结束标记，补足套餐错误不触发 stop Hook 的路径。
type outcomeCursor struct {
	// directory 是该 CLI 使用的日志目录，可能由隔离 TMPDIR 指定。
	directory string
	// pid 是本适配器直接创建的原生进程身份。
	pid int
	// started 排除进程标识重用前的旧日志。
	started time.Time
	// offsets 保留各轮与日志轮转的读取边界。
	offsets map[string]int64
}

// processOutcome 只反序列化分类字段，不保留日志中的账号、请求或凭据。
type processOutcome struct {
	// Message 必须是官方明确的执行终态记录。
	Message string `json:"message"`
	// Metadata 保存终态与供应商错误分类。
	Metadata struct {
		// Outcome 区分正常完成与真实失败。
		Outcome string `json:"outcome"`
		// ErrorCode 是官方套餐、认证等错误分类。
		ErrorCode string `json:"error_code"`
		// GRPCCode 保存结构化连接错误类别。
		GRPCCode string `json:"grpc_code"`
	} `json:"metadata"`
}

// newOutcomeCursor 绑定官方日志目录和刚启动的进程，不跟随全局 latest 链接。
func newOutcomeCursor(pid int, started time.Time, environment []string) *outcomeCursor {
	root := nativeacp.EnvironmentValue(environment, "TMPDIR")
	if root == "" {
		root = os.TempDir()
	}
	return &outcomeCursor{directory: filepath.Join(root, "cursor-agent-logs-"+strconv.Itoa(os.Getuid())), pid: pid, started: started, offsets: map[string]int64{}}
}

// failure 消费新增完整日志行，仅把真实错误终态转换为 ACP 失败，不以日志推测成功或用量。
func (c *outcomeCursor) failure() error {
	paths, err := filepath.Glob(filepath.Join(c.directory, "session-*-"+strconv.Itoa(c.pid)+"-*.log"))
	if err != nil {
		return err
	}
	var failure error
	for _, path := range paths {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		stat, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if stat.ModTime().Before(c.started) {
			file.Close()
			continue
		}
		offset := c.offsets[path]
		if stat.Size() < offset {
			offset = 0
		}
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return err
		}
		reader := bufio.NewReader(file)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					file.Close()
					return readErr
				}
				break
			}
			offset += int64(len(line))
			c.offsets[path] = offset
			marker := "structured-log.info "
			index := strings.Index(line, marker)
			if index < 0 {
				continue
			}
			var event processOutcome
			if json.Unmarshal([]byte(line[index+len(marker):]), &event) != nil || event.Message != "agent_cli.turn.outcome" || event.Metadata.Outcome != "error" {
				continue
			}
			kind := ""
			switch {
			case event.Metadata.ErrorCode == "upgrade":
				kind = "plan_upgrade_required"
			case event.Metadata.GRPCCode == "resource_exhausted":
				kind = "rate_limit"
			case event.Metadata.GRPCCode == "unauthenticated":
				kind = "authentication_failed"
			case event.Metadata.GRPCCode == "unavailable":
				kind = "server_error"
			case event.Metadata.GRPCCode == "deadline_exceeded":
				kind = "timeout"
			}
			failure = acp.NewInternalError(map[string]any{"errorKind": kind, "message": "Cursor reported an unsuccessful interactive turn", "cursorErrorCode": event.Metadata.ErrorCode})
		}
		file.Close()
	}
	return failure
}
