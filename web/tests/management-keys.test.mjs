import assert from 'node:assert/strict'
import { Buffer } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import { setImmediate } from 'node:timers/promises'
import { test } from 'node:test'
import { URL } from 'node:url'
import * as vue from 'vue'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as keys from '../src/managementKeys.ts'
import { managementKeyMessages } from '../src/managementKeyMessages.ts'

const { Response, Headers, AbortController } = globalThis

const requestId = 'req_11111111-1111-4111-8111-111111111111'
const syntheticToken = `mgmt_${'A'.repeat(43)}`
const fixtureKey = (changes = {}) => ({
  id: 'mkey_11111111-1111-4111-8111-111111111111',
  name: 'Synthetic manager',
  scopes: ['accounts:read'],
  createdAt: '2026-10-20T00:00:00.000Z',
  expiresAt: null,
  revokedAt: null,
  ...changes,
})
const envelope = (data, status = 200) =>
  Response.json({ data, requestId }, { status, headers: { 'Cache-Control': 'no-store' } })
const deferred = () => {
  let resolve
  let reject
  const promise = new Promise((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
function fixture(overrides = {}) {
  const calls = { list: 0, create: [], revoke: [] }
  const client = {
    list: async () => {
      calls.list++
      return [fixtureKey()]
    },
    create: async (input) => {
      calls.create.push(input)
      return { key: fixtureKey(), token: syntheticToken }
    },
    revoke: async (id) => {
      calls.revoke.push(id)
      return { id, revoked: true }
    },
    ...overrides,
  }
  let passwordSession = true
  const state = keys.useManagementKeys({
    client,
    passwordSession: () => passwordSession,
    session: async () => ({ enabled: true, authenticated: true }),
  })
  return {
    state,
    calls,
    client,
    setPasswordSession: (value) => {
      passwordSession = value
    },
  }
}

test('scope choices and all three templates exactly match the frozen OpenAPI', async () => {
  const schema = JSON.parse(
    await readFile(new URL('../../docs/management/openapi.json', import.meta.url), 'utf8'),
  )
  assert.deepEqual([...keys.managementScopes], schema.components.schemas.Scope.enum)
  assert.deepEqual(keys.scopeTemplates, schema['x-scope-templates'])
  assert.ok(keys.scopeTemplates.readonly.includes('audit:read'))
  for (const forbidden of ['accounts:export', 'accounts:archive', 'settings:write', 'audit:read']) {
    assert.ok(!keys.scopeTemplates.operator.includes(forbidden))
  }
})

test('HTTP uses only the exact console endpoints, metadata DTOs and same-origin session credentials', async () => {
  const calls = []
  const replies = [
    envelope({ items: [fixtureKey()] }),
    envelope({ key: fixtureKey(), token: syntheticToken }),
    envelope({ id: fixtureKey().id, revoked: true }),
  ]
  const client = keys.createManagementKeyClient(async (path, options) => {
    calls.push({ path, options })
    return replies.shift()
  })
  const signal = new AbortController().signal
  assert.deepEqual(await client.list(signal), [fixtureKey()])
  assert.equal(
    (await client.create({ name: 'Synthetic', scopes: ['audit:read'] }, signal)).token,
    syntheticToken,
  )
  assert.deepEqual(await client.revoke(fixtureKey().id, signal), {
    id: fixtureKey().id,
    revoked: true,
  })
  assert.deepEqual(
    calls.map(({ path, options }) => [path, options.method]),
    [
      ['/api/management-keys', 'GET'],
      ['/api/management-keys', 'POST'],
      [`/api/management-keys/${fixtureKey().id}`, 'DELETE'],
    ],
  )
  for (const { options } of calls) {
    assert.equal(options.credentials, 'same-origin')
    assert.equal(options.redirect, 'error')
    assert.equal(options.cache, 'no-store')
    assert.equal(options.signal, signal)
    const headers = new Headers(options.headers)
    assert.equal(headers.get('Authorization'), null)
    assert.equal(headers.get('X-Requested-With'), 'XMLHttpRequest')
  }
  assert.deepEqual(JSON.parse(calls[1].options.body), { name: 'Synthetic', scopes: ['audit:read'] })
})

test('create serializes custom scope selection and UTC millisecond expiration without extra fields', async () => {
  let body
  const client = keys.createManagementKeyClient(async (_path, options) => {
    body = JSON.parse(options.body)
    return envelope({ key: fixtureKey(), token: syntheticToken })
  })
  const input = {
    name: 'Custom',
    scopes: ['accounts:read', 'tasks:write'],
    expiresAt: '2030-01-02T03:04:05.000Z',
  }
  await client.create(input, new AbortController().signal)
  assert.deepEqual(body, input)
})

test('list refuses secret-bearing metadata and malformed states', async () => {
  for (const changes of [
    { token: syntheticToken },
    { hash: 'synthetic digest' },
    { scopes: ['unknown:scope'] },
    { expiresAt: '2030-01-01T00:00:00Z' },
    { revokedAt: 'not-a-date' },
  ]) {
    const client = keys.createManagementKeyClient(async () =>
      envelope({ items: [fixtureKey(changes)] }),
    )
    await assert.rejects(client.list(new AbortController().signal), { code: 'INVALID_RESPONSE' })
  }
})

test('HTTP failures retain only safe error codes and never reflect response text or credentials', async () => {
  for (const reply of [
    Response.json(
      { error: { code: 'CONSOLE_PASSWORD_REQUIRED', message: syntheticToken }, requestId },
      { status: 403 },
    ),
    new Response(syntheticToken, { status: 500 }),
    Response.json({ error: { code: syntheticToken, message: syntheticToken } }, { status: 500 }),
  ]) {
    const client = keys.createManagementKeyClient(async () => reply)
    await assert.rejects(client.list(new AbortController().signal), (error) => {
      assert.ok(error instanceof keys.ManagementKeyError)
      assert.ok(!JSON.stringify(error).includes(syntheticToken))
      assert.ok(!error.message.includes(syntheticToken))
      return true
    })
  }
})

test('HTML success/redirect-like responses and invalid create tokens cannot become a false success', async () => {
  for (const response of [
    new Response('<html>Sign in</html>'),
    envelope({ key: fixtureKey(), token: 'bad' }),
    envelope({ key: { ...fixtureKey(), token: syntheticToken }, token: syntheticToken }),
  ]) {
    const client = keys.createManagementKeyClient(async () => response)
    await assert.rejects(
      client.create({ name: 'Synthetic', scopes: ['system:read'] }, new AbortController().signal),
      { code: 'INVALID_RESPONSE' },
    )
  }
})

test('expiration is future UTC with exact milliseconds and rejects invalid local dates', () => {
  assert.equal(keys.expiresAtUTC(''), undefined)
  const local = '2030-01-02T03:04'
  assert.equal(keys.expiresAtUTC(local, 0), new Date(local).toISOString())
  assert.match(keys.expiresAtUTC(local, 0), /\.000Z$/)
  for (const invalid of [
    '2030-02-30T03:04',
    '2030-13-01T00:00',
    '2030-01-02',
    'invalid',
    '2000-01-01T00:00',
  ]) {
    assert.throws(() => keys.expiresAtUTC(invalid), { code: 'INVALID_EXPIRY' })
  }
})

test('revocation takes precedence over expiry; expiry changes exactly at its boundary', () => {
  const end = '2030-01-01T00:00:00.000Z'
  const key = fixtureKey({ expiresAt: end })
  assert.equal(keys.keyStatus(key, Date.parse(end) - 1), 'active')
  assert.equal(keys.keyStatus(key, Date.parse(end)), 'expired')
  assert.equal(keys.keyStatus({ ...key, revokedAt: end }, Date.parse(end)), 'revoked')
  assert.equal(keys.keyStatus(fixtureKey(), Date.parse(end)), 'active')
})

test('disabled console login blocks list/create/revoke even for a loopback-authenticated page', async () => {
  const f = fixture()
  f.setPasswordSession(false)
  await f.state.refresh()
  f.state.openCreate()
  await f.state.create({ name: 'Blocked', scopes: ['system:read'] })
  f.state.askRevoke(fixtureKey())
  await f.state.revoke()
  assert.equal(f.state.allowed.value, false)
  assert.equal(f.state.createOpen.value, false)
  assert.equal(f.state.error.value, 'CONSOLE_PASSWORD_REQUIRED')
  assert.deepEqual(f.calls, { list: 0, create: [], revoke: [] })
})

test('a missing/disabled password session returned by the session endpoint prevents list admission', async () => {
  for (const session of [
    { enabled: false, authenticated: true },
    { enabled: true, authenticated: false },
  ]) {
    const f = fixture()
    const state = keys.useManagementKeys({
      client: f.client,
      passwordSession: () => true,
      session: async () => session,
    })
    await state.refresh()
    assert.equal(state.allowed.value, false)
    assert.equal(f.calls.list, 0)
  }
})

test('token is shown once, copied explicitly, cleared on close and absent from refreshed metadata', async () => {
  const f = fixture()
  await f.state.refresh()
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['audit:read'] })
  assert.equal(f.state.token.value, syntheticToken)
  assert.ok(!JSON.stringify(f.state.items.value).includes(syntheticToken))
  let clipboard = ''
  await f.state.copyToken(async (value) => {
    clipboard = value
  })
  assert.equal(clipboard, syntheticToken)
  assert.equal(f.state.copied.value, true)
  f.state.closeToken()
  assert.equal(f.state.token.value, '')
  assert.equal(f.state.copied.value, false)
  await f.state.refresh()
  assert.equal(f.state.token.value, '')
})

test('failed copy keeps the one-time token available and shows a safe manual-copy error', async () => {
  const f = fixture()
  await f.state.refresh()
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  await f.state.copyToken(async () => {
    throw new Error(syntheticToken)
  })
  assert.equal(f.state.error.value, 'COPY_FAILED')
  assert.equal(f.state.token.value, syntheticToken)
  assert.equal(f.state.copied.value, false)
})

test('a copy completion cannot restore state after closing the token', async () => {
  const f = fixture()
  await f.state.refresh()
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  const pending = deferred()
  const work = f.state.copyToken(() => pending.promise)
  f.state.closeToken()
  pending.resolve()
  await work
  assert.equal(f.state.token.value, '')
  assert.equal(f.state.copied.value, false)
})

test('leaving a page clears secrets and prevents a late create response from restoring them', async () => {
  const pending = deferred()
  const f = fixture({ create: () => pending.promise })
  await f.state.refresh()
  f.state.openCreate()
  const work = f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  f.state.dispose()
  pending.resolve({ key: fixtureKey(), token: syntheticToken })
  await work
  assert.equal(f.state.token.value, '')
  assert.deepEqual(f.state.items.value, [])
  assert.equal(f.state.createOpen.value, false)
})

test('pagehide clears token, aborts pending work, and never reopens it on refresh', async () => {
  const f = fixture()
  await f.state.refresh()
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  f.state.clearSensitive()
  await f.state.refresh()
  assert.equal(f.state.token.value, '')
  assert.equal(f.state.createOpen.value, false)
})

test('lost create replies are reported as uncertain and block repeat creation until readback', async () => {
  let calls = 0
  const f = fixture({
    create: async () => {
      calls++
      throw new TypeError('Connection lost')
    },
  })
  await f.state.refresh()
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  assert.equal(f.state.error.value, 'CREATE_UNCERTAIN')
  assert.equal(f.state.writeUncertain.value, true)
  assert.equal(f.state.notice.value, '')
  assert.equal(f.state.token.value, '')
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  assert.equal(calls, 1)
  await f.state.refresh()
  assert.equal(f.state.writeUncertain.value, false)
  f.state.openCreate()
  assert.equal(f.state.createOpen.value, true)
})

test('known invalid create requests remain editable without a false success', async () => {
  const f = fixture({
    create: async () => {
      throw new keys.ManagementKeyError('INVALID_REQUEST', 400)
    },
  })
  await f.state.refresh()
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  assert.equal(f.state.error.value, 'INVALID_REQUEST')
  assert.equal(f.state.createOpen.value, true)
  assert.equal(f.state.writeUncertain.value, false)
  assert.equal(f.state.notice.value, '')
})

test('server 401/403 denies access and clears metadata and one-time token', async () => {
  for (const status of [401, 403]) {
    const f = fixture({
      revoke: async () => {
        throw new keys.ManagementKeyError('CONSOLE_PASSWORD_REQUIRED', status)
      },
    })
    await f.state.refresh()
    f.state.openCreate()
    await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
    f.state.askRevoke(fixtureKey())
    await f.state.revoke()
    assert.equal(f.state.allowed.value, false)
    assert.equal(f.state.token.value, '')
    assert.deepEqual(f.state.items.value, [])
    assert.equal(f.state.revokeTarget.value, null)
  }
})

test('revocation requires confirmation and refreshes only the key list', async () => {
  const f = fixture()
  await f.state.refresh()
  f.state.askRevoke(fixtureKey())
  assert.deepEqual(f.calls.revoke, [])
  await f.state.revoke()
  assert.deepEqual(f.calls.revoke, [fixtureKey().id])
  assert.equal(f.calls.list, 2)
  assert.equal(f.state.notice.value, 'revoked')
  assert.ok(f.state.confirmedRevocations.value.has(fixtureKey().id))
  assert.equal(f.state.revokeTarget.value, null)
})

test('failed revoke preserves error, does not fabricate revokedAt and does not silently retry', async () => {
  let count = 0
  const f = fixture({
    revoke: async () => {
      count++
      throw new TypeError('Lost reply')
    },
  })
  await f.state.refresh()
  f.state.askRevoke(fixtureKey())
  await f.state.revoke()
  assert.equal(f.state.error.value, 'REVOKE_UNCERTAIN')
  assert.equal(f.state.items.value[0].revokedAt, null)
  assert.equal(f.state.confirmedRevocations.value.size, 0)
  assert.equal(f.calls.list, 1)
  assert.equal(count, 1)
})

test('a stale list response cannot overwrite metadata received from create', async () => {
  const pending = deferred()
  const f = fixture()
  await f.state.refresh()
  f.client.list = () => pending.promise
  const read = f.state.refresh()
  await setImmediate()
  f.state.openCreate()
  await f.state.create({ name: 'Synthetic', scopes: ['system:read'] })
  pending.resolve([])
  await read
  assert.equal(f.state.items.value.length, 1)
  assert.equal(f.state.token.value, syntheticToken)
})

// Mount the actual SFC with Vue's custom renderer. Native input directives and the
// existing dialog/icon shells are substituted; no browser or Go server is started.
async function mountPanel({ passwordSession = true, client } = {}) {
  const source = await readFile(
    new URL('../src/components/ManagementKeysPanel.vue', import.meta.url),
    'utf8',
  )
  const descriptor = parse(source).descriptor
  const compiled = compileScript(descriptor, { id: 'g5-panel-test', inlineTemplate: true })
  const registryName = `__g5_panel_${Math.random().toString(36).slice(2)}`
  const listeners = new Map()
  const oldWindow = globalThis.window
  globalThis.window = {
    isSecureContext: true,
    addEventListener: (type, fn) => listeners.set(type, fn),
    removeEventListener: (type) => listeners.delete(type),
  }
  const observed = { calls: [], copied: [] }
  const panelClient = client ?? {
    list: async () => [fixtureKey()],
    create: async (input) => {
      observed.calls.push(input)
      return { key: fixtureKey(), token: syntheticToken }
    },
    revoke: async (id) => ({ id, revoked: true }),
  }
  const modules = {
    vue: { ...vue, withDirectives: (node) => node },
    '@/api': { api: { session: async () => ({ enabled: passwordSession, authenticated: true }) } },
    '@/i18n': { useI18n: () => ({ locale: vue.ref('zh-CN') }) },
    '@/managementKeys': { ...keys, createManagementKeyClient: () => panelClient },
    '@/managementKeyMessages': { managementKeyMessages },
    './DetailDialog.vue': {
      default: vue.defineComponent({
        props: ['open', 'title', 'description'],
        emits: ['close'],
        setup:
          (props, { slots, emit }) =>
          () =>
            props.open
              ? vue.h('dialog', { 'aria-label': props.title }, [
                  vue.h('p', props.description),
                  vue.h('button', { onClick: () => emit('close') }, 'dialog-close'),
                  slots.default?.(),
                ])
              : null,
      }),
    },
    './UiIcon.vue': { default: vue.defineComponent({ setup: () => () => vue.h('i') }) },
  }
  globalThis[registryName] = modules
  const output = ts.transpileModule(compiled.content, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText
  const code = output.replace(/from (["'])([^"']+)\1/g, (_match, _quote, specifier) => {
    assert.ok(modules[specifier], `Unexpected SFC dependency: ${specifier}`)
    const exports = Object.keys(modules[specifier])
      .filter((key) => key !== 'default' && /^[A-Za-z_$][\w$]*$/.test(key))
      .map(
        (key) =>
          `export const ${key}=globalThis[${JSON.stringify(registryName)}][${JSON.stringify(specifier)}][${JSON.stringify(key)}];`,
      )
      .join('')
    const defaultExport =
      'default' in modules[specifier]
        ? `export default globalThis[${JSON.stringify(registryName)}][${JSON.stringify(specifier)}].default;`
        : ''
    return `from ${JSON.stringify(`data:text/javascript;base64,${Buffer.from(exports + defaultExport).toString('base64')}`)}`
  })
  const component = (
    await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`)
  ).default
  const node = (tag, text = '') => ({ tag, text, children: [], props: {}, parent: null })
  const renderer = vue.createRenderer({
    createElement: (tag) => node(tag),
    createText: (text) => node('#text', text),
    createComment: () => node('#comment'),
    setText: (target, value) => {
      target.text = value
    },
    setElementText: (target, value) => {
      target.text = value
      target.children = []
    },
    patchProp: (target, key, _previous, value) => {
      target.props[key] = value
    },
    parentNode: (target) => target.parent,
    nextSibling: (target) =>
      target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
    insert: (target, parent, anchor) => {
      if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
      const index = anchor ? parent.children.indexOf(anchor) : -1
      if (index < 0) parent.children.push(target)
      else parent.children.splice(index, 0, target)
      target.parent = parent
    },
    remove: (target) => {
      if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
      target.parent = null
    },
  })
  const root = node('root')
  const app = renderer.createApp(component, { consolePasswordSession: passwordSession })
  app.mount(root)
  await setImmediate()
  await vue.nextTick()
  const nodes = (target = root) => [target, ...target.children.flatMap(nodes)]
  const content = (target = root) => target.text + target.children.map(content).join('')
  return {
    root,
    nodes,
    content,
    observed,
    listeners,
    button: (text) =>
      nodes().find((target) => target.tag === 'button' && content(target).trim() === text),
    flush: async () => {
      await setImmediate()
      await vue.nextTick()
    },
    close: () => {
      app.unmount()
      delete globalThis[registryName]
      globalThis.window = oldWindow
    },
  }
}

test('actual panel renders disabled-login guidance and prevents opening the create dialog', async () => {
  const panel = await mountPanel({ passwordSession: false })
  try {
    assert.ok(panel.content().includes('请先开启管理登录'))
    assert.equal(panel.button('创建管理 Key').props.disabled, true)
    assert.equal(
      panel.nodes().some((node) => node.tag === 'dialog'),
      false,
    )
  } finally {
    panel.close()
  }
})

test('actual panel creates from the form, displays token once, and clears it when its dialog closes', async () => {
  const panel = await mountPanel()
  try {
    assert.ok(panel.content().includes('Synthetic manager'))
    assert.ok(!panel.content().includes(syntheticToken))
    panel.button('创建管理 Key').props.onClick()
    await panel.flush()
    const nameInput = panel
      .nodes()
      .find((node) => node.tag === 'input' && node.props.autocomplete === 'off')
    nameInput.props['onUpdate:modelValue']('  Browser fixture  ')
    const form = panel.nodes().find((node) => node.tag === 'form')
    await form.props.onSubmit({ preventDefault() {} })
    await panel.flush()
    assert.equal(panel.observed.calls.length, 1)
    assert.equal(panel.observed.calls[0].name, 'Browser fixture')
    assert.deepEqual(panel.observed.calls[0].scopes, [...keys.scopeTemplates.readonly])
    assert.match(panel.observed.calls[0].expiresAt, /\.\d{3}Z$/)
    assert.ok(panel.content().includes(syntheticToken))
    assert.ok(panel.content().includes('仅在本次创建后显示一次'))
    const dialogClose = panel.button('dialog-close')
    dialogClose.props.onClick()
    await panel.flush()
    assert.ok(!panel.content().includes(syntheticToken))
    assert.equal(
      panel.nodes().some((node) => node.tag === 'dialog'),
      false,
    )
  } finally {
    panel.close()
  }
})

test('actual panel asks before revocation and pagehide removes the one-time token', async () => {
  const calls = []
  const panel = await mountPanel({
    client: {
      list: async () => [fixtureKey()],
      create: async () => ({ key: fixtureKey(), token: syntheticToken }),
      revoke: async (id) => {
        calls.push(id)
        return { id, revoked: true }
      },
    },
  })
  try {
    panel.button('撤销').props.onClick()
    await panel.flush()
    assert.equal(calls.length, 0)
    assert.ok(panel.content().includes('撤销后无法恢复'))
    panel.button('取消').props.onClick()
    await panel.flush()
    panel.button('创建管理 Key').props.onClick()
    await panel.flush()
    panel
      .nodes()
      .find((node) => node.tag === 'input' && node.props.autocomplete === 'off')
      .props['onUpdate:modelValue']('Synthetic')
    await panel
      .nodes()
      .find((node) => node.tag === 'form')
      .props.onSubmit({ preventDefault() {} })
    await panel.flush()
    assert.ok(panel.content().includes(syntheticToken))
    panel.listeners.get('pagehide')()
    await panel.flush()
    assert.ok(!panel.content().includes(syntheticToken))
  } finally {
    panel.close()
  }
})

test('actual panel submits custom scopes and a local expiration as UTC milliseconds', async () => {
  const panel = await mountPanel()
  try {
    panel.button('创建管理 Key').props.onClick()
    await panel.flush()
    panel
      .nodes()
      .find((node) => node.tag === 'input' && node.props.autocomplete === 'off')
      .props['onUpdate:modelValue']('Custom fixture')
    const selects = panel.nodes().filter((node) => node.tag === 'select')
    selects[0].props['onUpdate:modelValue']('custom')
    panel
      .nodes()
      .find((node) => node.tag === 'input' && node.props.type === 'checkbox')
      .props['onUpdate:modelValue'](['accounts:read', 'tasks:read'])
    selects[1].props['onUpdate:modelValue']('custom')
    await panel.flush()
    const local = '2030-01-02T03:04'
    panel
      .nodes()
      .find((node) => node.tag === 'input' && node.props.type === 'datetime-local')
      .props['onUpdate:modelValue'](local)
    await panel
      .nodes()
      .find((node) => node.tag === 'form')
      .props.onSubmit({ preventDefault() {} })
    await panel.flush()
    assert.deepEqual(panel.observed.calls[0], {
      name: 'Custom fixture',
      scopes: ['accounts:read', 'tasks:read'],
      expiresAt: new Date(local).toISOString(),
    })
  } finally {
    panel.close()
  }
})

test('actual panel validates an empty scope selection before issuing a create request', async () => {
  const panel = await mountPanel()
  try {
    panel.button('创建管理 Key').props.onClick()
    await panel.flush()
    panel
      .nodes()
      .find((node) => node.tag === 'input' && node.props.autocomplete === 'off')
      .props['onUpdate:modelValue']('Invalid fixture')
    panel
      .nodes()
      .find((node) => node.tag === 'input' && node.props.type === 'checkbox')
      .props['onUpdate:modelValue']([])
    await panel
      .nodes()
      .find((node) => node.tag === 'form')
      .props.onSubmit({ preventDefault() {} })
    await panel.flush()
    assert.equal(panel.observed.calls.length, 0)
    assert.ok(panel.content().includes('至少选择一项权限'))
  } finally {
    panel.close()
  }
})

test('actual panel shows expired/revoked metadata and disables only already revoked keys', async () => {
  const panel = await mountPanel({
    client: {
      list: async () => [
        fixtureKey({ name: 'Expired fixture', expiresAt: '2000-01-01T00:00:00.000Z' }),
        fixtureKey({
          id: 'mkey_22222222-2222-4222-8222-222222222222',
          name: 'Revoked fixture',
          revokedAt: '2000-01-01T00:00:00.000Z',
        }),
      ],
    },
  })
  try {
    assert.ok(panel.content().includes('已过期'))
    assert.ok(panel.content().includes('已撤销'))
    assert.equal(
      panel.nodes().find((node) => node.props['aria-label'] === '撤销 Revoked fixture').props
        .disabled,
      true,
    )
    assert.equal(
      panel.nodes().find((node) => node.props['aria-label'] === '撤销 Expired fixture').props
        .disabled,
      false,
    )
    assert.ok(!panel.content().includes(syntheticToken))
  } finally {
    panel.close()
  }
})
