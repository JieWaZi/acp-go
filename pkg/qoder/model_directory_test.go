package qoder_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/qoder"
)

func TestQoderSDKDirectoryKeepsUnavailableNewModels(t *testing.T) {
	options, _ := fixtureOptions(t, false)
	options.PrefixArgs = []string{"-test.run=^TestQoderSDKDirectoryChild$", "--"}
	options.ExpectedUserID = "fixture-user"
	options.Environment = append(options.Environment, "QODER_SDK_DIRECTORY_FIXTURE=1")
	client, err := qoder.NewClient(t.Context(), options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(t.Context())
	rows, err := client.ReadModelDirectory(t.Context())
	if err != nil || len(rows) != 2 || rows[0].ID != "new-free" || !rows[0].New ||
		!rows[0].Enabled || !rows[0].Free || rows[1].Enabled {
		t.Fatalf("unexpected SDK directory: %+v, %v", rows, err)
	}
}

func TestQoderSDKReadsIdentityBeforeCatalog(t *testing.T) {
	options, _ := fixtureOptions(t, false)
	options.PrefixArgs = []string{"-test.run=^TestQoderSDKDirectoryChild$", "--"}
	options.ExpectedUserID = "fixture-user"
	options.Environment = append(options.Environment, "QODER_SDK_DIRECTORY_FIXTURE=1")
	identity, err := qoder.ReadNativeIdentity(t.Context(), options)
	if err != nil || identity.UserID != "fixture-user" {
		t.Fatalf("SDK could not verify the account before opening the catalog: %v", err)
	}
}

func TestQoderContextWindowInitializationDoesNotQueryAccount(t *testing.T) {
	for _, runtimeID := range []string{"qoder", "qoder-cn"} {
		t.Run(runtimeID, func(t *testing.T) {
			options, trace := contextWindowFixtureOptions(t)
			options.RuntimeID = runtimeID
			client, err := qoder.NewClient(t.Context(), options, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(t.Context())
			if _, err := client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatalf("context selection was blocked by an account preflight: %v", err)
			}
			if _, err := client.NewSession(t.Context(), acp.NewSessionRequest{Cwd: options.WorkingDirectory}); err != nil {
				t.Fatal(err)
			}
			assertQoderNoAccountPreflight(t, trace)
		})
	}
}

func TestQoderContextWindowStillRejectsUnadvertisedSize(t *testing.T) {
	options, trace := contextWindowFixtureOptions(t)
	options.ContextWindow = 512000
	client, err := qoder.NewClient(t.Context(), options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(t.Context())
	if _, err := client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1}); !errors.Is(err, qoder.ErrContextWindowUnsupported) {
		t.Fatalf("unadvertised context size was not rejected: %v", err)
	}
	assertQoderNoAccountPreflight(t, trace)
}

func TestQoderContextWindowPreservesNativeAuthenticationErrors(t *testing.T) {
	for _, method := range []string{"initialize", "session/new", "session/prompt"} {
		t.Run(method, func(t *testing.T) {
			options, trace := contextWindowFixtureOptions(t)
			options.Environment = append(options.Environment, "QODER_AUTH_ERROR_METHOD="+method)
			client, err := qoder.NewClient(t.Context(), options, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(t.Context())
			_, err = client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1})
			if method != "initialize" {
				if err != nil {
					t.Fatalf("startup failed before native %s: %v", method, err)
				}
				var opened acp.NewSessionResponse
				opened, err = client.NewSession(t.Context(), acp.NewSessionRequest{Cwd: options.WorkingDirectory})
				if method == "session/prompt" {
					if err != nil {
						t.Fatal(err)
					}
					run, reserveErr := client.ReservePrompt(t.Context(), acp.PromptRequest{SessionId: opened.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}, agentclient.PromptOptions{})
					if reserveErr != nil {
						t.Fatal(reserveErr)
					}
					err = run().Receipt.OperationError
				}
			}
			var native *acp.RequestError
			if !errors.As(err, &native) || native.Code != -32000 || native.Message != "Authentication required: selected credential expired" {
				t.Fatalf("native %s authentication error was replaced: %v", method, err)
			}
			assertQoderNoAccountPreflight(t, trace)
		})
	}
}

func TestQoderExplicitModelDirectoryStillVerifiesIdentity(t *testing.T) {
	options, trace := fixtureOptions(t, false)
	options.Environment = append(options.Environment, "QODER_SDK_DIRECTORY_FIXTURE=1")
	options.ExpectedUserID = "another-selected-user"
	client, err := qoder.NewClient(t.Context(), options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(t.Context())
	if _, err := client.ReadModelDirectory(t.Context()); err == nil {
		t.Fatal("explicit directory read accepted a different account")
	}
	wire, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(wire), `"type":"daemon.account_info"`) || strings.Contains(string(wire), `"type":"daemon.get_models"`) {
		t.Fatalf("explicit directory did not verify identity before models: %s, %v", wire, err)
	}
}

