// 使用固定 npm 官方实现；只截获序列化请求，不访问网络。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';
import path from 'node:path';
const [proof, overlay] = process.argv.slice(2);
const { ModelConfigService } = await import(pathToFileURL(path.join(proof, 'core/package/dist/src/services/modelConfigService.js')));
const { DEFAULT_MODEL_CONFIGS } = await import(pathToFileURL(path.join(proof, 'core/package/dist/src/config/defaultModelConfigs.js')));
const require = createRequire(path.join(proof, 'package.json'));
const { GoogleGenAI } = require('@google/genai');
const generated = JSON.parse(fs.readFileSync(overlay, 'utf8')).modelConfigs;
const config = structuredClone(DEFAULT_MODEL_CONFIGS);
config.customAliases = generated.customAliases;
config.customOverrides = [
 ...Object.values(generated.customAliases).map(alias => ({ match: { model: alias.modelConfig.model }, modelConfig: { generateContentConfig: { temperature: 0.42, thinkingConfig: { includeThoughts: true, thinkingBudget: 4096, thinkingLevel: 'HIGH' } } } })),
 ...generated.customOverrides,
];
const service = new ModelConfigService(config);
let body;
globalThis.fetch = async (_url, options) => {
 body = JSON.parse(options.body);
 return new Response(JSON.stringify({ candidates: [{ content: { role: 'model', parts: [{ text: 'fixture' }] }, finishReason: 'STOP' }] }), { status: 200, headers: { 'content-type': 'application/json' } });
};
const ai = new GoogleGenAI({ apiKey: 'test-only', httpOptions: { baseUrl: 'http://127.0.0.1' } });
let count = 0;
for (const [alias, entry] of Object.entries(generated.customAliases)) {
 const selected = service.getResolvedConfig({ model: alias, isChatModel: true });
 const matching = generated.customOverrides.filter(o => o.match.model === alias);
 const expected = matching.at(-1).modelConfig.generateContentConfig.thinkingConfig;
 assert.equal(selected.model, entry.modelConfig.model);
 assert.equal(selected.generateContentConfig.temperature, 0.42);
 assert.deepEqual(selected.generateContentConfig.thinkingConfig, expected);
 await ai.models.generateContent({ model: selected.model, contents: 'fixture', config: selected.generateContentConfig });
 assert.deepEqual(body.generationConfig.thinkingConfig, expected);
 if ('thinkingLevel' in expected) assert.equal('thinkingBudget' in body.generationConfig.thinkingConfig, false);
 if ('thinkingBudget' in expected) assert.equal('thinkingLevel' in body.generationConfig.thinkingConfig, false);
 const base = service.getResolvedConfig({ model: entry.modelConfig.model, isChatModel: true });
 assert.equal(base.generateContentConfig.thinkingConfig.thinkingBudget, 4096);
 count++;
}
console.log(`verified ${count} presets against official resolver and SDK; actual canonical models preserved; fetch intercepted (no HTTP)`);
