<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from '@/i18n'
import { businessClient } from './client'
import type {
  BusinessAccount,
  BusinessAccounts,
  BusinessAccountState,
  BusinessCredential,
} from './types'

const props = defineProps<{ states?: BusinessAccountState[] }>()
const emit = defineEmits<{ changed: [] }>()
const { locale } = useI18n()
const chinese = computed(() => locale.value.startsWith('zh'))
const text = (zh: string, en: string) => (chinese.value ? zh : en)
const data = ref<BusinessAccounts | null>(null)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const mustRefresh = ref(false)
const editing = ref(false)
const mode = ref<'details' | 'manual' | 'browser'>('manual')
const executablePath = ref('')
const collecting = ref(false)
const target = ref('')
const label = ref('')
const enabled = ref(false)
const deleting = ref('')
const emptyCredential = (): BusinessCredential => ({
  secure_c_ses: '',
  host_c_oses: '',
  csesidx: '',
  config_id: '',
  user_agent: '',
})
const credential = ref(emptyCredential())
const clock = ref(Date.now())
const accountRows = computed(() => {
  const states = new Map((props.states ?? []).map((state) => [state.id, state]))
  return (data.value?.accounts ?? []).map((account) => ({
    ...account,
    health: states.get(account.id),
  }))
})
let clockTimer: ReturnType<typeof setInterval> | undefined
let controller = new AbortController()
let collectionController: AbortController | null = null
let sequence = 0
let active = true

function startClock(): void {
  stopClock()
  clock.value = Date.now()
  clockTimer = setInterval(() => {
    clock.value = Date.now()
  }, 1000)
}
function stopClock(): void {
  if (clockTimer !== undefined) clearInterval(clockTimer)
  clockTimer = undefined
}
function stateLabel(state: string): string {
  switch (state) {
    case 'ready':
      return text('可接受请求', 'Can accept requests')
    case 'busy':
      return text('账号忙碌', 'Account busy')
    case 'draining':
      return text('旧请求正在结束', 'Previous requests draining')
    case 'cooldown':
      return text('冷却中', 'Cooling down')
    case 'auth_required':
      return text('需要重新认证', 'Authentication required')
    case 'disabled':
      return text('已停用', 'Disabled')
    case 'suspended':
      return text('Business 已暂停', 'Business suspended')
    default:
      return state
  }
}
function errorKind(kind?: string): string {
  switch (kind) {
    case 'local_parameter':
      return text('参数或映射校验', 'Parameter or mapping validation')
    case 'local_resource':
      return text('本地资源额度', 'Local resource limit')
    case 'client_cancelled':
      return text('客户端取消', 'Client cancellation')
    case 'client_write':
      return text('响应写入失败', 'Response write failure')
    case 'authentication':
      return text('Google 认证失败', 'Google authentication failure')
    case 'permission_denied':
      return text('权限或配置拒绝', 'Permission or configuration rejection')
    case 'quota_exhausted':
      return text('上游限流或配额', 'Upstream rate or quota limit')
    case 'transient':
      return text('暂态上游或网络故障', 'Transient upstream or network failure')
    case 'protocol':
      return text('上游响应协议异常', 'Upstream response protocol failure')
    case 'upstream_rejected':
      return text('上游拒绝模型或请求', 'Upstream model or request rejection')
    default:
      return kind ? text('其他错误', 'Other failure') : text('无记录', 'None recorded')
  }
}
function errorStage(stage?: string): string {
  switch (stage) {
    case 'preflight':
      return text('请求校验', 'Request validation')
    case 'auth':
      return text('认证', 'Authentication')
    case 'session':
      return text('创建会话', 'Session creation')
    case 'upload':
      return text('上传附件', 'Attachment upload')
    case 'generate':
      return text('提交模型请求', 'Model request submission')
    case 'stream':
      return text('读取模型响应', 'Reading model response')
    default:
      return text('未提供', 'Not provided')
  }
}
function errorAdvice(kind?: string): string {
  switch (kind) {
    case 'local_parameter':
    case 'permission_denied':
    case 'upstream_rejected':
      return text(
        '先检查模型、参数、权限和配置；这类拒绝不等同于 Google 会话过期。',
        'Check the model, parameters, permissions and configuration first. These rejections do not imply an expired Google session.',
      )
    case 'local_resource':
      return text(
        '请减少请求数据或等待已有工作释放额度；资源不足不等同于会话过期。',
        'Reduce request data or wait for existing work to release capacity. Resource pressure does not imply an expired session.',
      )
    case 'authentication':
      return text(
        '请结合当前账号认证状态处理 Google 会话。此错误不会让控制台登录自动失效。',
        'Use the current account authentication state to assess the Google session. This failure does not expire the console login.',
      )
    case 'quota_exhausted':
    case 'transient':
    case 'protocol':
      return text(
        '按服务端冷却时间等待；到期后仅处理你发起的新请求，不会后台探测模型或自动重放。',
        'Wait for the server cooldown. After it ends, only new requests you initiate are processed; there are no background model probes or automatic replays.',
      )
    case 'client_cancelled':
    case 'client_write':
      return text(
        '取消或响应写入失败不判定为会话过期，也不增加连续暂态失败数。',
        'Cancellation or a response write failure does not imply an expired session or increase the consecutive transient failure count.',
      )
    default:
      return ''
  }
}
function dateLabel(value: string): string {
  return Number.isFinite(Date.parse(value))
    ? new Date(value).toLocaleString(chinese.value ? 'zh-CN' : 'en-US')
    : text('时间未提供', 'Timestamp unavailable')
}
function cooldownRemaining(until: string): string {
  const deadline = Date.parse(until)
  if (!Number.isFinite(deadline))
    return text('时间不可用，请刷新确认。', 'Timestamp unavailable. Refresh to check.')
  const seconds = Math.max(0, Math.ceil((deadline - clock.value) / 1000))
  if (seconds === 0)
    return text(
      '冷却时间已到，请刷新状态确认；不会自动发送请求。',
      'Cooldown time has elapsed. Refresh status to check; no request is sent automatically.',
    )
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const rest = seconds % 60
  if (hours > 0)
    return text(
      `约剩 ${hours} 小时 ${minutes} 分 ${rest} 秒`,
      `About ${hours}h ${minutes}m ${rest}s remaining`,
    )
  if (minutes > 0)
    return text(`约剩 ${minutes} 分 ${rest} 秒`, `About ${minutes}m ${rest}s remaining`)
  return text(`约剩 ${seconds} 秒`, `About ${seconds} seconds remaining`)
}

