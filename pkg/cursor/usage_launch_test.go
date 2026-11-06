package cursor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCursorUsageEnvironmentEmbedsImportWithoutMutatingCaller(t *testing.T) {
	original := []string{"PATH=/native/path", "NODE_OPTIONS=--max-old-space-size=768", "KEEP=value", "ACP_GO_CURSOR_USAGE_EVENT_DIR=/old"}
	before := append([]string(nil), original...)
	executable := filepath.Join(t.TempDir(), "2026.11.05-86047df", "cursor-agent")
	directory := filepath.Join(t.TempDir(), "private events")
	got := cursorUsageEnvironment(executable, directory, original)
	if !reflect.DeepEqual(original, before) {
		t.Fatal("launch builder mutated caller environment")
	}
	if envValue(got, "KEEP") != "value" || envValue(got, "ACP_GO_CURSOR_USAGE_ARTIFACT_DIR") != filepath.Dir(executable) || envValue(got, "ACP_GO_CURSOR_USAGE_EVENT_DIR") != directory {
		t.Fatalf("launch environment lost selected inputs: %#v", got)
	}
	options := envValue(got, "NODE_OPTIONS")
	prefix := "--max-old-space-size=768 --import=data:text/javascript;base64,"
	if !strings.HasPrefix(options, prefix) {
		t.Fatalf("existing Node options were replaced: %q", options[:min(len(options), 100)])
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(options, prefix))
	if err != nil || !strings.Contains(string(decoded), "createRequire") || !strings.Contains(string(decoded), "Module.prototype._compile") {
		t.Fatalf("invalid embedded usage preload: %v", err)
	}
	if len(options) >= 32767 {
		t.Fatalf("embedded preload exceeds the per-variable Windows environment limit: %d", len(options))
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("constructor performed directory I/O: %v", err)
	}
	got[0] = "PATH=changed"
	if original[0] != before[0] {
		t.Fatal("returned launch environment shares caller backing array")
	}
}

func TestCursorUsageEnvironmentReplacesDuplicatePrivateKeys(t *testing.T) {
	got := cursorUsageEnvironment("/selected/version/cursor-agent", "/selected/events", []string{
		"NODE_OPTIONS=--trace-warnings", "NODE_OPTIONS=--no-warnings",
		"ACP_GO_CURSOR_USAGE_EVENT_DIR=/one", "ACP_GO_CURSOR_USAGE_EVENT_DIR=/two",
	})
	for _, key := range []string{"NODE_OPTIONS", "ACP_GO_CURSOR_USAGE_EVENT_DIR", "ACP_GO_CURSOR_USAGE_ARTIFACT_DIR"} {
		count := 0
		for _, value := range got {
			if strings.HasPrefix(value, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("launch environment retained duplicate %s", key)
		}
	}
	if !strings.HasPrefix(envValue(got, "NODE_OPTIONS"), "--no-warnings --import=") {
		t.Fatal("launch builder did not preserve the selected last Node options")
	}
}

func cursorUsageTestNode(t *testing.T) string {
	t.Helper()
	if home, err := os.UserHomeDir(); err == nil {
		for _, version := range []string{"2026.11.05-86047df", "2026.10.22-27dcf5f"} {
			node := filepath.Join(home, ".local", "share", "cursor-agent", "versions", version, "node")
			if info, err := os.Stat(node); err == nil && !info.IsDir() {
				return node
			}
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node runtime is unavailable")
	}
	return node
}

func TestCursorUsageEmbeddedImportPreservesNativeProgramAndExistingHealth(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "2026.11.05-86047df")
	if err := os.Mkdir(artifact, 0700); err != nil {
		t.Fatal(err)
	}
	health := filepath.Join(directory, "health.json")
	original := []byte("{\"status\":\"ready\",\"fixture\":\"parent\"}\n")
	if err := os.WriteFile(health, original, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cursorUsageTestNode(t), "-e", "process.stdout.write('native-unchanged')")
	command.Env = cursorUsageEnvironment(filepath.Join(artifact, "cursor-agent"), directory, os.Environ())
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "native-unchanged" {
		t.Fatalf("inline import changed native startup: %s / %v", string(output[:min(len(output), 1500)]), err)
	}
	current, err := os.ReadFile(health)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatalf("helper that did not load ACP overwrote parent health: %s / %v", current, err)
	}
}

func TestCursorUsageEmbeddedImportFailureKeepsNativeProgramRunning(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	artifact := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cursorUsageTestNode(t), "-e", "process.stdout.write('native-unchanged')")
	command.Env = cursorUsageEnvironment(filepath.Join(artifact, "cursor-agent"), directory, os.Environ())
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "native-unchanged" {
		t.Fatalf("unsupported preload stopped original program: %s / %v", string(output[:min(len(output), 1500)]), err)
	}
	var health map[string]any
	raw, err := os.ReadFile(filepath.Join(directory, "health.json"))
	if err != nil || json.Unmarshal(raw, &health) != nil || health["status"] != "rejected" {
		t.Fatalf("unknown artifact did not remain unavailable: %s / %v", raw, err)
	}
}

