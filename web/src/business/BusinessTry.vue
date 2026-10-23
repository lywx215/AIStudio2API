<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError } from '@/api'
import { useI18n } from '@/i18n'
import { businessClient } from './client'
import type { BusinessBinding } from './types'

const props = defineProps<{ bindings: BusinessBinding[]; disabled: boolean }>()
const emit = defineEmits<{ completed: [] }>()
const { locale } = useI18n()
const chinese = computed(() => locale.value.startsWith('zh'))
const text = (zh: string, en: string) => (chinese.value ? zh : en)
const available = computed(() =>
  props.bindings.filter((binding) => binding.enabled && binding.verification_status === 'verified'),
)
const selected = ref('')
const draft = ref(
  JSON.stringify({ contents: [{ role: 'user', parts: [{ text: 'Hello' }] }] }, null, 2),
)
const pending = ref(false)
const result = ref('')
const error = ref('')
const notice = ref('')
let active = true
let sequence = 0
let controller: AbortController | null = null

function cancel(): void {
  ++sequence
  controller?.abort()
  controller = null
  pending.value = false
  notice.value = text('请求已取消。', 'Request canceled.')
}
watch(available, (bindings) => {
  if (!bindings.some((binding) => binding.public_id === selected.value)) {
    if (pending.value) cancel()
    selected.value = ''
  }
})

async function send(): Promise<void> {
  if (!active || pending.value || props.disabled) return
  if (!available.value.some((binding) => binding.public_id === selected.value)) {
    error.value = text('请选择已启用且已核实的绑定。', 'Choose an enabled, verified binding.')
    return
  }
  try {
    const value: unknown = JSON.parse(draft.value)
    if (value === null || typeof value !== 'object' || Array.isArray(value))
      throw new Error('Expected JSON object')
  } catch {
    error.value = text(
      '请求必须是合法的原生 Gemini JSON 对象。',
      'Enter a valid native Gemini JSON object.',
    )
    return
  }
  const request = new AbortController()
  controller = request
  const run = ++sequence
  pending.value = true
  result.value = ''
  error.value = ''
  notice.value = ''
  try {
    // Send the editor verbatim; do not infer fields or sampling defaults.
    const response = await businessClient.tryGenerate(selected.value, draft.value, request.signal)
    if (active && run === sequence && !request.signal.aborted) {
      result.value = JSON.stringify(response, null, 2)
      emit('completed')
    }
  } catch (failure) {
    if (!active || run !== sequence || request.signal.aborted) return
    error.value =
      failure instanceof ApiError
        ? `${failure.status}${failure.code ? ` ${failure.code}` : ''}: ${failure.message}`
        : text('请求失败，请检查连接或重试。', 'Request failed. Check the connection or retry.')
    if (failure instanceof ApiError && (failure.status === 400 || failure.status === 403))
      notice.value = text(
        '参数、模型或权限拒绝不等同于会话过期。请查看模型映射和账号诊断后再决定是否更新凭据。',
        'A parameter, model or permission rejection does not imply an expired session. Check the mapping and account diagnostics before deciding whether credentials need replacement.',
      )
    emit('completed')
  } finally {
    if (active && run === sequence) {
      controller = null
      pending.value = false
    }
  }
}
function hide(): void {
  active = false
  cancel()
  draft.value = ''
  result.value = ''
  error.value = ''
  notice.value = ''
}
function show(event: PageTransitionEvent): void {
  if (event.persisted) active = true
}
onMounted(() => {
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
  <section class="space-y-3 rounded-lg border border-line p-4">
    <h2 class="font-medium">{{ text('Business 原生试用', 'Business native trial') }}</h2>
    <p class="text-xs text-gray-400">
      {{
        text(
          '使用当前控制台管理会话。点击发送后才调用模型。这里只列出已保存、已启用且映射已核实的绑定；当前可调度情况和实际调用记录请查看模型状态。请求及响应中的未知字段完整保留，不自动补入采样参数。',
          'Uses the current console management session. A model is called only when you click Send. Only saved, enabled bindings with verified mappings are listed; check model status for current scheduling and actual call records. Unknown request and response fields are preserved; no sampling parameters are added.',
        )
      }}
    </p>
    <form class="space-y-3" @submit.prevent="send">
      <label class="block text-sm">
        {{ text('映射已核实的模型绑定', 'Model binding with verified mapping') }}
        <select
          v-model="selected"
          :disabled="pending || disabled"
          class="mt-1 block w-full rounded border border-line bg-canvas p-2 disabled:opacity-50"
        >
          <option value="">{{ text('请选择模型', 'Choose a model') }}</option>
          <option v-for="binding in available" :key="binding.public_id" :value="binding.public_id">
            {{ binding.display_name }} · {{ binding.public_id }}
          </option>
        </select>
      </label>
      <p v-if="!available.length" class="text-xs text-amber-200">
        {{
          text(
            '尚无可试用绑定。先核实映射、启用绑定并保存 Business 配置。',
            'No binding is available. Verify a mapping, enable its binding and save the Business configuration first.',
          )
        }}
      </p>
      <label class="block text-sm">
        {{ text('原生 Gemini 请求 JSON', 'Native Gemini request JSON') }}
        <textarea
          v-model="draft"
          :disabled="pending"
          rows="10"
          spellcheck="false"
          autocomplete="off"
          class="mt-1 block w-full rounded border border-line bg-canvas p-2 font-mono text-xs disabled:opacity-50"
        />
      </label>
      <div class="flex gap-4 text-sm">
        <button
          type="submit"
          :disabled="pending || disabled || !selected || !available.length"
          class="rounded bg-blue-600 px-4 py-2 text-white disabled:opacity-50"
        >
          {{ pending ? text('请求中…', 'Sending…') : text('发送试用请求', 'Send trial request') }}
        </button>
        <button v-if="pending" type="button" @click="cancel">
          {{ text('取消请求', 'Cancel request') }}
        </button>
      </div>
    </form>
    <p v-if="error" role="alert" class="whitespace-pre-wrap text-sm text-red-300">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-gray-400">{{ notice }}</p>
    <pre
      v-if="result"
      aria-live="polite"
      class="max-h-96 overflow-auto rounded border border-line bg-canvas p-3 text-xs whitespace-pre-wrap break-words"
      >{{ result }}</pre>
  </section>
</template>
