<script setup lang="ts">
import { computed, onUnmounted, ref, useId, watch } from 'vue'
import { useI18n } from '@/i18n'
import type {
  UsageDimension,
  UsageDimensionsReport,
  UsageBreakdownReport,
  UsagePair,
  UsageStats,
} from '@/types'
import UiIcon from '../UiIcon.vue'
import { useUsageFormat } from './format'
import { rateTone, successRate } from './model'
import { api } from '@/api'
import { UsageCache, UsageRequest } from './loading'

type Tab = UsageDimension | 'status'
type SortKey = 'key' | 'requests' | 'rate' | 'tokens' | 'p50' | 'p95' | 'first' | 'last'

const props = defineProps<{
  report: UsageDimensionsReport
  dimension: Tab
  now: number
  baseQuery: () => URLSearchParams | null
  cache: UsageCache
  ttl: number
}>()
const emit = defineEmits<{
  filter: [dimension: UsageDimension, key: string]
  status: [code: string]
  'update:dimension': [value: Tab]
}>()

const { t, errorText } = useI18n()
const { compact, integer, duration, percent, ago, dimensionLabel } = useUsageFormat()
const id = useId()
const tabs: Tab[] = ['account', 'model', 'channel', 'protocol', 'state', 'status']
const tab = computed({
  get: () => props.dimension,
  set: (value: Tab) => emit('update:dimension', value),
})
const sort = ref<{ key: SortKey; descending: boolean }>({ key: 'requests', descending: true })
const expanded = ref(new Set<string>())
const childRows = ref<Record<string, UsagePair[]>>({})
const childErrors = ref<Record<string, string>>({})
const childLoading = ref(new Set<string>())
const requests = new Map<string, UsageRequest<UsageBreakdownReport>>()
function resetChildren(): void {
  for (const request of requests.values()) request.cancel()
  requests.clear()
  expanded.value = new Set()
  childRows.value = {}
  childErrors.value = {}
  childLoading.value = new Set()
}
watch(() => props.report, resetChildren)
onUnmounted(resetChildren)
async function loadChildren(key: string): Promise<void> {
  const query = props.baseQuery()
  if (!query || (tab.value !== 'account' && tab.value !== 'model')) return
  const currentReport = props.report
  query.set('dimension', tab.value)
  query.set('key', key)
  const request = new UsageRequest<UsageBreakdownReport>(props.cache)
  requests.get(key)?.cancel()
  requests.set(key, request)
  childLoading.value = new Set([...childLoading.value, key])
  delete childErrors.value[key]
  try {
    const result = await request.load(
      `breakdown:${query.toString()}`,
      (signal) => api.usageView('breakdown', query, signal),
      props.ttl,
    )
    if (result && props.report === currentReport && expanded.value.has(key))
      childRows.value[key] = result.value.pairs
  } catch (reason) {
    if (props.report === currentReport && expanded.value.has(key))
      childErrors.value[key] = errorText(reason)
  } finally {
    if (requests.get(key) === request) {
      const next = new Set(childLoading.value)
      next.delete(key)
      childLoading.value = next
    }
  }
}

const columns: { key: SortKey; label: () => string; numeric: boolean }[] = [
  { key: 'key', label: () => t(`usage.dimension.${tab.value}`), numeric: false },
  { key: 'requests', label: () => t('usage.requests'), numeric: true },
  { key: 'rate', label: () => t('usage.successRate'), numeric: true },
  { key: 'tokens', label: () => t('usage.tokens'), numeric: true },
  { key: 'p50', label: () => `${t('usage.duration')} P50`, numeric: true },
  { key: 'p95', label: () => `${t('usage.duration')} P95`, numeric: true },
  { key: 'first', label: () => `${t('usage.firstEvent')} P95`, numeric: true },
  { key: 'last', label: () => t('usage.lastRequest'), numeric: true },
]

// sortValue 返回行在排序列上的比较值
function sortValue(key: string, stats: UsageStats, column: SortKey): number | string {
  switch (column) {
    case 'key':
      return dimensionLabel(tab.value, key).toLowerCase()
    case 'requests':
      return stats.requests
    case 'rate':
      return successRate(stats) ?? -1
    case 'tokens':
      return stats.total_tokens
    case 'p50':
      return stats.duration.p50_ms
    case 'p95':
      return stats.duration.p95_ms
    case 'first':
      return stats.first_event.p95_ms
    case 'last':
      return stats.last_at ? new Date(stats.last_at).getTime() : 0
  }
}

const rows = computed(() => {
  const { key, descending } = sort.value
  return [...props.report.groups].sort((a, b) => {
    const left = sortValue(a.key, a, key)
    const right = sortValue(b.key, b, key)
    const order = left < right ? -1 : left > right ? 1 : 0
    return descending ? -order : order
  })
})

