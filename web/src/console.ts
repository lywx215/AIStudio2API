import { computed, reactive, ref, shallowRef } from 'vue'
import { api, openAdminEvents, type EventConnection } from '@/api'
import { useI18n } from '@/i18n'
import type {
  Account,
  AdminEvent,
  AdminLog,
  Cooldown,
  Model,
  RequestSummary,
  ServiceConfig,
  ServiceStatus,
  UpdateStatus,
} from '@/types'

const { t, errorText } = useI18n()

// status 为生成服务状态，null 表示管理接口不可达
const status = ref<ServiceStatus | null>(null)
const logs = shallowRef<AdminLog[]>([])
const accounts = ref<Account[]>([])
const models = ref<Model[]>([])
const cooldowns = ref<Cooldown[]>([])
const requests = ref<RequestSummary[]>([])
const config = ref<ServiceConfig | null>(null)
const update = ref<UpdateStatus | null>(null)
const startPending = ref(false)
const stopPending = ref(false)
const launchCancellationRequested = ref(false)
const notice = reactive({ message: '', tone: 'success' as 'success' | 'error' })
const loading = reactive({
  accounts: true,
  models: true,
  requests: true,
  cooldowns: true,
  config: true,
})
const errors = reactive({ accounts: '', models: '', requests: '', cooldowns: '', config: '' })
// pendingDelete 为撤销时限内尚未提交的账户删除
const pendingDelete = shallowRef<{ account: Account; timer: number } | null>(null)
const deletingAccounts = ref(new Set<string>())
const visibleAccounts = computed(() =>
  accounts.value.filter(
    (account) =>
      account.id !== pendingDelete.value?.account.id && !deletingAccounts.value.has(account.id),
  ),
)

// serviceState 为界面显示的生成服务状态
const serviceState = computed<'unavailable' | 'running' | 'launching' | 'stopped'>(() => {
  if (status.value === null) return 'unavailable'
  if (status.value.state === 'RUNNING') return 'running'
  if (status.value.state === 'LAUNCHING') return 'launching'
  if (startPending.value && !launchCancellationRequested.value) return 'launching'
  return 'stopped'
})

let eventConnection: EventConnection | undefined
let noticeTimer: number | undefined
let pendingLogs: AdminLog[] = []
let logFlushTimer: number | undefined
let restartTimer: number | undefined
let generation = 0
let active = false

// current rejects callbacks from a stopped console or a previous administrator session.
function current(version: number): boolean {
  return active && version === generation
}

// showNotice 显示一次短暂操作结果
function showNotice(message: string, tone: 'success' | 'error'): void {
  if (!active) return
  notice.message = message
  notice.tone = tone
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer)
  noticeTimer = window.setTimeout(() => {
    notice.message = ''
  }, 3200)
}

async function loadStatus(): Promise<void> {
  const version = generation
  if (!current(version)) return
  try {
    const value = await api.status()
    if (current(version)) status.value = value
  } catch {
    if (current(version)) status.value = null
  }
}

async function loadAccounts(): Promise<void> {
  const version = generation
  if (!current(version)) return
  loading.accounts = accounts.value.length === 0
  errors.accounts = ''
  try {
    const value = await api.accounts()
    if (current(version)) accounts.value = value
  } catch (error) {
    if (current(version)) errors.accounts = errorText(error)
  } finally {
    if (current(version)) loading.accounts = false
  }
}

async function loadModels(): Promise<void> {
  const version = generation
  if (!current(version)) return
  loading.models = models.value.length === 0
  errors.models = ''
  try {
    const value = await api.models()
    if (current(version)) models.value = value
  } catch (error) {
    if (current(version)) errors.models = errorText(error)
  } finally {
    if (current(version)) loading.models = false
  }
}

async function loadCooldowns(): Promise<void> {
  const version = generation
  if (!current(version)) return
  loading.cooldowns = cooldowns.value.length === 0
  errors.cooldowns = ''
  try {
    const value = await api.cooldowns()
    if (current(version)) cooldowns.value = value
  } catch (error) {
    if (current(version)) errors.cooldowns = errorText(error)
  } finally {
    if (current(version)) loading.cooldowns = false
  }
}

