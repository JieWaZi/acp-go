package acpmeta

import (
	"encoding/json"
	"fmt"

	acp "github.com/coder/acp-go-sdk"
)

const (
	// ForkUnsupported 表示适配器没有经过验证的原生分叉能力。
	ForkUnsupported = "unsupported"
	// ForkLatest 表示只能复制原生会话当前末尾。
	ForkLatest = "latest"
	// ForkTurn 表示可以按适配器返回的不透明位置截断。
	ForkTurn = "turn"
)

// ForkMetadata 声明原生分叉精度；宿主不得根据厂商名称推测能力。
func ForkMetadata(mode string) map[string]any {
	return map[string]any{"fork": map[string]any{"mode": mode}}
}

// ForkMode 读取初始化响应的分叉精度；缺失或未知声明一律拒绝。
func ForkMode(meta map[string]any) string {
	value, _ := meta["fork"].(map[string]any)
	mode, _ := value["mode"].(string)
	if mode == ForkLatest || mode == ForkTurn {
		return mode
	}
	return ForkUnsupported
}

// ForkPosition 从终态或分叉请求读取原生位置；只能在同类适配器之间原样传递。
func ForkPosition(meta map[string]any) string {
	value, _ := meta["forkPosition"].(string)
	return value
}

// PositionMetadata 封装已经由原生协议确认的结束位置。
func PositionMetadata(position string) map[string]any {
	if position == "" {
		return nil
	}
	return map[string]any{"forkPosition": position}
}

// ForkMCPServers 将 ACP 实验接口中的同义结构转换成稳定会话参数。
func ForkMCPServers(servers []acp.UnstableMcpServer) ([]acp.McpServer, error) {
	data, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	var result []acp.McpServer
	err = json.Unmarshal(data, &result)
	return result, err
}

// ForkResponse 保留会话配置并验证分支拥有独立原生身份。
func ForkResponse(source acp.SessionId, response acp.NewSessionResponse) (acp.UnstableForkSessionResponse, error) {
	if response.SessionId == "" || response.SessionId == source {
		return acp.UnstableForkSessionResponse{}, fmt.Errorf("native fork did not return an independent session")
	}
	data, err := json.Marshal(response)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	var result acp.UnstableForkSessionResponse
	err = json.Unmarshal(data, &result)
	return result, err
}

// ForkCapability 让标准能力与版本限定的扩展精度保持一致。
func ForkCapability(mode string) *acp.SessionForkCapabilities {
	if mode != ForkLatest && mode != ForkTurn {
		return nil
	}
	return &acp.SessionForkCapabilities{}
}
