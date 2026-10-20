import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'
import { EventSourceParserStream } from 'eventsource-parser/stream'
import { sourceModule } from './source-module.mjs'

const originalFetch = globalThis.fetch
const request = await sourceModule(new URL('../src/playground/request.ts', import.meta.url), {
  '@/console': { useConsole: () => ({ config: { value: null } }) },
  './catalog': { hasCode: () => false, toolCodes: {} },
})
const protocols = await sourceModule(new URL('../src/playground/protocols.ts', import.meta.url), {
  'eventsource-parser/stream': { EventSourceParserStream },
  './request': request,
})
afterEach(() => {
  globalThis.fetch = originalFetch
})

const secret = 'synthetic-private-upstream-body'
const options = (protocol, changes = {}) => ({
  protocol,
  stream: true,
  model: undefined,
  modelID: 'synthetic-model',
  apiKey: 'synthetic-model-key',
  request: {
    contents: [{ role: 'user', parts: [{ text: 'Synthetic prompt' }] }],
    generationConfig: {},
  },
  images: { count: 1, size: 'auto', quality: 'auto' },
  signal: new AbortController().signal,
  onUpdate() {},
  ...changes,
})
const stream = (...events) =>
  new Response(
    events
      .map((event) => `data: ${typeof event === 'string' ? event : JSON.stringify(event)}\n\n`)
      .join(''),
    {
      headers: { 'Content-Type': 'text/event-stream' },
    },
  )

test('Playground transport errors expose only the documented message field', async () => {
  for (const response of [
    new Response(secret, { status: 502 }),
    new Response(`{"secret":"${secret}"}`, {
      status: 502,
      headers: { 'Content-Type': 'application/json' },
    }),
    new Response(`{"${secret}`, { status: 502, headers: { 'Content-Type': 'application/json' } }),
    Response.json({ error: { message: { secret } } }, { status: 502 }),
  ]) {
    assert.equal(await request.errorMessage(response), 'Request failed')
  }
  assert.equal(
    await request.errorMessage(
      Response.json({ error: { message: 'Safe protocol message' } }, { status: 401 }),
    ),
    'Safe protocol message',
  )
})

test('each public streaming protocol requires its own completion event', async () => {
  const completions = {
    gemini: { candidates: [{ content: { parts: [{ text: 'complete' }] }, finishReason: 'STOP' }] },
    'openai-chat': '[DONE]',
    'openai-responses': { type: 'response.completed', response: { output: [] } },
    anthropic: { type: 'message_stop' },
    'openai-images': { type: 'image_generation.completed' },
  }
  for (const [protocol, completion] of Object.entries(completions)) {
    globalThis.fetch = async () => stream(completion)
    await protocols.generate(options(protocol))
    globalThis.fetch = async () => stream()
    await assert.rejects(
      protocols.generate(options(protocol)),
      /before the protocol completion event/,
    )
  }
})

test('a truncated Gemini stream keeps partial output and reports a failure', async () => {
  const updates = []
  globalThis.fetch = async () =>
    stream({ candidates: [{ content: { parts: [{ text: 'partial output' }] } }] })
  await assert.rejects(
    protocols.generate(options('gemini', { onUpdate: (update) => updates.push(update) })),
    /before the protocol completion event/,
  )
  assert.equal(updates[0].parts[0].text, 'partial output')
  globalThis.fetch = async () => stream({ promptFeedback: { blockReason: 'SAFETY' } })
  await protocols.generate(options('gemini'))
})

test('malformed JSON and unknown error objects do not leak transport bodies', async () => {
  for (const response of [
    stream(`{"${secret}`),
    stream({ error: { secret } }),
    new Response(`{"${secret}`, { headers: { 'Content-Type': 'application/json' } }),
  ]) {
    globalThis.fetch = async () => response
    await assert.rejects(
      protocols.generate(
        options('gemini', { stream: response.headers.get('Content-Type') === 'text/event-stream' }),
      ),
      (error) => error instanceof request.StreamError && !error.message.includes(secret),
    )
  }
})

test('cancellation is not turned into an unexpected-EOF error', async () => {
  const controller = new AbortController()
  globalThis.fetch = async () => {
    controller.abort()
    return stream()
  }
  await assert.rejects(protocols.generate(options('gemini', { signal: controller.signal })), {
    name: 'AbortError',
  })
})
