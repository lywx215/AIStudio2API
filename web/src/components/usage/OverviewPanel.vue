<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from '@/i18n'
import type { UsageOverviewReport, UsageStats } from '@/types'
import KpiCard from './KpiCard.vue'
import { useUsageFormat } from './format'
import { rateTone, successRate } from './model'
const props = defineProps<{ report: UsageOverviewReport }>()
const { t } = useI18n()
const { compact, integer, usd, percent, duration, dateTime, bucketLabel } = useUsageFormat()
const report = computed(() => props.report)
// relative 返回相对变化比例，上一周期为 0 时返回 null
function relative(current: number, previous: number): number | null {
  return previous > 0 ? (current - previous) / previous : null
}

// delta 生成相对上一周期的变化标注
function delta(
  change: number | null,
  text: (value: number) => string,
  better: 'up' | 'down' | 'none',
):
  | {
      text: string
      tone: 'good' | 'bad' | 'neutral'
      direction: 'up' | 'down'
      help: string
    }
  | undefined {
  const value = report.value
  if (change === null || value === null || value.previous.requests === 0) return undefined
  const span = new Date(value.to).getTime() - new Date(value.from).getTime()
  const range = `${dateTime(new Date(new Date(value.from).getTime() - span))} – ${dateTime(value.from)}`
  const flat = Math.abs(change) < 0.0005
  return {
    text: `${change >= 0 ? '+' : '−'}${text(Math.abs(change))}`,
    tone: flat || better === 'none' ? 'neutral' : change > 0 === (better === 'up') ? 'good' : 'bad',
    direction: change >= 0 ? 'up' : 'down',
    help: t('usage.vsPrevious').replace('{range}', range),
  }
}

const kpis = computed(() => {
  const value = report.value
  if (value === null) return []
  const { totals, previous, buckets, recent } = value
  const rate = successRate(totals)
  const previousRate = successRate(previous)
  const series = (pick: (bucket: UsageStats) => number | null) =>
    buckets.map(pick).filter((item): item is number => item !== null)
  const otherErrors = totals.failed - totals.rate_limited
  const first = totals.first_event
  return [
    {
      label: t('usage.requests'),
      icon: 'requests' as const,
      value: integer(totals.requests),
      raw: totals.requests,
      help: t('usage.requestsHelp'),
      details: [
        {
          label: t('usage.failed'),
          value: integer(totals.failed),
          tone: totals.failed > 0 ? 'text-red-400' : undefined,
        },
        { label: t('usage.canceled'), value: integer(totals.canceled) },
      ],
      delta: delta(relative(totals.requests, previous.requests), (item) => percent(item), 'none'),
      spark: buckets.map((bucket) => bucket.requests),
      color: '#60a5fa',
    },
    {
      label: t('usage.successRate'),
      icon: 'success' as const,
      value: percent(rate),
      raw: rate,
      help: t('usage.successRateHelp'),
      details: [
        {
          label: t('usage.rateLimitedShort'),
          value: integer(totals.rate_limited),
          tone: totals.rate_limited > 0 ? 'text-yellow-400' : undefined,
        },
        {
          label: t('usage.otherErrors'),
          value: integer(otherErrors),
          tone: otherErrors > 0 ? 'text-red-400' : undefined,
        },
      ],
      delta: delta(
        rate === null || previousRate === null ? null : rate - previousRate,
        (item) => t('usage.points').replace('{value}', (item * 100).toFixed(1)),
        'up',
      ),
      spark: series((bucket) => {
        const item = successRate(bucket)
        return item === null ? null : item * 100
      }),
      color: '#22c55e',
      tone: rateTone(rate),
    },
    {
      label: t('usage.tokens'),
      icon: 'tokens' as const,
      value: compact(totals.total_tokens),
      raw: totals.total_tokens,
      help: `${t('usage.input')} + ${t('usage.output')}`,
      details: [
        { label: t('usage.input'), value: compact(totals.input_tokens) },
        { label: t('usage.cached'), value: compact(totals.cached_tokens) },
        { label: t('usage.output'), value: compact(totals.reasoning_tokens + totals.reply_tokens) },
      ],
      delta: delta(
        relative(totals.total_tokens, previous.total_tokens),
        (item) => percent(item),
        'none',
      ),
      spark: buckets.map((bucket) => bucket.total_tokens),
      color: '#2dd4bf',
    },
    {
      label: t('usage.throughput'),
      icon: 'throughput' as const,
      value: `${compact(recent.requests / recent.minutes)} RPM`,
      raw: recent.requests / recent.minutes,
      help: t('usage.throughputHelp').replace('{minutes}', String(recent.minutes)),
      details: [
        { label: t('usage.recentMinutes').replace('{minutes}', String(recent.minutes)), value: '' },
        { label: 'TPM', value: compact(recent.tokens / recent.minutes) },
      ],
      spark: buckets.map((bucket) => bucket.requests),
      color: '#f59e0b',
    },
    {
      label: t('usage.cost'),
      icon: 'cost' as const,
      value: value.cost === 0 && value.unpriced_models.length > 0 ? '—' : usd(value.cost),
      raw: value.cost === 0 && value.unpriced_models.length > 0 ? null : value.cost,
      help:
        value.unpriced_models.length > 0
          ? `${t('usage.costHelp')} · ${t('usage.unpriced')} ${value.unpriced_models.join(', ')}`
          : t('usage.costHelp'),
      details: [
        {
          label: t('usage.cacheHitRate'),
          value:
            totals.input_tokens > 0 ? percent(totals.cached_tokens / totals.input_tokens) : '—',
        },
        ...(value.cost === 0 && value.unpriced_models.length > 0
          ? [
              {
                label: t('usage.unpriced'),
                value: value.unpriced_models.join(', '),
                tone: 'text-amber-300',
              },
            ]
          : []),
      ],
      delta: delta(relative(value.cost, value.previous_cost), (item) => percent(item), 'none'),
      spark: buckets.map((bucket) => bucket.cost),
      color: '#34d399',
    },
    {
      label: `${t('usage.firstEvent')} P50`,
      icon: 'firstEvent' as const,
      value: duration(first.p50_ms),
      raw: first.p50_ms > 0 ? first.p50_ms : null,
      help: t('usage.firstEventHelp'),
      details: [{ label: 'P95', value: duration(first.p95_ms) }],
      delta: delta(
        relative(first.p50_ms, previous.first_event.p50_ms),
        (item) => percent(item),
        'down',
      ),
      spark: series((bucket) => (bucket.first_event.p50_ms > 0 ? bucket.first_event.p50_ms : null)),
      color: '#a78bfa',
    },
  ]
})

