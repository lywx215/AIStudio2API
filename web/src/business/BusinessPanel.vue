<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError } from '@/api'
import { useI18n } from '@/i18n'
import { businessClient } from './client'
import type { BusinessBinding, BusinessConfig, BusinessModelState, BusinessStatus } from './types'
import BusinessAccounts from './BusinessAccounts.vue'
import BusinessTry from './BusinessTry.vue'
import BusinessCollection from './BusinessCollection.vue'
import BusinessImport from './BusinessImport.vue'

const importBusy = ref(false)
const savedConfigJSON = ref('')
const hasUnsavedChanges = computed(
  () =>
    config.value !== null &&
    (JSON.stringify(config.value) !== savedConfigJSON.value ||
      Object.keys(parameterDrafts.value).length > 0 ||
      Number(resourceMiB.value) * mib !==
        (config.value.max_inflight_bytes ?? defaultInflightBytes)),
)

const { locale } = useI18n()
const chinese = computed(() => locale.value.startsWith('zh'))
const text = (zh: string, en: string) => (chinese.value ? zh : en)
const config = ref<BusinessConfig | null>(null)
const status = ref<BusinessStatus | null>(null)
const pending = ref(false)
const error = ref('')
const notice = ref('')
const accountVersion = ref(0)
const mustRefresh = ref(false)
const parameterDrafts = ref<Record<string, string>>({})
const savedBindings = ref<BusinessBinding[]>([])
const savedEnabled = ref(false)
const mib = 1024 * 1024
const defaultInflightBytes = 256 * mib
const resourceMiB = ref(256)
const modelStates = computed(() => status.value?.model_states ?? [])
const canSchedule = computed(
  () => status.value?.generation_ready === true && modelStates.value.some(isSchedulable),
)
let controller = new AbortController()
let active = true
let epoch = 0
let statusSequence = 0

function isSchedulable(model: BusinessModelState): boolean {
  return (
    status.value?.enabled === true &&
    (status.value.capacity?.available ?? 0) > 0 &&
    model.configuration_valid &&
    model.state === 'ready' &&
    model.schedulable
  )
}

function modelName(id: string): string {
  return savedBindings.value.find((binding) => binding.public_id === id)?.display_name ?? id
}

function reason(code?: string): string {
  switch (code) {
    case 'BUSINESS_DISABLED':
      return text('Business 已停用', 'Business is disabled')
    case 'BUSINESS_MODEL_DISABLED':
      return text('模型绑定已停用', 'Model binding disabled')
    case 'BUSINESS_MODEL_CONFIGURATION_INVALID':
      return text('模型映射配置无效', 'Model mapping configuration invalid')
    case 'BUSINESS_ACCOUNT_REQUIRED':
      return text('需要 Business 账号', 'Business account required')
    case 'BUSINESS_ACCOUNT_SCOPE_EMPTY':
      return text('绑定范围内没有账号', 'No accounts in the binding scope')
    case 'BUSINESS_ACCOUNT_DISABLED':
      return text('绑定范围内的账号已停用', 'Accounts in the binding scope are disabled')
    case 'BUSINESS_ACCOUNT_UNAVAILABLE':
      return text('绑定范围内暂无可用账号', 'No account in the binding scope is available')
    case 'BUSINESS_ACCOUNT_BUSY':
      return text('账号忙碌，当前不可调度', 'Accounts busy; cannot schedule now')
    case 'BUSINESS_ACCOUNT_COOLDOWN':
      return text('账号冷却中，当前不可调度', 'Accounts cooling down; cannot schedule now')
    case 'BUSINESS_ACCOUNT_DRAINING':
      return text(
        '旧请求正在结束，当前不可调度',
        'Previous requests are draining; cannot schedule now',
      )
    case 'BUSINESS_AUTH_REQUIRED':
      return text('账号需要重新认证', 'Account authentication required')
    case 'BUSINESS_CAPACITY_FULL':
      return text('并发已满，当前不可调度', 'Concurrency limit reached; cannot schedule now')
    case 'BUSINESS_RESOURCE_BUDGET_FULL':
      return text(
        '处理数据预算不足，当前不可调度',
        'In-flight data budget insufficient; cannot schedule now',
      )
    case 'BUSINESS_RUNTIME_SUSPENDED':
      return text('Business 已暂停，当前不可调度', 'Business is suspended; cannot schedule now')
    case 'BUSINESS_RUNTIME_STOPPED':
    case 'BUSINESS_CLOSED':
      return text('Business 已关闭', 'Business is closed')
    default:
      return code ?? ''
  }
}

