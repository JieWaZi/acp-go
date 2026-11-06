package acpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

func TestCatalogProcess(t *testing.T) {
	if os.Getenv("ACP_DISCOVERY_FIXTURE") == "" {
		return
	}
	trace, _ := os.OpenFile(os.Getenv("ACP_DISCOVERY_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	out := json.NewEncoder(os.Stdout)
	options := func(model string) []any {
		effort := "low"
		if model == "second" {
			effort = "high"
		}
		return []any{
			map[string]any{"id": "native-model", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []any{map[string]any{"value": "first", "name": "First"}, map[string]any{"value": "second", "name": "Second"}}},
			map[string]any{"id": "effort", "name": "Effort", "category": "thought_level", "type": "select", "currentValue": effort, "options": []any{map[string]any{"value": effort, "name": effort}}},
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	selectedSecond := false
	for scanner.Scan() {
		var frame struct {
			// ID correlates the fixture response.
			ID json.RawMessage `json:"id"`
			// Method selects a native operation.
			Method string `json:"method"`
			// Params contains the exact requested selection.
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &frame)
		fmt.Fprintln(trace, string(scanner.Bytes()))
		var result any
		switch frame.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"close": map[string]any{}}}}
		case "session/new":
			result = map[string]any{"sessionId": "probe", "configOptions": options("first"), "modes": map[string]any{"currentModeId": "default", "availableModes": []any{map[string]any{"id": "default", "name": "Default"}}}}
			if os.Getenv("ACP_DISCOVERY_BEHAVIOR") == "no-id" {
				result = map[string]any{"configOptions": options("first")}
			}
			if os.Getenv("ACP_DISCOVERY_BEHAVIOR") == "mixed-catalog" {
				result.(map[string]any)["models"] = map[string]any{"currentModelId": "second", "availableModels": []any{map[string]any{"modelId": "second", "name": "Second", "_meta": acpmeta.WithModelConfigOptions(map[string]any{"native-property": "retained"}, []acp.SessionConfigOption{})}}}
			}
		case "session/set_config_option":
			value := frame.Params["value"].(string)
			if value == "second" && os.Getenv("ACP_DISCOVERY_BEHAVIOR") == "reject" {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": map[string]any{"code": -32602, "message": "selection unavailable"}})
				continue
			}
			if value == "first" && selectedSecond && os.Getenv("ACP_DISCOVERY_BEHAVIOR") == "restore-mismatch" {
				value = "second"
			}
			selectedSecond = selectedSecond || value == "second"
			result = map[string]any{"configOptions": options(value)}
		case "session/close":
			result = map[string]any{}
		default:
			os.Exit(4)
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result})
	}
	os.Exit(0)
}

func TestModelDiscoveryUsesDisposableSessionAndPerModelReceipts(t *testing.T) {
	executable, _ := os.Executable()
	dir := t.TempDir()
	trace := filepath.Join(dir, "wire")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := NewClient(ctx, Options{RuntimeID: "fixture", Process: agentclient.ProcessSpec{Executable: executable, Args: []string{"-test.run=^TestCatalogProcess$"}, Env: append(os.Environ(), "ACP_DISCOVERY_FIXTURE=1", "ACP_DISCOVERY_TRACE="+trace), Dir: dir}}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err = c.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	catalog, err := c.ReadModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.DefaultID == nil || *catalog.DefaultID != "first" || catalog.Options.Ungrouped == nil || len(*catalog.Options.Ungrouped) != 2 {
		t.Fatalf("catalog: %#v", catalog)
	}
	if len(catalog.Modes) != 1 || catalog.Modes[0].Id != "default" {
		t.Fatalf("permission facts lost: %#v", catalog.Modes)
	}
	for i, row := range *catalog.Options.Ungrouped {
		config, ok := acpmeta.ReadModelConfigCatalog(row.Meta)
		if !ok || len(config.UnknownConfigIDs) != 0 || len(config.Options) != 1 {
			t.Fatalf("model config: %#v", row)
		}
		want := "low"
		if i == 1 {
			want = "high"
		}
		if string(config.Options[0].Select.CurrentValue) != want {
			t.Fatalf("shared opening-model effort leaked: %#v", config)
		}
	}
	if len(c.sessions) != 0 {
		t.Fatal("discovery session became a chat owner")
	}
	if _, err = c.ReadModels(ctx); err != nil {
		t.Fatal(err)
	}
	wire, _ := os.ReadFile(trace)
	if strings.Count(string(wire), `"method":"session/new"`) != 1 || strings.Contains(string(wire), "session/prompt") || !strings.Contains(string(wire), "session/close") {
		t.Fatalf("discovery lifecycle: %s", wire)
	}
	if strings.LastIndex(string(wire), `"value":"first"`) < strings.LastIndex(string(wire), `"value":"second"`) {
		t.Fatal("opening selection was not restored")
	}
}

func TestModelDiscoveryDoesNotBorrowUnprobedCapabilitiesOrTrustRestoreACK(t *testing.T) {
	for _, behavior := range []string{"reject", "restore-mismatch", "no-id", "mixed-catalog"} {
		t.Run(behavior, func(t *testing.T) {
			executable, _ := os.Executable()
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := NewClient(ctx, Options{Process: agentclient.ProcessSpec{Executable: executable, Args: []string{"-test.run=^TestCatalogProcess$"}, Dir: dir, Env: append(os.Environ(), "ACP_DISCOVERY_FIXTURE=1", "ACP_DISCOVERY_TRACE="+filepath.Join(dir, "wire"), "ACP_DISCOVERY_BEHAVIOR="+behavior)}}, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(context.Background())
			if _, err = c.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			catalog, err := c.ReadModels(ctx)
			if behavior == "restore-mismatch" || behavior == "no-id" {
				if err == nil {
					t.Fatal("contradictory restore ACK treated as success")
				}
				if !c.unusable {
					t.Fatal("unproven native mutation left reusable owner")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			row := (*catalog.Options.Ungrouped)[1]
			if behavior == "mixed-catalog" {
				if catalog.DefaultID == nil || *catalog.DefaultID != "first" || row.Meta["native-property"] != "retained" {
					t.Fatalf("modern default or legacy metadata lost: %#v", catalog)
				}
				return
			}
			config, known := acpmeta.ReadModelConfigCatalog(row.Meta)
			if !known || len(config.Options) != 0 || len(config.UnknownConfigIDs) == 0 {
				t.Fatalf("unprobed model inherited opening settings: %#v", config)
			}
		})
	}
}
