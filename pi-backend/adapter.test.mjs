import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { completeSimple, getModel } from '@earendil-works/pi-ai/compat';
import { applyProxy, convertRequest, convertResponse, proxyDispatcher, resolveModel, run, socksProxy } from './adapter.mjs';

const model = { id: 'test', provider: 'test', api: 'openai-completions' };
const request = {
  model: 'test/test', structured: false, max_output_tokens: 100, session_id: 'conversation',
  messages: [{ role: 'system', content: 'instructions' }, { role: 'user', content: 'hello' }],
  tools: [{ type: 'function', function: { name: 'lookup', description: 'look up', parameters: { type: 'object', properties: {} } } }],
};
const reply = { stopReason: 'stop', content: [{ type: 'text', text: 'answer' }] };
const iceman = {
  id: 'iceman-contributor-free', name: 'Iceman Contributor Free', reasoning: true,
  modalities: { input: ['text'], output: ['text'] },
  limit: { context: 262144, output: 131072 },
  provider: { api: 'https://opencode.ai/inference/go/openai/v1' },
  cost: { input: 0, output: 0, cache_read: 0 },
};
const catalog = { 'opencode-go': { api: 'https://opencode.ai/inference/go/openai/v1',
  models: { [iceman.id]: iceman } } };
const catalogFetch = async () => new Response(JSON.stringify(catalog));

test('converts tool calls and results without executing tools', () => {
  const transcript = convertRequest({ ...request, messages: [
    ...request.messages,
    { role: 'assistant', content: 'checking', tool_calls: [{ id: 'call-1', type: 'function', function: { name: 'lookup', arguments: '{"key":1}' } }] },
    { role: 'tool', content: 'result', tool_call_id: 'call-1' },
  ] }, model);
  assert.deepEqual(transcript.messages.map(m => m.role), ['system', 'user', 'assistant', 'toolResult']);
  assert.equal(transcript.messages[0].content, 'instructions');
  assert.deepEqual(transcript.messages[0].toolsAdded[0].parameters, request.tools[0].function.parameters);
  assert.equal('tools' in transcript, false);
  assert.deepEqual(transcript.messages[2].content[1], { type: 'toolCall', id: 'call-1', name: 'lookup', arguments: { key: 1 } });
  assert.equal(transcript.messages[3].toolName, 'lookup');
  const userFirst = convertRequest({ ...request, messages: [{ role: 'user', content: 'hello' }] }, model);
  assert.deepEqual(userFirst.messages.map(m => m.role), ['system', 'user']);
  assert.equal(userFirst.messages[0].content, '');
  assert.equal(userFirst.messages[0].toolsAdded[0].name, 'lookup');
  const noTools = convertRequest({ ...request, tools: [], messages: [{ role: 'user', content: 'hello' }] }, model);
  assert.deepEqual(noTools.messages.map(m => m.role), ['user']);
  assert.throws(() => convertRequest({ ...request, messages: [{ role: 'tool', content: 'x', tool_call_id: 'unknown' }] }, model));
  assert.throws(() => convertRequest({ ...request, messages: [{ role: 'assistant', content: '', tool_calls: [{ id: 'x', type: 'function', function: { name: 'lookup', arguments: '[]' } }] }] }, model));
});

test('converts response, rejects failures and malformed arguments', () => {
  const message = convertResponse({ stopReason: 'toolUse', content: [
    { type: 'text', text: 'checking' }, { type: 'toolCall', id: 'x', name: 'lookup', arguments: { key: 1 } },
  ] }, false);
  assert.deepEqual(message, { role: 'assistant', content: 'checking', tool_calls: [
    { id: 'x', type: 'function', function: { name: 'lookup', arguments: '{"key":1}' } },
  ] });
  assert.throws(() => convertResponse({ ...reply, stopReason: 'length' }, false));
  assert.throws(() => convertResponse({ stopReason: 'toolUse', content: [{ type: 'toolCall', id: 'x', name: 'lookup', arguments: {}, argumentsError: 'secret' }] }, false));
  assert.throws(() => convertResponse({ stopReason: 'error', errorMessage: 'secret', content: [] }, false));
});

test('passes credentials, session and identity and checks structured payload and JSON', async () => {
  let options;
  const sdk = { getModel: () => model, complete: async (_model, _context, opts) => {
    options = opts;
    assert.deepEqual(opts.onPayload({ model: 'test' }), { model: 'test', response_format: { type: 'json_object' } });
    return { stopReason: 'stop', content: [{ type: 'text', text: '{"ok":true}' }] };
  } };
  assert.deepEqual(await run({ ...request, structured: true }, 'key', sdk), { role: 'assistant', content: '{"ok":true}' });
  assert.equal(options.apiKey, 'key');
  assert.equal(options.sessionId, 'conversation');
  assert.equal(options.maxTokens, undefined);
  assert.equal(options.identity.userAgent.custom, 'quasar-bridge/1.0');
  await assert.rejects(run({ ...request, structured: true }, 'key', { ...sdk, complete: async () => reply }));
  await assert.rejects(run({ ...request, structured: true }, 'key', { ...sdk, getModel: () => ({ ...model, api: 'anthropic-messages' }) }));
  assert.throws(() => convertResponse({ ...reply, content: [{ type: 'text', text: '[]' }] }, true));
  assert.throws(() => convertResponse({ ...reply, content: [{ type: 'text', text: 'bad' }] }, true));
});

