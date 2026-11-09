'use strict';

// This preload observes structurally compatible ACP artifacts without retaining prompts, replies, credentials, or terminal execution results.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const Module = require('node:module');

const HOOK = Symbol.for('acp-go/cursor-usage-hook/v1');
const NONCE_KEY = 'acp-go/cursor-usage-nonce';
const EVENT_DIR_KEY = 'ACP_GO_CURSOR_USAGE_EVENT_DIR';
const ARTIFACT_DIR_KEY = 'ACP_GO_CURSOR_USAGE_ARTIFACT_DIR';
const MAX_RUNS = 128;
const MAX_RECORDS = 128;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const MANIFESTS = Object.freeze({
  '2026.10.22-27dcf5f': Object.freeze({
    chunk: '7096.index.js',
    sha256: 'd17166706e1fe42db212c511a2e128cdf3ca8c6fa006bdc5bc7ce343410fbe8e',
  }),
  '2026.11.05-86047df': Object.freeze({
    chunk: '9673.index.js',
    sha256: 'b7d4d9747f761eff7fc102d26de69152bdd34c70bc78b0cbad442cecee9463ed',
  }),
});
const ENTRY = 'async processPrompt(e,t,s){';
const UPDATE = 'sendUpdate:async(e,t)=>{this.claimTaskToolCall';
const RUN = 'q=e=>this.sharedServices.agentClient.run(t,this.agentStore.getConversationStateStructure(),e,m.modelDetails,R,this.resources,this.agentStore.getBlobStore(),c,this.agentStore,P,w)';

let eventDir = '';
let artifactDir = '';
let version = '';
let manifest;
let ready = false;

function atomicJSON(name, record) {
  if (!eventDir) return;
  const destination = path.join(eventDir, name);
  const staged = destination + '.' + crypto.randomUUID() + '.tmp';
  try {
    fs.writeFileSync(staged, JSON.stringify(record) + '\n', { mode: 0o600, flag: 'wx' });
    fs.renameSync(staged, destination);
    return true;
  } catch (_) {
    try { fs.unlinkSync(staged); } catch (_) {}
    return false;
  }
}

function health(status, reason) {
  atomicJSON('health.json', {
    schema: 1,
    status,
    reason,
    version,
    chunk: manifest ? manifest.chunk : '',
  });
}

function validUUID(value) {
  return typeof value === 'string' && UUID.test(value);
}

function nativeCounter(value) {
  if (typeof value === 'bigint') {
    if (value < 0n || value > BigInt(Number.MAX_SAFE_INTEGER)) return undefined;
    return Number(value);
  }
  if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0) return value;
  return undefined;
}

const noop = Object.freeze({
  start() { return null; },
  observe() {},
  async finish() {},
});

function prompt(sessionID, nonce) {
  if (!ready || !validUUID(sessionID) || !validUUID(nonce)) return noop;
  let runCount = 0;
  const active = new Set();
  return {
    start(action, model) {
      try {
        if (++runCount > MAX_RUNS) {
          for (const run of active) run.tainted = true;
          return null;
        }
        const value = action && action.action;
        const user = value && value.case === 'userMessageAction' &&
          value.value && value.value.userMessage;
        const userMessageID = user && user.messageId;
        const run = {
          id: crypto.randomUUID(),
          index: runCount,
          userMessageID: validUUID(userMessageID) ? userMessageID : '',
          model: typeof model === 'string' && model.length <= 200 && model.trim() === model ? model : '',
          counters: null,
          tainted: active.size !== 0,
        };
        if (active.size !== 0) {
          for (const previous of active) previous.tainted = true;
        }
        active.add(run);
        return run;
      } catch (_) {
        for (const run of active) run.tainted = true;
        return null;
      }
    },
    observe(update) {
      try {
        if (active.size !== 1) {
          for (const run of active) run.tainted = true;
          return;
        }
        const message = update && update.message;
        if (!message || message.case !== 'turnEnded' || !message.value) return;
        const run = active.values().next().value;
        if (!run || run.tainted || !run.userMessageID) return;
        const counters = {};
        for (const [nativeName, outputName] of [
          ['inputTokens', 'input_tokens'],
          ['outputTokens', 'output_tokens'],
          ['cacheReadTokens', 'cache_read_tokens'],
          ['cacheWriteTokens', 'cache_write_tokens'],
        ]) {
          const value = nativeCounter(message.value[nativeName]);
          if (value !== undefined) counters[outputName] = value;
        }
        // Repeated reports replace one native run snapshot. Regressing counters invalidate attribution.
        if (Object.keys(counters).length !== 0) {
          if (run.counters && Object.keys(counters).some(key =>
              Object.hasOwn(run.counters, key) && counters[key] < run.counters[key])) {
            run.tainted = true;
            run.counters = null;
            return;
          }
          run.counters = counters;
        }
      } catch (_) {
        for (const run of active) run.tainted = true;
      }
    },
    async finish(run, store, context) {
      try {
        if (!run || !active.has(run) || run.tainted || !run.userMessageID ||
            !run.counters || active.size !== 1) return;
        if (!store || typeof store.getLastRequestIdFromConversation !== 'function') return;
        const requestID = await store.getLastRequestIdFromConversation(context);
        if (!validUUID(requestID) || run.tainted || active.size !== 1 || !active.has(run)) return;
        // Go removes consumed events; retained records bound disk usage without limiting total chat turns.
        if (fs.readdirSync(eventDir).filter(name =>
            name !== 'health.json' && name.endsWith('.json')).length >= MAX_RECORDS) return;
        atomicJSON(nonce + '-' + run.id + '.json', {
          schema: 1,
          source: 'cursor.acp.turn_ended',
          version,
          nonce,
          session_id: sessionID,
          request_id: requestID,
          user_message_id: run.userMessageID,
          run_id: run.id,
          run_index: run.index,
          model: run.model,
          ...run.counters,
        });
      } catch (_) {
        // Native reads, absent usage, and private-file failures must not change the original ACP result.
      } finally {
        if (run) active.delete(run);
      }
    },
  };
}

