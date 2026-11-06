package qoder_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
		var result any
		switch frame.Request["type"] {
		case "daemon.initialize":
			result = map[string]any{"daemon_protocol_version": 2, "capabilities": []string{"daemon_queries_v1"}}
		case "daemon.account_info":
			result = map[string]any{"account": map[string]string{"userId": "fixture-user"}}
		case "daemon.get_models":
			if frame.Request["fetchStrategy"] != "live" {
				os.Exit(3)
			}
			waitForCatalogPeer("sdk", "acp")
			models := []map[string]any{
				{"value": "new-free", "displayName": "Free Model", "isNew": true, "isEnabled": true, "isFree": true},
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