// children 返回账户或模型行展开后的组合明细
function children(key: string): { key: string; label: string; stats: UsageStats }[] {
  if (tab.value === 'account')
    return (childRows.value[key] ?? [])
      .filter((pair) => pair.account === key)
      .map((pair) => ({ key: pair.model, label: dimensionLabel('model', pair.model), stats: pair }))
  if (tab.value === 'model')
    return (childRows.value[key] ?? [])
      .filter((pair) => pair.model === key)
      .map((pair) => ({
        key: pair.account,
        label: dimensionLabel('account', pair.account),
        stats: pair,
      }))
  return []
}

// toggleSort 切换排序列，再次点击同一列时反转方向
function toggleSort(key: SortKey): void {
  sort.value =
    sort.value.key === key
      ? { key, descending: !sort.value.descending }
      : { key, descending: key !== 'key' }
}

// toggleRow 展开或收起一行的组合明细
function toggleRow(key: string): void {
  const next = new Set(expanded.value)
  if (next.has(key)) {
    next.delete(key)
    requests.get(key)?.cancel()
  } else {
    next.add(key)
    void loadChildren(key)
  }
  expanded.value = next
}

// applyFilter 把行取值加入筛选；状态码行筛选请求记录
function applyFilter(key: string): void {
  if (tab.value === 'status') emit('status', key)
  else emit('filter', tab.value, key)
}

const total = computed(() => props.report.requests)
</script>