// Check compiled content against the selected on-disk artifact and require unique instrumentation anchors.
function patchACPSource(content, expectedDigest) {
  if (typeof content !== 'string' ||
      crypto.createHash('sha256').update(content).digest('hex') !== expectedDigest) {
    return { code: null, reason: 'artifact_digest_mismatch' };
  }
  const count = needle => content.split(needle).length - 1;
  if (count(ENTRY) !== 1 || count(UPDATE) !== 1 || count(RUN) !== 1) {
    return { code: null, reason: 'instrumentation_anchor_mismatch' };
  }
  const patched = content
        .replace(ENTRY, ENTRY + 'const __acpGoUsage=globalThis[Symbol.for("acp-go/cursor-usage-hook/v1")].prompt(this.sessionId,e?._meta?.["acp-go/cursor-usage-nonce"]);')
        .replace(UPDATE, 'sendUpdate:async(e,t)=>{if(typeof __acpGoUsage!=="undefined")__acpGoUsage.observe(t);this.claimTaskToolCall')
        .replace(RUN, 'q=async e=>{if(typeof __acpGoUsage==="undefined")return this.sharedServices.agentClient.run(t,this.agentStore.getConversationStateStructure(),e,m.modelDetails,R,this.resources,this.agentStore.getBlobStore(),c,this.agentStore,P,w);const __acpGoUsageRun=__acpGoUsage.start(e,m.requestedModel?.modelId??m.modelDetails?.modelId);try{return await this.sharedServices.agentClient.run(t,this.agentStore.getConversationStateStructure(),e,m.modelDetails,R,this.resources,this.agentStore.getBlobStore(),c,this.agentStore,P,w)}finally{await __acpGoUsage.finish(__acpGoUsageRun,this.agentStore,t)}}');
  return { code: patched, reason: '' };
}


try {
  const selectedEvents = process.env[EVENT_DIR_KEY];
  const selectedArtifact = process.env[ARTIFACT_DIR_KEY];
  if (!selectedEvents || !selectedArtifact || !path.isAbsolute(selectedEvents) ||
      !path.isAbsolute(selectedArtifact)) {
    throw new Error('invalid_configuration');
  }
  const info = fs.lstatSync(selectedEvents);
  if (!info.isDirectory() || info.isSymbolicLink() || (info.mode & 0o077) !== 0) {
    throw new Error('event_directory_not_private');
  }
  eventDir = fs.realpathSync(selectedEvents);
  artifactDir = fs.realpathSync(selectedArtifact);
  version = path.basename(artifactDir);
  manifest = MANIFESTS[version];
  if (!manifest) {
    // Discover one compatible native chunk; release names do not disable collection.
    const matches = [];
    for (const entry of fs.readdirSync(artifactDir, { withFileTypes: true }).slice(0, 4096)) {
      if (!entry.isFile() || !/^\d+\.index\.js$/.test(entry.name)) continue;
      const filename = path.join(artifactDir, entry.name);
      if (fs.statSync(filename).size > 32 * 1024 * 1024) continue;
      const content = fs.readFileSync(filename, 'utf8');
      if (!content.includes(ENTRY) || !content.includes(UPDATE) || !content.includes(RUN)) continue;
      const digest = crypto.createHash('sha256').update(content).digest('hex');
      if (patchACPSource(content, digest).code !== null) matches.push({ chunk: entry.name, sha256: digest });
    }
    if (matches.length !== 1) throw new Error('instrumentation_anchor_mismatch');
    manifest = matches[0];
  }

  Object.defineProperty(globalThis, HOOK, {
    value: Object.freeze({ prompt }),
    configurable: false,
    writable: false,
  });
  const originalCompile = Module.prototype._compile;
  const expectedFile = path.join(artifactDir, manifest.chunk);
  let inspected = false;
  Module.prototype._compile = function cursorUsageCompile(...args) {
    const [content, filename] = args;
    let compiledFile = path.resolve(filename);
    try { compiledFile = fs.realpathSync(compiledFile); } catch (_) {}
    if (compiledFile !== expectedFile || inspected) {
      return Reflect.apply(originalCompile, this, args);
    }
    inspected = true;
    let candidate;
    try {
      candidate = patchACPSource(content, manifest.sha256);
    } catch (_) {
      health('rejected', 'instrumentation_failed');
      return Reflect.apply(originalCompile, this, args);
    }
    if (candidate.code === null) {
      health('rejected', candidate.reason);
      return Reflect.apply(originalCompile, this, args);
    }
    let result;
    try {
      const patchedArgs = [...args];
      patchedArgs[0] = candidate.code;
      result = Reflect.apply(originalCompile, this, patchedArgs);
    } catch (_) {
      ready = false;
      health('rejected', 'instrumented_compile_failed');
      return Reflect.apply(originalCompile, this, args);
    }
    ready = true;
    health('ready', 'exact_artifact_instrumented');
    return result;
  };
} catch (error) {
  health('rejected', error && [
    'invalid_configuration',
    'event_directory_not_private',
    'instrumentation_anchor_mismatch',
  ].includes(error.message) ? error.message : 'preload_initialization_failed');
}
