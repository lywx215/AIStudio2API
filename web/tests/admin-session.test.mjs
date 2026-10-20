import assert from 'node:assert/strict'
import { afterEach, beforeEach, test } from 'node:test'
import { api, ApiError } from '../src/api.ts'

const { EventTarget, Response } = globalThis
const originalFetch = globalThis.fetch
const originalWindow = globalThis.window
let expired
beforeEach(() => {
  globalThis.window = new EventTarget()
  expired = 0
  globalThis.window.addEventListener('admin-session-expired', () => expired++)
})
afterEach(() => {
  globalThis.fetch = originalFetch
  if (originalWindow === undefined) delete globalThis.window
  else globalThis.window = originalWindow
})
const failure = (code, status = 401) =>
  Response.json({ error: { code, message: 'Synthetic failure' } }, { status })

test('account refresh and relogin failures preserve a still-valid administrator session', async () => {
  globalThis.fetch = async (path) =>
    path === '/api/auth/session'
      ? Response.json({ enabled: true, authenticated: true, username: 'synthetic' })
      : failure('upstream_error')
  for (const operation of [
    () => api.refreshAccount('synthetic'),
    () => api.loginAccount('synthetic', 'view-synthetic', new AbortController().signal),
  ]) {
    await assert.rejects(operation, (error) => error instanceof ApiError && error.status === 401)
    assert.equal((await api.session()).authenticated, true)
    assert.equal(expired, 0)
  }
})

test('a failed member of refresh-all does not expire the console session', async () => {
  globalThis.fetch = async (path) =>
    path.includes('/expired/') ? failure('upstream_error') : Response.json({ account: {} })
  const results = await Promise.allSettled(['ready', 'expired'].map((id) => api.refreshAccount(id)))
  assert.equal(results[0].status, 'fulfilled')
  assert.equal(results[1].status, 'rejected')
  assert.equal(expired, 0)
})

test('actual console authentication failures expire both JSON and command requests', async () => {
  globalThis.fetch = async () => failure('admin_login_required')
  await assert.rejects(() => api.refreshAccount('synthetic'), { code: 'admin_login_required' })
  await assert.rejects(
    () => api.loginAccount('synthetic', 'view-synthetic', new AbortController().signal),
    { code: 'admin_login_required' },
  )
  assert.equal(expired, 2)
})

test('login rejection, model-key failures, forbidden and malformed responses do not expire sessions', async () => {
  globalThis.fetch = async () => failure('invalid_credentials')
  await assert.rejects(() => api.login('synthetic', 'synthetic', false), ApiError)
  for (const response of [
    failure('invalid_api_key'),
    failure('admin_login_required', 403),
    new Response('bad gateway', { status: 401 }),
    new Response('{', { status: 401, headers: { 'Content-Type': 'application/json' } }),
  ]) {
    globalThis.fetch = async () => response
    await assert.rejects(() => api.status(), ApiError)
  }
  assert.equal(expired, 0)
})

test('login preference and browser-view cancellation are sent by the updated API', async () => {
  const calls = []
  const controller = new AbortController()
  globalThis.fetch = async (path, init) => {
    calls.push({ path, init })
    return Response.json({ enabled: true, authenticated: true, username: 'synthetic' })
  }
  await api.login('synthetic', 'synthetic', true)
  assert.deepEqual(JSON.parse(calls[0].init.body), {
    username: 'synthetic',
    password: 'synthetic',
    remember: true,
  })
  await api.createAccount(
    { locale: 'en-US', timezone: 'America/Los_Angeles' },
    'synthetic-view',
    controller.signal,
  )
  await api.loginAccount('synthetic', 'synthetic-view', controller.signal)
  for (const call of calls.slice(1)) {
    assert.equal(call.init.signal, controller.signal)
    assert.equal(JSON.parse(call.init.body).view, 'synthetic-view')
  }
  assert.equal(calls[1].path, '/api/accounts')
  assert.equal(calls[2].path, '/api/accounts/synthetic/login')
  assert.equal(expired, 0)
})