test('resolves missing OpenCode models from catalog without sending credentials or messages', async () => {
  let fetchArgs;
  const resolved = await resolveModel(() => undefined, 'opencode-go', iceman.id, async (...args) => {
    fetchArgs = args;
    return catalogFetch();
  });
  assert.equal(fetchArgs[0], 'https://models.dev/api.json');
  assert.deepEqual(Object.keys(fetchArgs[1]).sort(), ['redirect', 'signal']);
  assert.equal(resolved.contextWindow, iceman.limit.context);
  assert.equal(resolved.maxTokens, iceman.limit.output);
  assert.equal(resolved.baseUrl, iceman.provider.api);
  assert.equal(resolved.reasoning, iceman.reasoning);
  assert.deepEqual(resolved.input, ['text']);
  assert.equal(await resolveModel(() => model, 'test', 'test', () => { throw new Error('offline'); }), model);
  assert.equal(await resolveModel(() => undefined, 'other', 'missing', () => { throw new Error('offline'); }), undefined);
  assert.equal(await resolveModel(() => undefined, 'opencode-go', iceman.id, async () => { throw new Error('offline'); }), undefined);
  assert.equal(await resolveModel(() => undefined, 'opencode-go', iceman.id, async () => new Response('', { status: 503 })), undefined);
  assert.equal(await resolveModel(() => undefined, 'opencode-go', iceman.id, async () => new Response('{')), undefined);
  assert.equal(await resolveModel(() => undefined, 'opencode-go', iceman.id, async () =>
    new Response(JSON.stringify({ 'opencode-go': { ...catalog['opencode-go'], api: 'https://attacker.invalid' } }))), undefined);
  assert.equal(await resolveModel(() => undefined, 'opencode-go', iceman.id, async () =>
    new Response(JSON.stringify({ 'opencode-go': { ...catalog['opencode-go'], models: { [iceman.id]: { ...iceman, limit: {} } } } }))), undefined);
  await assert.rejects(run({ ...request, model: `opencode-go/${iceman.id}` }, 'secret-key', {
    getModel: () => undefined, catalogFetch: async () => { throw new Error('timeout'); },
    complete: () => { throw new Error('must not call provider'); },
  }), /unknown model/);
});

test('aborts a stalled catalog request', async () => {
  const keepAlive = setTimeout(() => {}, 5000);
  try {
    assert.equal(await resolveModel(() => undefined, 'opencode-go', iceman.id, (_url, { signal }) =>
      new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true }))), undefined);
  } finally {
    clearTimeout(keepAlive);
  }
});

test('real SDK uses model limits and session without unsupported JSON mode', async () => {
  const catalogModel = await resolveModel(getModel, 'opencode-go', iceman.id, catalogFetch);
  assert.ok(catalogModel);
  assert.equal(catalogModel.api, 'openai-completions');
  let body;
  let headers;
  const sdk = { getModel: () => catalogModel, complete: (m, c, options) => completeSimple(m, c, {
    ...options,
    fetch: async (url, init) => {
      body = JSON.parse(init.body);
      headers = new Headers(init.headers);
      const event = { id: 'chatcmpl-test', object: 'chat.completion.chunk', created: 1, model: m.id,
        choices: [{ index: 0, delta: { role: 'assistant', content: '{"ok":true}' }, finish_reason: 'stop' }] };
      return new Response(`data: ${JSON.stringify(event)}\n\ndata: [DONE]\n\n`, { status: 200, headers: { 'content-type': 'text/event-stream' } });
    },
  }) };
  const result = await run({ ...request, structured: true, tools: [] }, 'fake-key', sdk);
  assert.equal(result.content, '{"ok":true}');
  assert.equal(body.response_format, undefined);
  assert.ok(body.max_tokens > 0 && body.max_tokens <= iceman.limit.output);
  assert.notEqual(body.max_tokens, request.max_output_tokens);
  assert.equal(headers.get('x-opencode-session'), 'conversation');
  assert.equal(headers.get('user-agent'), 'quasar-bridge/1.0');
  const withTools = await run(request, 'fake-key', sdk);
  assert.equal(withTools.content, '{"ok":true}');
  assert.equal(body.messages[0].role, 'system');
  assert.equal(body.messages[0].content, 'instructions');
  assert.ok(body.tools.some(tool => tool.function?.name === 'lookup'));
});

