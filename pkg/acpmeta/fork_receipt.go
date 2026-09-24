package acpmeta

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	acp "github.com/coder/acp-go-sdk"
)

// ErrForkUnconfirmed 表示目前没有可证明的原生结果，调用者不得重新创建分支。
var ErrForkUnconfirmed = errors.New("native fork outcome is unconfirmed")

// BeginForkReceipt 在原生创建前占用宿主提供的操作专属目录，重试不能重复创建分支。
func BeginForkReceipt(request acp.UnstableForkSessionRequest) error {
	root, err := forkReceiptDirectory(request)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	marker, err := os.OpenFile(filepath.Join(root, "pending"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return ErrForkUnconfirmed
	}
	if err != nil {
		return err
	}
	if err = errors.Join(marker.Sync(), marker.Close()); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// forkReceipt 是宿主持久操作目录内跨连接和重启保留的原生分叉结果。
type forkReceipt struct {
	// Source 是来源原生会话身份。
	Source acp.SessionId `json:"source"`
	// Position 是创建分支时使用的不透明位置。
	Position string `json:"position"`
	// Child 是已确认的独立原生身份。
	Child acp.SessionId `json:"child"`
}

// ReconcileOnly 标识宿主正在对账不确定结果，适配器不得再次执行创建命令。
func ReconcileOnly(request acp.UnstableForkSessionRequest) bool {
	value, _ := request.Meta["reconcileOnly"].(bool)
	return value
}

// ReadForkReceipt 验证请求归属后读取 Adapter 已同步的原生结果。
func ReadForkReceipt(request acp.UnstableForkSessionRequest) (acp.SessionId, error) {
	root, err := forkReceiptDirectory(request)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(root, "receipt.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrForkUnconfirmed
	}
	if err != nil {
		return "", err
	}
	var receipt forkReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Source != request.SessionId || receipt.Position != ForkPosition(request.Meta) || receipt.Child == "" || receipt.Child == receipt.Source {
		return "", ErrForkUnconfirmed
	}
	return receipt.Child, nil
}

// WriteForkReceipt 在 ACP 返回前原子保存原生身份，响应丢失时可安全对账。
func WriteForkReceipt(request acp.UnstableForkSessionRequest, child acp.SessionId) error {
	if child == "" || child == request.SessionId {
		return ErrForkUnconfirmed
	}
	root, err := forkReceiptDirectory(request)
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(root, "pending")); err != nil {
		return ErrForkUnconfirmed
	}
	if _, statErr := os.Stat(filepath.Join(root, "receipt.json")); statErr == nil {
		if previous, readErr := ReadForkReceipt(request); readErr != nil || previous != child {
			return ErrForkUnconfirmed
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	data, err := json.Marshal(forkReceipt{Source: request.SessionId, Position: ForkPosition(request.Meta), Child: child})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(root, ".acp-fork-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), filepath.Join(root, "receipt.json")); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// forkReceiptDirectory 要求宿主为每次操作提供唯一且绝对的持久目录。
func forkReceiptDirectory(request acp.UnstableForkSessionRequest) (string, error) {
	root, _ := request.Meta["forkReceiptDirectory"].(string)
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return "", acp.NewInvalidParams(map[string]any{"message": "forkReceiptDirectory must be a unique absolute directory for this operation"})
	}
	return root, nil
}
