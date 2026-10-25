// OpenAI Responses 协议一致性测试（非流式 + 工具调用回放 + 错误码）。
//
// 需要：Node 18+、可访问的网关地址、一个有效的网关 api_key。
// 用法：
//   BASE_URL=http://127.0.0.1:7863 API_KEY=xxx MODEL=cn:fast-model node Test-Harness.mjs
// 可选：
//   PI_AI_PATH —— 指向 @earendil-works/pi-ai 的 openai-responses.js，用于验证真实客户端库；
//                 不设置则只跑纯 HTTP 用例。
import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

const baseURL = (process.env.BASE_URL || process.argv[2] || 'http://127.0.0.1:7863').replace(/\/$/, '')
const apiKey = process.env.API_KEY || process.argv[3]
const modelId = process.env.MODEL || process.argv[4] || 'cn:fast-model'
if (!apiKey) throw Error('API_KEY is required (env or 3rd argument)')

async function post(path, body, key = apiKey) {
  const response = await fetch(`${baseURL}${path}`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(120000),
  })
  const text = await response.text()
  let parsed
  try { parsed = JSON.parse(text) } catch { parsed = { raw: text.slice(0, 400) } }
  return { status: response.status, body: parsed }
}

const report = { baseURL, model: modelId }

// 1. 非流式
const plain = await post('/responses', { model: modelId, input: 'Reply with OK only.', max_output_tokens: 256, store: false })
if (plain.status !== 200 || plain.body.object !== 'response' || plain.body.status !== 'completed' || !plain.body.output_text?.trim()) {
  throw Error(`Responses non-streaming failed: HTTP ${plain.status}, ${plain.body.error?.message || plain.body.status || plain.body.raw}`)
}
report.nonStreaming = { status: plain.status, reply: plain.body.output_text }

// 2. 工具调用：先要求模型发起 function_call，再把结果回放
const tools = [{
  type: 'function', name: 'add_numbers', description: 'Return the sum of two numbers.',
  parameters: { type: 'object', properties: { a: { type: 'number' }, b: { type: 'number' } }, required: ['a', 'b'] },
}]
const toolCall = await post('/responses', {
  model: modelId, input: 'Use add_numbers once to add 2 and 3.', tool_choice: 'required', tools, store: false,
})
if (toolCall.status !== 200) throw Error(`Tool call request failed: HTTP ${toolCall.status}`)
const calls = (toolCall.body.output || []).filter(item => item.type === 'function_call')
if (!calls.length) throw Error('Expected a function call in the response')
const args = JSON.parse(calls[0].arguments)
if (calls[0].name !== 'add_numbers' || args.a !== 2 || args.b !== 3) throw Error('Tool identity/arguments were corrupted')

const replay = await post('/responses', {
  model: modelId, store: false,
  input: [
    { role: 'user', content: 'Add 2 and 3 using add_numbers, then report the answer.' },
    ...toolCall.body.output,
    { type: 'function_call_output', call_id: calls[0].call_id, output: String(args.a + args.b) },
  ],
})
if (replay.status !== 200 || !String(replay.body.output_text || '').includes('5')) {
  throw Error(`Tool-result replay failed: HTTP ${replay.status}, ${replay.body.error?.message || replay.body.output_text}`)
}
report.toolRoundTrip = { toolCalls: calls.length, reply: replay.body.output_text }

// 3. 错误路径
const wrongKey = await post('/responses', { model: modelId, input: 'x' }, 'invalid-test-key')
if (wrongKey.status !== 401) throw Error(`Invalid key must be rejected with 401, got ${wrongKey.status}`)
const unsupported = await post('/responses', { model: modelId, input: 'x', previous_response_id: 'resp_missing' })
if (unsupported.status !== 400) throw Error(`Unsupported stateful request must be rejected with 400, got ${unsupported.status}`)
report.errors = { invalidKeyHTTP: wrongKey.status, unsupportedStateHTTP: unsupported.status }

// 4. 可选：用真实客户端库（@earendil-works/pi-ai）复验流式链路
const piPath = process.env.PI_AI_PATH
if (piPath) {
  const { streamSimple } = await import(pathToFileURL(piPath).href)
  const model = {
    id: modelId, provider: 'workbuddy', api: 'openai-responses', baseUrl: baseURL,
    input: ['text'], reasoning: false, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  }
  const result = await streamSimple(model, {
    messages: [{ role: 'user', content: 'Reply with OK only. Do not use tools.', timestamp: Date.now() }],
  }, { apiKey, maxTokens: 512, signal: AbortSignal.timeout(120000) }).result()
  if (['error', 'aborted'].includes(result.stopReason)) {
    throw Error(`Library call failed: ${String(result.errorMessage || '').replaceAll(apiKey, '[REDACTED]')}`)
  }
  const text = result.content.filter(b => b.type === 'text').map(b => b.text).join('')
  if (!text.trim()) throw Error('Library returned no visible response')
  report.streamingLibrary = { reply: text }
}

console.log(JSON.stringify({ ...report, status: 'passed' }, null, 2))
