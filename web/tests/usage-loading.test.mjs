import assert from 'node:assert/strict'
import { test } from 'node:test'
import { UsageCache, UsageRequest, usageRefreshDue } from '../src/components/usage/loading.ts'

test('bounded cache expires live results, preserves fixed ranges and promotes recent hits', () => {
  const cache = new UsageCache(2)
  cache.set('a', { value: 1, updatedAt: 1000 })
  cache.set('b', { value: 2, updatedAt: 1000 })
  assert.equal(cache.get('a', 30000, 30999).value, 1)
  assert.equal(cache.get('a', 30000, 31000), undefined)
  assert.equal(cache.get('a', Infinity, 999999).value, 1)
  cache.set('c', { value: 3, updatedAt: 1000 })
  assert.equal(cache.get('b', Infinity), undefined)
  cache.clear()
  assert.equal(cache.get('a', Infinity), undefined)
})

test('repeat visits use cache without fetching; explicit refresh fetches again', async () => {
  const resource = new UsageRequest(new UsageCache())
  let calls = 0
  const fetcher = async () => ++calls
  assert.equal((await resource.load('overview:scope', fetcher, Infinity)).value, 1)
  assert.equal((await resource.load('overview:scope', fetcher, Infinity)).value, 1)
  assert.equal(calls, 1)
  assert.equal((await resource.load('overview:scope', fetcher, Infinity, true)).value, 2)
})

test('switching cancels transport and ignores late completions even when transport ignores abort', async () => {
  const cache = new UsageCache()
  const resource = new UsageRequest(cache)
  let resolveOld, oldSignal
  const old = resource.load(
    'old',
    (signal) => {
      oldSignal = signal
      return new Promise((resolve) => {
        resolveOld = resolve
      })
    },
    Infinity,
  )
  const current = await resource.load('new', async () => 'new', Infinity)
  assert.equal(oldSignal.aborted, true)
  resolveOld('stale')
  assert.equal(await old, undefined)
  assert.equal(current.value, 'new')
  assert.equal(cache.get('old', Infinity), undefined)
})

test('leaving a page cancels work and failed refresh does not replace cached data', async () => {
  const cache = new UsageCache()
  const resource = new UsageRequest(cache)
  await resource.load('key', async () => 'previous', Infinity)
  await assert.rejects(
    resource.load(
      'key',
      async () => {
        throw Error('failed')
      },
      Infinity,
      true,
    ),
    /failed/,
  )
  assert.equal(cache.get('key', Infinity).value, 'previous')
  let signal, rejectPending
  const pending = resource.load(
    'pending',
    (value) => {
      signal = value
      return new Promise((_resolve, reject) => {
        rejectPending = reject
      })
    },
    Infinity,
  )
  resource.cancel()
  rejectPending(Error('aborted'))
  assert.equal(await pending, undefined)
  assert.equal(signal.aborted, true)
})

test('refresh requires active visible live page and pauses past the first records page', () => {
  const ready = {
    interval: 30,
    live: true,
    visible: true,
    loading: false,
    page: 0,
    lastAttempt: 1000,
    now: 31000,
  }
  assert.equal(usageRefreshDue(ready), true)
  for (const change of [
    { interval: 0 },
    { live: false },
    { visible: false },
    { loading: true },
    { page: 1 },
    { now: 30999 },
  ])
    assert.equal(usageRefreshDue({ ...ready, ...change }), false)
  // Resuming visibility yields one refresh; the new attempt prevents a burst.
  assert.equal(usageRefreshDue({ ...ready, lastAttempt: ready.now }), false)
})
