package pi

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

// directoryProbe supplies an actual generated extension's tool-call input.
type directoryProbe struct {
	// Tool selects the native tool whose permission hook runs.
	Tool string `json:"tool"`
	// Input contains the original native arguments, never executed by the probe.
	Input map[string]any `json:"input"`
	// Cwd optionally simulates a native context rebound after admission.
	Cwd string `json:"cwd,omitempty"`
	// Replace swaps this directory for a new object at the same path.
	Replace string `json:"replace,omitempty"`
	// Reload reloads the same frozen extension after replacement.
	Reload bool `json:"reload,omitempty"`
	// Source attempts to install a different session's generated extension.
	Source string `json:"source,omitempty"`
}

// directoryObservation reports externally visible permission decisions.
type directoryObservation struct {
	// Blocked records the extension's actual tool-call result.
	Blocked bool `json:"blocked"`
	// Confirmations counts human confirmation requests made for this call.
	Confirmations int `json:"confirmations"`
	// Reviews counts calls to the existing external classifier boundary.
	Reviews int `json:"reviews"`
	// Environment is the context supplied to that classifier.
	Environment string `json:"environment"`
	// Failure records extension initialization errors before any tool is invoked.
	Failure string `json:"failure,omitempty"`
}

func directoryFixture(t *testing.T, cwd, mode string) (*Client, context.Context) {
	t.Helper()
	options := fixtureOptions(t, cwd)
	options.PermissionMode = mode
	if mode == "auto" {
		options.MCPModulePath = filepath.Join(t.TempDir(), "bridge.mjs")
		// Only the external classifier is substituted. The generated extension
		// still decides when to invoke it, what context to supply, and fallback.
		const bridge = `export const createMcpAdapter = () => async () => {};
export const createReviewSubject = (name, input, cwd) => ({name, input, cwd});
export const completeSimple = () => { throw new Error("no model runs in this fixture"); };
export async function classifyAction(_ctx, config) {
  globalThis.directoryReviews.push(config.environment);
  return {outcome: "deny", risk_level: "high"};
}`
		if err := os.WriteFile(options.MCPModulePath, []byte(bridge), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	t.Cleanup(cancel)
	client, err := NewClient(ctx, options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	return client, ctx
}

func probeDirectories(t *testing.T, client *Client, id acp.SessionId, probes []directoryProbe) []directoryObservation {
	t.Helper()
	session, err := client.session(id)
	if err != nil {
		t.Fatal(err)
	}
	return probePreparedDirectories(t, client, session.cwd, filepath.Join(session.directory, "extension.ts"), probes)
}

func probePreparedDirectories(t *testing.T, client *Client, cwd, source string, probes []directoryProbe) []directoryObservation {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute the generated Pi extension")
	}
	input, err := json.Marshal(probes)
	if err != nil {
		t.Fatal(err)
	}
	const script = `import {readFileSync, renameSync, mkdirSync, writeFileSync} from "node:fs";
import {join} from "node:path";
import {pathToFileURL} from "node:url";
const hooks = new Map();
const api = {on(name, hook) {hooks.set(name, hook);}, registerTool() {}};
globalThis.directoryReviews = [];
const source = pathToFileURL(process.argv[1]).href;
let generation = 0;
await (await import(source)).default(api);
const result = [];
for (const item of JSON.parse(readFileSync(0, "utf8"))) {
  if (item.replace) {
    renameSync(item.replace, item.replace + ".previous");
    mkdirSync(item.replace);
    writeFileSync(join(item.replace, "input.txt"), "rebound");
  }
  if (item.reload || item.source) {
    hooks.clear();
    try {
      const next = item.source ? pathToFileURL(item.source).href : source;
      await (await import(next + "?generation=" + ++generation)).default(api);
    } catch (error) {
      result.push({failure: String(error)});
      continue;
    }
  }
  let confirmations = 0;
  const before = globalThis.directoryReviews.length;
  const ctx = {cwd: item.cwd || process.argv[2], hasUI: true,
    ui: {setStatus() {}, async confirm() {confirmations++; return false;}}};
  const decision = await hooks.get("tool_call")?.({toolName:item.tool, input:item.input, toolCallId:"fixture"}, ctx);
  result.push({blocked: decision?.block === true, confirmations,
    reviews:globalThis.directoryReviews.length-before, environment:globalThis.directoryReviews.at(-1) || ""});
}
process.stdout.write(JSON.stringify(result));`
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--experimental-strip-types", "--input-type=module", "-e", script, source, cwd)
	command.Stdin = strings.NewReader(string(input))
	config, err := bridgeConfiguration(nil, client.options.PermissionMode)
	if err != nil {
		t.Fatal(err)
	}
	if client.options.MCPModulePath == "" {
		config = ""
	}
	command.Env = append(os.Environ(), "NODE_NO_WARNINGS=1", bridgeEnv+"="+config)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated Pi extension failed: %s / %v", output, err)
	}
	var result []directoryObservation
	if err := json.Unmarshal(output, &result); err != nil || len(result) != len(probes) {
		t.Fatalf("invalid extension observations: %s / %v", output, err)
	}
	return result
}

func TestPiAdmissionIdentitySurvivesUntilFirstExtensionLoad(t *testing.T) {
	for _, target := range []string{"cwd", "additional"} {
		for _, replaced := range []bool{false, true} {
			name := target + "/unchanged"
			if replaced {
				name = target + "/replaced-before-first-import"
			}
			t.Run(name, func(t *testing.T) {
				parent := t.TempDir()
				cwd, extra := filepath.Join(parent, "cwd"), filepath.Join(parent, "extra")
				for _, root := range []string{cwd, extra} {
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("admitted"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				client, _ := directoryFixture(t, cwd, "default")
				input, err := openResources(cwd, nil, []string{extra}, nil)
				if err != nil {
					t.Fatal(err)
				}
				dir, _, _, err := client.prepareResources(cwd, input)
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(dir)
				root := cwd
				if target == "additional" {
					root = extra
				}
				if replaced {
					if err := os.Rename(root, root+".admitted"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("replacement"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				// There is no Node import before this point: the first consumer
				// must use Go's admitted identity, never bless its own new stat.
				got := probePreparedDirectories(t, client, cwd, filepath.Join(dir, "extension.ts"), []directoryProbe{{Tool: "read", Input: map[string]any{"path": filepath.Join(root, "input.txt")}}})[0]
				if got.Blocked != replaced || got.Confirmations != boolCount(replaced) {
					t.Fatalf("first consumer replaced=%v: %+v", replaced, got)
				}
			})
		}
	}
}

func TestPiUnverifiedNativeIdentityRetainsHumanApproval(t *testing.T) {
	cwd, root := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	client, _ := directoryFixture(t, cwd, "default")
	input, err := openResources(cwd, nil, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The Go identity remains valid, but an unavailable native mapping must
	// not become a new Node stat-based grant at the receiving boundary.
	input.roots[1].device, input.roots[1].inode = "", ""
	dir, _, _, err := client.prepareResources(cwd, input)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	got := probePreparedDirectories(t, client, cwd, filepath.Join(dir, "extension.ts"), []directoryProbe{{Tool: "read", Input: map[string]any{"path": filepath.Join(root, "input.txt")}}})[0]
	if !got.Blocked || got.Confirmations != 1 {
		t.Fatalf("unverified identity acquired a read grant: %+v", got)
	}
}

func TestPiAdditionalDirectoriesReachNativePermissionHook(t *testing.T) {
	cwd, root, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, directory := range []string{cwd, root, outside} {
		if err := os.WriteFile(filepath.Join(directory, "input.txt"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	// Pi strips a leading @ before resolving tool paths. A literal @ file
	// must not authorize the different symlink target selected by Pi.
	if err := os.WriteFile(filepath.Join(cwd, "@alias.txt"), []byte("literal"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "input.txt"), filepath.Join(cwd, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	client, ctx := directoryFixture(t, cwd, "default")
	request := acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{root}}
	session, err := client.NewSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.AdditionalDirectories[0] = outside
	probes := []directoryProbe{
		{Tool: "read", Input: map[string]any{"path": "input.txt"}},
		{Tool: "read", Input: map[string]any{"path": filepath.Join(root, "input.txt")}},
		{Tool: "grep", Input: map[string]any{"path": root, "pattern": "fixture"}},
		{Tool: "find", Input: map[string]any{"path": root, "pattern": "*.txt"}},
		{Tool: "ls", Input: map[string]any{"path": root}},
		{Tool: "read", Input: map[string]any{"path": filepath.Join(outside, "input.txt")}},
		{Tool: "read", Input: map[string]any{"path": filepath.Join(link, "input.txt")}},
		{Tool: "read", Input: map[string]any{"path": filepath.Join(root, "planned.txt")}},
		{Tool: "write", Input: map[string]any{"path": filepath.Join(root, "planned.txt"), "content": "new"}},
		{Tool: "edit", Input: map[string]any{"path": filepath.Join(root, "input.txt"), "oldText": "fixture", "newText": "changed"}},
		{Tool: "bash", Input: map[string]any{"command": "rm -rf example"}},
		{Tool: "read", Input: map[string]any{"path": "input.txt"}, Cwd: outside},
		{Tool: "read", Input: map[string]any{"path": "@alias.txt"}},
		{Tool: "write", Input: map[string]any{"path": filepath.Join(link, "planned.txt"), "content": "new"}},
	}
	for i, observed := range probeDirectories(t, client, session.SessionId, probes) {
		wantConfirmation := i >= 5
		if observed.Blocked != wantConfirmation || observed.Confirmations != boolCount(wantConfirmation) {
			t.Errorf("tool %d (%s) = %+v, want confirmation %v", i, probes[i].Tool, observed, wantConfirmation)
		}
	}
}

func TestPiDifferentFrozenScopesCannotShareOneExtensionCache(t *testing.T) {
	cwd, root := t.TempDir(), t.TempDir()
	otherCwd, otherRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(otherRoot, "input.txt"), []byte("other scope"), 0600); err != nil {
		t.Fatal(err)
	}
	first, ctx := directoryFixture(t, cwd, "default")
	opened, err := first.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	second, otherCtx := directoryFixture(t, otherCwd, "default")
	other, err := second.NewSession(otherCtx, acp.NewSessionRequest{Cwd: otherCwd, AdditionalDirectories: []string{otherRoot}})
	if err != nil {
		t.Fatal(err)
	}
	probe := directoryProbe{Tool: "read", Input: map[string]any{"path": filepath.Join(otherRoot, "input.txt")}}
	if got := probeDirectories(t, second, other.SessionId, []directoryProbe{probe})[0]; got.Failure != "" || got.Blocked {
		t.Fatalf("the independent scope was not usable in its own process: %+v", got)
	}
	otherSession, err := second.session(other.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	probe.Source = filepath.Join(otherSession.directory, "extension.ts")
	probe.Cwd = otherCwd
	result := probeDirectories(t, first, opened.SessionId, []directoryProbe{probe})[0]
	if !strings.Contains(result.Failure, "workspace roots changed in a live process") {
		t.Fatalf("different scope silently replaced or accumulated roots: %+v", result)
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestPiAdditionalDirectoryReplacementDoesNotRebindAcrossReload(t *testing.T) {
	cwd, root := t.TempDir(), filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	client, ctx := directoryFixture(t, cwd, "default")
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"path": filepath.Join(root, "input.txt")}
	observations := probeDirectories(t, client, session.SessionId, []directoryProbe{
		{Tool: "read", Input: input}, {Tool: "read", Input: input, Replace: root}, {Tool: "read", Input: input, Reload: true},
	})
	for i, result := range observations {
		if result.Blocked != (i != 0) || result.Confirmations != boolCount(i != 0) {
			t.Fatalf("root replacement step %d: %+v", i, result)
		}
	}
}

func TestPiAdditionalDirectoriesReachExistingClassifier(t *testing.T) {
	cwd, root := t.TempDir(), t.TempDir()
	client, ctx := directoryFixture(t, cwd, "auto")
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	result := probeDirectories(t, client, session.SessionId, []directoryProbe{
		{Tool: "write", Input: map[string]any{"path": filepath.Join(root, "planned.txt"), "content": "new"}},
	})[0]
	if !result.Blocked || result.Confirmations != 1 || result.Reviews != 1 || !strings.Contains(result.Environment, canonical) {
		t.Fatalf("auto policy bypassed its classifier or omitted the frozen scope: %+v", result)
	}
}

func TestPiAdditionalDirectoriesRemainDiscoverableInFullAccess(t *testing.T) {
	cwd, root := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "project-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	client, ctx := directoryFixture(t, cwd, "full-access")
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{alias, root, cwd}})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := client.session(session.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	if opened.directory == "" {
		t.Fatal("full-access discarded additional-directory discovery")
	}
	instructions, err := os.ReadFile(filepath.Join(opened.directory, "instructions.md"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(canonical)
	if strings.Count(string(instructions), string(encoded)) != 1 {
		t.Fatalf("additional directory was missing or duplicated in native instructions: %q", instructions)
	}
	if _, err := os.Stat(filepath.Join(opened.directory, "extension.ts")); !os.IsNotExist(err) {
		t.Fatalf("full-access unexpectedly acquired a permission extension: %v", err)
	}
}

func TestPiAdditionalDirectoryReplacementIsRejectedBeforePreparation(t *testing.T) {
	cwd, root := t.TempDir(), filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	client, _ := directoryFixture(t, cwd, "default")
	config, err := openResources(cwd, nil, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir, _, _, err := client.prepareResources(cwd, config)
	if dir != "" {
		defer os.RemoveAll(dir)
	}
	if err == nil {
		t.Fatal("resource preparation rebound an authorized root to a replacement directory")
	}
	if dir != "" {
		t.Fatal("root replacement was rejected only after preparing native resources")
	}
}

func TestPiInvalidDirectoriesAreRejectedBeforeNativeResources(t *testing.T) {
	cwd, invalidFile := t.TempDir(), filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(invalidFile, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	client, ctx := directoryFixture(t, cwd, "default")
	for _, additional := range []string{"relative-project", filepath.Join(t.TempDir(), "missing"), invalidFile} {
		if _, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{additional}}); err == nil {
			t.Errorf("invalid additional root was accepted: %q", additional)
		}
	}
	client.mu.Lock()
	processes := len(client.processes)
	client.mu.Unlock()
	if processes != 0 {
		t.Fatal("invalid roots started or retained a native process")
	}
	if _, err := os.Stat(filepath.Join(cwd, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("invalid roots created native session files: %v", err)
	}
}

func TestPiAdditionalDirectoriesApplyToResumeAndFork(t *testing.T) {
	cwd, root := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	client, ctx := directoryFixture(t, cwd, "default")
	request := acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{root}}
	opened, err := client.NewSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	check := func(id acp.SessionId) {
		t.Helper()
		result := probeDirectories(t, client, id, []directoryProbe{{Tool: "read", Input: map[string]any{"path": filepath.Join(root, "input.txt")}}})[0]
		if result.Blocked || result.Confirmations != 0 {
			t.Errorf("native session %s did not consume its additional root: %+v", id, result)
		}
	}
	check(opened.SessionId)
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: opened.SessionId}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: opened.SessionId, Cwd: cwd, AdditionalDirectories: []string{root}}); err != nil {
		t.Fatal(err)
	}
	check(opened.SessionId)
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: opened.SessionId}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: opened.SessionId, Cwd: cwd, AdditionalDirectories: []string{root}}); err != nil {
		t.Fatal(err)
	}
	check(opened.SessionId)
	fork, err := client.UnstableForkSession(ctx, acp.UnstableForkSessionRequest{SessionId: opened.SessionId, Cwd: cwd, AdditionalDirectories: []string{root}, Meta: map[string]any{"forkReceiptDirectory": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	check(fork.SessionId)
}