function verifiedAt(value?: string): string {
  if (!value || !Number.isFinite(Date.parse(value)))
    return text('时间未提供', 'Timestamp unavailable')
  return new Date(value).toLocaleString(chinese.value ? 'zh-CN' : 'en-US')
}

function byteLabel(value: number): string {
  if (!Number.isFinite(value) || value < 0) return text('未提供', 'Not provided')
  return `${(value / mib).toLocaleString(chinese.value ? 'zh-CN' : 'en-US', { maximumFractionDigits: 2 })} MiB`
}

function collectionChanged(): void {
  accountVersion.value++
  void refreshStatus()
}

function rememberSaved(value: BusinessConfig): void {
  savedConfigJSON.value = JSON.stringify(value)
  savedBindings.value = value.bindings.map((binding) => ({ ...binding }))
  savedEnabled.value = value.enabled
  resourceMiB.value = (value.max_inflight_bytes ?? defaultInflightBytes) / mib
}

async function refreshStatus(): Promise<void> {
  if (!active) return
  const run = epoch
  const statusRun = ++statusSequence
  try {
    const next = await businessClient.status(controller.signal)
    if (active && run === epoch && statusRun === statusSequence) status.value = next
  } catch {
    if (active && run === epoch && statusRun === statusSequence) status.value = null
  }
}

function resetVerification(binding: BusinessBinding): void {
  binding.enabled = false
  binding.verification_status = 'unverified'
  binding.evidence = ''
  if (binding.selector_mode !== 'exact') binding.upstream_model_id = ''
}

async function refresh(): Promise<void> {
  if (!active || pending.value || importBusy.value) return
  const run = epoch
  const statusRun = ++statusSequence
  pending.value = true
  error.value = ''
  notice.value = ''
  try {
    const [nextConfig, nextStatus] = await Promise.all([
      businessClient.config(controller.signal),
      businessClient.status(controller.signal),
    ])
    if (!active || run !== epoch) return
    config.value = nextConfig
    rememberSaved(nextConfig)
    parameterDrafts.value = {}
    if (statusRun === statusSequence) status.value = nextStatus
    mustRefresh.value = false
  } catch {
    if (active && run === epoch)
      error.value = text(
        '无法读取 Business 配置，请刷新重试。',
        'Could not load Business settings. Please refresh.',
      )
  } finally {
    if (active && run === epoch) pending.value = false
  }
}

async function save(): Promise<void> {
  if (!active || !config.value || pending.value || importBusy.value || mustRefresh.value) return
  const run = epoch
  const resourceBytes = Number(resourceMiB.value) * mib
  if (!Number.isInteger(resourceMiB.value) || resourceMiB.value < 64 || resourceMiB.value > 4096) {
    error.value = text(
      '资源预算必须是 64–4096 之间的整数 MiB。',
      'The resource budget must be an integer from 64 to 4096 MiB.',
    )
    return
  }
  config.value.max_inflight_bytes = resourceBytes
  try {
    for (const binding of config.value.bindings) {
      const draft = parameterDrafts.value[binding.public_id]
      if (draft !== undefined) {
        const parsed: unknown = JSON.parse(draft)
        if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed))
          throw new Error('Invalid mappings')
        binding.generation_parameters = parsed as NonNullable<
          BusinessBinding['generation_parameters']
        >
      }
    }
  } catch {
    error.value = text(
      '参数映射必须是合法 JSON 对象；请修正后保存。',
      'Parameter mappings must be valid JSON objects.',
    )
    return
  }
  pending.value = true
  error.value = ''
  notice.value = ''
  try {
    const saved = await businessClient.save(config.value, controller.signal)
    if (!active || run !== epoch) return
    config.value = saved
    parameterDrafts.value = {}
    rememberSaved(saved)
    notice.value = text(
      '配置已保存。请分别查看模型调度、认证记录和模型调用记录。',
      'Settings saved. Check model scheduling, authentication and model-call records separately.',
    )
    const statusRun = ++statusSequence
    try {
      const nextStatus = await businessClient.status(controller.signal)
      if (active && run === epoch && statusRun === statusSequence) status.value = nextStatus
    } catch {
      if (active && run === epoch && statusRun === statusSequence) status.value = null
    }
  } catch (failure) {
    if (!active || run !== epoch) return
    mustRefresh.value = !(failure instanceof ApiError && failure.status === 400)
    error.value = text(
      mustRefresh.value
        ? '保存结果未确认或版本已变化，请刷新核对后再保存。'
        : '配置校验失败。请检查模型 ID、核实记录和启用条件。',
      mustRefresh.value
        ? 'Save result is uncertain or the revision changed. Refresh before saving again.'
        : 'Validation failed. Check model IDs, evidence and activation requirements.',
    )
  } finally {
    if (active && run === epoch) pending.value = false
  }
}

