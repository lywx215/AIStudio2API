<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { api } from '@/api'
import { useI18n } from '@/i18n'
import type {
  Model,
  UsageDimension,
  UsagePage,
  UsageRankingDimension,
  UsageOverviewReport,
  UsageTrendsReport,
  UsageDimensionsReport,
  UsageRecordsMeta,
} from '@/types'
import UiIcon from './UiIcon.vue'
import UiSelect from './UiSelect.vue'
import FilterMenu from './usage/FilterMenu.vue'
import RangePicker from './usage/RangePicker.vue'
import { useUsageFormat } from './usage/format'
import { readPreference, writePreference } from '@/preference'
import { dimensions, liveRange, resolveRange, useUsageState, type RangePreset } from './usage/model'
import { UsageCache, UsageRequest, usageRefreshDue } from './usage/loading'

const OverviewPanel = defineAsyncComponent(() => import('./usage/OverviewPanel.vue'))
const UsageCharts = defineAsyncComponent(() => import('./usage/UsageCharts.vue'))
const RankingTable = defineAsyncComponent(() => import('./usage/RankingTable.vue'))
const RecordsTable = defineAsyncComponent(() => import('./usage/RecordsTable.vue'))
const props = defineProps<{ models: Model[] }>()
const { t, errorText } = useI18n()
const { clock, ago, dimensionLabel } = useUsageFormat()
const state = useUsageState()
const pages: UsagePage[] = ['overview', 'trends', 'dimensions', 'records']
const rankingDimensions: UsageRankingDimension[] = [...dimensions, 'status']
const rankingDimension = ref<UsageRankingDimension>(
  readPreference('usage.rankingTab', 'account', (value) =>
    rankingDimensions.includes(value as UsageRankingDimension),
  ),
)
watch(rankingDimension, (value) => writePreference('usage.rankingTab', value))
type Report = UsageOverviewReport | UsageTrendsReport | UsageDimensionsReport | UsageRecordsMeta
const report = shallowRef<Report | null>(null)
const overviewReport = computed(() =>
  state.view === 'overview' ? (report.value as UsageOverviewReport | null) : null,
)
const trendsReport = computed(() =>
  state.view === 'trends' ? (report.value as UsageTrendsReport | null) : null,
)
const dimensionsReport = computed(() =>
  state.view === 'dimensions' ? (report.value as UsageDimensionsReport | null) : null,
)
const recordsMeta = computed(() =>
  state.view === 'records' ? (report.value as UsageRecordsMeta | null) : null,
)
const status = ref<'loading' | 'ready' | 'error'>('loading')
const loading = ref(false)
const error = ref('')
const updatedAt = ref(0)
const lastAttempt = ref(0)
const now = ref(Date.now())
const anchor = ref(now.value)
const recordsRefresh = ref(0)
const recordsPage = ref(0)
const recordsBusy = ref(false)
const refreshChoices = [0, 15, 30, 60] as const
const autoRefresh = ref<number>(
  readPreference('usage.autoRefresh', 30, (value) =>
    refreshChoices.includes(value as 0 | 15 | 30 | 60),
  ),
)
watch(autoRefresh, (value) => writePreference('usage.autoRefresh', value))
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
const cache = new UsageCache()
const request = new UsageRequest<Report>(cache)
const live = computed(() => liveRange(state.range))
const resolved = computed(() => resolveRange(state, new Date(anchor.value)))
const ttl = computed(() => (live.value ? 30000 : Infinity))
const hasFilters = computed(() => dimensions.some((item) => (state.filters[item] ?? []).length > 0))
const scope = computed(() => JSON.stringify([state.range, state.from, state.to, state.filters]))
let clockTimer: number | undefined
let generation = 0

