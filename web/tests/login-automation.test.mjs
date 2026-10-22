import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'

// Run the production browser function and the same fake DOM used by Goja.
// No browser, server credentials or real Google requests are involved.
const source = readFileSync(
  new URL('../../internal/camoufoxnative/login_automation.go', import.meta.url),
  'utf8',
)
const fixtures = readFileSync(
  new URL('../../internal/camoufoxnative/login_totp_test.go', import.meta.url),
  'utf8',
)
const production = source.match(/const loginAutomationFunction = `([\s\S]*?)`/)[1]
const fixture = fixtures.match(/vm\.RunString\(`([\s\S]*?)`\)/)[1]
const modes = JSON.parse(`[${fixtures.match(/for _, mode := range \[\]string\{([^}]+)\}/)[1]}]`)
const loading = new Set([
  'loading',
  'next_loading',
  'identity_loading',
  'input_loading',
  'selection_stale_password',
  'selection_stale_identifier',
  'totp_stale_password',
  'totp_stale_identifier',
])

function page(mode) {
  const context = vm.createContext({ mode })
  vm.runInContext(fixture, context)
  const action = vm.runInContext(`(${production})`, context)
  return { context, action: (kind) => action(kind, '005924', 'user@example.test') }
}

for (const mode of modes) {
  test(`Google TOTP DOM: ${mode}`, () => {
    const { context, action } = page(mode)
    const phase = action('inspect')
    if (loading.has(mode)) assert.equal(phase, 'totp_loading')
    if (mode === 'origin_changed') {
      assert.equal(phase, 'totp')
      vm.runInContext(`location.origin = 'https://example.test'`, context)
    }
    const result = action('totp')
    if (mode === 'valid') {
      assert.equal(phase, 'totp')
      assert.equal(result, 'submitted')
      assert.equal(context.filled, '005924')
      assert.equal(context.clicked, 1)
      assert.equal(action('inspect'), 'totp_pending')
    } else {
      assert.notEqual(result, 'submitted')
      assert.equal(context.filled, '')
      assert.equal(context.clicked, 0)
    }
  })
  test(`Google login diagnostics remain read-only and contain only a reason code: ${mode}`, () => {
    const { context, action } = page(mode)
    assert.match(action('diagnose'), /^diag_[a-z_]+$/)
    assert.equal(context.clicked, 0)
    assert.equal(context.filled, '')
  })
}

test('Google TOTP resumes when a partially rendered challenge becomes ready', () => {
  const { context, action } = page('loading')
  assert.equal(action('inspect'), 'totp_loading')
  assert.equal(action('totp'), 'totp_loading')
  assert.equal(context.clicked, 0)
  context.mode = 'valid'
  assert.equal(action('inspect'), 'totp')
  assert.equal(action('totp'), 'submitted')
  assert.equal(context.clicked, 1)
  assert.equal(action('inspect'), 'totp_pending')
})

test('Google TOTP waits for its submit button without filling prematurely', () => {
  const { context, action } = page('next_loading')
  assert.equal(action('inspect'), 'totp_loading')
  assert.equal(action('totp'), 'totp_loading')
  assert.equal(context.filled, '')
  vm.runInContext(`next.disabled = false; mode = 'valid'`, context)
  assert.equal(action('totp'), 'submitted')
  assert.equal(context.clicked, 1)
})

test('Google TOTP selects the unique Authenticator method once without sending a code', () => {
  const { context, action } = page('selection')
  assert.equal(action('inspect'), 'totp_select')
  assert.equal(action('totp_select'), 'submitted')
  assert.equal(context.clicked, 1)
  assert.equal(context.filled, '')
  assert.equal(action('inspect'), 'totp_loading')
  assert.equal(action('totp_select'), 'totp_loading')
  assert.equal(context.clicked, 1)
  context.mode = 'valid'
  vm.runInContext(`location.pathname = '/v3/signin/challenge/totp'`, context)
  assert.equal(action('inspect'), 'totp')
  assert.equal(action('totp'), 'submitted')
  assert.equal(context.clicked, 2)
})

for (const mode of [
  'selection_wrong_email',
  'selection_no_email',
  'selection_ambiguous',
  'selection_missing',
  'selection_disabled',
]) {
  test(`Google TOTP refuses unsafe method selection: ${mode}`, () => {
    const { context, action } = page(mode)
    assert.notEqual(action('totp_select'), 'submitted')
    assert.equal(context.clicked, 0)
    assert.equal(context.filled, '')
  })
}

for (const mode of [
  'selection_stale_password',
  'selection_stale_identifier',
  'totp_stale_password',
  'totp_stale_identifier',
]) {
  test(`Google waits for the previous credential form to disappear: ${mode}`, () => {
    const { context, action } = page(mode)
    const selecting = mode.startsWith('selection')
    const kind = selecting ? 'totp_select' : 'totp'
    assert.equal(action('inspect'), 'totp_loading')
    assert.equal(action(kind), 'totp_loading')
    assert.equal(context.clicked, 0)
    assert.equal(context.filled, '')
    context.mode = selecting ? 'selection' : 'valid'
    assert.equal(action('inspect'), kind)
    assert.equal(action(kind), 'submitted')
    assert.equal(context.clicked, 1)
    assert.equal(context.filled, selecting ? '' : '005924')
  })
}

for (const change of [
  "location.origin = 'https://example.test'",
  'window.__aistudioLoginManual = true',
]) {
  test(`Google TOTP rechecks origin and takeover before choosing a method: ${change}`, () => {
    const { context, action } = page('selection')
    assert.equal(action('inspect'), 'totp_select')
    vm.runInContext(change, context)
    assert.notEqual(action('totp_select'), 'submitted')
    assert.equal(context.clicked, 0)
  })
}