function hide(): void {
  active = false
  ++epoch
  controller.abort()
  config.value = null
  status.value = null
  savedBindings.value = []
  savedEnabled.value = false
  parameterDrafts.value = {}
  pending.value = false
}
function show(event: PageTransitionEvent): void {
  if (!event.persisted) return
  controller = new AbortController()
  active = true
  void refresh()
}
onMounted(() => {
  void refresh()
  window.addEventListener('pagehide', hide)
  window.addEventListener('pageshow', show)
  window.addEventListener('admin-session-expired', hide)
})
onUnmounted(() => {
  hide()
  window.removeEventListener('pagehide', hide)
  window.removeEventListener('pageshow', show)
  window.removeEventListener('admin-session-expired', hide)
})
</script>

<template>
  <section class="min-h-0 flex-1 overflow-auto p-4 md:p-6" aria-label="Gemini Business">
    <div class="mx-auto max-w-6xl space-y-5">
      <header class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">Gemini Business</h1>
          <p class="mt-2 text-sm text-gray-400">
            {{
              text(
                '使用当前控制台登录和 API 密钥。AI Studio 的启动、停止和账号设置保持原有含义。',
                'Uses the current console login and API keys. AI Studio start, stop and account settings retain their existing behavior.',
              )
            }}
          </p>
        </div>
        <button
          type="button"
          class="rounded border border-line px-3 py-2 text-sm disabled:opacity-50"
          :disabled="pending"
          @click="refresh"
        >
          {{ text('刷新', 'Refresh') }}
        </button>
      </header>

      <div
        class="rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-200"
        role="status"
      >
        {{
          text(
            'implemented 只表示本地实现。模型配置有效、可立即调度、认证已验证和模型调用成功是四种不同状态。保存配置不会调用模型；单个模型的成功记录也不代表完整在线验收已完成。',
            'implemented describes local implementation. Valid configuration, immediate scheduling, verified authentication and a successful model call are separate states. Saving settings does not call a model; a model success record does not complete overall live validation.',
          )
        }}
      </div>
      <p v-if="error" role="alert" class="text-sm text-red-300">{{ error }}</p>
      <p v-if="notice" role="status" class="text-sm text-green-300">{{ notice }}</p>

      <div v-if="status" class="flex flex-wrap gap-4 rounded-lg border border-line p-4 text-sm">
        <span
          >{{ text('状态', 'Status') }}: <strong>{{ status.state }}</strong></span
        >
        <span
          >{{ text('有模型可立即调度', 'A model can be scheduled now') }}:
          <strong>{{ canSchedule ? text('是', 'Yes') : text('否', 'No') }}</strong></span
        >
        <span>{{ text('配置版本', 'Revision') }}: {{ status.config_revision }}</span>
        <span
          >{{ text('完整在线验收', 'Overall live validation') }}:
          {{ status.live_verified ? text('已完成', 'Verified') : text('待验证', 'Pending') }}</span
        >
        <code v-if="status.code" class="text-amber-300">{{ status.code }}</code>
      </div>

      <section
        v-if="status"
        class="space-y-2 rounded-lg border border-line p-4 text-sm"
        aria-label="Business resource budget"
      >
        <h2 class="font-medium">{{ text('处理中数据预算', 'In-flight data budget') }}</h2>
        <dl v-if="status.resources" data-business-resources class="grid gap-3 sm:grid-cols-3">
          <div>
            <dt class="text-xs text-gray-400">{{ text('总预算', 'Total budget') }}</dt>
            <dd>{{ byteLabel(status.resources.limit_bytes) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-400">{{ text('已占用', 'In use') }}</dt>
            <dd>{{ byteLabel(status.resources.in_use_bytes) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-400">{{ text('可用', 'Available') }}</dt>
            <dd>{{ byteLabel(status.resources.available_bytes) }}</dd>
          </div>
        </dl>
        <p v-else class="text-gray-400">
          {{
            text(
              '服务端尚未提供资源预算状态，请刷新确认。',
              'Resource budget status is unavailable. Refresh to check.',
            )
          }}
        </p>
        <p
          v-if="status.resources && status.resources.available_bytes < 32 * mib"
          role="status"
          class="text-amber-200"
        >
          <strong v-if="status.resources.available_bytes === 0">{{
            text('资源预算已满。', 'The resource budget is full.')
          }}</strong>
          {{
            text(
              '资源预算不足以预留一次生成所需的 32 MiB，生成请求当前不可立即调度。请等待已有工作释放额度，或减少输入与附件大小。',
              'Available resources cannot cover the 32 MiB base reservation for generation. Generation requests cannot be scheduled now. Wait for existing work to release capacity, or reduce input and attachment sizes.',
            )
          }}
        </p>
        <p
          v-if="status.resources && status.resources.in_use_bytes > status.resources.limit_bytes"
          class="text-xs text-amber-200"
        >
          {{
            text(
              '当前占用高于新预算；预算调整不会取消已有工作，额度会在已有工作结束后释放。',
              'Usage is above the new budget. Changing the budget does not cancel existing work; capacity is released as that work ends.',
            )
          }}
        </p>
        <p class="text-xs text-gray-400">
          {{
            text(
              '此处显示处理中数据的预留额度，不代表整个进程的内存用量。剩余额度不足以处理本次输入时，服务会拒绝请求；不会后台探测模型或自动重试。',
              'This is reserved capacity for in-flight data, not total process memory. A request is rejected when its input exceeds available capacity; there are no background model probes or automatic retries.',
            )
          }}
        </p>
      </section>

      <section v-if="status" class="space-y-3 rounded-lg border border-line p-4 text-sm">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <h2 class="font-medium">
            {{ text('模型配置与调用状态', 'Model configuration and call status') }}
          </h2>
          <p v-if="status.capacity" data-business-capacity class="text-gray-400">
            {{ text('并发额度', 'Concurrency') }}: {{ status.capacity.active }} /
            {{ status.capacity.limit }} {{ text('已占用', 'active') }} ·
            {{ text('可用', 'Available') }} {{ status.capacity.available }}
          </p>
        </div>
        <p class="text-xs text-gray-400">
          {{
            text(
              '状态仅反映最近一次刷新。账号忙碌、冷却或并发已满时不可立即调度。认证时间记录最近成功；认证有效性以当前状态为准。模型成功记录属于当前凭据，不保证下一次请求成功。刷新状态不会发送认证或模型探测请求。',
              'Status reflects the latest refresh. Busy accounts, cooldowns or a full concurrency limit prevent immediate scheduling. Authentication timestamps record the last success; validity reflects the current state. Model successes belong to the current credentials and do not guarantee the next request. Refreshing status sends no authentication or model probes.',
            )
          }}
        </p>
        <p v-if="!modelStates.length" class="text-gray-400">
          {{
            text(
              '服务端尚未提供模型状态明细，请刷新确认。',
              'Model status details are unavailable. Refresh to check.',
            )
          }}
        </p>
        <article
          v-for="model in modelStates"
          :key="model.public_id"
          :data-business-model="model.public_id"
          class="space-y-3 border-t border-line pt-3"
        >
          <div class="flex flex-wrap items-baseline justify-between gap-2">
            <div>
              <h3 class="font-medium">{{ modelName(model.public_id) }}</h3>
              <code class="text-xs text-gray-400">{{ model.public_id }}</code>
            </div>
            <span v-if="model.code" class="text-xs text-amber-200">{{ reason(model.code) }}</span>
          </div>
          <dl class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <div data-business-dimension="configuration">
              <dt class="text-xs text-gray-400">{{ text('模型配置', 'Model configuration') }}</dt>
              <dd :class="model.configuration_valid ? 'text-green-300' : 'text-amber-200'">
                {{ model.configuration_valid ? text('有效', 'Valid') : text('无效', 'Invalid') }}
              </dd>
            </div>
            <div data-business-dimension="scheduling">
              <dt class="text-xs text-gray-400">{{ text('当前调度', 'Scheduling now') }}</dt>
              <dd :class="isSchedulable(model) ? 'text-green-300' : 'text-amber-200'">
                {{
                  isSchedulable(model)
                    ? text('可立即调度', 'Can schedule now')
                    : text('不可立即调度', 'Cannot schedule now')
                }}
              </dd>
            </div>
            <div data-business-dimension="authentication">
              <dt class="text-xs text-gray-400">
                {{ text('最近认证验证', 'Recent authentication verification') }}
              </dt>
              <dd :class="model.auth_verified ? 'text-green-300' : 'text-gray-400'">
                {{
                  model.auth_verified
                    ? text('当前认证有效', 'Authentication currently valid')
                    : text('当前无有效认证', 'No currently valid authentication')
                }}
              </dd>
              <dd v-if="model.auth_verified_at" class="text-xs text-gray-400">
                {{ text('最近成功', 'Last success') }}:
                <time :datetime="model.auth_verified_at">{{
                  verifiedAt(model.auth_verified_at)
                }}</time>
              </dd>
            </div>
            <div data-business-dimension="model-call">
              <dt class="text-xs text-gray-400">{{ text('实际模型调用', 'Actual model call') }}</dt>
              <dd :class="model.model_verified ? 'text-green-300' : 'text-gray-400'">
                {{
                  model.model_verified
                    ? text('已有成功记录', 'Successful call recorded')
                    : text('尚无成功记录', 'No successful call recorded')
                }}
              </dd>
              <dd v-if="model.model_verified" class="text-xs text-gray-400">
                <time :datetime="model.model_verified_at">{{
                  verifiedAt(model.model_verified_at)
                }}</time>
              </dd>
            </div>
          </dl>
          <p class="text-xs text-gray-400">
            {{ text('当前符合条件的账号数', 'Currently eligible accounts') }}:
            {{ model.eligible_account_ids.length }}
          </p>
        </article>
      </section>

      <div v-if="status" class="rounded-lg border border-line p-4 text-sm">
        <h2 class="mb-2 font-medium">
          {{ text('服务端能力（本地实现状态）', 'Server capabilities (local implementation)') }}
        </h2>
        <dl class="grid gap-2 sm:grid-cols-2">
          <div
            v-for="(value, name) in status.capabilities"
            :key="name"
            class="flex flex-wrap gap-2"
          >
            <dt class="font-mono">{{ name }}</dt>
            <dd class="text-gray-400">{{ value }}</dd>
          </div>
        </dl>
      </div>

      <BusinessAccounts
        :key="accountVersion"
        :states="status?.account_states ?? []"
        @changed="refreshStatus"
      />
      <BusinessCollection @changed="collectionChanged" />

      <BusinessTry
        :bindings="savedEnabled ? savedBindings : []"
        :disabled="pending || mustRefresh"
        @completed="refreshStatus"
      />

      <form v-if="config" class="space-y-5" @submit.prevent="save">
        <fieldset class="space-y-5 disabled:opacity-60" :disabled="pending || importBusy">
          <div class="flex flex-wrap items-center gap-5 rounded-lg border border-line p-4 text-sm">
            <label class="flex items-center gap-2">
              <input v-model="config.enabled" type="checkbox" />
              {{ text('启用 Business 配置', 'Enable Business configuration') }}
            </label>
            <label class="flex items-center gap-2">
              {{ text('并发上限', 'Concurrency limit') }}
              <input
                v-model.number="config.max_concurrent"
                type="number"
                min="1"
                max="16"
                required
                class="w-20 rounded border border-line bg-canvas px-2 py-1"
              />
            </label>
            <label class="flex items-center gap-2">
              {{ text('处理数据预算（MiB）', 'In-flight data budget (MiB)') }}
              <input
                v-model.number="resourceMiB"
                data-business-resource-budget
                type="number"
                min="64"
                max="4096"
                step="1"
                required
                class="w-28 rounded border border-line bg-canvas px-2 py-1"
              />
            </label>
            <label class="flex items-center gap-2">
              {{ text('超时（秒）', 'Timeout (seconds)') }}
              <input
                v-model.number="config.request_timeout_seconds"
                type="number"
                min="10"
                max="300"
                required
                class="w-24 rounded border border-line bg-canvas px-2 py-1"
              />
            </label>
          </div>

          <p class="text-sm text-gray-400">
            {{
              text(
                '截图显示名称不是上游 modelId。填写网页请求中观察到的 ID 和核实记录后，才可启用绑定。请勿在记录中填写 Cookie 或令牌。',
                'Display names are not upstream model IDs. Record an observed request ID and verification evidence before enabling a binding. Never put cookies or tokens in evidence.',
              )
            }}
          </p>
          <article
            v-for="binding in config.bindings"
            :key="binding.public_id"
            class="space-y-3 rounded-lg border border-line p-4"
          >
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div>
                <h2 class="font-medium">{{ binding.display_name }}</h2>
                <code class="text-xs text-gray-400">{{ binding.public_id }}</code>
              </div>
              <label class="flex items-center gap-2 text-sm">
                <input
                  v-model="binding.enabled"
                  type="checkbox"
                  :disabled="
                    binding.verification_status !== 'verified' ||
                    !binding.evidence.trim() ||
                    binding.selector_mode === 'unresolved' ||
                    (binding.selector_mode === 'exact' && !binding.upstream_model_id.trim())
                  "
                />
                {{ text('启用绑定', 'Enable binding') }}
              </label>
            </div>
            <div class="grid gap-3 text-sm md:grid-cols-2">
              <label class="space-y-1"
                ><span>{{ text('选择方式', 'Selection mode') }}</span>
                <select
                  v-model="binding.selector_mode"
                  class="block w-full rounded border border-line bg-canvas px-2 py-2"
                  @change="resetVerification(binding)"
                >
                  <option value="unresolved">{{ text('待核实', 'Unresolved') }}</option>
                  <option value="exact">{{ text('指定 modelId', 'Exact modelId') }}</option>
                  <option value="auto">Auto</option>
                </select>
              </label>
              <label class="space-y-1"
                ><span>Upstream modelId</span>
                <input
                  v-model="binding.upstream_model_id"
                  :disabled="binding.selector_mode !== 'exact'"
                  :required="binding.selector_mode === 'exact'"
                  maxlength="160"
                  class="block w-full rounded border border-line bg-canvas px-2 py-2 font-mono disabled:opacity-50"
                  @input="resetVerification(binding)"
                />
              </label>
              <label class="space-y-1 md:col-span-2"
                ><span>{{
                  text(
                    '核实记录（观察时间及请求字段，不含凭据）',
                    'Evidence (observation date and request field, without credentials)',
                  )
                }}</span>
                <input
                  v-model="binding.evidence"
                  maxlength="1024"
                  class="block w-full rounded border border-line bg-canvas px-2 py-2"
                />
              </label>
              <label class="flex items-center gap-2 md:col-span-2">
                <input
                  v-model="binding.verification_status"
                  type="checkbox"
                  true-value="verified"
                  false-value="unverified"
                  @change="binding.enabled = false"
                />
                {{
                  text(
                    '我已核实此映射；Auto 也需要核实请求形式',
                    'I verified this mapping, including the request shape for Auto',
                  )
                }}
              </label>
            </div>
            <details class="rounded border border-line p-3 text-sm">
              <summary class="cursor-pointer">
                {{
                  text(
                    '高级：generationConfig 参数映射',
                    'Advanced: generationConfig parameter mappings',
                  )
                }}
              </summary>
              <p class="my-2 text-xs text-gray-400">
                {{
                  text(
                    '仅添加已核实的上游字段。每项需要 upstream_field、verified 和 evidence；未映射的可选参数会被忽略，不代表生效。示例字段：temperature。记录中不要填写凭据。',
                    'Only map observed upstream fields. Each entry needs upstream_field, verified and evidence. Unmapped optional parameters are ignored and have no effect. Example key: temperature. Never include credentials.',
                  )
                }}
              </p>
              <textarea
                :value="
                  parameterDrafts[binding.public_id] ??
                  JSON.stringify(binding.generation_parameters ?? {}, null, 2)
                "
                rows="5"
                spellcheck="false"
                :aria-label="`${binding.display_name} generationConfig mappings`"
                class="block w-full rounded border border-line bg-canvas p-2 font-mono text-xs"
                @input="
                  parameterDrafts[binding.public_id] = ($event.target as HTMLTextAreaElement).value
                "
              />
            </details>
          </article>
        </fieldset>
        <button
          type="submit"
          :disabled="pending || importBusy || mustRefresh"
          class="rounded bg-blue-600 px-5 py-2 text-sm text-white disabled:opacity-50"
        >
          {{
            pending
              ? text('处理中…', 'Working…')
              : text('保存 Business 配置', 'Save Business settings')
          }}
        </button>
      </form>
      <BusinessImport
        v-if="config"
        :revision="config.revision"
        :disabled="pending || mustRefresh || hasUnsavedChanges"
        :unsaved="hasUnsavedChanges"
        @busy="importBusy = $event"
        @changed="refresh"
      />
    </div>
  </section>
</template>