test('real SDK sends JSON mode when the catalog model supports structured output', async () => {
  const structuredCatalog = { 'opencode-go': { ...catalog['opencode-go'],
    models: { [iceman.id]: { ...iceman, structured_output: true } } } };
  const catalogModel = await resolveModel(getModel, 'opencode-go', iceman.id, async () => new Response(JSON.stringify(structuredCatalog)));
  assert.equal(catalogModel.bridgeStructuredOutput, true);
  let body;
  const sdk = { getModel: () => catalogModel, complete: (m, c, options) => completeSimple(m, c, {
    ...options,
    fetch: async (_url, init) => {
      body = JSON.parse(init.body);
      const event = { id: 'chatcmpl-test', object: 'chat.completion.chunk', created: 1, model: m.id,
        choices: [{ index: 0, delta: { role: 'assistant', content: '{"ok":true}' }, finish_reason: 'stop' }] };
      return new Response(`data: ${JSON.stringify(event)}\n\ndata: [DONE]\n\n`, { status: 200, headers: { 'content-type': 'text/event-stream' } });
    },
  }) };
  const result = await run({ ...request, structured: true, tools: [] }, 'fake-key', sdk);
  assert.equal(result.content, '{"ok":true}');
  assert.deepEqual(body.response_format, { type: 'json_object' });
});

test('applies base_url and reasoning_effort overrides', async () => {
  let seen;
  const sdk = { getModel: () => ({ ...model }), complete: async (m, _c, options) => {
    seen = { baseUrl: m.baseUrl, effort: options.reasoningEffort };
    return reply;
  } };
  await run({ ...request, base_url: 'https://llm.example.invalid/v1', reasoning_effort: 'high' }, 'key', sdk);
  assert.deepEqual(seen, { baseUrl: 'https://llm.example.invalid/v1', effort: 'high' });
  await assert.rejects(run({ ...request, base_url: 'file:///etc/passwd' }, 'key', sdk));
  await assert.rejects(run({ ...request, base_url: 'https://user:pa@host/v1' }, 'key', sdk));
  await assert.rejects(run({ ...request, reasoning_effort: 'turbo' }, 'key', sdk));
});

test('selects a socks dispatcher only for socks proxy URLs', () => {
  assert.deepEqual(socksProxy('socks5://proxy.example.invalid:1080'), { host: 'proxy.example.invalid', port: 1080, type: 5 });
  assert.deepEqual(socksProxy('socks4://user:pa%20ss@192.0.2.10:1080'), { host: '192.0.2.10', port: 1080, type: 4, userId: 'user', password: 'pa ss' });
  assert.equal(socksProxy('http://proxy:3128'), undefined);
  assert.equal(socksProxy('socks5://proxy-without-port'), undefined);
  assert.equal(socksProxy(''), undefined);
  assert.equal(proxyDispatcher('http://127.0.0.1:3128'), undefined);
  assert.equal(applyProxy({}), false);
  assert.equal(applyProxy({ HTTPS_PROXY: 'socks5://127.0.0.1:1080' }), true);
});

test('subprocess never emits request data or provider errors on failure', async () => {
  const child = spawn(process.execPath, ['adapter.mjs'], { cwd: new URL('.', import.meta.url), env: { ...process.env, QUASAR_LLM_API_KEY: 'secret-key' } });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; });
  child.stderr.on('data', chunk => { stderr += chunk; });
  child.stdin.end('{"secret-request":');
  const code = await new Promise(resolve => child.on('close', resolve));
  assert.equal(code, 0);
  assert.deepEqual(JSON.parse(stdout), { error: 'Pi backend request failed' });
  assert.equal(stderr, '');
});

test('subprocess reports whitelisted adapter errors only', async () => {
  const runAdapter = input => new Promise(resolve => {
    const child = spawn(process.execPath, ['adapter.mjs'], { cwd: new URL('.', import.meta.url), env: { ...process.env, QUASAR_LLM_API_KEY: 'secret-key' } });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', chunk => { stdout += chunk; });
    child.stderr.on('data', chunk => { stderr += chunk; });
    child.stdin.end(input);
    child.on('close', code => resolve({ code, stdout, stderr }));
  });
  const unknown = await runAdapter(JSON.stringify({ model: 'missing/model', messages: [], tools: [], structured: false }));
  assert.equal(unknown.code, 0);
  assert.deepEqual(JSON.parse(unknown.stdout), { error: 'unknown model' });
  assert.equal(unknown.stderr, '');
  const invalid = await runAdapter(JSON.stringify({ model: 'noslash', messages: [], tools: [], structured: false }));
  assert.equal(invalid.code, 0);
  assert.deepEqual(JSON.parse(invalid.stdout), { error: 'Pi backend request failed' });
  assert.equal(invalid.stderr, '');
});