const traffic = computed(() => {
  const values = props.report.buckets.map((bucket) => bucket.requests)
  const peak = Math.max(1, ...values)
  return values
    .map(
      (value, index) =>
        `${index === 0 ? 'M' : 'L'}${((index / Math.max(1, values.length - 1)) * 960 + 20).toFixed(2)},${(156 - (value / peak) * 136).toFixed(2)}`,
    )
    .join(' ')
})
const ticks = computed(() => {
  const buckets = props.report.buckets
  return [0, Math.floor((buckets.length - 1) / 2), buckets.length - 1]
    .filter((value, index, array) => value >= 0 && array.indexOf(value) === index)
    .map((index) => ({
      x: (index / Math.max(1, buckets.length - 1)) * 960 + 20,
      label: bucketLabel(
        buckets[index]!.at,
        props.report.bucket_seconds,
        new Date(props.report.to).getTime() - new Date(props.report.from).getTime() > 86400000,
      ),
    }))
})
</script>
<template>
  <div class="space-y-4">
    <div
      class="grid grid-cols-1 gap-3 @min-[37rem]:grid-cols-2 @min-[56rem]:grid-cols-3 @min-[110rem]:grid-cols-6"
    >
      <KpiCard v-for="kpi in kpis" :key="kpi.label" v-bind="kpi" />
    </div>
    <figure class="rounded-lg border border-line bg-panel p-4">
      <figcaption class="mb-3 text-sm font-medium text-gray-200">
        {{ t('usage.chartTraffic') }}
      </figcaption>
      <svg
        viewBox="0 0 1000 200"
        class="h-52 w-full text-blue-400"
        role="img"
        :aria-label="`${t('usage.chartTraffic')}: ${integer(report.totals.requests)}`"
      >
        <path d="M20 156 H980" fill="none" stroke="currentColor" opacity="0.15" />
        <path
          :d="traffic"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          vector-effect="non-scaling-stroke"
        />
        <text
          v-for="tick in ticks"
          :key="tick.x"
          :x="tick.x"
          y="187"
          :text-anchor="tick.x === 20 ? 'start' : tick.x === 980 ? 'end' : 'middle'"
          class="fill-gray-500 text-xs"
        >
          {{ tick.label }}
        </text>
      </svg>
    </figure>
  </div>
</template>
