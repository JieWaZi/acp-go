package acpclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/cursor"
	"github.com/JieWaZi/acp-go/pkg/qoder"
	"github.com/JieWaZi/acp-go/pkg/workbuddy"
)

// TestPluginDirectoryLaunchFixture records the exact argv received by the native child.
func TestPluginDirectoryLaunchFixture(t *testing.T) {
	trace := os.Getenv("PLUGIN_DIRECTORY_FIXTURE_TRACE")
	if trace == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if args[len(args)-1] == "--version" {
		fmt.Println(os.Getenv("PLUGIN_DIRECTORY_FIXTURE_VERSION"))
		os.Exit(0)
	}
	body, _ := json.Marshal(args)
	if err := os.WriteFile(trace, body, 0600); err != nil {
		os.Exit(2)
	}
	os.Exit(1)
}

func TestTypedPluginDirectoriesReachOnlyNativeACPProcess(t *testing.T) {
	for _, provider := range []string{"cursor", "workbuddy", "qoder", "qoder-cn"} {
		t.Run(provider, func(t *testing.T) {
			cwd := t.TempDir()
			directories := []string{filepath.Join(cwd, "plugin one"), filepath.Join(cwd, "plugin-two")}
			for i, dir := range directories {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				directories[i], _ = filepath.EvalSymlinks(dir)
			}
			trace := filepath.Join(cwd, "argv.json")
			exe, _ := os.Executable()
			prefix := []string{"-test.run=^TestPluginDirectoryLaunchFixture$", "--"}
			version := map[string]string{"cursor": "2026.11.05-86047df", "workbuddy": "2.181.0", "qoder": "1.1.83", "qoder-cn": "1.1.83"}[provider]
			env := append(os.Environ(), "PLUGIN_DIRECTORY_FIXTURE_TRACE="+trace, "PLUGIN_DIRECTORY_FIXTURE_VERSION="+version)
			var client agentclient.Client
			var err error
			switch provider {
			case "cursor":
				client, err = cursor.NewClient(t.Context(), cursor.Options{Executable: exe, PrefixArgs: prefix, Environment: env, WorkingDirectory: cwd, PluginDirectories: directories}, agentclient.HostCallbacks{})
			case "workbuddy":
				client, err = workbuddy.NewClient(t.Context(), workbuddy.Options{Executable: exe, PrefixArgs: prefix, Environment: env, WorkingDirectory: cwd, PluginDirectories: directories}, agentclient.HostCallbacks{})
			default:
				client, err = qoder.NewClient(t.Context(), qoder.Options{RuntimeID: provider, Executable: exe, PrefixArgs: prefix, Environment: env, WorkingDirectory: cwd, PluginDirectories: directories}, agentclient.HostCallbacks{})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			_, _ = client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1})
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			if err = json.Unmarshal(raw, &args); err != nil {
				t.Fatal(err)
			}
			expected := []string{"--plugin-dir", directories[0], "--plugin-dir", directories[1], "acp"}
			switch provider {
			case "workbuddy":
				expected = []string{"--plugin-dir", directories[0], directories[1], "--acp"}
			case "qoder", "qoder-cn":
				expected[len(expected)-1] = "--acp"
			}
			if !slices.Equal(args, expected) {
				t.Fatalf("native argv %q, want %q", args, expected)
			}
		})
	}
}
