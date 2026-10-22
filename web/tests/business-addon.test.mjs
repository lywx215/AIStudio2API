import assert from 'node:assert/strict'
import { Buffer } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import { setImmediate } from 'node:timers/promises'
import { test } from 'node:test'
import { URL } from 'node:url'
import * as vue from 'vue'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { sourceModule } from './source-module.mjs'

test('Business client uses shared console requests and preserves config revision', async () => {
  const calls = []
  const { businessClient } = await sourceModule(
    new URL('../src/business/client.ts', import.meta.url),
    {
      '@/api': {
        requestJSON: async (...args) => {
          calls.push(args)
          return {}
        },
      },
    },
  )
  const controller = new AbortController()
  await businessClient.config(controller.signal)
  await businessClient.status(controller.signal)
  await businessClient.save({ revision: 7, bindings: [] }, controller.signal)
  await businessClient.collectAccount(
    { revision: 4, id: 'account-1', label: 'Fixture', executable_path: '/fixture/browser' },
    controller.signal,
  )
  const body = '{"contents":[],"unknown":{"keep":"verbatim"}}'
  await businessClient.tryGenerate('business-test/name', body, controller.signal)
  assert.deepEqual(
    calls.map(([path]) => path),
    [
      '/api/addons/business/config',
      '/api/addons/business/status',
      '/api/addons/business/config',
      '/api/addons/business/collect',
      '/api/addons/business/try/models/business-test%2Fname:generateContent',
    ],
  )
  assert.equal(calls[2][1].method, 'PUT')
  assert.deepEqual(JSON.parse(calls[2][1].body), { revision: 7, bindings: [] })
  assert.deepEqual(JSON.parse(calls[3][1].body), {
    revision: 4,
    id: 'account-1',
    label: 'Fixture',
    executable_path: '/fixture/browser',
  })
  assert.equal(calls[4][1].body, body)
  assert.equal(calls[4][1].method, 'POST')
  assert.ok(calls.every(([, options]) => options.signal === controller.signal && !options.headers))
})

const fixtureConfig = () => ({
  schema_version: 1,
  revision: 0,
  enabled: false,
  max_concurrent: 2,
  request_timeout_seconds: 120,
  bindings: [
    {
      public_id: 'business-test',
      display_name: 'Synthetic model',
      selector_mode: 'unresolved',
      upstream_model_id: '',
      enabled: false,
      verification_status: 'unverified',
      evidence: '',
    },
  ],
})
const fixtureStatus = () => ({
  state: 'disabled',
  enabled: false,
  generation_ready: false,
  config_revision: 0,
  capabilities: { text: 'implemented', files: 'implemented', tools: 'unsupported' },
  live_verified: false,
  account_states: [],
  model_states: [],
  capacity: { limit: 2, active: 0, available: 2 },
})
const fixtureModelState = (overrides = {}) => ({
  public_id: 'business-test',
  state: 'ready',
  configuration_valid: true,
  schedulable: true,
  auth_verified: false,
  model_verified: false,
  eligible_account_ids: ['synthetic-account'],
  ...overrides,
})
class FixtureAPIError extends Error {
  constructor(status, code = '', message = 'Synthetic failure') {
    super(message)
    this.status = status
    this.code = code
  }
}