function filterOptions(dimension: UsageDimension): string[] {
  const options = report.value?.options[dimension] ?? []
  if (dimension !== 'model') return options
  const order = new Map(props.models.map((model, index) => [model.id, index]))
  return [...options].sort(
    (left, right) => (order.get(left) ?? order.size) - (order.get(right) ?? order.size),
  )
}
function baseQuery(): URLSearchParams | null {
  const range = resolved.value
  if (range === null) return null
  const query = new URLSearchParams({ from: range.from.toISOString(), to: range.to.toISOString() })
  for (const dimension of dimensions) {
    const values = state.filters[dimension] ?? []
    if (values.length > 0) query.set(dimension, values.join(','))
  }
  return query
}
const recordScope = computed(() => baseQuery()?.toString() ?? '')
const logicalKey = computed(() =>
  JSON.stringify([
    scope.value,
    state.view,
    state.view === 'trends' ? state.stack : '',
    state.view === 'dimensions' ? rankingDimension.value : '',
  ]),
)
let displayedKey = ''
async function load(force = false): Promise<void> {
  if (live.value && Date.now() - anchor.value >= 30000) anchor.value = Date.now()
  const query = baseQuery()
  const current = ++generation
  request.cancel()
  if (displayedKey !== logicalKey.value) {
    report.value = null
    updatedAt.value = 0
    displayedKey = logicalKey.value
  }
  error.value = ''
  if (query === null) {
    loading.value = false
    error.value = t('usage.rangeInvalid')
    status.value = report.value === null ? 'error' : 'ready'
    return
  }
  query.set('tz', timezone)
  if (state.view === 'trends') query.set('stack', state.stack)
  if (state.view === 'dimensions') query.set('dimension', rankingDimension.value)
  const view = state.view === 'records' ? 'records-meta' : state.view
  const key = `report:${view}:${query.toString()}`
  loading.value = true
  status.value = report.value === null ? 'loading' : 'ready'
  lastAttempt.value = Date.now()
  try {
    const result = await request.load(
      key,
      (signal) => api.usageView(view, query, signal),
      ttl.value,
      force,
    )
    if (!result || current !== generation) return
    report.value = result.value
    updatedAt.value = result.updatedAt
    lastAttempt.value = result.updatedAt
    status.value = 'ready'
  } catch (reason) {
    if (current !== generation) return
    error.value = errorText(reason)
    status.value = report.value === null ? 'error' : 'ready'
  } finally {
    if (current === generation) loading.value = false
  }
}
function refresh(manual = true): void {
  if (manual) cache.clear()
  if (live.value) anchor.value = Date.now()
  if (state.view === 'records') recordsRefresh.value++
  void load(true)
}
function tick(): void {
  now.value = Date.now()
  if (
    usageRefreshDue({
      interval: autoRefresh.value,
      live: live.value,
      visible: document.visibilityState === 'visible',
      loading: loading.value || recordsBusy.value,
      page: state.view === 'records' ? recordsPage.value : 0,
      lastAttempt: lastAttempt.value,
      now: now.value,
    })
  )
    refresh(false)
}
watch(scope, () => {
  anchor.value = Date.now()
})
watch(logicalKey, () => {
  recordsPage.value = 0
  recordsBusy.value = false
  void load()
})
onMounted(() => {
  void load()
  clockTimer = window.setInterval(tick, 1000)
  document.addEventListener('visibilitychange', tick)
})
onUnmounted(() => {
  request.cancel()
  cache.clear()
  window.clearInterval(clockTimer)
  document.removeEventListener('visibilitychange', tick)
})
function applyRange(range: RangePreset, from: string, to: string): void {
  Object.assign(state, { range, from, to })
}
function setFilter(dimension: UsageDimension, values: string[]): void {
  const filters = { ...state.filters }
  if (values.length === 0) delete filters[dimension]
  else filters[dimension] = values
  state.filters = filters
}
function addFilter(dimension: UsageDimension, value: string): void {
  const values = state.filters[dimension] ?? []
  if (!values.includes(value)) setFilter(dimension, [...values, value])
}
function clearFilters(): void {
  state.filters = {}
}
function selectPage(page: UsagePage): void {
  if (page === state.view) return
  const url = new URL(window.location.href)
  if (page === 'overview') url.searchParams.delete('usage_view')
  else url.searchParams.set('usage_view', page)
  window.history.pushState(null, '', url)
  state.view = page
}
function filterStatus(code: string): void {
  state.status = code
  selectPage('records')
}
const activeChips = computed(() =>
  dimensions.flatMap((dimension) =>
    (state.filters[dimension] ?? []).map((value) => ({ dimension, value })),
  ),
)
const freshness = computed(() => {
  const latest = report.value?.latest_at
  if (latest === undefined) return { tone: 'bg-gray-600', text: t('usage.latestNone') }
  const age = now.value - new Date(latest).getTime()
  return {
    tone: age < 300000 ? 'bg-green-500' : age < 3600000 ? 'bg-yellow-500' : 'bg-gray-500',
    text: t('usage.latest').replace('{time}', ago(latest, now.value)),
  }
})
const countdown = computed(() => {
  if (!live.value) return t('usage.fixedRange')
  if (state.view === 'records' && recordsPage.value > 0) return t('usage.refreshPaused')
  if (autoRefresh.value === 0 || updatedAt.value === 0) return ''
  const seconds = Math.max(
    0,
    autoRefresh.value - Math.floor((now.value - lastAttempt.value) / 1000),
  )
  return t('usage.nextRefresh').replace('{seconds}', String(seconds))
})
</script>
<template>
  <section class="flex min-h-0 flex-1 flex-col overflow-auto">
    <header
      class="flex shrink-0 flex-wrap items-center gap-3 border-b border-line px-4 py-4 md:px-8"
    >
      <div class="min-w-0 flex-1">
        <h2 class="text-xl font-semibold text-white">{{ t('usage.title') }}</h2>
        <p class="mt-1 text-sm text-gray-500">{{ t('usage.description') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-gray-500">
        <span v-tooltip="t('usage.latestHelp')" class="flex items-center gap-1.5">
          <span class="h-2 w-2 rounded-full" :class="freshness.tone" aria-hidden="true"></span>
          {{ freshness.text }}
        </span>
        <span v-if="updatedAt > 0" class="tabular-nums" aria-live="polite">
          {{ t('usage.updatedAt').replace('{time}', clock(new Date(updatedAt))) }}
          <span v-if="countdown" class="ml-1 text-gray-600">· {{ countdown }}</span>
        </span>
        <UiSelect
          v-if="live"
          :model-value="autoRefresh"
          :aria-label="t('usage.autoRefresh')"
          class="w-max rounded-md border border-line bg-canvas px-2 py-1 text-xs text-gray-200"
          @update:model-value="autoRefresh = Number($event)"
        >
          <option v-for="choice in refreshChoices" :key="choice" :value="choice">
            {{ t(`usage.autoRefresh.${choice}`) }}
          </option>
        </UiSelect>
        <button
          v-tooltip="t('app.refresh')"
          type="button"
          class="rounded-md border border-line p-1.5 text-gray-400 transition hover:bg-raised hover:text-white"
          :aria-label="t('app.refresh')"
          @click="refresh()"
        >
          <UiIcon name="refresh" :size="14" :class="{ 'animate-spin': loading }" />
        </button>
      </div>
    </header>

    <div class="sticky top-0 z-10 shrink-0 bg-canvas">
      <div
        class="flex shrink-0 flex-wrap items-center gap-2 border-b border-line bg-canvas/95 px-4 py-2.5 backdrop-blur md:px-8"
        role="toolbar"
        :aria-label="t('usage.filters')"
      >
        <RangePicker :range="state.range" :resolved="resolved" @apply="applyRange" />
        <span class="mx-1 hidden h-5 w-px bg-line sm:block" aria-hidden="true"></span>
        <FilterMenu
          v-for="dimension in dimensions"
          :key="dimension"
          :label="t(`usage.dimension.${dimension}`)"
          :options="filterOptions(dimension)"
          :selected="state.filters[dimension] ?? []"
          :format="(value: string) => dimensionLabel(dimension, value)"
          @change="setFilter(dimension, $event)"
        />
        <button
          v-if="hasFilters"
          type="button"
          class="rounded-md px-2 py-1.5 text-xs text-gray-400 hover:bg-raised hover:text-white"
          @click="clearFilters"
        >
          {{ t('usage.filtersClear') }}
        </button>
      </div>
      <div v-if="activeChips.length > 0" class="flex flex-wrap gap-1.5 px-4 pt-3 md:px-8">
        <span
          v-for="chip in activeChips"
          :key="`${chip.dimension}:${chip.value}`"
          class="flex max-w-full items-center gap-1 rounded-md border border-blue-500/40 bg-blue-600/10 py-0.5 pr-1 pl-2.5 text-xs text-blue-200"
        >
          <span class="shrink-0 text-blue-300/70"
            >{{ t(`usage.dimension.${chip.dimension}`) }}:</span
          >
          <span
            v-tooltip.overflow="dimensionLabel(chip.dimension, chip.value)"
            class="min-w-0 truncate"
            >{{ dimensionLabel(chip.dimension, chip.value) }}</span
          >
          <button
            type="button"
            class="shrink-0 rounded-sm p-0.5 hover:bg-blue-500/20"
            :aria-label="`${t('common.close')} ${dimensionLabel(chip.dimension, chip.value)}`"
            @click="
              setFilter(
                chip.dimension,
                (state.filters[chip.dimension] ?? []).filter((item) => item !== chip.value),
              )
            "
          >
            <UiIcon name="close" :size="11" />
          </button>
        </span>
      </div>

      <nav
        class="flex shrink-0 gap-1 overflow-x-auto border-b border-line px-4 md:px-8"
        :aria-label="t('usage.title')"
        role="tablist"
      >
        <button
          v-for="page in pages"
          :id="`usage-tab-${page}`"
          :key="page"
          type="button"
          role="tab"
          :aria-selected="state.view === page"
          :aria-controls="`usage-panel-${page}`"
          :tabindex="state.view === page ? 0 : -1"
          class="border-b-2 px-4 py-3 text-sm whitespace-nowrap"
          :class="
            state.view === page
              ? 'border-blue-500 text-white'
              : 'border-transparent text-gray-400 hover:text-gray-200'
          "
          @click="selectPage(page)"
          @keydown.right.prevent="selectPage(pages[(pages.indexOf(page) + 1) % pages.length]!)"
          @keydown.left.prevent="
            selectPage(pages[(pages.indexOf(page) + pages.length - 1) % pages.length]!)
          "
        >
          {{ t(`usage.view.${page}`) }}
        </button>
      </nav>
    </div>
    <div
      :id="`usage-panel-${state.view}`"
      role="tabpanel"
      :aria-labelledby="`usage-tab-${state.view}`"
      class="@container w-full shrink-0 space-y-4 p-4 md:p-8"
      :aria-busy="loading"
    >
      <div
        v-if="error"
        role="alert"
        class="flex items-center gap-3 rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-3 text-sm text-red-300"
      >
        <UiIcon name="warning" :size="16" /><span class="flex-1">{{ error }}</span>
        <button
          type="button"
          class="rounded-md border border-red-500/40 px-3 py-1 text-xs"
          @click="load(true)"
        >
          {{ t('usage.retry') }}
        </button>
      </div>
      <div v-if="status === 'loading'" role="status" class="space-y-4">
        <div class="h-32 animate-pulse rounded-lg bg-panel" aria-hidden="true"></div>
        <div class="h-72 animate-pulse rounded-lg bg-panel" aria-hidden="true"></div>
        <span class="sr-only">{{ t('common.loading') }}</span>
      </div>
      <OverviewPanel v-if="overviewReport" :report="overviewReport" />
      <UsageCharts
        v-else-if="trendsReport"
        :report="trendsReport"
        :stack="state.stack"
        @update:stack="state.stack = $event"
      />
      <RankingTable
        v-else-if="dimensionsReport"
        :report="dimensionsReport"
        :dimension="rankingDimension"
        :now="now"
        :base-query="baseQuery"
        :cache="cache"
        :ttl="ttl"
        @update:dimension="rankingDimension = $event"
        @filter="addFilter"
        @status="filterStatus"
      />
      <RecordsTable
        v-else-if="recordsMeta"
        :scope="recordScope"
        :base-query="baseQuery"
        :status="state.status"
        :search="state.search"
        :total="recordsMeta.requests"
        :status-options="recordsMeta.statuses"
        :refresh-key="recordsRefresh"
        :cache="cache"
        :ttl="ttl"
        @page="recordsPage = $event"
        @busy="recordsBusy = $event"
        @update:status="state.status = $event"
        @update:search="state.search = $event"
      />
    </div>
  </section>
</template>
