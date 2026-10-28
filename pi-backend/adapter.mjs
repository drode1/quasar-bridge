import { completeSimple, getModel } from '@earendil-works/pi-ai/compat';
import { pathToFileURL } from 'node:url';
import tls from 'node:tls';
import { SocksClient } from 'socks';
import { Agent, setGlobalDispatcher } from 'undici';

const MAX_INPUT_BYTES = 4 * 1024 * 1024;
const ADAPTER_ERRORS = new Set([
  'unknown model',
  'missing API key',
  'structured output unsupported',
  'LLM answer exceeded token budget',
  'LLM request failed',
]);
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);

function adapterError(error) {
  const message = error instanceof Error ? error.message : '';
  return ADAPTER_ERRORS.has(message) ? message : 'Pi backend request failed';
}

const EFFORTS = new Set(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']);

function validBaseURL(value) {
  if (typeof value !== 'string' || !/^https?:\/\//i.test(value)) return false;
  try {
    const url = new URL(value);
    return Boolean(url.hostname) && !url.username && !url.password;
  } catch {
    return false;
  }
}

export function socksProxy(raw) {
  if (typeof raw !== 'string' || !/^socks(4|5|5h)?:\/\//i.test(raw)) return undefined;
  const url = new URL(raw);
  if (!url.hostname || !url.port) return undefined;
  return {
    host: url.hostname,
    port: Number(url.port),
    type: url.protocol.toLowerCase() === 'socks4:' ? 4 : 5,
    ...(url.username ? { userId: decodeURIComponent(url.username) } : {}),
    ...(url.password ? { password: decodeURIComponent(url.password) } : {}),
  };
}

export function proxyDispatcher(raw) {
  const proxy = socksProxy(raw);
  if (!proxy) return undefined;
  return new Agent({
    connect(options, callback) {
      const secure = options.protocol === 'https:';
      const port = Number(options.port) || (secure ? 443 : 80);
      SocksClient.createConnection({ proxy, command: 'connect', destination: { host: options.hostname, port } })
        .then(({ socket }) => {
          if (!secure) {
            callback(null, socket);
            return;
          }
          callback(null, tls.connect({ socket, servername: options.servername || options.hostname }));
        })
        .catch(error => callback(error));
    },
  });
}

export function applyProxy(env) {
  const dispatcher = proxyDispatcher(env.HTTPS_PROXY ?? env.ALL_PROXY);
  if (dispatcher) setGlobalDispatcher(dispatcher);
  return Boolean(dispatcher);
}

function argumentsObject(value) {
  if (typeof value !== 'string') throw new Error('invalid tool arguments');
  const parsed = JSON.parse(value);
  if (!object(parsed)) throw new Error('invalid tool arguments');
  return parsed;
}

export async function resolveModel(getCatalogModel, provider, id, catalogFetch = fetch) {
  const model = getCatalogModel(provider, id);
  if (model) return model;
  if (provider !== 'opencode-go') return undefined;
  try {
    const response = await catalogFetch('https://models.dev/api.json', {
      signal: AbortSignal.timeout(3000), redirect: 'error',
    });
    if (!response.ok) return undefined;
    const catalog = await response.json();
    const entry = catalog?.[provider]?.models?.[id];
    const baseUrl = 'https://opencode.ai/inference/go/openai/v1';
    if (catalog?.[provider]?.api !== baseUrl || entry?.id !== id ||
        (entry.provider?.api && entry.provider.api !== baseUrl) ||
        typeof entry.name !== 'string' || typeof entry.reasoning !== 'boolean' ||
        !Array.isArray(entry.modalities?.input) || !entry.modalities.input.includes('text') ||
        !Number.isSafeInteger(entry.limit?.context) || entry.limit.context <= 0 ||
        !Number.isSafeInteger(entry.limit?.output) || entry.limit.output <= 0 ||
        !object(entry.cost) || !['input', 'output', 'cache_read', ...(entry.cost.cache_write === undefined ? [] : ['cache_write'])].every(key =>
          typeof entry.cost[key] === 'number' && Number.isFinite(entry.cost[key]) && entry.cost[key] >= 0)) {
      return undefined;
    }
    return {
      id, name: entry.name, provider, api: 'openai-completions', baseUrl,
      reasoning: entry.reasoning, input: entry.modalities.input.filter(type => type === 'text' || type === 'image'),
      contextWindow: entry.limit.context, maxTokens: entry.limit.output,
      cost: { input: entry.cost.input, output: entry.cost.output, cacheRead: entry.cost.cache_read,
        cacheWrite: entry.cost.cache_write ?? 0 },
      compat: { supportsStore: false, supportsDeveloperRole: false, supportsStrictMode: true, maxTokensField: 'max_tokens' },
      bridgeStructuredOutput: entry.structured_output === true,
      type: 'chat',
    };
  } catch {
    return undefined;
  }
}

export function convertRequest(request, model) {
  if (!object(request) || !Array.isArray(request.messages) || !Array.isArray(request.tools) ||
      typeof request.structured !== 'boolean' ||
      (request.session_id !== undefined && typeof request.session_id !== 'string')) {
    throw new Error('invalid request');
  }
  const tools = request.tools.map(tool => {
    const fn = tool?.function;
    if (tool?.type !== 'function' || !object(fn) || typeof fn.name !== 'string' || !fn.name ||
        typeof fn.description !== 'string' || !object(fn.parameters)) throw new Error('invalid tool');
    return { name: fn.name, description: fn.description, parameters: fn.parameters };
  });
  const calls = new Map();
  const messages = request.messages.map(message => {
    if (!object(message) || typeof message.content !== 'string') throw new Error('invalid message');
    const timestamp = Date.now();
    if (message.role === 'system' || message.role === 'user') {
      return { role: message.role, content: message.content, timestamp };
    }
    if (message.role === 'assistant') {
      const content = message.content ? [{ type: 'text', text: message.content }] : [];
      if (message.tool_calls !== undefined && !Array.isArray(message.tool_calls)) throw new Error('invalid tool calls');
      for (const call of message.tool_calls ?? []) {
        if (call?.type !== 'function' || typeof call.id !== 'string' || !call.id ||
            typeof call.function?.name !== 'string' || !call.function.name || calls.has(call.id)) {
          throw new Error('invalid tool call');
        }
        calls.set(call.id, call.function.name);
        content.push({ type: 'toolCall', id: call.id, name: call.function.name, arguments: argumentsObject(call.function.arguments) });
      }
      return { role: 'assistant', content, api: model.api, provider: model.provider, model: model.id,
        usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
        stopReason: message.tool_calls?.length ? 'toolUse' : 'stop', timestamp };
    }
    if (message.role === 'tool') {
      const name = calls.get(message.tool_call_id);
      if (!name) throw new Error('unknown tool call');
      calls.delete(message.tool_call_id);
      return { role: 'toolResult', toolCallId: message.tool_call_id, toolName: name,
        content: [{ type: 'text', text: message.content }], isError: false, timestamp };
    }
    throw new Error('invalid role');
  });
  if (tools.length > 0) {
    if (messages[0]?.role === 'system') messages[0] = { ...messages[0], toolsAdded: tools };
    else messages.unshift({ role: 'system', content: '', toolsAdded: tools, timestamp: Date.now() });
  }
  return { messages };
}

export function convertResponse(result, structured) {
  if (!object(result) || !Array.isArray(result.content)) throw new Error('invalid response');
  if (result.stopReason === 'length') throw new Error('LLM answer exceeded token budget');
  if (result.stopReason !== 'stop' && result.stopReason !== 'toolUse') throw new Error('LLM request failed');
  let content = '';
  const toolCalls = [];
  for (const block of result.content) {
    if (block.type === 'text') {
      if (typeof block.text !== 'string') throw new Error('invalid response');
      content += block.text;
    } else if (block.type === 'toolCall') {
      if (block.argumentsError || typeof block.id !== 'string' || !block.id ||
          typeof block.name !== 'string' || !block.name || !object(block.arguments)) throw new Error('invalid tool call');
      toolCalls.push({ id: block.id, type: 'function', function: { name: block.name, arguments: JSON.stringify(block.arguments) } });
    }
  }
  if (!content.trim() && !toolCalls.length) throw new Error('LLM returned no answer');
  if (structured) {
    if (toolCalls.length || !object(JSON.parse(content))) throw new Error('invalid structured response');
  }
  return { role: 'assistant', content, ...(toolCalls.length ? { tool_calls: toolCalls } : {}) };
}

export async function run(request, apiKey, sdk = { getModel, complete: completeSimple }) {
  if (!object(request) || typeof request.model !== 'string') throw new Error('invalid request');
  const separator = request.model.indexOf('/');
  if (separator <= 0 || separator === request.model.length - 1) throw new Error('invalid model');
  const model = await resolveModel(sdk.getModel, request.model.slice(0, separator), request.model.slice(separator + 1), sdk.catalogFetch);
  if (!model) throw new Error('unknown model');
  if (typeof apiKey !== 'string' || !apiKey) throw new Error('missing API key');
  if (request.base_url !== undefined && !validBaseURL(request.base_url)) throw new Error('invalid request');
  if (request.reasoning_effort !== undefined && !EFFORTS.has(request.reasoning_effort)) throw new Error('invalid request');
  const effective = request.base_url ? { ...model, baseUrl: request.base_url } : model;
  const context = convertRequest(request, effective);
  if (request.structured && effective.api !== 'openai-completions') throw new Error('structured output unsupported');
  const jsonMode = request.structured && model.bridgeStructuredOutput !== false;
  let payloadChecked = false;
  const options = {
    apiKey,
    maxRetries: 0,
    identity: { userAgent: { custom: 'quasar-bridge/1.0' } },
    ...(request.session_id ? { sessionId: request.session_id } : {}),
    ...(request.reasoning_effort ? { reasoningEffort: request.reasoning_effort } : {}),
    ...(jsonMode ? { onPayload(payload) {
      if (!object(payload)) throw new Error('invalid provider payload');
      payloadChecked = true;
      return { ...payload, response_format: { type: 'json_object' } };
    } } : {}),
  };
  const result = await sdk.complete(effective, context, options);
  if (jsonMode && !payloadChecked) throw new Error('structured output unsupported');
  return convertResponse(result, request.structured);
}

async function main() {
  applyProxy(process.env);
  try {
    const chunks = [];
    let length = 0;
    for await (const chunk of process.stdin) {
      length += chunk.length;
      if (length > MAX_INPUT_BYTES) throw new Error('input too large');
      chunks.push(chunk);
    }
    const request = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    const message = await run(request, process.env.QUASAR_LLM_API_KEY);
    process.stdout.write(JSON.stringify({ message }));
  } catch (error) {
    process.stdout.write(JSON.stringify({ error: adapterError(error) }));
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
