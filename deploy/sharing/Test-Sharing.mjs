import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const base = process.argv[2] || process.env.SHARE_BASE || 'http://127.0.0.1:17864'
const here = dirname(fileURLToPath(import.meta.url))
const shareKeyFile = process.env.SHARE_KEY_FILE || join(here, 'private', 'share-key.json')
const gatewayConfigFile = process.env.GATEWAY_CONFIG || join(here, '..', '..', 'gateway', 'config.json')
const { api_key: key } = JSON.parse(readFileSync(shareKeyFile, 'utf8'))
const { api_key: internalKey } = JSON.parse(readFileSync(gatewayConfigFile, 'utf8'))
const checks = []
let activeRequest = ''
async function request(path, { token = key, method = 'GET', body } = {}) {
  activeRequest = `${method} ${path}`
  return fetch(base + path, {
    method, headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    redirect: 'manual', signal: AbortSignal.timeout(90000),
  })
}
try {
  assert.notEqual(key, internalKey)
  for (const token of ['', 'incorrect-key', internalKey]) {
    const response = await request('/v1/models', { token })
    assert.equal(response.status, 401, 'Anonymous, invalid and original gateway keys must be rejected')
    await response.text()
  }
  checks.push('independent-key-auth')
  for (const [method, path] of [
    ['GET', '/'], ['GET', '/access'], ['GET', '/v1/stats'], ['GET', '/v1/telemetry/requests'],
    ['GET', '/v1/responses'], ['POST', '/v1/models'], ['GET', '/v1/models?redirect=/access'],
    ['POST', '/v1/responses/'], ['GET', '/v1/%6dodels'],
  ]) {
    const response = await request(path, { method })
    assert.equal(response.status, 404, `${method} ${path} should not be shared`)
    await response.text()
  }
  checks.push('exact-endpoint-allowlist')
  const models = await request('/v1/models')
  assert.equal(models.status, 200)
  const modelData = await models.json()
  assert.ok(modelData.data.length > 0)
  checks.push('models')
  const body = { model: 'cn:deepseek-v4.1-flash', input: 'Reply with OK only.', max_output_tokens: 256, store: false }
  const plain = await request('/v1/responses', { method: 'POST', body })
  assert.equal(plain.status, 200)
  const result = await plain.json()
  assert.equal(result.status, 'completed')
  assert.ok(result.output_text.trim())
  checks.push('responses-nonstream')
  const streaming = await request('/v1/responses', { method: 'POST', body: {
    ...body, input: 'Count from 1 to 30 using comma-separated numbers. No other text.', stream: true,
  } })
  assert.equal(streaming.status, 200)
  assert.ok(streaming.headers.get('content-type').includes('text/event-stream'))
  const decoder = new TextDecoder()
  let stream = '', deltaBeforeCompletion = false
  for await (const chunk of streaming.body) {
    stream += decoder.decode(chunk, { stream: true })
    if (stream.includes('event: response.output_text.delta') && !stream.includes('event: response.completed')) {
      deltaBeforeCompletion = true
    }
  }
  stream += decoder.decode()
  assert.ok(stream.includes('event: response.output_text.delta'))
  assert.ok(stream.includes('event: response.completed'))
  assert.ok(!stream.includes('event: response.failed'))
  assert.ok(deltaBeforeCompletion, 'Text must arrive before the completed event, not as one buffered response')
  assert.ok(!stream.includes(internalKey) && !stream.includes(key))
  checks.push('responses-stream')
  const chat = await request('/v1/chat/completions', { method: 'POST', body: {
    model: body.model, messages: [{ role: 'user', content: 'Reply with OK only.' }], max_tokens: 256,
  } })
  assert.equal(chat.status, 200)
  assert.ok((await chat.json()).choices[0].message.content.trim())
  checks.push('chat-completions')
  const toolRequest = await request('/v1/responses', { method: 'POST', body: {
    ...body, input: 'Use add_numbers to add 2 and 3.', tool_choice: 'required',
    tools: [{ type: 'function', name: 'add_numbers', description: 'Add two numbers.',
      parameters: { type: 'object', properties: { a: { type: 'number' }, b: { type: 'number' } }, required: ['a', 'b'] } }],
  } })
  assert.equal(toolRequest.status, 200)
  const toolResponse = await toolRequest.json()
  const calls = toolResponse.output.filter(item => item.type === 'function_call')
  assert.equal(calls.length, 1)
  assert.equal(calls[0].name, 'add_numbers')
  const args = JSON.parse(calls[0].arguments)
  assert.deepEqual(args, { a: 2, b: 3 })
  const replay = await request('/v1/responses', { method: 'POST', body: {
    ...body, input: [
      { role: 'user', content: 'Add 2 and 3 using add_numbers, then report the answer.' },
      ...toolResponse.output,
      { type: 'function_call_output', call_id: calls[0].call_id, output: String(args.a + args.b) },
    ],
  } })
  assert.equal(replay.status, 200)
  assert.ok((await replay.json()).output_text.includes('5'))
  checks.push('function-tool-roundtrip')
  if (new URL(base).hostname === '127.0.0.1') {
    const burst = await Promise.all(Array.from({ length: 20 }, async () => {
    const response = await request('/v1/models')
    await response.text()
    return response.status
  }))
  assert.ok(burst.includes(429), 'Sharing rate/concurrency limit must reject a burst')
  assert.ok(burst.every(status => status === 200 || status === 429))
    checks.push('request-limits')
  }
  console.log(JSON.stringify({ base, checks, status: 'passed' }, null, 2))
} catch (error) {
  console.error(JSON.stringify({
    completed: checks, activeRequest,
    error: String(error.message || error).replaceAll(key, '[REDACTED]').replaceAll(internalKey, '[REDACTED]'),
    cause: error.cause?.code,
  }, null, 2))
  process.exitCode = 1
}