func TestCursorUsagePreloadFixturePreservesStreamAndBoundsUsage(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "preload.cjs")
	if err := os.WriteFile(source, []byte(cursorUsagePreload), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cursorUsageTestNode(t), "-e", cursorUsagePreloadFixtureTest, source, directory)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("usage-only preload fixture failed: %s / %v", string(output[:min(len(output), 2500)]), err)
	}
	if strings.TrimSpace(string(output)) != "fixture-ok" {
		t.Fatalf("unexpected preload fixture output: %s", output)
	}
}

const cursorUsagePreloadFixtureTest = `const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const vm = require('node:vm');
const source = fs.readFileSync(process.argv[1], 'utf8');
const directory = process.argv[2];
const context = vm.createContext({require, process: {env: {}, cwd: () => process.cwd()}, Buffer});
vm.runInContext(source, context);
context.eventDirectory = directory;
vm.runInContext("ready=true;eventDir=eventDirectory;version='2026.11.05-86047df';Object.defineProperty(globalThis,HOOK,{value:Object.freeze({prompt})});", context);
const fixture = [
 'globalThis.Fixture=class {async processPrompt(e,t,s){',
 'const R={sendUpdate:async(e,t)=>{this.claimTaskToolCall(t,s),this.noteWatchedShell(t),await this.presenter.presentInteractionUpdate(t)}};',
 'const m={requestedModel:{modelId:"composer-2.5"}},c={},P=[],w={};const f=globalThis.nativeAction;let q;',
 'q=e=>this.sharedServices.agentClient.run(t,this.agentStore.getConversationStateStructure(),e,m.modelDetails,R,this.resources,this.agentStore.getBlobStore(),c,this.agentStore,P,w);',
 'return await q(f)}};'
].join('');
context.fixture = fixture;
context.fixtureHash = crypto.createHash('sha256').update(fixture).digest('hex');
assert.equal(vm.runInContext("patchACPSource(fixture,'wrong').reason", context), 'artifact_digest_mismatch');
context.duplicate = fixture + fixture;
context.duplicateHash = crypto.createHash('sha256').update(context.duplicate).digest('hex');
assert.equal(vm.runInContext('patchACPSource(duplicate,duplicateHash).reason', context), 'instrumentation_anchor_mismatch');
const patched = vm.runInContext('patchACPSource(fixture,fixtureHash).code', context);
const sessionID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
const nonce = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
const userID = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc';
const requestID = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd';
const action = {action: {case: 'userMessageAction', value: {userMessage: {messageId: userID}}}};
context.nativeAction = action;
const ended = (input, output) => ({message: {case: 'turnEnded', value: {inputTokens: input, outputTokens: output, cacheReadTokens: 40n, cacheWriteTokens: 0n}}});
const records = () => fs.readdirSync(directory).filter(name => name.endsWith('.json') && name !== 'health.json');
const clear = () => records().forEach(name => fs.unlinkSync(path.join(directory, name)));
function instance() {
 const output = [];
 const target = new context.Fixture();
 Object.assign(target, {
  sessionId: sessionID, resources: {}, claimTaskToolCall() {}, noteWatchedShell() {},
  agentStore: {getConversationStateStructure() {return {};}, getBlobStore() {return {};}, async getLastRequestIdFromConversation() {return requestID;}},
  presenter: {async presentInteractionUpdate(update) {if (update.message.case === 'textDelta') output.push(update.message.value);}},
  sharedServices: {agentClient: {async run(...args) {
   assert.equal(args.length, 11); assert.equal(args[2], action);
   await args[4].sendUpdate({}, {message: {case: 'textDelta', value: 'first'}});
   await args[4].sendUpdate({}, {message: {case: 'textDelta', value: 'second'}});
   await args[4].sendUpdate({}, ended(50n, 9n));
   await args[4].sendUpdate({}, ended(51n, 10n));
   return 'native-result';
  }}}
 });
 return {target, output};
}
(async () => {
 vm.runInContext(fixture, context);
 const original = instance();
 assert.equal(await original.target.processPrompt({}, {}, {}), 'native-result');
 assert.deepEqual(original.output, ['first', 'second']);
 vm.runInContext(patched, context);
 const modified = instance();
 assert.equal(await modified.target.processPrompt({_meta: {'acp-go/cursor-usage-nonce': nonce}}, {}, {}), 'native-result');
 assert.deepEqual(modified.output, original.output);
 assert.equal(records().length, 1);
 const text = fs.readFileSync(path.join(directory, records()[0]), 'utf8');
 const record = JSON.parse(text);
 assert.equal(record.session_id, sessionID); assert.equal(record.user_message_id, userID);
 assert.equal(record.request_id, requestID); assert.equal(record.nonce, nonce);
 assert.equal(record.model, 'composer-2.5');
 assert.equal(record.input_tokens, 51); assert.equal(record.output_tokens, 10);
 assert.equal(record.cache_read_tokens, 40); assert.equal(record.cache_write_tokens, 0);
 assert.equal(text.includes('first'), false); assert.equal(text.includes('second'), false);
 clear();
 const noNonce = instance();
 assert.equal(await noNonce.target.processPrompt({}, {}, {}), 'native-result');
 assert.equal(records().length, 0);
 const unreadable = instance();
 unreadable.target.agentStore.getLastRequestIdFromConversation = async () => {throw new Error('private fixture detail');};
 assert.equal(await unreadable.target.processPrompt({_meta: {'acp-go/cursor-usage-nonce': nonce}}, {}, {}), 'native-result');
 assert.equal(records().length, 0);
 const failing = instance();
 failing.target.sharedServices.agentClient.run = async () => {throw new Error('original-native-error');};
 await assert.rejects(failing.target.processPrompt({_meta: {'acp-go/cursor-usage-nonce': nonce}}, {}, {}), /original-native-error/);
 assert.equal(records().length, 0);
 const hook = vm.runInContext('globalThis[HOOK]', context);
 const store = {async getLastRequestIdFromConversation() {return requestID;}};
 const overlapping = hook.prompt(sessionID, nonce);
 const first = overlapping.start(action), second = overlapping.start(action);
 overlapping.observe(ended(51n, 10n));
 await overlapping.finish(first, store, {}); await overlapping.finish(second, store, {});
 assert.equal(records().length, 0);
 const regression = hook.prompt(sessionID, nonce); const regRun = regression.start(action);
 regression.observe(ended(100n, 10n)); regression.observe(ended(80n, 11n));
 await regression.finish(regRun, store, {}); assert.equal(records().length, 0);
 const followup = hook.prompt(sessionID, nonce); const followRun = followup.start({action: {case: 'resumeAction', value: {}}});
 followup.observe(ended(51n, 10n)); await followup.finish(followRun, store, {});
 assert.equal(records().length, 0);
 const unsafe = hook.prompt(sessionID, nonce); const unsafeRun = unsafe.start(action);
 unsafe.observe({message: {case: 'turnEnded', value: {inputTokens: 9007199254740992n, outputTokens: -1n}}});
 await unsafe.finish(unsafeRun, store, {}); assert.equal(records().length, 0);
 const bounded = hook.prompt(sessionID, nonce); let emitted = 0;
 for (let index = 0; index < 130; index++) {
  const run = bounded.start(action); bounded.observe(ended(51n, 10n));
  await bounded.finish(run, store, {}); emitted += records().length; clear();
 }
 assert.equal(emitted, 128);
 for (let index = 0; index < 129; index++) {
  const scope = hook.prompt(sessionID, nonce); const run = scope.start(action);
  scope.observe(ended(51n, 10n)); await scope.finish(run, store, {});
 }
 assert.equal(records().length, 128);
 process.stdout.write('fixture-ok');
})().catch(error => {process.stderr.write(error.stack); process.exitCode = 1;});
`