async function loadRequests(): Promise<void> {
  const version = generation
  if (!current(version)) return
  loading.requests = requests.value.length === 0
  errors.requests = ''
  try {
    const value = await api.requests()
    if (current(version)) requests.value = value
  } catch (error) {
    if (current(version)) errors.requests = errorText(error)
  } finally {
    if (current(version)) loading.requests = false
  }
}

async function loadConfig(): Promise<void> {
  const version = generation
  if (!current(version)) return
  loading.config = config.value === null
  errors.config = ''
  try {
    const value = await api.config()
    if (current(version)) config.value = value
  } catch (error) {
    if (current(version)) errors.config = errorText(error)
  } finally {
    if (current(version)) loading.config = false
  }
}

// loadAccountData 刷新账户、模型目录与服务状态
async function loadAccountData(): Promise<void> {
  await Promise.all([loadAccounts(), loadModels(), loadStatus()])
}

// loadRequestData 刷新请求列表与冷却
async function loadRequestData(): Promise<void> {
  await Promise.all([loadRequests(), loadCooldowns()])
}

// deleteAccount 隐藏账户并在撤销时限结束后删除，新的删除会先提交上一项
function deleteAccount(account: Account): void {
  if (!active) return
  void commitDelete()
  pendingDelete.value = { account, timer: window.setTimeout(() => void commitDelete(), 6000) }
}

// undoDelete 撤销尚未提交的账户删除
function undoDelete(): void {
  if (pendingDelete.value === null) return
  window.clearTimeout(pendingDelete.value.timer)
  pendingDelete.value = null
}

// commitDelete 立即提交等待中的账户删除，页面关闭时使用 keepalive 请求
async function commitDelete(keepalive = false): Promise<void> {
  const version = generation
  if (!current(version)) return
  const pending = pendingDelete.value
  if (pending === null) return
  window.clearTimeout(pending.timer)
  pendingDelete.value = null
  const id = pending.account.id
  deletingAccounts.value = new Set(deletingAccounts.value).add(id)
  try {
    await api.deleteAccount(id, keepalive)
    if (current(version)) await loadAccountData()
  } catch (error) {
    if (current(version)) showNotice(errorText(error), 'error')
  } finally {
    if (current(version)) {
      const deleting = new Set(deletingAccounts.value)
      deleting.delete(id)
      deletingAccounts.value = deleting
    }
  }
}

// flushDelete 在页面隐藏时提交等待中的删除
function flushDelete(): void {
  void commitDelete(true)
}

async function startService(): Promise<void> {
  const version = generation
  if (!current(version)) return
  startPending.value = true
  launchCancellationRequested.value = false
  try {
    const value = await api.startService()
    if (!current(version)) return
    status.value = value
    if (launchCancellationRequested.value || value.state !== 'RUNNING') return
    showNotice(t('app.start'), 'success')
    await Promise.all([loadAccounts(), loadModels(), loadCooldowns()])
  } catch (error) {
    if (!current(version)) return
    if (!launchCancellationRequested.value) showNotice(errorText(error), 'error')
    await loadStatus()
  } finally {
    if (current(version)) startPending.value = false
  }
}

async function stopService(): Promise<void> {
  const version = generation
  if (!current(version)) return
  launchCancellationRequested.value = serviceState.value === 'launching'
  stopPending.value = true
  try {
    const value = await api.stopService()
    if (!current(version)) return
    status.value = value
    showNotice(t('app.stop'), 'success')
    await Promise.all([loadAccounts(), loadModels(), loadCooldowns(), loadRequests()])
  } catch (error) {
    if (!current(version)) return
    showNotice(errorText(error), 'error')
    await loadStatus()
  } finally {
    if (current(version)) stopPending.value = false
  }
}

function flushLogs(): void {
  logFlushTimer = undefined
  if (pendingLogs.length === 0) return
  logs.value = logs.value.concat(pendingLogs).slice(-2000)
  pendingLogs = []
}

function resetLogs(): void {
  pendingLogs = []
  if (logFlushTimer !== undefined) {
    window.clearTimeout(logFlushTimer)
    logFlushTimer = undefined
  }
  logs.value = []
}