<template>
  <section class="min-w-0 rounded-lg border border-line bg-panel">
    <header class="flex flex-wrap items-center gap-3 border-b border-line px-4 pt-3">
      <h3 class="mr-auto pb-3 text-sm font-medium text-gray-200">{{ t('usage.ranking') }}</h3>
      <div role="tablist" :aria-label="t('usage.ranking')" class="flex gap-1 overflow-x-auto">
        <button
          v-for="item in tabs"
          :id="`${id}-tab-${item}`"
          :key="item"
          type="button"
          role="tab"
          :aria-selected="tab === item"
          :aria-controls="`${id}-panel`"
          :tabindex="tab === item ? 0 : -1"
          class="border-b-2 px-2.5 pb-2.5 text-xs whitespace-nowrap transition-colors"
          :class="
            tab === item
              ? 'border-blue-500 text-white'
              : 'border-transparent text-gray-400 hover:text-gray-200'
          "
          @click="tab = item"
          @keydown.right.prevent="tab = tabs[(tabs.indexOf(item) + 1) % tabs.length]!"
          @keydown.left.prevent="tab = tabs[(tabs.indexOf(item) + tabs.length - 1) % tabs.length]!"
        >
          {{ t(`usage.dimension.${item}`) }}
          <span class="ml-1 text-gray-500 tabular-nums">{{
            item === tab ? report.groups.length : ''
          }}</span>
        </button>
      </div>
    </header>
    <div
      :id="`${id}-panel`"
      role="tabpanel"
      :aria-labelledby="`${id}-tab-${tab}`"
      class="relative overflow-x-auto"
    >
      <table class="w-full min-w-[56rem] text-left text-xs whitespace-nowrap">
        <thead class="text-gray-500">
          <tr>
            <th
              v-for="column in columns"
              :key="column.key"
              scope="col"
              class="py-2 font-normal"
              :class="column.numeric ? 'px-2 text-right' : 'px-4'"
              :aria-sort="
                sort.key === column.key ? (sort.descending ? 'descending' : 'ascending') : 'none'
              "
            >
              <button
                type="button"
                class="inline-flex items-center gap-1 hover:text-gray-200"
                :class="sort.key === column.key ? 'text-gray-200' : ''"
                @click="toggleSort(column.key)"
              >
                {{ column.label() }}
                <UiIcon
                  v-if="sort.key === column.key"
                  name="chevronDown"
                  :size="11"
                  :class="sort.descending ? '' : 'rotate-180'"
                />
              </button>
            </th>
            <th scope="col" class="w-10 px-2">
              <span class="sr-only">{{ t('usage.onlyThis') }}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          <template v-for="row in rows" :key="row.key">
            <tr class="border-t border-line/60 text-gray-300 hover:bg-raised/40">
              <th scope="row" class="max-w-72 px-4 py-2 font-normal">
                <div class="flex items-center gap-1.5">
                  <button
                    v-if="tab === 'account' || tab === 'model'"
                    type="button"
                    class="rounded p-0.5 text-gray-500 hover:bg-raised hover:text-white"
                    :aria-expanded="expanded.has(row.key)"
                    :aria-label="
                      expanded.has(row.key) ? t('usage.hideBreakdown') : t('usage.showBreakdown')
                    "
                    @click="toggleRow(row.key)"
                  >
                    <UiIcon
                      name="chevronRight"
                      :size="12"
                      class="transition-transform"
                      :class="expanded.has(row.key) ? 'rotate-90' : ''"
                    />
                  </button>
                  <span v-else class="w-4"></span>
                  <span v-tooltip="dimensionLabel(tab, row.key)" class="truncate text-gray-200">{{
                    dimensionLabel(tab, row.key)
                  }}</span>
                </div>
                <div class="mt-1 ml-5 h-1 rounded-full bg-raised">
                  <div
                    class="h-1 rounded-full bg-blue-500/70"
                    :style="{ width: `${Math.max((row.requests / Math.max(total, 1)) * 100, 1)}%` }"
                  ></div>
                </div>
              </th>
              <td class="px-2 py-2 text-right tabular-nums">
                {{ integer(row.requests) }}
                <span class="ml-1 text-gray-500">{{
                  percent(row.requests / Math.max(total, 1), 0)
                }}</span>
              </td>
              <td class="px-2 py-2 text-right tabular-nums" :class="rateTone(successRate(row))">
                {{ percent(successRate(row)) }}
              </td>
              <td class="px-2 py-2 text-right tabular-nums">{{ compact(row.total_tokens) }}</td>
              <td class="px-2 py-2 text-right tabular-nums">{{ duration(row.duration.p50_ms) }}</td>
              <td class="px-2 py-2 text-right tabular-nums">{{ duration(row.duration.p95_ms) }}</td>
              <td class="px-2 py-2 text-right tabular-nums">
                {{ duration(row.first_event.p95_ms) }}
              </td>
              <td class="px-2 py-2 text-right text-gray-400">
                {{ row.last_at ? ago(row.last_at, now) : '—' }}
              </td>
              <td class="px-2 py-2 text-right">
                <button
                  v-tooltip="t('usage.onlyThis')"
                  type="button"
                  class="rounded p-1 text-gray-500 hover:bg-raised hover:text-white"
                  :aria-label="`${t('usage.onlyThis')}: ${dimensionLabel(tab, row.key)}`"
                  @click="applyFilter(row.key)"
                >
                  <UiIcon name="filter" :size="13" />
                </button>
              </td>
            </tr>
            <template v-if="expanded.has(row.key)">
              <tr
                v-if="
                  childLoading.has(row.key) ||
                  childErrors[row.key] ||
                  children(row.key).length === 0
                "
                class="border-t border-line/60"
              >
                <td
                  colspan="9"
                  class="px-11 py-3 text-gray-400"
                  :role="childErrors[row.key] ? 'alert' : 'status'"
                >
                  {{
                    childErrors[row.key] ||
                    (childLoading.has(row.key) ? t('common.loading') : t('usage.recordsEmpty'))
                  }}
                  <button
                    v-if="childErrors[row.key]"
                    type="button"
                    class="ml-3 text-blue-300"
                    @click="loadChildren(row.key)"
                  >
                    {{ t('usage.retry') }}
                  </button>
                </td>
              </tr>
              <tr
                v-for="child in children(row.key)"
                :key="`${row.key}/${child.key}`"
                class="bg-canvas/60 text-gray-400"
              >
                <th scope="row" class="max-w-72 py-1.5 pr-4 pl-11 font-normal">
                  <span v-tooltip="child.label" class="block truncate">{{ child.label }}</span>
                </th>
                <td class="px-2 py-1.5 text-right tabular-nums">
                  {{ integer(child.stats.requests) }}
                </td>
                <td
                  class="px-2 py-1.5 text-right tabular-nums"
                  :class="rateTone(successRate(child.stats))"
                >
                  {{ percent(successRate(child.stats)) }}
                </td>
                <td class="px-2 py-1.5 text-right tabular-nums">
                  {{ compact(child.stats.total_tokens) }}
                </td>
                <td class="px-2 py-1.5 text-right tabular-nums">
                  {{ duration(child.stats.duration.p50_ms) }}
                </td>
                <td class="px-2 py-1.5 text-right tabular-nums">
                  {{ duration(child.stats.duration.p95_ms) }}
                </td>
                <td class="px-2 py-1.5 text-right tabular-nums">
                  {{ duration(child.stats.first_event.p95_ms) }}
                </td>
                <td class="px-2 py-1.5 text-right">
                  {{ child.stats.last_at ? ago(child.stats.last_at, now) : '—' }}
                </td>
                <td></td>
              </tr>
            </template>
          </template>
        </tbody>
      </table>
    </div>
  </section>
</template>
