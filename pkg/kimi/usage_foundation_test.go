package kimi

import (
	"context"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUsageFoundationKimiConsumptionWithoutContext 验证没有上下文数据时消费仍有标准通知。
func TestUsageFoundationKimiConsumptionWithoutContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"usage.record","model":"fixture","usageScope":"turn","usage":{"inputOther":7,"output":5,"inputCacheRead":3,"inputCacheCreation":2}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	usage, update := readWireUsage(path, 0, "")
	if usage == nil || update == nil {
		t.Fatalf("consumption notification missing: %+v %+v", usage, update)
	}
	reported, ok := acpmeta.ReadTurnUsage(update.Meta)
	if !ok || reported.InputTokens == nil || *reported.InputTokens != 7 || reported.ContextKnown || update.Used != 0 || update.Size != 0 || reported.Complete {
		t.Fatalf("wrong partial snapshot: %+v %+v", reported, update)
	}
}

// TestUsageFoundationKimiFieldPresence 验证缺失分类不被反序列化默认值伪造为零。
func TestUsageFoundationKimiFieldPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"usage.record","usageScope":"turn","usage":{"inputOther":7,"output":0}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	usage, update := readWireUsage(path, 0, "")
	if usage == nil || update == nil {
		t.Fatal("partial usage lost")
	}
	got, ok := acpmeta.ReadTurnUsage(update.Meta)
	if !ok || got.InputTokens == nil || *got.InputTokens != 7 || got.OutputTokens == nil || *got.OutputTokens != 0 || got.CacheReadTokens != nil || got.CacheWriteTokens != nil {
		t.Fatalf("unknown became zero: %+v", got)
	}
}

// TestUsageFixKimiTruncatedTail 保留截断尾行与扫描错误前已经解析的真实消费。
func TestUsageFixKimiTruncatedTail(t *testing.T) {
	for _, tail := range []string{`{"type":"usage.record","usage":`, strings.Repeat("x", 9*1024*1024)} {
		path := filepath.Join(t.TempDir(), "wire.jsonl")
		valid := `{"type":"usage.record","usageScope":"turn","usage":{"inputOther":7,"output":5,"inputCacheRead":3,"inputCacheCreation":2}}` + "\n"
		if err := os.WriteFile(path, []byte(valid+tail), 0600); err != nil {
			t.Fatal(err)
		}
		usage, update := readWireUsage(path, 0, "")
		if usage == nil || update == nil {
			t.Fatal("valid consumption erased by broken tail")
		}
		reported, ok := acpmeta.ReadTurnUsage(update.Meta)
		if !ok || usage.InputTokens != 7 || usage.OutputTokens != 5 || reported.Complete {
			t.Fatalf("wrong partial: %+v / %+v", usage, reported)
		}
	}
}

// usageFixKimiClient 记录真实 ACP pipe 到达的消费通知。
type usageFixKimiClient struct {
	// Client 保留测试未使用的其他客户端接口。
	acp.Client
	// notifications 保存有界测试通知，不读取真实资源。
	notifications chan acp.SessionNotification
}

// SessionUpdate 接受适配器经真实 SDK 连接发出的通知。
func (client *usageFixKimiClient) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	client.notifications <- notification
	return nil
}

// TestUsageFixKimiAdapterTerminals 验证 native/wire 一致或冲突、失败截断和取消均投影同源消费。
func TestUsageFixKimiAdapterTerminals(t *testing.T) {
	for _, mode := range []string{"equal", "conflict", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			home := t.TempDir()
			agent, err := NewAgent(context.Background(), Config{KimiPath: executable, PrefixArgs: []string{"-test.run=^TestKimiCurrentUsageACPProcess$", "--"}, Environment: append(os.Environ(), "ACP_GO_KIMI_CURRENT_USAGE_FIXTURE=1", "KIMI_CODE_HOME="+home, "ACP_GO_KIMI_USAGE_FIX_MODE="+mode), WorkingDirectory: cwd, StateDirectory: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close(context.Background())
			agentIn, hostOut := io.Pipe()
			hostIn, agentOut := io.Pipe()
			defer agentIn.Close()
			defer hostOut.Close()
			defer hostIn.Close()
			defer agentOut.Close()
			host := &usageFixKimiClient{notifications: make(chan acp.SessionNotification, 20)}
			agent.SetAgentConnection(acp.NewAgentSideConnection(agent, agentOut, agentIn))
			acp.NewClientSideConnection(host, hostOut, hostIn)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err = agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			session, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
			if err != nil {
				t.Fatal(err)
			}
			result, promptErr := agent.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("usage")}})
			if mode == "failure" {
				if promptErr == nil {
					t.Fatal("fake CLI failure became success")
				}
			} else if promptErr != nil {
				t.Fatal(promptErr)
			}
			if mode == "cancel" && result.StopReason != acp.StopReasonCancelled {
				t.Fatalf("cancel lost: %+v", result)
			}
			reported, ok := acpmeta.ReadTurnUsage(result.Meta)
			if !ok || reported.Source != "kimi.wire.usage" || reported.Complete || result.Usage == nil || result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 5 || result.Usage.TotalTokens != 17 || reported.InputTokens == nil || *reported.InputTokens != 7 {
				t.Fatalf("native/wire mismatch mode=%s result=%+v reported=%+v", mode, result, reported)
			}
			for {
				select {
				case update := <-host.notifications:
					if update.Update.UsageUpdate == nil {
						continue
					}
					notified, known := acpmeta.ReadTurnUsage(update.Update.UsageUpdate.Meta)
					if !known || notified.InputTokens == nil || *notified.InputTokens != 7 || notified.Source != reported.Source || notified.Complete {
						t.Fatalf("notice differs: %+v", notified)
					}
					return
				case <-ctx.Done():
					t.Fatal("consumption notification missing before terminal")
					return
				}
			}
		})
	}
}
