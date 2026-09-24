package kimi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// wireTurnOutcome 只读取官方主 Agent 终态，不按助手正文或中间重试推测失败。
type wireTurnOutcome struct {
	// Type 是官方 Wire 事件名称。
	Type string `json:"type"`
	// AgentID 区分主任务与子 Agent。
	AgentID string `json:"agentId"`
	// Reason 是完整一轮的最终结果。
	Reason string `json:"reason"`
	// Error 保存失败终态的结构化供应商证据。
	Error json.RawMessage `json:"error"`
}

// wireTurnError 保留官方错误类别和仅供诊断的说明。
type wireTurnError struct {
	// Code 是 Kimi 发布的稳定错误类别。
	Code string `json:"code"`
	// Message 是原始诊断，不作为助手正文发送。
	Message string `json:"message"`
	// Details 保存官方提供的 HTTP 状态，避免按错误文案猜测类别。
	Details struct {
		// StatusCode 是供应商请求返回的 HTTP 状态。
		StatusCode int `json:"statusCode"`
	} `json:"details"`
}

// readWireFailure 补齐 Kimi 2.0.2 将失败误报为 end_turn 的 ACP 路径。
func readWireFailure(ctx context.Context, path string, offset int64) error {
	// 官方 ACP 响应先于异步 Wire 写入完成；等待当前轮终态落盘；本地斜杠命令可能不产生模型轮次。
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		failure, settled, _ := scanWireFailure(path, offset)
		if settled {
			return failure
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			failure, settled, started := scanWireFailure(path, offset)
			if settled {
				return failure
			}
			if !started {
				return nil
			}
			return acp.NewInternalError(map[string]any{"error": "Kimi turn outcome unavailable"})
		case <-ticker.C:
		}
	}
}

// scanWireFailure 只扫描本轮新增记录，同时识别尚未落盘的主任务终态。
func scanWireFailure(path string, offset int64) (error, bool, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, false
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || offset < 0 || info.Size() < offset || info.Size()-offset > 64*1024*1024 {
		return nil, false, false
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, false, false
	}
	scanner := bufio.NewScanner(io.LimitReader(file, 64*1024*1024))
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	var failure error
	started, settled := false, false
	for scanner.Scan() {
		var record wireTurnOutcome
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			return failure, settled, started
		}
		if record.Type == "turn.prompt" && record.AgentID == "main" {
			started, settled = true, false
		}
		if record.Type != "turn.ended" || record.AgentID != "main" {
			continue
		}
		failure = nil
		settled = true
		if record.Reason != "failed" {
			continue
		}
		message, code := "Kimi turn failed", ""
		var detail wireTurnError
		if len(record.Error) > 0 {
			_ = json.Unmarshal(record.Error, &detail)
		}
		code = detail.Code
		if detail.Message != "" {
			message = detail.Message
		}
		data := map[string]any{"kimiErrorCode": code}
		if kind := kimiErrorKind(&detail); kind != "" {
			data["errorKind"] = kind
		}
		failure = &acp.RequestError{Code: -32603, Message: message, Data: data}
	}
	if scanner.Err() != nil {
		return nil, false, false
	}
	return failure, settled, started
}

// kimiErrorKind 只提升已知类别，未识别错误保持不可推断重试资格。
func kimiErrorKind(failure *wireTurnError) string {
	if failure == nil {
		return ""
	}
	switch failure.Code {
	case "provider.rate_limit":
		return "rate_limit"
	case "provider.auth_error":
		return "authentication_failed"
	case "provider.overloaded":
		return "overloaded"
	case "provider.connection_error":
		return "network_error"
	case "provider.api_error":
		switch failure.Details.StatusCode {
		case 401:
			return "authentication_failed"
		case 429:
			return "rate_limit"
		case 408, 504:
			return "timeout"
		}
		if failure.Details.StatusCode >= 500 && failure.Details.StatusCode <= 599 {
			return "server_error"
		}
	}
	return ""
}
