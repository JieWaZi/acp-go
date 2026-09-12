package cursor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// cursorEnvironment 隔离可变 CLI 配置，只有受管聊天日志跨 Agent 生命周期持久保存。
func cursorEnvironment(config Config) (directory, state string, environment []string, err error) {
	source := nativeacp.EnvironmentValue(config.Environment, "CURSOR_CONFIG_DIR")
	if source == "" {
		home := nativeacp.EnvironmentValue(config.Environment, "HOME")
		if home == "" {
			home, err = os.UserHomeDir()
			if err != nil {
				return
			}
		}
		source = filepath.Join(home, ".cursor")
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return
	}
	state = config.StateDirectory
	if state == "" {
		var cache string
		cache, err = os.UserCacheDir()
		if err != nil {
			return
		}
		sum := sha256.Sum256([]byte(source + "\x00" + config.WorkingDirectory + "\x00" + config.CursorPath))
		state = filepath.Join(cache, "acp-go", "cursor", hex.EncodeToString(sum[:16]))
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return
	}
	err = os.MkdirAll(filepath.Join(state, "chats"), 0700)
	if err != nil {
		return
	}
	directory, err = os.MkdirTemp("", "acp-go-cursor-")
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	data, readErr := os.ReadFile(filepath.Join(source, "cli-config.json"))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		err = readErr
		return
	}
	if len(data) > 0 {
		var configObject map[string]any
		if json.Unmarshal(data, &configObject) != nil {
			err = errors.New("invalid Cursor CLI configuration")
			return
		}
		err = os.WriteFile(filepath.Join(directory, "cli-config.json"), data, 0600)
		if err != nil {
			return
		}
	}
	for _, name := range []string{"chats", "acp-sessions"} {
		if err = os.MkdirAll(filepath.Join(state, name), 0700); err != nil {
			return
		}
		if err = os.Symlink(filepath.Join(state, name), filepath.Join(directory, name)); err != nil {
			return
		}
	}
	if err != nil {
		return
	}
	environment = config.Environment
	for key, value := range map[string]string{"CURSOR_CONFIG_DIR": directory, "CURSOR_DATA_DIR": filepath.Join(state, "data"), "NO_OPEN_BROWSER": "1", "TERM": "xterm-256color"} {
		environment = nativeacp.WithEnvironment(environment, key, value)
	}
	return
}

// sessionEnvironment 隔离每个交互会话的模型选择，避免并发会话覆盖彼此的配置。
func sessionEnvironment(source, state, directory string, environment []string, selection nativeacp.CursorModelSelection) ([]string, error) {
	config := filepath.Join(directory, "config")
	if err := os.MkdirAll(config, 0700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(source, "cli-config.json"))
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil || value == nil {
		return nil, errors.New("invalid Cursor session configuration")
	}
	value["selectedModel"] = selection
	data, err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(config, "cli-config.json"), data, 0600); err != nil {
		return nil, err
	}
	link := filepath.Join(config, "chats")
	if _, err = os.Lstat(link); errors.Is(err, os.ErrNotExist) {
		if err = os.Symlink(filepath.Join(state, "chats"), link); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return nativeacp.WithEnvironment(environment, "CURSOR_CONFIG_DIR", config), nil
}

// verifySelection 只比较已声明的模型参数；终端未采用指定配置时禁止发送提示。
func verifySelection(directory string, expected nativeacp.CursorModelSelection) error {
	data, err := os.ReadFile(filepath.Join(directory, "config", "cli-config.json"))
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return errors.New("invalid Cursor execution configuration")
	}
	var actual nativeacp.CursorModelSelection
	if json.Unmarshal(fields["selectedModel"], &actual) != nil || actual.ModelID != expected.ModelID {
		return errors.New("Cursor did not retain the selected execution model")
	}
	values := map[string]string{}
	for _, parameter := range actual.Parameters {
		values[parameter.ID] = parameter.Value
	}
	for _, parameter := range expected.Parameters {
		if values[parameter.ID] != parameter.Value {
			return errors.New("Cursor did not retain the selected execution parameters")
		}
	}
	return nil
}

// executionModelArgument 使用官方 --model 的参数式语法覆盖恢复历史中的旧模型，不猜测模型别名或思考后缀。
func executionModelArgument(selection nativeacp.CursorModelSelection) (string, error) {
	valid := func(value string) bool { return value != "" && !strings.ContainsAny(value, "[],=\x00\r\n") }
	if !valid(selection.ModelID) {
		return "", errors.New("invalid Cursor execution model")
	}
	if len(selection.Parameters) == 0 {
		return selection.ModelID, nil
	}
	parameters := make([]string, 0, len(selection.Parameters))
	for _, parameter := range selection.Parameters {
		if !valid(parameter.ID) || !valid(parameter.Value) {
			return "", errors.New("invalid Cursor execution model parameter")
		}
		parameters = append(parameters, parameter.ID+"="+parameter.Value)
	}
	return selection.ModelID + "[" + strings.Join(parameters, ",") + "]", nil
}