async function mountPanel(overrides = {}, componentName = 'BusinessPanel', props = {}) {
  const calls = { saves: [], signals: [], collections: [], trials: [] }
  const client = {
    config: async (signal) => {
      calls.signals.push(signal)
      return fixtureConfig()
    },
    status: async (signal) => {
      calls.signals.push(signal)
      return fixtureStatus()
    },
    save: async (config, signal) => {
      calls.saves.push(structuredClone(vue.toRaw(config)))
      calls.signals.push(signal)
      return { ...config, revision: config.revision + 1 }
    },
    accounts: async (signal) => {
      calls.signals.push(signal)
      return { revision: 0, accounts: [] }
    },
    saveAccount: async (input, signal) => {
      calls.saves.push(input)
      calls.signals.push(signal)
      return { revision: 1, accounts: [] }
    },
    collectAccount: async (input, signal) => {
      calls.collections.push(input)
      calls.signals.push(signal)
      return { revision: input.revision + 1, accounts: [] }
    },
    tryGenerate: async (id, body, signal) => {
      calls.trials.push({ id, body })
      calls.signals.push(signal)
      return { candidates: [], unknown: { preserved: true } }
    },
    ...overrides,
  }
  const source = await readFile(
    new URL(`../src/business/${componentName}.vue`, import.meta.url),
    'utf8',
  )
  const compiled = compileScript(parse(source).descriptor, {
    id: 'business-test',
    inlineTemplate: true,
  })
  const registry = `__business_panel_${Math.random().toString(36).slice(2)}`
  const modules = {
    vue: { ...vue, withDirectives: (node) => node },
    '@/api': { ApiError: FixtureAPIError },
    '@/i18n': { useI18n: () => ({ locale: vue.ref('zh-CN') }) },
    './client': { businessClient: client },
    './BusinessAccounts.vue': {
      default: vue.defineComponent({
        setup: () => () => vue.h('section', 'Account panel fixture'),
      }),
    },
    './BusinessTry.vue': {
      default: vue.defineComponent({
        props: ['bindings', 'disabled'],
        emits: ['completed'],
        setup:
          (props, { emit }) =>
          () =>
            vue.h('section', {
              'data-trial-bindings': JSON.stringify(props.bindings),
              onClick: () => emit('completed'),
            }),
      }),
    },
  }
  globalThis[registry] = modules
  const oldWindow = globalThis.window
  const oldDocument = globalThis.document
  const listeners = new Map()
  const documentListeners = new Map()
  globalThis.window = {
    addEventListener: (event, handler) => listeners.set(event, handler),
    removeEventListener: (event) => listeners.delete(event),
  }
  globalThis.document = {
    visibilityState: 'visible',
    addEventListener: (event, handler) => documentListeners.set(event, handler),
    removeEventListener: (event) => documentListeners.delete(event),
  }
  const output = ts.transpileModule(compiled.content, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText
  const code = output.replace(/from (["'])([^"']+)\1/g, (_match, _quote, specifier) => {
    assert.ok(modules[specifier], `Unexpected dependency: ${specifier}`)
    const exports = Object.keys(modules[specifier])
      .filter((key) => key !== 'default' && /^[A-Za-z_$][\w$]*$/.test(key))
      .map(
        (key) =>
          `export const ${key}=globalThis[${JSON.stringify(registry)}][${JSON.stringify(specifier)}][${JSON.stringify(key)}];`,
      )
      .join('')
    const defaultExport =
      'default' in modules[specifier]
        ? `export default globalThis[${JSON.stringify(registry)}][${JSON.stringify(specifier)}].default;`
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
  const app = renderer.createApp(component, props)
  app.mount(root)
  const flush = async () => {
    await setImmediate()
    await vue.nextTick()
  }
  await flush()
  const nodes = (target = root) => [target, ...target.children.flatMap(nodes)]
  const content = (target = root) => target.text + target.children.map(content).join('')
  return {
    calls,
    listeners,
    documentListeners,
    nodes,
    content,
    flush,
    button: (label) =>
      nodes().find((target) => target.tag === 'button' && content(target).trim() === label),
    submit: async () => {
      await nodes()
        .find((target) => target.tag === 'form')
        .props.onSubmit({ preventDefault() {} })
      await flush()
    },
    close: () => {
      app.unmount()
      delete globalThis[registry]
      globalThis.window = oldWindow
      globalThis.document = oldDocument
    },
  }
}

test('Business panel renders real readiness and disables unresolved model bindings', async () => {
  const panel = await mountPanel()
  try {
    assert.ok(panel.content().includes('implemented'))
    assert.ok(panel.content().includes('filesimplemented'))
    assert.ok(panel.content().includes('待验证'))
    assert.ok(!panel.content().includes('尚未接通'))
    assert.ok(panel.content().includes('Synthetic model'))
    const checkboxes = panel
      .nodes()
      .filter((node) => node.tag === 'input' && node.props.type === 'checkbox')
    assert.equal(checkboxes[1].props.disabled, true)
    await panel.submit()
    assert.equal(panel.calls.saves.length, 1)
    assert.equal(panel.calls.saves[0].revision, 0)
    assert.ok(panel.content().includes('配置已保存'))
  } finally {
    panel.close()
  }
})

function modelDimensions(panel, id = 'business-test') {
  const article = panel.nodes().find((node) => node.props['data-business-model'] === id)
  assert.ok(article, `Missing model status: ${id}`)
  const dimension = (name) => {
    const node = panel.nodes(article).find((node) => node.props['data-business-dimension'] === name)
    assert.ok(node, `Missing status dimension: ${name}`)
    return panel.content(node)
  }
  return { article, dimension }
}

test('model configuration and scheduling do not imply authentication or model verification', async () => {
  const panel = await mountPanel({
    status: async () => ({
      ...fixtureStatus(),
      enabled: true,
      state: 'ready',
      generation_ready: true,
      model_states: [fixtureModelState()],
    }),
  })
  try {
    const { dimension } = modelDimensions(panel)
    assert.ok(dimension('configuration').includes('有效'))
    assert.ok(dimension('scheduling').includes('可立即调度'))
    assert.ok(dimension('authentication').includes('当前无有效认证'))
    assert.ok(dimension('model-call').includes('尚无成功记录'))
    assert.match(panel.content(), /有模型可立即调度:\s*是/)
    assert.match(panel.content(), /完整在线验收:\s*待验证/)
    assert.equal(panel.calls.trials.length, 0)
    assert.equal(panel.calls.collections.length, 0)
    assert.equal(panel.calls.saves.length, 0)
  } finally {
    panel.close()
  }
})

test('busy cooldown draining and capacity preserve evidence without claiming schedulability', async () => {
  for (const [code, reason] of [
    ['BUSINESS_ACCOUNT_BUSY', '账号忙碌'],
    ['BUSINESS_ACCOUNT_COOLDOWN', '账号冷却中'],
    ['BUSINESS_ACCOUNT_DRAINING', '旧请求正在结束'],
    ['BUSINESS_CAPACITY_FULL', '并发已满'],
    ['BUSINESS_RUNTIME_SUSPENDED', 'Business 已暂停'],
  ]) {
    const full = code === 'BUSINESS_CAPACITY_FULL'
    const suspended = code === 'BUSINESS_RUNTIME_SUSPENDED'
    const panel = await mountPanel({
      status: async () => ({
        ...fixtureStatus(),
        enabled: true,
        state: 'unavailable',
        capacity: { limit: 2, active: full ? 2 : 1, available: full || suspended ? 0 : 1 },
        model_states: [
          fixtureModelState({
            state: 'unavailable',
            code,
            schedulable: false,
            auth_verified: true,
            model_verified: true,
            auth_verified_at: '2026-10-22T01:00:00Z',
            model_verified_at: '2026-10-22T01:02:00Z',
          }),
        ],
      }),
    })
    try {
      const { article, dimension } = modelDimensions(panel)
      assert.ok(panel.content(article).includes(reason))
      assert.ok(dimension('configuration').includes('有效'))
      assert.ok(dimension('scheduling').includes('不可立即调度'))
      assert.ok(dimension('authentication').includes('当前认证有效'))
      assert.ok(dimension('model-call').includes('已有成功记录'))
      assert.match(panel.content(), /有模型可立即调度:\s*否/)
      assert.match(panel.content(), /完整在线验收:\s*待验证/)
      const times = panel.nodes(article).filter((node) => node.tag === 'time')
      assert.deepEqual(
        times.map((node) => node.props.datetime),
        ['2026-10-22T01:00:00Z', '2026-10-22T01:02:00Z'],
      )
      if (full)
        assert.match(
          panel.content(panel.nodes().find((node) => 'data-business-capacity' in node.props)),
          /2 \/ 2 已占用 · 可用 0/,
        )
    } finally {
      panel.close()
    }
  }
})

test('expired authentication retains its historical success time separately from current validity', async () => {
  const panel = await mountPanel({
    status: async () => ({
      ...fixtureStatus(),
      enabled: true,
      model_states: [
        fixtureModelState({
          state: 'unavailable',
          code: 'BUSINESS_AUTH_REQUIRED',
          schedulable: false,
          auth_verified: false,
          auth_verified_at: '2026-10-22T01:00:00Z',
          model_verified: true,
          model_verified_at: '2026-10-22T01:02:00Z',
        }),
      ],
    }),
  })
  try {
    const { article, dimension } = modelDimensions(panel)
    assert.ok(dimension('authentication').includes('当前无有效认证'))
    assert.ok(dimension('authentication').includes('最近成功'))
    assert.ok(dimension('model-call').includes('已有成功记录'))
    assert.ok(panel.content(article).includes('账号需要重新认证'))
    assert.equal(panel.nodes(article).filter((node) => node.tag === 'time').length, 2)
  } finally {
    panel.close()
  }
})

test('model verification is scoped to each binding and empty account scope remains unavailable', async () => {
  const panel = await mountPanel({
    status: async () => ({
      ...fixtureStatus(),
      enabled: true,
      state: 'ready',
      generation_ready: true,
      model_states: [
        fixtureModelState({ auth_verified: true, model_verified: true }),
        fixtureModelState({
          public_id: 'business-other',
          state: 'unavailable',
          code: 'BUSINESS_ACCOUNT_SCOPE_EMPTY',
          schedulable: false,
          eligible_account_ids: [],
        }),
      ],
    }),
  })
  try {
    const { dimension } = modelDimensions(panel, 'business-other')
    assert.ok(dimension('configuration').includes('有效'))
    assert.ok(dimension('scheduling').includes('不可立即调度'))
    assert.ok(dimension('authentication').includes('当前无有效认证'))
    assert.ok(dimension('model-call').includes('尚无成功记录'))
    assert.ok(panel.content().includes('绑定范围内没有账号'))
  } finally {
    panel.close()
  }
})

test('saved model configuration can remain valid while Business is disabled', async () => {
  const panel = await mountPanel({
    status: async () => ({
      ...fixtureStatus(),
      model_states: [
        fixtureModelState({
          state: 'disabled',
          code: 'BUSINESS_DISABLED',
          schedulable: false,
        }),
      ],
    }),
  })
  try {
    const { dimension } = modelDimensions(panel)
    assert.ok(dimension('configuration').includes('有效'))
    assert.ok(dimension('scheduling').includes('不可立即调度'))
    assert.match(panel.content(), /有模型可立即调度:\s*否/)
  } finally {
    panel.close()
  }
})

test('an older status reply cannot overwrite a newer post-trial verification record', async () => {
  let finishOld,
    reads = 0
  const initial = { ...fixtureStatus(), enabled: true, model_states: [fixtureModelState()] }
  const fresh = {
    ...initial,
    model_states: [
      fixtureModelState({ model_verified: true, model_verified_at: '2026-10-22T01:02:00Z' }),
    ],
  }
  const panel = await mountPanel({
    status: () => {
      reads++
      if (reads === 2)
        return new Promise((resolve) => {
          finishOld = resolve
        })
      return Promise.resolve(reads > 2 ? fresh : initial)
    },
  })
  try {
    const trial = panel.nodes().find((node) => node.props['data-trial-bindings'])
    trial.props.onClick()
    await panel.flush()
    trial.props.onClick()
    await panel.flush()
    assert.ok(modelDimensions(panel).dimension('model-call').includes('已有成功记录'))
    finishOld(initial)
    await panel.flush()
    assert.ok(modelDimensions(panel).dimension('model-call').includes('已有成功记录'))
    assert.equal(reads, 3)
  } finally {
    panel.close()
  }
})

test('Business trial choices use saved bindings rather than unsaved edits', async () => {
  const config = fixtureConfig()
  config.enabled = true
  config.bindings[0].selector_mode = 'exact'
  config.bindings[0].upstream_model_id = 'synthetic'
  config.bindings[0].evidence = 'Synthetic evidence'
  config.bindings[0].verification_status = 'verified'
  const panel = await mountPanel({ config: async () => config })
  const trialBindings = () =>
    JSON.parse(
      panel.nodes().find((node) => node.props['data-trial-bindings'])?.props['data-trial-bindings'],
    )
  try {
    const enabled = panel
      .nodes()
      .filter((node) => node.tag === 'input' && node.props.type === 'checkbox')[1]
    enabled.props['onUpdate:modelValue'](true)
    await panel.flush()
    assert.equal(trialBindings()[0].enabled, false)
    await panel.submit()
    assert.equal(trialBindings()[0].enabled, true)
  } finally {
    panel.close()
  }
})

test('Business panel requires readback after uncertain save and prevents double submission', async () => {
  let saves = 0
  const panel = await mountPanel({
    save: async () => {
      saves++
      throw new Error('lost response')
    },
  })
  try {
    await panel.submit()
    assert.equal(panel.button('保存 Business 配置').props.disabled, true)
    await panel.submit()
    assert.equal(saves, 1)
    await panel.button('刷新').props.onClick()
    await panel.flush()
    assert.equal(panel.button('保存 Business 配置').props.disabled, false)
  } finally {
    panel.close()
  }
})

test('Business panel aborts in-flight loads and ignores late replies after navigation', async () => {
  let finish, signal
  const panel = await mountPanel({
    config: (value) => {
      signal = value
      return new Promise((resolve) => {
        finish = resolve
      })
    },
  })
  panel.close()
  assert.equal(signal.aborted, true)
  finish(fixtureConfig())
  await panel.flush()
  assert.equal(panel.content(), '')
})

test('Business panel ignores an old load after a persisted page is restored', async () => {
  let finish,
    reads = 0
  const fresh = fixtureConfig()
  fresh.bindings[0].display_name = 'Restored binding'
  const panel = await mountPanel({
    config: () => {
      reads++
      if (reads > 1) return Promise.resolve(fresh)
      return new Promise((resolve) => {
        finish = resolve
      })
    },
  })
  try {
    panel.listeners.get('pagehide')()
    panel.listeners.get('pageshow')({ persisted: true })
    await panel.flush()
    assert.ok(panel.content().includes('Restored binding'))
    finish(fixtureConfig())
    await panel.flush()
    assert.ok(panel.content().includes('Restored binding'))
    assert.ok(!panel.content().includes('Synthetic model'))
  } finally {
    panel.close()
  }
})

test('Business accounts submit credentials only to the account endpoint and abort on pagehide', async () => {
  const panel = await mountPanel({}, 'BusinessAccounts')
  try {
    panel.button('添加账号').props.onClick()
    await panel.flush()
    const inputs = panel.nodes().filter((node) => node.tag === 'input')
    for (const [index, value] of [
      'Fixture',
      'config-id',
      'synthetic-cookie',
      'synthetic-host',
      'index',
      'Agent',
    ].entries())
      inputs[index].props['onUpdate:modelValue'](value)
    await panel.submit()
    assert.equal(panel.calls.saves.length, 1)
    assert.equal(panel.calls.saves[0].credential.secure_c_ses, 'synthetic-cookie')
    assert.equal(panel.calls.saves[0].enabled, false)
    assert.ok(!panel.content().includes('synthetic-cookie'))
    panel.listeners.get('pagehide')()
    assert.ok(panel.calls.signals.every((signal) => signal.aborted))
  } finally {
    panel.close()
  }
})

test('editing an account label omits credentials and preserves enabled state and credential version', async () => {
  let saved
  const account = {
    id: 'synthetic-account',
    label: 'Original label',
    enabled: true,
    epoch: 7,
    credential_set: true,
  }
  const panel = await mountPanel(
    {
      accounts: async () => ({ revision: 6, accounts: [account] }),
      saveAccount: async (input) => {
        saved = structuredClone(input)
        return { revision: 7, accounts: [{ ...account, label: input.label }] }
      },
    },
    'BusinessAccounts',
  )
  try {
    panel.button('编辑标签').props.onClick()
    await panel.flush()
    const inputs = panel.nodes().filter((node) => node.tag === 'input')
    assert.equal(inputs.length, 1)
    assert.ok(panel.content().includes('仅修改账号标签'))
    inputs[0].props['onUpdate:modelValue']('Renamed account')
    await panel.submit()
    assert.deepEqual(saved, {
      revision: 6,
      id: 'synthetic-account',
      label: 'Renamed account',
      enabled: true,
    })
    assert.equal('credential' in saved, false)
    assert.ok(panel.content().includes('Renamed account'))
    assert.match(panel.content(), /凭据版本\s*7/)
    assert.ok(panel.content().includes('账号标签已保存，凭据未更新'))
    assert.ok(!panel.content().includes('账号凭据已保存'))
    assert.equal(panel.calls.collections.length, 0)
    assert.equal(panel.calls.trials.length, 0)
  } finally {
    panel.close()
  }
})

test('enabling an account only sends metadata without resubmitting credentials', async () => {
  const panel = await mountPanel(
    {
      accounts: async () => ({
        revision: 4,
        accounts: [
          {
            id: 'synthetic-account',
            label: 'Existing',
            enabled: false,
            epoch: 2,
            credential_set: true,
          },
        ],
      }),
    },
    'BusinessAccounts',
  )
  try {
    panel.button('启用').props.onClick()
    await panel.flush()
    assert.deepEqual(panel.calls.saves, [
      { revision: 4, id: 'synthetic-account', label: 'Existing', enabled: true },
    ])
    assert.ok(!panel.content().includes('账号凭据已保存'))
  } finally {
    panel.close()
  }
})

test('visible collection requires an explicit submit and preserves the frozen account contract', async () => {
  const panel = await mountPanel(
    {
      accounts: async () => ({
        revision: 6,
        accounts: [{ id: 'fixture-id', label: 'Existing', enabled: true, epoch: 2 }],
      }),
    },
    'BusinessAccounts',
  )
  try {
    panel.button('浏览器替换').props.onClick()
    await panel.flush()
    assert.equal(panel.calls.collections.length, 0)
    assert.ok(panel.content().includes('服务端桌面'))
    assert.ok(panel.content().includes('远程无头交互尚未实现'))
    assert.ok(panel.content().includes('默认停用'))
    const inputs = panel.nodes().filter((node) => node.tag === 'input')
    inputs[0].props['onUpdate:modelValue']('Replacement')
    inputs[1].props['onUpdate:modelValue']('relative/browser')
    await panel.submit()
    assert.equal(panel.calls.collections.length, 0)
    inputs[1].props['onUpdate:modelValue']('C:\\Fixture\\browser.exe')
    await panel.submit()
    assert.deepEqual(panel.calls.collections, [
      {
        revision: 6,
        id: 'fixture-id',
        label: 'Replacement',
        executable_path: 'C:\\Fixture\\browser.exe',
      },
    ])
    assert.equal(panel.calls.saves.length, 0)
  } finally {
    panel.close()
  }
})

test('canceling collection aborts its request and ignores late results after a refresh', async () => {
  let finish, signal, request
  const panel = await mountPanel(
    {
      collectAccount: (input, value) => {
        request = input
        signal = value
        return new Promise((resolve) => {
          finish = resolve
        })
      },
    },
    'BusinessAccounts',
  )
  try {
    panel.button('浏览器采集').props.onClick()
    await panel.flush()
    const inputs = panel.nodes().filter((node) => node.tag === 'input')
    inputs[0].props['onUpdate:modelValue']('New fixture')
    inputs[1].props['onUpdate:modelValue']('/fixture/browser')
    const sending = panel.submit()
    await panel.flush()
    assert.deepEqual(request, {
      revision: 0,
      label: 'New fixture',
      executable_path: '/fixture/browser',
    })
    panel.button('取消采集').props.onClick()
    await panel.flush()
    assert.equal(signal.aborted, true)
    assert.equal(panel.button('浏览器采集').props.disabled, true)
    await panel.button('刷新账号').props.onClick()
    await panel.flush()
    assert.equal(panel.button('浏览器采集').props.disabled, false)
    finish({ revision: 99, accounts: [{ id: 'late', label: 'Late credential', enabled: true }] })
    await sending
    assert.ok(!panel.content().includes('Late credential'))
  } finally {
    panel.close()
  }
})

test('pagehide and session expiry abort collection and discard its late credential response', async () => {
  for (const event of ['pagehide', 'admin-session-expired']) {
    let finish, signal
    const panel = await mountPanel(
      {
        collectAccount: (_input, value) => {
          signal = value
          return new Promise((resolve) => {
            finish = resolve
          })
        },
      },
      'BusinessAccounts',
    )
    try {
      panel.button('浏览器采集').props.onClick()
      await panel.flush()
      const inputs = panel.nodes().filter((node) => node.tag === 'input')
      inputs[0].props['onUpdate:modelValue']('Fixture')
      inputs[1].props['onUpdate:modelValue']('/fixture/browser')
      const sending = panel.submit()
      await panel.flush()
      panel.listeners.get(event)()
      assert.equal(signal.aborted, true)
      finish({ revision: 99, accounts: [{ id: 'late', label: 'Late credential', enabled: true }] })
      await sending
      assert.ok(!panel.content().includes('Late credential'))
      assert.equal(
        panel.nodes().some((node) => node.tag === 'form'),
        false,
      )
    } finally {
      panel.close()
    }
  }
})

test('hiding the document clears an unsent manual credential draft', async () => {
  const panel = await mountPanel({}, 'BusinessAccounts')
  try {
    panel.button('添加账号').props.onClick()
    await panel.flush()
    panel
      .nodes()
      .filter((node) => node.tag === 'input')[2]
      .props['onUpdate:modelValue']('synthetic-secret')
    globalThis.document.visibilityState = 'hidden'
    panel.documentListeners.get('visibilitychange')()
    await panel.flush()
    assert.equal(
      panel.nodes().some((node) => node.tag === 'form'),
      false,
    )
    assert.equal(panel.calls.saves.length, 0)
    panel.button('添加账号').props.onClick()
    await panel.flush()
    await panel.submit()
    assert.equal(panel.calls.saves[0].credential.secure_c_ses, '')
  } finally {
    panel.close()
  }
})

const trialProps = () => ({
  disabled: false,
  bindings: [
    { ...fixtureConfig().bindings[0], enabled: true, verification_status: 'verified' },
    {
      ...fixtureConfig().bindings[0],
      public_id: 'business-disabled',
      enabled: false,
      verification_status: 'verified',
    },
    { ...fixtureConfig().bindings[0], public_id: 'business-unverified', enabled: true },
  ],
})

test('native trial is opt-in, filters models and preserves unknown JSON without sampling defaults', async () => {
  const panel = await mountPanel({}, 'BusinessTry', trialProps())
  try {
    assert.equal(panel.calls.trials.length, 0)
    assert.deepEqual(
      panel
        .nodes()
        .filter((node) => node.tag === 'option')
        .map((node) => node.props.value),
      ['', 'business-test'],
    )
    const editor = panel.nodes().find((node) => node.tag === 'textarea')
    panel
      .nodes()
      .find((node) => node.tag === 'select')
      .props['onUpdate:modelValue']('business-test')
    const body = '{ "contents": [], "futureField": { "verbatim": [1, null, "keep"] } }'
    editor.props['onUpdate:modelValue'](body)
    await panel.submit()
    assert.deepEqual(panel.calls.trials, [{ id: 'business-test', body }])
    assert.ok(panel.content().includes('"unknown"'))
    assert.ok(panel.content().includes('"preserved": true'))
    assert.ok(!panel.calls.trials[0].body.includes('temperature'))
  } finally {
    panel.close()
  }
})

test('native trial validates JSON and selected binding before sending', async () => {
  const panel = await mountPanel({}, 'BusinessTry', trialProps())
  try {
    const select = panel.nodes().find((node) => node.tag === 'select')
    const editor = panel.nodes().find((node) => node.tag === 'textarea')
    select.props['onUpdate:modelValue']('business-unverified')
    await panel.submit()
    assert.equal(panel.calls.trials.length, 0)
    select.props['onUpdate:modelValue']('business-test')
    for (const invalid of ['{broken', '[]', 'null', '42']) {
      editor.props['onUpdate:modelValue'](invalid)
      await panel.submit()
      assert.equal(panel.calls.trials.length, 0)
      assert.ok(panel.content().includes('合法的原生 Gemini JSON'))
    }
  } finally {
    panel.close()
  }
})

test('native trial displays shared API errors and blocks sending during a config operation', async () => {
  const disabled = await mountPanel({}, 'BusinessTry', { ...trialProps(), disabled: true })
  try {
    disabled
      .nodes()
      .find((node) => node.tag === 'select')
      .props['onUpdate:modelValue']('business-test')
    await disabled.submit()
    assert.equal(disabled.calls.trials.length, 0)
  } finally {
    disabled.close()
  }
  const panel = await mountPanel(
    {
      tryGenerate: async () => {
        throw new FixtureAPIError(400, 'INVALID_ARGUMENT', 'Synthetic unknown field')
      },
    },
    'BusinessTry',
    trialProps(),
  )
  try {
    panel
      .nodes()
      .find((node) => node.tag === 'select')
      .props['onUpdate:modelValue']('business-test')
    await panel.submit()
    assert.ok(panel.content().includes('400 INVALID_ARGUMENT: Synthetic unknown field'))
  } finally {
    panel.close()
  }
})

test('native trial completion refreshes status after success or failure without an automatic model call', async () => {
  for (const fails of [false, true]) {
    let completed = 0
    const panel = await mountPanel(
      {
        tryGenerate: async () => {
          if (fails)
            throw new FixtureAPIError(503, 'UNAVAILABLE', 'Synthetic authentication failure')
          return { candidates: [] }
        },
      },
      'BusinessTry',
      {
        ...trialProps(),
        onCompleted: () => {
          completed++
        },
      },
    )
    try {
      assert.equal(completed, 0)
      panel
        .nodes()
        .find((node) => node.tag === 'select')
        .props['onUpdate:modelValue']('business-test')
      await panel.submit()
      assert.equal(completed, 1)
    } finally {
      panel.close()
    }
  }
})

test('native trial cancels and ignores an old result when another request completes', async () => {
  let finish,
    firstSignal,
    requests = 0
  const panel = await mountPanel(
    {
      tryGenerate: (_id, _body, signal) => {
        requests++
        if (requests > 1) return Promise.resolve({ fresh: true })
        firstSignal = signal
        return new Promise((resolve) => {
          finish = resolve
        })
      },
    },
    'BusinessTry',
    trialProps(),
  )
  try {
    panel
      .nodes()
      .find((node) => node.tag === 'select')
      .props['onUpdate:modelValue']('business-test')
    const sending = panel.submit()
    await panel.flush()
    await panel.submit()
    assert.equal(requests, 1)
    panel.button('取消请求').props.onClick()
    assert.equal(firstSignal.aborted, true)
    await panel.submit()
    assert.equal(requests, 2)
    finish({ stale: true })
    await sending
    assert.ok(panel.content().includes('"fresh": true'))
    assert.ok(!panel.content().includes('"stale"'))
  } finally {
    panel.close()
  }
})

test('native trial aborts on navigation, pagehide and session expiry without rendering late results', async () => {
  for (const event of ['unmount', 'pagehide', 'admin-session-expired']) {
    let finish, signal
    const panel = await mountPanel(
      {
        tryGenerate: (_id, _body, value) => {
          signal = value
          return new Promise((resolve) => {
            finish = resolve
          })
        },
      },
      'BusinessTry',
      trialProps(),
    )
    let closed = false
    try {
      panel
        .nodes()
        .find((node) => node.tag === 'select')
        .props['onUpdate:modelValue']('business-test')
      const sending = panel.submit()
      await panel.flush()
      if (event === 'unmount') {
        panel.close()
        closed = true
      } else panel.listeners.get(event)()
      assert.equal(signal.aborted, true)
      finish({ late: 'must not render' })
      await sending
      assert.ok(!panel.content().includes('must not render'))
    } finally {
      if (!closed) panel.close()
    }
  }
})