function clearDraft(): void {
  credential.value = emptyCredential()
  target.value = ''
  label.value = ''
  enabled.value = false
  editing.value = false
  executablePath.value = ''
}
function edit(
  account?: BusinessAccount,
  method: 'details' | 'manual' | 'browser' = 'manual',
): void {
  if (!active || busy.value || mustRefresh.value) return
  clearDraft()
  notice.value = ''
  mode.value = method
  target.value = account?.id ?? ''
  label.value = account?.label ?? ''
  enabled.value = account?.enabled ?? false
  editing.value = true
}
async function refresh(): Promise<void> {
  if (!active || busy.value) return
  const run = ++sequence
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await businessClient.accounts(controller.signal)
    if (active && run === sequence) {
      data.value = result
      mustRefresh.value = false
    }
  } catch {
    if (active && run === sequence)
      error.value = text('无法读取 Business 账号。', 'Could not read Business accounts.')
  } finally {
    if (active && run === sequence) busy.value = false
  }
}
async function mutate(
  operation: () => Promise<BusinessAccounts>,
  signal = controller.signal,
  success = '',
): Promise<void> {
  if (!active || busy.value || mustRefresh.value) return
  const run = ++sequence
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await operation()
    if (active && run === sequence && !signal.aborted) {
      data.value = result
      clearDraft()
      deleting.value = ''
      notice.value = success
      emit('changed')
    }
  } catch {
    if (active && run === sequence && !signal.aborted) {
      mustRefresh.value = true
      error.value = text(
        '操作未确认，请刷新账号列表后核对。凭据不会回显。',
        'Operation not confirmed. Refresh the account list before retrying. Credentials are never returned.',
      )
    }
  } finally {
    if (active && run === sequence) busy.value = false
  }
}
async function save(): Promise<void> {
  if (!active || !data.value || busy.value || mustRefresh.value) return
  const metadataOnly = mode.value === 'details'
  const input = {
    revision: data.value.revision,
    id: target.value,
    label: label.value.trim(),
    enabled: enabled.value,
    ...(metadataOnly ? {} : { credential: { ...credential.value } }),
  }
  // Remove secrets from the form immediately, including when a reply is lost.
  credential.value = emptyCredential()
  await mutate(
    () => businessClient.saveAccount(input, controller.signal),
    controller.signal,
    metadataOnly
      ? text('账号标签已保存，凭据未更新。', 'Account label saved. Credentials were not changed.')
      : text(
          '账号凭据已保存。认证和模型调用结果请查看状态记录。',
          'Account credentials saved. Check status for authentication and model-call results.',
        ),
  )
}
async function collect(): Promise<void> {
  if (!active || !data.value || busy.value || mustRefresh.value) return
  const path = executablePath.value.trim()
  if (!label.value.trim() || !/^(?:[A-Za-z]:[\\/]|\/|\\\\)/.test(path)) {
    error.value = text(
      '请填写账号标签和服务端浏览器的绝对程序路径。',
      'Enter an account label and an absolute server browser executable path.',
    )
    return
  }
  const input = {
    revision: data.value.revision,
    ...(target.value ? { id: target.value } : {}),
    label: label.value.trim(),
    executable_path: path,
  }
  credential.value = emptyCredential()
  const request = new AbortController()
  collectionController = request
  collecting.value = true
  await mutate(() => businessClient.collectAccount(input, request.signal), request.signal)
  if (collectionController === request) {
    collectionController = null
    collecting.value = false
  }
}
function cancelCollection(): void {
  if (!collectionController) return
  ++sequence
  collectionController.abort()
  collectionController = null
  collecting.value = false
  busy.value = false
  mustRefresh.value = true
  clearDraft()
  error.value = text(
    '采集已取消。请刷新账号列表核对后再操作。',
    'Collection canceled. Refresh the account list before another operation.',
  )
}
function toggle(account: BusinessAccount): void {
  if (!data.value) return
  const input = {
    revision: data.value.revision,
    id: account.id,
    label: account.label,
    enabled: !account.enabled,
  }
  void mutate(() => businessClient.saveAccount(input, controller.signal))
}
function remove(id: string): void {
  if (!data.value) return
  const revision = data.value.revision
  void mutate(() => businessClient.deleteAccount(revision, id, controller.signal))
}
function hide(): void {
  active = false
  stopClock()
  ++sequence
  clearDraft()
  data.value = null
  controller.abort()
  collectionController?.abort()
  collectionController = null
  collecting.value = false
  busy.value = false
  deleting.value = ''
  error.value = ''
  notice.value = ''
}
function show(event: PageTransitionEvent): void {
  if (!event.persisted) return
  controller = new AbortController()
  active = true
  startClock()
  void refresh()
}
function visibilityChanged(): void {
  if (document.visibilityState === 'hidden') {
    clearDraft()
    stopClock()
  } else if (active) startClock()
}
onMounted(() => {
  startClock()
  void refresh()
  window.addEventListener('pagehide', hide)
  window.addEventListener('pageshow', show)
  window.addEventListener('admin-session-expired', hide)
  document.addEventListener('visibilitychange', visibilityChanged)
})
onUnmounted(() => {
  hide()
  window.removeEventListener('pagehide', hide)
  window.removeEventListener('pageshow', show)
  window.removeEventListener('admin-session-expired', hide)
  document.removeEventListener('visibilitychange', visibilityChanged)
})
</script>

