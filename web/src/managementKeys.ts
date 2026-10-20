import { computed, ref } from 'vue'

// Keep these scopes and templates aligned with docs/management/openapi.json.
export const managementScopes = [
  'system:read',
  'accounts:read',
  'accounts:write',
  'accounts:test',
  'accounts:export',
  'accounts:archive',
  'settings:read',
  'settings:write',
  'usage:read',
  'audit:read',
  'tasks:read',
  'tasks:write',
] as const
export type ManagementScope = (typeof managementScopes)[number]
export const scopeTemplates = {
  readonly: [
    'system:read',
    'accounts:read',
    'settings:read',
    'usage:read',
    'audit:read',
    'tasks:read',
  ],
  operator: [
    'system:read',
    'accounts:read',
    'accounts:write',
    'accounts:test',
    'settings:read',
    'usage:read',
    'tasks:read',
    'tasks:write',
  ],
  admin: [...managementScopes],
} as const satisfies Record<string, readonly ManagementScope[]>
export type ScopeTemplate = keyof typeof scopeTemplates

export interface ManagementKey {
  id: string
  name: string
  scopes: ManagementScope[]
  createdAt: string
  expiresAt: string | null
  revokedAt: string | null
}
export interface KeyCreateRequest {
  name: string
  scopes: ManagementScope[]
  expiresAt?: string
}
export interface KeyCreated {
  key: ManagementKey
  token: string
}
export interface KeyRevoked {
  id: string
  revoked: boolean
}
export interface ManagementKeyClient {
  list(signal: AbortSignal): Promise<ManagementKey[]>
  create(input: KeyCreateRequest, signal: AbortSignal): Promise<KeyCreated>
  revoke(id: string, signal: AbortSignal): Promise<KeyRevoked>
}

export class ManagementKeyError extends Error {
  readonly code: string
  readonly status: number

  constructor(code: string, status = 0) {
    // Never retain an untrusted response body/message: it may contain a token.
    super('Management key request failed')
    this.name = 'ManagementKeyError'
    this.code = code
    this.status = status
  }
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
function invalidResponse(): never {
  throw new ManagementKeyError('INVALID_RESPONSE')
}
function timestamp(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(value) &&
    Number.isFinite(Date.parse(value)) &&
    new Date(value).toISOString() === value
  )
}
function readKey(value: unknown): ManagementKey {
  const fields = ['id', 'name', 'scopes', 'createdAt', 'expiresAt', 'revokedAt']
  if (
    !record(value) ||
    Object.keys(value).some((key) => !fields.includes(key)) ||
    typeof value.id !== 'string' ||
    value.id === '' ||
    typeof value.name !== 'string' ||
    !Array.isArray(value.scopes) ||
    !value.scopes.every((scope) => managementScopes.includes(scope)) ||
    new Set(value.scopes).size !== value.scopes.length ||
    !timestamp(value.createdAt) ||
    !(value.expiresAt === null || timestamp(value.expiresAt)) ||
    !(value.revokedAt === null || timestamp(value.revokedAt))
  ) {
    return invalidResponse()
  }
  // Build the metadata object explicitly. Never copy a server object into UI state.
  return {
    id: value.id,
    name: value.name,
    scopes: [...value.scopes] as ManagementScope[],
    createdAt: value.createdAt,
    expiresAt: value.expiresAt,
    revokedAt: value.revokedAt,
  }
}

