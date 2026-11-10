package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/codex"
)

func TestCodexSessionOpensWithoutAccountPreflight(t *testing.T) {
	for _, operation := range []string{"new", "resume", "load", "fork"} {
		t.Run(operation, func(t *testing.T) {
			client, ctx, cwd, trace := accountPreflightFixture(t, "no_account_read")
			if err := openAccountPreflightSession(ctx, client, operation, cwd); err != nil {
				t.Fatalf("%s was blocked by an account preflight: %v", operation, err)
			}
			assertCodexNoAccountPreflight(t, trace)
		})
	}
}

func TestCodexNativeAuthenticationErrorsSurviveWithoutAccountPreflight(t *testing.T) {
	for operation, method := range map[string]string{"new": "thread/start", "resume": "thread/resume", "fork": "thread/fork", "prompt": "turn/start"} {
		t.Run(operation, func(t *testing.T) {
			client, ctx, cwd, trace := accountPreflightFixture(t, "no_account_read_auth_"+method)
			var err error
			if operation == "prompt" {
				opened, openErr := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
				if openErr != nil {
					t.Fatalf("session open was blocked before the native prompt: %v", openErr)
				}
				run, reserveErr := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: opened.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}, agentclient.PromptOptions{})
				if reserveErr != nil {
					t.Fatal(reserveErr)
				}
				result := run()
				err = result.Receipt.OperationError
				if result.Outcome != nil {
					t.Fatalf("rejected native prompt produced an outcome: %+v", result.Outcome)
				}
			} else {
				err = openAccountPreflightSession(ctx, client, operation, cwd)
			}
			var native *acp.RequestError
			if !errors.As(err, &native) || native.Code != -32000 || native.Message != "Authentication required: selected credential expired" {
				t.Fatalf("native %s authentication error was replaced: %v", method, err)
			}
			if data, ok := native.Data.(map[string]any); !ok || data["resource"] != "thread" {
				t.Fatalf("native authentication details were lost: %+v", native)
			}
			assertCodexNoAccountPreflight(t, trace)
		})
	}
}

func accountPreflightFixture(t *testing.T, mode string) (*codex.Client, context.Context, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	cwd := t.TempDir()
	trace := filepath.Join(cwd, "trace.ndjson")
	client, err := codex.NewClient(ctx, codex.Options{
		Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestAppServerFixture$", "--"},
		Environment: append(os.Environ(), "CODEX_NEXT_FIXTURE="+mode, "CODEX_NEXT_TRACE="+trace), WorkingDirectory: cwd,
	}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	return client, ctx, cwd, trace
}

func openAccountPreflightSession(ctx context.Context, client *codex.Client, operation, cwd string) error {
	switch operation {
	case "new":
		_, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
		return err
	case "resume":
		_, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "thread-1", Cwd: cwd})
		return err
	case "load":
		_, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: "thread-1", Cwd: cwd})
		return err
	case "fork":
		_, err := client.UnstableForkSession(ctx, acp.UnstableForkSessionRequest{SessionId: "thread-1", Cwd: cwd, Meta: map[string]any{"forkReceiptDirectory": filepath.Join(cwd, "fork-receipts"), "sourceCwd": cwd}})
		return err
	default:
		return errors.New("unknown fixture operation")
	}
}

func assertCodexNoAccountPreflight(t *testing.T, trace string) {
	t.Helper()
	wire, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	for {
		var frame struct {
			// Method identifies the native operation received by the child.
			Method string `json:"method"`
		}
		if err := decoder.Decode(&frame); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		if frame.Method == "account/read" {
			t.Fatal("session execution queried account status")
		}
	}
}