async function clearLogs(): Promise<void> {
  const version = generation
  if (!current(version)) return
  try {
    await api.clearLogs()
    if (current(version)) resetLogs()
  } catch (error) {
    if (current(version)) showNotice(errorText(error), 'error')
  }
}

function replaceByID<T extends { id: string }>(items: T[], incoming: T): void {
  const index = items.findIndex((item) => item.id === incoming.id)
  if (index === -1) items.unshift(incoming)
  else items[index] = incoming
}

// waitForRestart 在线更新后等新进程接管再刷新页面：状态接口返回其他版本或要求重新登录都表示旧进程已退出
function waitForRestart(previous = status.value?.version): void {
  const version = generation
  if (!current(version)) return
  eventConnection?.close()
  if (restartTimer !== undefined) window.clearTimeout(restartTimer)
  restartTimer = window.setTimeout(async () => {
    const response = await fetch('/api/status', { cache: 'no-store' }).catch(() => undefined)
    const current: ServiceStatus | undefined = response?.ok
      ? await response.json().catch(() => undefined)
      : undefined
    if (!active || generation !== version) return
    if (response?.status === 401 || (current !== undefined && current.version !== previous)) {
      window.location.reload()
      return
    }
    waitForRestart(previous)
  }, 1000)
}

function handleAdminEvent(event: AdminEvent): void {
  if (event.type === 'status') {
    status.value = event.data
    return
  }
  if (event.type === 'log') {
    pendingLogs.push(event.data)
    if (logFlushTimer === undefined) logFlushTimer = window.setTimeout(flushLogs, 40)
    return
  }
  if (event.type === 'accounts') {
    accounts.value = event.data.accounts
    return
  }
  if (event.type === 'models') {
    models.value = event.data.models
    return
  }
  if (event.type === 'cooldowns') {
    cooldowns.value = event.data
    return
  }
  replaceByID(requests.value, event.data)
}

// startConsole loads management data once and ignores work from an earlier session.
async function startConsole(): Promise<void> {
  if (active) return
  active = true
  const version = ++generation
  window.addEventListener('pagehide', flushDelete)
  await Promise.all([
    loadStatus(),
    loadAccounts(),
    loadModels(),
    loadCooldowns(),
    loadRequests(),
    loadConfig(),
  ])
  if (!current(version)) return
  api.updateStatus().then(
    (value) => {
      if (current(version)) update.value = value
    },
    () => undefined,
  )
  eventConnection = openAdminEvents(
    (event) => {
      if (current(version)) handleAdminEvent(event)
    },
    () => {
      if (!current(version)) return
      resetLogs()
      requests.value = []
    },
  )
}

// stopConsole clears session-owned data; expired sessions never submit queued writes.
function stopConsole(commitPending = true): void {
  if (active && commitPending) flushDelete()
  active = false
  generation++
  eventConnection?.close()
  eventConnection = undefined
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer)
  if (restartTimer !== undefined) window.clearTimeout(restartTimer)
  noticeTimer = undefined
  restartTimer = undefined
  window.removeEventListener('pagehide', flushDelete)
  if (pendingDelete.value !== null) window.clearTimeout(pendingDelete.value.timer)
  pendingDelete.value = null
  deletingAccounts.value = new Set()
  resetLogs()
  status.value = null
  accounts.value = []
  models.value = []
  cooldowns.value = []
  requests.value = []
  config.value = null
  update.value = null
  startPending.value = false
  stopPending.value = false
  launchCancellationRequested.value = false
  notice.message = ''
  notice.tone = 'success'
  for (const key of Object.keys(loading) as (keyof typeof loading)[]) {
    loading[key] = true
    errors[key] = ''
  }
}

// useConsole 返回管理端共享的服务数据与操作
export function useConsole() {
  return {
    status,
    logs,
    accounts,
    visibleAccounts,
    models,
    cooldowns,
    requests,
    config,
    update,
    loading,
    errors,
    notice,
    pendingDelete,
    serviceState,
    startPending,
    stopPending,
    showNotice,
    loadAccountData,
    loadRequestData,
    deleteAccount,
    undoDelete,
    commitDelete,
    startService,
    stopService,
    clearLogs,
    waitForRestart,
    startConsole,
    stopConsole,
  }
}