func contextWindowFixtureOptions(t *testing.T) (qoder.Options, string) {
	t.Helper()
	options, trace := fixtureOptions(t, false)
	options.Model = "new-free"
	options.ContextWindow = 256000
	options.ExpectedUserID = "locally-selected-user"
	options.Environment = append(options.Environment, "QODER_SDK_DIRECTORY_FIXTURE=1", "QODER_ACCOUNT_INFO_FORBIDDEN=1")
	return options, trace
}

func assertQoderNoAccountPreflight(t *testing.T, trace string) {
	t.Helper()
	wire, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), `"type":"daemon.account_info"`) {
		t.Fatal("context selection queried account identity")
	}
	if !strings.Contains(string(wire), `"type":"daemon.get_models"`) {
		t.Fatal("context selection skipped native model metadata")
	}
}

func TestQoderReadsACPAndSDKCatalogsConcurrently(t *testing.T) {
	options, _ := fixtureOptions(t, false)
	options.ExpectedUserID = "fixture-user"
	options.Environment = append(options.Environment, "QODER_CATALOG_RENDEZVOUS="+t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(t.Context())
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	catalog, err := client.ReadModels(ctx)
	if err != nil || catalog.Options.Grouped == nil {
		t.Fatalf("the ACP and SDK probes did not complete together: %+v, %v", catalog, err)
	}
	var models []acp.SessionConfigValueId
	for _, group := range *catalog.Options.Grouped {
		for _, model := range group.Options {
			models = append(models, model.Value)
		}
	}
	slices.Sort(models)
	if want := []acp.SessionConfigValueId{"custom-model", "new-free", "other", "shared-model"}; !slices.Equal(models, want) {
		t.Fatalf("model catalog = %v, want %v", models, want)
	}
	if catalog.DefaultID != nil {
		t.Fatalf("model catalog retained unavailable default %q", *catalog.DefaultID)
	}
}

func waitForCatalogPeer(self, peer string) {
	dir := os.Getenv("QODER_CATALOG_RENDEZVOUS")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, self), nil, 0o600); err != nil {
		os.Exit(5)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, peer)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(6)
}

func TestQoderSDKDirectoryChild(t *testing.T) {
	if os.Getenv("QODER_SDK_DIRECTORY_FIXTURE") != "1" {
		return
	}
	if os.Args[len(os.Args)-1] != "--sdk" {
		os.Exit(2)
	}
	runSDKDirectoryFixture()
}

func runSDKDirectoryFixture() {
	in, out := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	trace, err := os.OpenFile(os.Getenv("ACP_FAMILY_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer trace.Close()
	for in.Scan() {
		var frame struct {
			// ID correlates the SDK request and reply.
			ID string `json:"request_id"`
			// Request holds the control parameters received by the fixture.
			Request map[string]any `json:"request"`
		}
		if json.Unmarshal(in.Bytes(), &frame) != nil {
			os.Exit(2)
		}
		fmt.Fprintln(trace, in.Text())
		var result any
		switch frame.Request["type"] {
		case "daemon.initialize":
			result = map[string]any{"daemon_protocol_version": 2, "capabilities": []string{"daemon_queries_v1"}}
		case "daemon.account_info":
			if os.Getenv("QODER_ACCOUNT_INFO_FORBIDDEN") == "1" {
				_ = out.Encode(map[string]any{"type": "control_response", "response": map[string]any{
					"request_id": frame.ID, "subtype": "error", "code": "ACCOUNT_PREFLIGHT_FORBIDDEN",
				}})
				continue
			}
			result = map[string]any{"account": map[string]string{"userId": "fixture-user"}}
		case "daemon.get_models":
			if frame.Request["fetchStrategy"] != "live" {
				os.Exit(3)
			}
			waitForCatalogPeer("sdk", "acp")
			models := []map[string]any{
				{"value": "new-free", "displayName": "Free Model", "isNew": true, "isEnabled": true, "isFree": true, "availableContextWindows": []int64{128000, 256000}, "defaultContextWindow": 128000},
				{"value": "new-paid", "displayName": "Paid Model", "isNew": true, "isEnabled": false},
			}
			if os.Getenv("QODER_CATALOG_RENDEZVOUS") != "" {
				models = append(models,
					map[string]any{"value": "standard", "isEnabled": false},
					map[string]any{"value": "shared-model", "displayName": "SDK Shared Model", "isNew": true, "isEnabled": true},
					map[string]any{"value": "custom-model", "source": "custom", "isEnabled": true},
				)
			}
			result = map[string]any{"models": models}
		case "daemon.shutdown":
			result = map[string]any{}
		default:
			os.Exit(4)
		}
		if out.Encode(map[string]any{"type": "control_response", "response": map[string]any{
			"request_id": frame.ID, "subtype": "success", "response": result,
		}}) != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}
