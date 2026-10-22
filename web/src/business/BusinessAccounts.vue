<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from '@/i18n'
import { businessClient } from './client'
import type { BusinessAccount, BusinessAccounts, BusinessCredential } from './types'

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
let controller = new AbortController()
let collectionController: AbortController | null = null
let sequence = 0
let active = true

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
  void refresh()
}
function visibilityChanged(): void {
  if (document.visibilityState === 'hidden') clearDraft()
}
onMounted(() => {
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
      v-for="account in data?.accounts"
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
