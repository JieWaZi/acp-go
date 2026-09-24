// 真实 CLI 故障注入：只使用隔离目录、测试凭据和回环模型接口。
import { spawn, execFileSync } from 'node:child_process';
import { createServer } from 'node:http';
import { createInterface } from 'node:readline';
import { mkdtempSync, mkdirSync, writeFileSync, appendFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import assert from 'node:assert/strict';

const binary = resolve(process.argv[2] ?? '/tmp/acp-error-probe');
const root = mkdtempSync(join(tmpdir(), 'acp-real-errors-'));
const selected = (process.env.ACP_ERROR_CLIS ?? 'codex,claude,cursor,kimi,pi').split(',');
const scenarios = (process.env.ACP_ERROR_SCENARIOS ?? 'auth,rate,unavailable').split(',');
for (const cli of selected) assert.ok(['codex', 'claude', 'cursor', 'kimi', 'pi'].includes(cli));
const paths = Object.fromEntries(selected.map(cli => [cli, execFileSync('/bin/zsh', ['-lc', `command -v ${cli === 'cursor' ? 'agent' : cli}`], { encoding: 'utf8' }).trim()]));
const variants = {
  success: { status: 200 },
  auth: { status: 401, type: 'authentication_error', message: 'Invalid API key (local fault fixture)' },
  rate: { status: 429, type: 'rate_limit_error', message: 'Rate limit exceeded (local fault fixture)' },
  unavailable: { status: 503, type: 'overloaded_error', message: 'Service unavailable (local fault fixture)' },
};
console.log(`Artifacts: ${root}`);
const results = [];
for (const cli of selected) for (const scenario of scenarios) {
  const work = join(root, `${cli}-${scenario}`);
  const home = join(work, 'home'), config = join(work, 'config'), cwd = join(work, 'workspace');
  for (const dir of [home, config, cwd]) mkdirSync(dir, { recursive: true });
  const fault = variants[scenario];
  assert.ok(fault, "Unknown fault scenario");
  let servingSuccess = scenario === "success";
  const requests = [];
  const handler = async (req, res) => {
    // 不记录请求头和请求正文；只保存触发故障的路径、状态和顺序。
    for await (const _ of req) { /* 消费输入以完成真实 HTTP 往返。 */ }
    requests.push({ method: req.method, path: req.url, status: servingSuccess ? 200 : fault.status });
    writeFileSync(join(work, 'requests.json'), JSON.stringify(requests, null, 2));
    if (servingSuccess) {
      assert.ok(cli === 'kimi' || cli === 'pi', 'Success control currently supports the OpenAI chat fixture');
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      for (const [delta, finish_reason] of [[{ role: 'assistant', content: 'The word error is ordinary text.' }, null], [{}, 'stop']]) {
        res.write('data: ' + JSON.stringify({ id: 'local-control', object: 'chat.completion.chunk', created: 0, model: 'local-model', choices: [{ index: 0, delta, finish_reason }], ...(finish_reason ? { usage: { prompt_tokens: 10, completion_tokens: 8, total_tokens: 18 } } : {}) }) + '\n\n');
      }
      res.end('data: [DONE]\n\n'); return;
    }
    res.writeHead(fault.status, { 'content-type': 'application/json', 'retry-after': '0' });
    res.end(JSON.stringify({ type: 'error', error: { type: fault.type, code: fault.type, message: fault.message } }));
  };
  const server = createServer(handler);
  const sockets = new Set();
  server.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); });
  server.on('sessionError', () => {});
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const base = `http://127.0.0.1:${server.address().port}`;
  const env = {
    PATH: process.env.PATH, HOME: home, USERPROFILE: home, TMPDIR: config,
    XDG_CONFIG_HOME: config, XDG_CACHE_HOME: join(config, 'cache'),
    ACP_ERROR_CLI: cli, ACP_ERROR_CLI_PATH: paths[cli], ACP_ERROR_STATE: join(config, 'state'),
    CODEX_HOME: config, CLAUDE_CONFIG_DIR: config, CLAUDE_CODE_SIMPLE: '1',
    ANTHROPIC_API_KEY: 'local-fixture-only', ANTHROPIC_BASE_URL: base,
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1', CLAUDE_CODE_MAX_RETRIES: '0',
    CURSOR_CONFIG_DIR: config, CURSOR_API_KEY: 'local-fixture-only', CURSOR_API_ENDPOINT: base,
    KIMI_CODE_HOME: config, KIMI_SHARE_DIR: config, PI_CODING_AGENT_DIR: config,
    PI_SKIP_VERSION_CHECK: '1', NO_COLOR: '1', NO_OPEN_BROWSER: '1', DO_NOT_TRACK: '1',
    ACP_GO_TEST_HOME: home, NODE_OPTIONS: '--import=' + pathToFileURL(resolve('scripts/pi-integration/home-isolation.mjs')).href,
  };
  if (cli === 'codex') writeFileSync(join(config, 'config.toml'), `model="local-model"\nmodel_provider="fixture"\napproval_policy="never"\nsandbox_mode="read-only"\n[model_providers.fixture]\nname="Local error fixture"\nbase_url="${base}/v1"\nwire_api="responses"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\nstream_idle_timeout_ms=3000\n`);
  if (cli === 'pi') {
    writeFileSync(join(config, 'models.json'), JSON.stringify({ providers: { fixture: { baseUrl: base + '/v1', api: 'openai-completions', apiKey: 'local-fixture-only', models: [{ id: 'local-model', name: 'Local fault fixture' }] } } }));
    writeFileSync(join(config, 'settings.json'), JSON.stringify({ defaultProvider: 'fixture', defaultModel: 'local-model', quietStartup: true, retry: { enabled: false } }));
  }
  if (cli === 'kimi') writeFileSync(join(config, 'config.toml'), `default_model="local"\n[models.local]\nprovider="fixture"\nmodel="local-model"\nmax_context_size=100000\n[providers.fixture]\ntype="openai"\nbase_url="${base}/v1"\napi_key="local-fixture-only"\n[loop_control]\nmax_attempts_per_step=1\n`);
  const child = spawn(binary, [], { cwd, env, detached: true, stdio: ['pipe', 'pipe', 'pipe'] });
  let sequence = 0;
  const pending = new Map(), updates = [];
  let exited = false;
  child.on('exit', () => { exited = true; for (const p of pending.values()) { clearTimeout(p.timer); p.reject(Error('Adapter exited')); } pending.clear(); });
  child.stderr.on('data', data => appendFileSync(join(work, 'stderr.log'), data));
  child.stdin.on('error', () => {});
  const send = message => child.stdin.write(JSON.stringify(message) + '\n');
  createInterface({ input: child.stdout }).on('line', line => {
    appendFileSync(join(work, 'acp.jsonl'), line + '\n');
    let message; try { message = JSON.parse(line); } catch { return; }
    if (message.method) {
      if (message.method === 'session/update') updates.push(message.params.update);
      if (message.id !== undefined) send({ jsonrpc: '2.0', id: message.id, error: { code: -32601, message: 'No tool execution allowed in fault fixture' } });
      return;
    }
    const entry = pending.get(message.id);
    if (entry) { pending.delete(message.id); clearTimeout(entry.timer); entry.resolve(message); }
  });
  const request = (method, params) => new Promise((resolve, reject) => {
    const id = ++sequence;
    const timer = setTimeout(() => { pending.delete(id); reject(Error('Timeout: ' + method)); }, 45000);
    pending.set(id, { resolve, reject, timer }); send({ jsonrpc: '2.0', id, method, params });
  });
  let stage = 'initialize';
  const result = { cli, scenario, expectedStatus: fault.status };
  try {
    const init = await request('initialize', { protocolVersion: 1, clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: false } });
    result.version = init.result?.agentInfo?._meta?.runtime?.version ?? init.result?.agentInfo?.version;
    assert.ok(!init.error, JSON.stringify(init.error));
    stage = 'session/new';
    const session = await request(stage, { cwd, mcpServers: [] });
    let response = session;
    if (!session.error) {
      stage = 'session/prompt';
      response = await request(stage, { sessionId: session.result.sessionId, prompt: [{ type: 'text', text: 'Reply only OK. Do not use tools.' }] });
    }
    result.stage = stage; result.response = response;
    result.coverage = stage === 'session/prompt' ? 'model-request' : 'session-bootstrap';
    result.assistantText = updates.filter(x => x.sessionUpdate === 'agent_message_chunk').map(x => x.content?.text ?? '').join('');
    result.requestCount = requests.length;
    assert.ok(requests.some(x => x.status === fault.status), 'CLI did not reach the injected provider');
    if (scenario === 'success') {
      assert.ok(!response.error, 'Normal assistant content was classified as a failure');
      assert.equal(result.assistantText, 'The word error is ordinary text.');
    } else {
      assert.ok(response.error, 'Failure returned a successful ACP result');
      assert.equal(result.assistantText, '', 'Failure was emitted as normal assistant text');
    }
    if (process.env.ACP_ERROR_RECOVER === '1') {
      assert.ok(['kimi', 'pi'].includes(cli) && scenario !== 'success');
      assert.ok(session.result?.sessionId, 'Recovery needs an existing native session');
      servingSuccess = true;
      const before = updates.length;
      const recovery = await request('session/prompt', { sessionId: session.result.sessionId, prompt: [{ type: 'text', text: 'Reply only OK. Do not use tools.' }] });
      result.recovery = { response: recovery, assistantText: updates.slice(before).filter(x => x.sessionUpdate === 'agent_message_chunk').map(x => x.content?.text ?? '').join('') };
      assert.ok(!recovery.error, 'Old failure contaminated the next turn');
      assert.equal(result.recovery.assistantText, 'The word error is ordinary text.');
    }
    result.pass = true;
  } catch (error) { result.stage = stage; result.failure = String(error); result.requestCount = requests.length; result.pass = false; }
  finally {
    // 进程组属于本次测试；清理适配器及全部 CLI 子进程。
    child.stdin.end();
    try { child.kill('SIGTERM'); } catch {}
    if (!exited) await Promise.race([new Promise(r => child.once('exit', r)), new Promise(r => setTimeout(r, 5000))]);
    try { process.kill(-child.pid, 'SIGKILL'); } catch {}
    for (const socket of sockets) socket.destroy();
    await new Promise(r => server.close(r));
  }
  results.push(result);
  writeFileSync(join(root, 'results.json'), JSON.stringify(results, null, 2));
  console.log(`${cli} ${scenario}: ${result.pass ? 'PASS' : 'FAIL'} (${result.stage}, requests=${result.requestCount}) ${result.failure ?? ''}`);
}
console.log(`Results: ${join(root, 'results.json')}`);
if (results.some(x => !x.pass)) process.exitCode = 1;