<template>
  <section class="space-y-3 rounded-lg border border-line p-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 class="font-medium">{{ text('Business 账号', 'Business accounts') }}</h2>
      <div class="flex gap-3 text-sm">
        <button type="button" :disabled="busy" class="disabled:opacity-50" @click="refresh">
          {{ text('刷新账号', 'Refresh accounts') }}
        </button>
        <button
          type="button"
          :disabled="busy || !data || mustRefresh"
          class="text-blue-400 disabled:opacity-50"
          @click="edit(undefined, 'browser')"
        >
          {{ text('浏览器采集', 'Collect in browser') }}
        </button>
        <button
          type="button"
          :disabled="busy || !data || mustRefresh"
          class="text-blue-400 disabled:opacity-50"
          @click="edit()"
        >
          {{ text('添加账号', 'Add account') }}
        </button>
      </div>
    </div>
    <p class="text-xs text-gray-400">
      {{
        text(
          '账号凭据加密保存且不会回显。浏览器采集只在点击开始后连接 Google；也可手动填写已有会话字段。',
          'Credentials are encrypted and never returned. Browser collection contacts Google only after you click Start; existing session fields can also be entered manually.',
        )
      }}
    </p>
    <p v-if="error" role="alert" class="text-sm text-red-300">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-green-300">{{ notice }}</p>
    <div v-if="collecting" role="status" class="space-y-2 rounded border border-line p-3 text-sm">
      <p>
        {{
          text(
            '正在等待服务端桌面浏览器中的登录完成…',
            'Waiting for login in the browser on the server desktop…',
          )
        }}
      </p>
      <button type="button" class="text-blue-400" @click="cancelCollection">
        {{ text('取消采集', 'Cancel collection') }}
      </button>
    </div>
    <div
      v-for="account in accountRows"
      :key="account.id"
      class="flex flex-wrap items-center justify-between gap-3 border-t border-line py-3 text-sm"
    >
      <div>
        <strong>{{ account.label }}</strong>
        <p class="text-xs text-gray-400">
          {{ account.id }} ·
          {{ account.enabled ? text('已启用', 'Enabled') : text('已停用', 'Disabled') }} ·
          {{ text('凭据版本', 'Credential version') }}
          {{ account.epoch }}
        </p>
      </div>
      <div
        v-if="account.health"
        :data-business-account-health="account.id"
        class="w-full space-y-2 text-xs"
      >
        <p>
          {{ text('当前运行状态', 'Current runtime state') }}:
          {{ stateLabel(account.health.state) }}
        </p>
        <dl class="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          <div>
            <dt class="text-gray-400">{{ text('最近错误类别', 'Last failure category') }}</dt>
            <dd>{{ errorKind(account.health.last_error_kind) }}</dd>
          </div>
          <div v-if="account.health.last_error_kind">
            <dt class="text-gray-400">{{ text('发生阶段', 'Failure stage') }}</dt>
            <dd>{{ errorStage(account.health.last_error_stage) }}</dd>
          </div>
          <div>
            <dt class="text-gray-400">
              {{ text('连续暂态失败数', 'Consecutive transient failures') }}
            </dt>
            <dd>{{ account.health.consecutive_failures ?? 0 }}</dd>
          </div>
          <div v-if="account.health.last_error_at">
            <dt class="text-gray-400">{{ text('最近错误时间', 'Last failure time') }}</dt>
            <dd>
              <time :datetime="account.health.last_error_at">{{
                dateLabel(account.health.last_error_at)
              }}</time>
            </dd>
          </div>
        </dl>
        <p
          v-if="
            account.health.last_error_http_status ||
            account.health.last_error_status ||
            account.health.last_error_reason
          "
          class="flex flex-wrap gap-2 text-gray-400"
        >
          <span v-if="account.health.last_error_http_status"
            >HTTP {{ account.health.last_error_http_status }}</span
          >
          <code v-if="account.health.last_error_status">{{
            account.health.last_error_status
          }}</code>
          <code v-if="account.health.last_error_reason">{{
            account.health.last_error_reason
          }}</code>
        </p>
        <p v-if="account.health.last_error_kind" class="text-gray-400">
          {{ errorAdvice(account.health.last_error_kind) }}
        </p>
        <div v-if="account.health.cooldown_until" class="space-y-1 text-amber-200">
          <p>
            {{ text('冷却截止', 'Cooldown until') }}:
            <time :datetime="account.health.cooldown_until">{{
              dateLabel(account.health.cooldown_until)
            }}</time>
          </p>
          <p data-business-cooldown>{{ cooldownRemaining(account.health.cooldown_until) }}</p>
          <p class="text-gray-400">
            {{
              text(
                '剩余时间按本机时钟估算，以服务端截止时间为准；上游限流提示可能较长。只有限流、暂态或协议失败累积冷却，不会在后台进行认证或模型探测。',
                'Remaining time uses the local clock and the server deadline; upstream rate-limit hints can be longer. Only quota, transient or protocol failures accumulate cooldown; no background authentication or model probes run.',
              )
            }}
          </p>
        </div>
      </div>
      <div class="flex gap-3">
        <button type="button" :disabled="busy || mustRefresh" @click="edit(account, 'details')">
          {{ text('编辑标签', 'Edit label') }}
        </button>
        <button type="button" :disabled="busy || mustRefresh" @click="toggle(account)">
          {{ account.enabled ? text('停用', 'Disable') : text('启用', 'Enable') }}
        </button>
        <button type="button" :disabled="busy || mustRefresh" @click="edit(account)">
          {{ text('替换凭据', 'Replace credentials') }}
        </button>
        <button type="button" :disabled="busy || mustRefresh" @click="edit(account, 'browser')">
          {{ text('浏览器替换', 'Replace in browser') }}
        </button>
        <button
          type="button"
          :disabled="busy || mustRefresh"
          class="text-red-300"
          @click="deleting = account.id"
        >
          {{ text('删除', 'Delete') }}
        </button>
      </div>
      <div v-if="deleting === account.id" class="w-full rounded border border-red-500/30 p-3">
        <p>
          {{
            text(
              '确认删除此账号及其加密凭据？',
              'Delete this account and its encrypted credentials?',
            )
          }}
        </p>
        <button
          type="button"
          :disabled="busy"
          class="mr-4 mt-2 text-red-300"
          @click="remove(account.id)"
        >
          {{ text('确认删除', 'Confirm deletion') }}
        </button>
        <button type="button" :disabled="busy" @click="deleting = ''">
          {{ text('取消', 'Cancel') }}
        </button>
      </div>
    </div>
    <form
      v-if="editing"
      class="space-y-3 border-t border-line pt-4"
      autocomplete="off"
      @submit.prevent="mode === 'browser' ? collect() : save()"
    >
      <p v-if="mode === 'details'" class="text-xs text-gray-400">
        {{
          text(
            '仅修改账号标签，保留启用状态、凭据版本和验证记录。',
            'Only the account label changes. Enabled state, credential version and verification records are preserved.',
          )
        }}
      </p>
      <p v-if="mode === 'browser'" class="text-sm text-amber-200">
        {{
          text(
            '浏览器将显示在服务端桌面，需要你在那里完成登录；远程无头交互尚未实现。填写服务端兼容浏览器的绝对程序路径。采集成功后，新建或替换的账号均默认停用，需另行启用。',
            'The browser opens visibly on the server desktop, where you must complete login. Remote/headless interaction is not implemented. Enter the absolute path to a compatible browser on the server. New and replacement accounts are disabled after collection until you enable them.',
          )
        }}
      </p>
      <fieldset
        :disabled="busy || mustRefresh"
        class="grid gap-3 text-sm md:grid-cols-2 disabled:opacity-50"
      >
        <label
          >{{ text('账号标签', 'Account label')
          }}<input
            v-model="label"
            required
            maxlength="160"
            class="mt-1 block w-full rounded border border-line bg-canvas p-2"
        /></label>
        <label v-if="mode === 'browser'"
          >{{ text('服务端浏览器绝对程序路径', 'Absolute server browser executable path')
          }}<input
            v-model="executablePath"
            required
            maxlength="4096"
            spellcheck="false"
            class="mt-1 block w-full rounded border border-line bg-canvas p-2 font-mono"
        /></label>
        <template v-if="mode === 'manual'">
          <label
            >configId<input
              v-model="credential.config_id"
              required
              maxlength="1024"
              class="mt-1 block w-full rounded border border-line bg-canvas p-2"
          /></label>
          <label
            >__Secure-C_SES<input
              v-model="credential.secure_c_ses"
              type="password"
              autocomplete="new-password"
              required
              maxlength="16384"
              class="mt-1 block w-full rounded border border-line bg-canvas p-2"
          /></label>
          <label
            >__Host-C_OSES<input
              v-model="credential.host_c_oses"
              type="password"
              autocomplete="new-password"
              maxlength="16384"
              class="mt-1 block w-full rounded border border-line bg-canvas p-2"
          /></label>
          <label
            >csesidx<input
              v-model="credential.csesidx"
              required
              maxlength="1024"
              class="mt-1 block w-full rounded border border-line bg-canvas p-2"
          /></label>
          <label
            >User-Agent<input
              v-model="credential.user_agent"
              maxlength="1024"
              class="mt-1 block w-full rounded border border-line bg-canvas p-2"
          /></label>
          <label class="flex items-center gap-2"
            ><input v-model="enabled" type="checkbox" />{{
              text('启用此账号', 'Enable account')
            }}</label
          >
        </template>
      </fieldset>
      <button
        type="submit"
        :disabled="busy || mustRefresh"
        class="mr-4 rounded bg-blue-600 px-4 py-2 text-sm text-white disabled:opacity-50"
      >
        {{
          mode === 'browser'
            ? text('开始可见浏览器采集', 'Start visible browser collection')
            : mode === 'details'
              ? text('保存标签', 'Save label')
              : text('保存账号', 'Save account')
        }}
      </button>
      <button type="button" :disabled="busy" class="text-sm" @click="clearDraft">
        {{ text('取消', 'Cancel') }}
      </button>
    </form>
  </section>
</template>