func TestCursorUsagePreloadKnownArtifactsDryCompile(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("home directory is unavailable")
	}
	found := false
	for _, version := range []string{"2026.10.22-27dcf5f", "2026.11.05-86047df"} {
		artifact := filepath.Join(home, ".local", "share", "cursor-agent", "versions", version)
		chunk := "7096.index.js"
		if version == "2026.11.05-86047df" {
			chunk = "9673.index.js"
		}
		if _, err := os.Stat(filepath.Join(artifact, chunk)); err != nil {
			continue
		}
		found = true
		for _, tampered := range []bool{false, true} {
			name := version + "/exact"
			if tampered {
				name = version + "/tampered-memory-source"
			}
			t.Run(name, func(t *testing.T) {
				directory := t.TempDir()
				if err := os.Chmod(directory, 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				// Compile only webpack factory definitions without invoking release factories, the CLI entry point, or a model.
				script := "const fs=require('node:fs'),Module=require('node:module');const file=process.argv[1];const probe=new Module(file);probe.filename=file;probe.paths=[];let source=fs.readFileSync(file,'utf8');if(process.argv[2]==='tampered')source+='\\n';probe._compile(source,file);process.stdout.write('compiled');"
				mode := "exact"
				if tampered {
					mode = "tampered"
				}
				command := exec.CommandContext(ctx, cursorUsageTestNode(t), "-e", script, filepath.Join(artifact, chunk), mode)
				command.Env = cursorUsageEnvironment(filepath.Join(artifact, "cursor-agent"), directory, os.Environ())
				output, err := command.CombinedOutput()
				if err != nil || string(output) != "compiled" {
					t.Fatalf("artifact dry compile changed original module: %s / %v", string(output[:min(len(output), 1500)]), err)
				}
				var health map[string]any
				raw, err := os.ReadFile(filepath.Join(directory, "health.json"))
				if err != nil || json.Unmarshal(raw, &health) != nil {
					t.Fatalf("missing compile health: %s / %v", raw, err)
				}
				wantStatus, wantReason := "ready", "exact_artifact_instrumented"
				if tampered {
					wantStatus, wantReason = "rejected", "artifact_digest_mismatch"
				}
				if health["status"] != wantStatus || health["reason"] != wantReason || health["version"] != version {
					t.Fatalf("unexpected artifact health: %s", raw)
				}
			})
		}
	}
	if !found {
		t.Skip("verified Cursor artifact versions are not installed")
	}
}

