<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from '@/i18n'
import { ApiError } from '@/api'
import { businessClient } from './client'
import type { BusinessImportPreview } from './types'

const props = defineProps<{ revision: number; disabled?: boolean; unsaved?: boolean }>()
const emit = defineEmits<{ changed: []; busy: [value: boolean] }>()
const { locale } = useI18n()
const text = (zh: string, en: string) => (locale.value === 'zh-CN' ? zh : en)
const source = ref('')
const preview = ref<BusinessImportPreview | null>(null)
const pending = ref(false)
const notice = ref('')
const error = ref('')
const undo = ref<{ revision: number; rollback_id: string } | null>(null)
const canUndo = computed(() => undo.value?.revision === props.revision)
let active = true
let epoch = 0
let controller = new AbortController()
watch([source, () => props.revision], () => {
  preview.value = null
})

function input(): object {
  if (new TextEncoder().encode(source.value).length > 128 * 1024) throw new Error('size')
  const parsed: unknown = JSON.parse(source.value)
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('shape')
  return parsed
}

async function run(action: 'preview' | 'apply' | 'rollback'): Promise<void> {
  if (!active || pending.value || props.disabled) return
  const revision = props.revision
  const runEpoch = epoch
  const original = source.value
  const receipt = preview.value
  if (action === 'apply' && (!receipt || !receipt.added.length || receipt.revision !== revision))
    return
  if (action === 'rollback' && !canUndo.value) return
  pending.value = true
  emit('busy', true)
  notice.value = ''
  error.value = ''
  let changed = false
  try {
    if (action === 'preview') {
      const next = await businessClient.previewImport(
        { revision, source: input() },
        controller.signal,
      )
      if (active && runEpoch === epoch && props.revision === revision && source.value === original)
        preview.value = next
    } else if (action === 'apply') {
      const result = await businessClient.applyImport(
        { revision, source_hash: receipt!.source_hash, source: input() },
        controller.signal,
      )
      if (!active || runEpoch !== epoch) return
      undo.value = { revision: result.config.revision, rollback_id: result.rollback_id }
      preview.value = null
      source.value = ''
      notice.value = text(
        '模型映射已导入，默认停用。核对后可在下方配置中启用。',
        'Model mappings imported and disabled. Verify them in the settings below before enabling.',
      )
      changed = true
    } else {
      await businessClient.rollbackImport(undo.value!, controller.signal)
      if (!active || runEpoch !== epoch) return
      undo.value = null
      preview.value = null
      notice.value = text('已撤销本次导入。', 'Import rolled back.')
      changed = true
    }
  } catch (failure) {
    if (!active || runEpoch !== epoch) return
    preview.value = null
    if (action !== 'preview') {
      undo.value = null
      changed = true
    }
    const stale = failure instanceof ApiError && failure.status === 409
    if (stale) changed = true
    error.value = text(
      action === 'preview' && !stale
        ? '预览失败：仅支持不含凭据的 models 或 bindings JSON，大小不超过 128 KiB。'
        : '操作结果未确认或配置版本已变化。已刷新配置，请核对后重新预览。',
      action === 'preview' && !stale
        ? 'Preview failed. Use credential-free models or bindings JSON up to 128 KiB.'
        : 'The result is uncertain or the revision changed. Settings have been refreshed; check before previewing again.',
    )
  } finally {
    if (active && runEpoch === epoch) {
      pending.value = false
      emit('busy', false)
      if (changed) emit('changed')
    }
  }
}

function hide(): void {
  active = false
  epoch++
  controller.abort()
  source.value = ''
  preview.value = null
  undo.value = null
  pending.value = false
  emit('busy', false)
}
function show(event: PageTransitionEvent): void {
  if (!event.persisted) return
  controller = new AbortController()
  active = true
}
onMounted(() => {
  window.addEventListener('pagehide', hide)
  window.addEventListener('pageshow', show)
})
onUnmounted(() => {
  hide()
  window.removeEventListener('pagehide', hide)
  window.removeEventListener('pageshow', show)
})
</script>

<template>
  <details class="rounded-lg border border-line p-4 text-sm" data-business-import>
    <summary class="cursor-pointer font-medium">
      {{ text('导入 Business 模型配置', 'Import Business model settings') }}
    </summary>
    <p class="my-3 text-gray-400">
      {{
        text(
          '粘贴参考项目的 {"models":[…]} 或本应用的 {"bindings":[…]}。预览后仅追加新模型；同名跳过，新映射默认停用。请勿粘贴 Cookie、API Key 或账号配置。',
          'Paste {"models":[…]} from the reference pool or {"bindings":[…]} from this app. Preview, then append new disabled models; existing names are skipped. Exclude cookies, API keys and accounts.',
        )
      }}
    </p>
    <p v-if="unsaved" class="mb-3 text-amber-200">
      {{
        text(
          '请先保存或重新加载下方未保存的配置，再导入。',
          'Save or reload the unsaved settings below before importing.',
        )
      }}
    </p>
    <textarea
      v-model="source"
      :disabled="pending || disabled"
      rows="6"
      spellcheck="false"
      :aria-label="text('模型配置 JSON', 'Model settings JSON')"
      class="block w-full rounded border border-line bg-canvas p-2 font-mono text-xs"
    />
    <div class="my-3 flex flex-wrap gap-3">
      <button
        type="button"
        :disabled="pending || disabled || !source.trim()"
        class="rounded border border-line px-3 py-2 disabled:opacity-50"
        @click="run('preview')"
      >
        {{ text('预览导入', 'Preview import') }}
      </button>
      <button
        type="button"
        :disabled="pending || disabled || !preview?.added.length"
        class="rounded bg-blue-600 px-3 py-2 text-white disabled:opacity-50"
        @click="run('apply')"
      >
        {{ text('确认导入', 'Apply import') }}
      </button>
      <button
        type="button"
        :disabled="pending || disabled || !canUndo"
        class="rounded border border-line px-3 py-2 disabled:opacity-50"
        @click="run('rollback')"
      >
        {{ text('撤销本次导入', 'Undo this import') }}
      </button>
    </div>
    <div v-if="preview" class="space-y-2" role="status">
      <p>
        {{ text('新增 / 同名跳过', 'New / skipped') }}: {{ preview.added.length }} /
        {{ preview.skipped.length }}
      </p>
      <ul class="list-inside list-disc">
        <li v-for="binding in preview.added" :key="binding.public_id">
          {{ binding.display_name }} · {{ binding.public_id }}
        </li>
      </ul>
      <p v-for="warning in preview.warnings" :key="warning" class="text-amber-200">{{ warning }}</p>
    </div>
    <p class="mt-2 text-xs text-gray-400">
      {{
        text(
          '撤销仅在本次页面会话中可用；其他配置保存或应用重启后失效。',
          'Undo is available in this page session until another settings save or application restart.',
        )
      }}
    </p>
    <p v-if="notice" role="status" class="mt-2 text-green-300">{{ notice }}</p>
    <p v-if="error" role="alert" class="mt-2 text-red-300">{{ error }}</p>
  </details>
</template>
