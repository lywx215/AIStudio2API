import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'
import * as vue from 'vue'
import { sourceModule } from './source-module.mjs'

const originalWindow = globalThis.window
let activeStore
const deferred = () => {
  let resolve
  const promise = new Promise((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const values = (label) => ({
  status: { state: 'RUNNING', version: label },
  accounts: [{ id: label, label }],
  models: [{ id: label }],
  cooldowns: [{ account: label }],
  requests: [{ id: label }],
  config: { proxy_api_key: `synthetic-${label}` },
})
async function fixture(overrides = {}) {
  const listeners = new Map()
  const timers = new Map()
  const connections = []
  const writes = []
  let nextTimer = 0
  globalThis.window = {
    addEventListener: (name, listener) => listeners.set(name, listener),
    removeEventListener: (name) => listeners.delete(name),
    setTimeout: (callback) => {
      timers.set(++nextTimer, callback)
      return nextTimer
    },
    clearTimeout: (id) => timers.delete(id),
  }
  const initial = values('current')
  const api = Object.fromEntries(
    Object.entries(initial).map(([key, value]) => [key, async () => value]),
  )
  Object.assign(
    api,
    {
      updateStatus: async () => ({ current: 'current' }),
      deleteAccount: async (id) => {
        writes.push(id)
      },
      startService: async () => initial.status,
      stopService: async () => ({ state: 'STOPPED' }),
      clearLogs: async () => {},
    },
    overrides,
  )
  const module = await sourceModule(new URL('../src/console.ts', import.meta.url), {
    vue,
    '@/api': {
      api,
      openAdminEvents: (onEvent, onOpen) => {
        const connection = { onEvent, onOpen, closed: false }
        connections.push(connection)
        return {
          close: () => {
            connection.closed = true
          },
        }
      },
    },
    '@/i18n': { useI18n: () => ({ t: (key) => key, errorText: (error) => error.message }) },
  })
  activeStore = module.useConsole()
  return { store: activeStore, api, connections, writes, listeners, timers }
}
afterEach(() => {
  activeStore?.stopConsole(false)
  activeStore = undefined
  if (originalWindow === undefined) delete globalThis.window
  else globalThis.window = originalWindow
})

test('stopping during initial requests prevents late state writes and late SSE creation', async () => {
  const old = values('old-session')
  const pending = Object.fromEntries(Object.keys(old).map((key) => [key, deferred()]))
  const f = await fixture(
    Object.fromEntries(Object.entries(pending).map(([key, value]) => [key, () => value.promise])),
  )
  const first = f.store.startConsole()
  f.store.stopConsole(false)
  assert.equal(f.listeners.has('pagehide'), false)
  const fresh = values('new-session')
  for (const [key, value] of Object.entries(fresh)) f.api[key] = async () => value
  await f.store.startConsole()
  for (const [key, value] of Object.entries(old)) pending[key].resolve(value)
  await first
  assert.equal(f.store.accounts.value[0].id, 'new-session')
  assert.equal(f.store.config.value.proxy_api_key, 'synthetic-new-session')
  assert.equal(f.connections.length, 1)
})

test('session expiry clears sensitive state, closes SSE and cancels queued deletions', async () => {
  const f = await fixture()
  await f.store.startConsole()
  f.connections[0].onEvent({ type: 'log', data: { message: 'synthetic private log' } })
  f.store.showNotice('synthetic private notice', 'error')
  f.store.deleteAccount({ id: 'pending-delete', label: 'Synthetic account' })
  f.store.stopConsole(false)
  assert.equal(f.connections[0].closed, true)
  assert.equal(f.store.status.value, null)
  assert.equal(f.store.config.value, null)
  assert.equal(f.store.update.value, null)
  for (const key of ['accounts', 'models', 'cooldowns', 'requests', 'logs'])
    assert.deepEqual(f.store[key].value, [])
  assert.equal(f.store.notice.message, '')
  assert.equal(f.store.pendingDelete.value, null)
  assert.deepEqual(f.writes, [])
  assert.equal(f.timers.size, 0)
  f.connections[0].onEvent({ type: 'accounts', data: { accounts: [{ id: 'late' }] } })
  f.connections[0].onOpen()
  assert.deepEqual(f.store.accounts.value, [])
})

test('late refresh, update checks and service operations cannot repopulate a stopped store', async () => {
  const update = deferred()
  const service = deferred()
  const f = await fixture({
    updateStatus: () => update.promise,
    startService: () => service.promise,
  })
  await f.store.startConsole()
  const account = deferred()
  f.api.accounts = () => account.promise
  const refresh = f.store.loadAccountData()
  const launch = f.store.startService()
  f.store.stopConsole(false)
  account.resolve([{ id: 'late-account' }])
  service.resolve({ state: 'RUNNING', version: 'late' })
  update.resolve({ current: 'late-update' })
  await Promise.all([refresh, launch])
  assert.deepEqual(f.store.accounts.value, [])
  assert.equal(f.store.status.value, null)
  assert.equal(f.store.update.value, null)
  assert.equal(f.store.startPending.value, false)
  assert.equal(f.store.notice.message, '')
})

test('normal navigation submits an already queued deletion only once', async () => {
  const f = await fixture()
  await f.store.startConsole()
  await f.store.startConsole()
  assert.equal(f.connections.length, 1)
  f.store.deleteAccount({ id: 'queued', label: 'Synthetic' })
  f.store.stopConsole()
  f.store.stopConsole()
  await Promise.resolve()
  assert.deepEqual(f.writes, ['queued'])
  assert.deepEqual(f.store.accounts.value, [])
})