const keyPath = '/api/management-keys'
const publicErrorCodes = new Set([
  'CONSOLE_PASSWORD_REQUIRED',
  'UNAUTHORIZED',
  'FORBIDDEN',
  'INVALID_REQUEST',
  'NOT_FOUND',
  'PAYLOAD_TOO_LARGE',
  'AUDIT_PERSISTENCE_FAILED',
  'PERSISTENCE_ERROR',
  'INTERNAL_ERROR',
  'METHOD_NOT_ALLOWED',
  'INVALID_STATE',
])
export function createManagementKeyClient(
  fetcher: typeof fetch = globalThis.fetch,
): ManagementKeyClient {
  async function request(
    method: string,
    path: string,
    signal: AbortSignal,
    body?: KeyCreateRequest,
  ) {
    const response = await fetcher(path, {
      method,
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'error',
      signal,
      headers: {
        Accept: 'application/json',
        'X-Requested-With': 'XMLHttpRequest',
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
    const value: unknown = await response.json().catch(() => null)
    if (!response.ok) {
      const code = record(value) && record(value.error) ? value.error.code : undefined
      throw new ManagementKeyError(
        typeof code === 'string' && publicErrorCodes.has(code) ? code : 'REQUEST_FAILED',
        response.status,
      )
    }
    if (
      !record(value) ||
      Object.keys(value).some((key) => key !== 'data' && key !== 'requestId') ||
      typeof value.requestId !== 'string' ||
      !/^req_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(
        value.requestId,
      ) ||
      !record(value.data)
    )
      return invalidResponse()
    return value.data
  }
  return {
    async list(signal) {
      const data = await request('GET', keyPath, signal)
      if (Object.keys(data).some((key) => key !== 'items') || !Array.isArray(data.items))
        return invalidResponse()
      return data.items.map(readKey)
    },
    async create(input, signal) {
      const data = await request('POST', keyPath, signal, input)
      if (
        Object.keys(data).some((key) => key !== 'key' && key !== 'token') ||
        typeof data.token !== 'string' ||
        !/^mgmt_[A-Za-z0-9_-]{43}$/.test(data.token)
      )
        return invalidResponse()
      return { key: readKey(data.key), token: data.token }
    },
    async revoke(id, signal) {
      const data = await request('DELETE', `${keyPath}/${encodeURIComponent(id)}`, signal)
      if (
        Object.keys(data).some((key) => key !== 'id' && key !== 'revoked') ||
        data.id !== id ||
        data.revoked !== true
      )
        return invalidResponse()
      return { id, revoked: true }
    },
  }
}

export function keyStatus(key: ManagementKey, now = Date.now()): 'active' | 'expired' | 'revoked' {
  if (key.revokedAt !== null) return 'revoked'
  if (key.expiresAt !== null && Date.parse(key.expiresAt) <= now) return 'expired'
  return 'active'
}

// datetime-local is displayed in the browser's local time; wire timestamps are UTC milliseconds.
export function expiresAtUTC(local: string, now = Date.now()): string | undefined {
  if (local === '') return undefined
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(local)
  const date = new Date(local)
  if (
    !match ||
    !Number.isFinite(date.getTime()) ||
    date.getTime() <= now ||
    date.getFullYear() !== Number(match[1]) ||
    date.getMonth() + 1 !== Number(match[2]) ||
    date.getDate() !== Number(match[3]) ||
    date.getHours() !== Number(match[4]) ||
    date.getMinutes() !== Number(match[5]) ||
    date.getSeconds() !== Number(match[6] ?? 0)
  )
    throw new ManagementKeyError('INVALID_EXPIRY')
  return date.toISOString()
}

interface Session {
  enabled: boolean
  authenticated: boolean
}
interface ConsoleOptions {
  client: ManagementKeyClient
  session(): Promise<Session>
  passwordSession(): boolean
}

// The server authorizer is authoritative; this state also prevents disabled-login UI writes.
export function useManagementKeys(options: ConsoleOptions) {
  const items = ref<ManagementKey[]>([])
  const allowed = ref(false)
  const loading = ref(false)
  const creating = ref(false)
  const revoking = ref('')
  const error = ref('')
  const notice = ref('')
  const createOpen = ref(false)
  const revokeTarget = ref<ManagementKey | null>(null)
  const token = ref('')
  const copied = ref(false)
  const confirmedRevocations = ref(new Set<string>())
  const writeUncertain = ref(false)
  let disposed = false
  let revision = 0
  let readController: AbortController | undefined
  let writeController: AbortController | undefined
  const busy = computed(() => creating.value || revoking.value !== '')

  function closeToken() {
    token.value = ''
    copied.value = false
    if (error.value === 'COPY_FAILED') error.value = ''
  }
  function clearSensitive() {
    revision++
    readController?.abort()
    writeController?.abort()
    closeToken()
    createOpen.value = false
    revokeTarget.value = null
    if (creating.value) error.value = 'CREATE_UNCERTAIN'
    if (revoking.value) error.value = 'REVOKE_UNCERTAIN'
    if (creating.value || revoking.value) writeUncertain.value = true
    creating.value = false
    revoking.value = ''
    loading.value = false
  }
  function deny() {
    clearSensitive()
    allowed.value = false
    items.value = []
    error.value = 'CONSOLE_PASSWORD_REQUIRED'
  }
  function failure(cause: unknown, mutation?: 'create' | 'revoke') {
    if (cause instanceof ManagementKeyError && (cause.status === 401 || cause.status === 403)) {
      deny()
      return
    }
    const code = cause instanceof ManagementKeyError ? cause.code : 'REQUEST_FAILED'
    // Network failures, malformed replies and persistence failures leave write outcome unknown.
    const uncertain = !['INVALID_REQUEST', 'NOT_FOUND', 'PAYLOAD_TOO_LARGE'].includes(code)
    error.value = mutation && uncertain ? `${mutation.toUpperCase()}_UNCERTAIN` : code
    if (mutation && uncertain) {
      writeUncertain.value = true
      createOpen.value = false
      revokeTarget.value = null
    }
  }
  async function refresh() {
    if (disposed) return
    if (!options.passwordSession()) {
      deny()
      return
    }
    if (busy.value) return
    readController?.abort()
    const controller = new AbortController()
    readController = controller
    const current = ++revision
    loading.value = true
    error.value = ''
    try {
      const session = await options.session()
      if (disposed || current !== revision) return
      if (!session.enabled || !session.authenticated || !options.passwordSession()) {
        deny()
        return
      }
      allowed.value = true
      const result = await options.client.list(controller.signal)
      if (!disposed && current === revision) {
        items.value = result
        writeUncertain.value = false
      }
    } catch (cause) {
      if (!disposed && current === revision) failure(cause)
    } finally {
      if (current === revision) loading.value = false
    }
  }
  function openCreate() {
    if (!allowed.value || busy.value || writeUncertain.value || !options.passwordSession()) return
    closeToken()
    error.value = ''
    notice.value = ''
    createOpen.value = true
  }
  async function create(input: KeyCreateRequest) {
    if (
      disposed ||
      !allowed.value ||
      !createOpen.value ||
      busy.value ||
      writeUncertain.value ||
      !options.passwordSession()
    )
      return
    readController?.abort()
    const current = ++revision
    const controller = new AbortController()
    writeController = controller
    creating.value = true
    loading.value = false
    error.value = ''
    notice.value = ''
    try {
      const result = await options.client.create(input, controller.signal)
      if (disposed || current !== revision || !options.passwordSession()) return
      token.value = result.token
      copied.value = false
      createOpen.value = false
      items.value = [result.key, ...items.value.filter((key) => key.id !== result.key.id)]
      notice.value = 'created'
    } catch (cause) {
      if (!disposed && current === revision) failure(cause, 'create')
    } finally {
      if (current === revision) creating.value = false
    }
  }
  function askRevoke(key: ManagementKey) {
    if (
      !allowed.value ||
      busy.value ||
      writeUncertain.value ||
      !options.passwordSession() ||
      key.revokedAt ||
      confirmedRevocations.value.has(key.id)
    )
      return
    closeToken()
    error.value = ''
    notice.value = ''
    revokeTarget.value = key
  }
  async function revoke() {
    const target = revokeTarget.value
    if (
      disposed ||
      !target ||
      !allowed.value ||
      busy.value ||
      writeUncertain.value ||
      !options.passwordSession()
    )
      return
    readController?.abort()
    const current = ++revision
    const controller = new AbortController()
    writeController = controller
    revoking.value = target.id
    loading.value = false
    error.value = ''
    let succeeded = false
    try {
      await options.client.revoke(target.id, controller.signal)
      if (disposed || current !== revision || !options.passwordSession()) return
      confirmedRevocations.value = new Set(confirmedRevocations.value).add(target.id)
      revokeTarget.value = null
      notice.value = 'revoked'
      succeeded = true
    } catch (cause) {
      if (!disposed && current === revision) failure(cause, 'revoke')
    } finally {
      if (current === revision) revoking.value = ''
    }
    if (succeeded && !disposed && current === revision) await refresh()
  }
  async function copyToken(writeText: (value: string) => Promise<void>) {
    if (!token.value) return
    const currentToken = token.value
    try {
      await writeText(currentToken)
      if (!disposed && token.value === currentToken) {
        copied.value = true
        if (error.value === 'COPY_FAILED') error.value = ''
      }
    } catch {
      if (!disposed && token.value === currentToken) error.value = 'COPY_FAILED'
    }
  }
  function dispose() {
    clearSensitive()
    disposed = true
    items.value = []
    confirmedRevocations.value.clear()
  }
  return {
    items,
    allowed,
    loading,
    creating,
    revoking,
    busy,
    error,
    notice,
    createOpen,
    revokeTarget,
    token,
    copied,
    confirmedRevocations,
    writeUncertain,
    refresh,
    openCreate,
    create,
    askRevoke,
    revoke,
    closeToken,
    copyToken,
    clearSensitive,
    deny,
    dispose,
  }
}
