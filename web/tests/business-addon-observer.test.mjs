import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { setImmediate } from 'node:timers/promises'
import vm from 'node:vm'
import { test } from 'node:test'

test('Passive observer captures only explicit selectors and never initiates requests', async () => {
  const source = await readFile(new URL('../../internal/addons/business/browser/observer.js', import.meta.url), 'utf8')
  const calls = []
  class XHR { open() {} send() {} }
  const window = { fetch: async (...args) => {
    calls.push(args)
    return new Response(JSON.stringify({ configId: 'cfg', secret: 'do-not-retain', uiSettings: { modelConfigInfo: { resolvedModels: [{ modelId: 'real-id', displayName: 'Model', secret: 'do-not-retain' }] } } }), { headers: { 'content-type': 'application/json' } })
  } }
  const location = { origin: 'https://business.gemini.google', href: 'https://business.gemini.google/u/0/cid/cfg?csesidx=123' }
  vm.runInNewContext(`(${source})()`, { window, location, XMLHttpRequest: XHR, URL, TextDecoder, Uint8Array })
  assert.equal(calls.length, 0)
  const body = JSON.stringify({ configId: 'cfg', streamAssistRequest: { assistGenerationConfig: { modelId: 'real-id', secret: 'do-not-retain' }, prompt: 'do-not-retain' }, token: 'do-not-retain' })
  const pending = window.fetch('https://biz-discoveryengine.googleapis.com/widget?token=do-not-retain', { method: 'POST', body })
  location.href = 'https://business.gemini.google/u/0/cid/other?csesidx=456'
  await pending
  for (let n = 0; n < 20; n++) await setImmediate()
  const observations = JSON.parse(JSON.stringify(window.__businessObserve()))
  assert.equal(calls.length, 1)
  assert.equal(calls[0][1].body, body)
  assert.equal(observations.length, 2)
  assert.ok(observations.every(item => item.config_id === 'cfg' && item.csesidx === '123'))
  assert.ok(!JSON.stringify(observations).includes('do-not-retain'))
  assert.equal(observations[0].payload.streamAssistRequest.assistGenerationConfig.modelId, 'real-id')
  assert.equal(observations[1].payload.uiSettings.modelConfigInfo.resolvedModels[0].modelId, 'real-id')
  assert.equal(window.__businessObserve().length, 0)
})

test('Passive observer does not attach to unrelated origins', async () => {
  const source = await readFile(new URL('../../internal/addons/business/browser/observer.js', import.meta.url), 'utf8')
  const window = {}
  vm.runInNewContext(`(${source})()`, { window, location: { origin: 'https://accounts.google.com' } })
  assert.equal(window.__businessObserve, undefined)
})