func TestCursorUsagePreloadPreservesCompileArguments(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "2026.11.05-86047df")
	if err := os.Mkdir(artifact, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "preload.cjs")
	if err := os.WriteFile(source, []byte(cursorUsagePreload), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cursorUsageTestNode(t), "-e", cursorUsageCompileArgumentsTest, source, directory, artifact)
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "arguments-ok" {
		t.Fatalf("compile wrapper changed native call contract: %s / %v", string(output[:min(len(output), 2500)]), err)
	}
}

const cursorUsageCompileArgumentsTest = `const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto');
const source = fs.readFileSync(process.argv[1], 'utf8');
const directory = process.argv[2];
const artifact = fs.realpathSync(process.argv[3]);
const filename = path.join(artifact, '9673.index.js');
const fixture = 'async processPrompt(e,t,s){ sendUpdate:async(e,t)=>{this.claimTaskToolCall q=e=>this.sharedServices.agentClient.run(t,this.agentStore.getConversationStateStructure(),e,m.modelDetails,R,this.resources,this.agentStore.getBlobStore(),c,this.agentStore,P,w)';
function check(mode) {
 const format = {sentinel: 'format'}, future = {sentinel: 'future'};
 const receiver = {sentinel: 'module-this'}, returned = {sentinel: 'return'};
 const nativeError = new Error('native-original-error');
 const target = mode.startsWith('unmatched') ? path.join(artifact, 'unmatched.js') : filename;
 let calls = 0;
 const ModuleFixture = {prototype: {_compile(...args) {
  calls++;
  assert.equal(this, receiver);
  assert.equal(args.length, 4);
  assert.equal(args[1], target);
  assert.equal(args[2], format);
  assert.equal(args[3], future);
  if (mode === 'patched-fallback' || mode === 'patched-fallback-throw') {
   if (calls === 1) {assert.notEqual(args[0], fixture); throw new Error('instrumented-compile-error');}
   assert.equal(args[0], fixture);
  } else if (mode === 'patched') {
   assert.notEqual(args[0], fixture);
  } else {
   assert.equal(args[0], fixture);
  }
  if (mode.endsWith('throw')) throw nativeError;
  return returned;
 }}};
 const fakeCrypto = Object.assign({}, crypto, {createHash() {return {update() {return this;}, digest() {return mode === 'digest-rejected' ? 'not-the-manifest' : 'b7d4d9747f761eff7fc102d26de69152bdd34c70bc78b0cbad442cecee9463ed';}};}});
 const context = vm.createContext({
  require(name) {if (name === 'node:module') return ModuleFixture;if (name === 'node:crypto') return fakeCrypto;return require(name);},
  process: {env: {ACP_GO_CURSOR_USAGE_EVENT_DIR: directory, ACP_GO_CURSOR_USAGE_ARTIFACT_DIR: artifact}},
 });
 vm.runInContext(source, context);
 if (mode.endsWith('throw')) {
  assert.throws(() => Reflect.apply(ModuleFixture.prototype._compile, receiver, [fixture, target, format, future]), error => error === nativeError);
 } else {
  assert.equal(Reflect.apply(ModuleFixture.prototype._compile, receiver, [fixture, target, format, future]), returned);
 }
 assert.equal(calls, mode.startsWith('patched-fallback') ? 2 : 1);
}
for (const mode of ['unmatched', 'unmatched-throw', 'patched', 'digest-rejected', 'patched-fallback', 'patched-fallback-throw']) check(mode);
process.stdout.write('arguments-ok');
`
