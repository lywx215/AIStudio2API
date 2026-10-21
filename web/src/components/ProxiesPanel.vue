<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ApiError } from '@/api'
import {
  proxyAPI,
  jobActive,
  useProxyText,
  type ProxySource,
  type SourceDraft,
  type ProxyPage,
  type ProxyStats,
  type ProxyQuery,
} from '@/proxy-management'
import { useProxyJob } from '@/useProxyJob'
import DetailDialog from './DetailDialog.vue'
import ProxyJobResults from './ProxyJobResults.vue'
const props = defineProps<{ consolePasswordSession: boolean }>()
const text = useProxyText()
const sources = ref<ProxySource[]>([]),
  page = ref<ProxyPage>({ items: [], total: 0, offset: 0, limit: 50 }),
  stats = ref<ProxyStats | null>(null)
const query = reactive<ProxyQuery>({
  search: '',
  source_id: '',
  protocol: '',
  state: '',
  offset: 0,
  limit: 50,
})
const selected = ref<string[]>([]),
  loading = ref(false),
  working = ref(''),
  error = ref(''),
  notice = ref(''),
  showSource = ref(false),
  sourceID = ref(''),
  concurrency = ref(10)
const draft = reactive<SourceDraft>({
  name: '',
  kind: 'webshare',
  protocol: 'socks5',
  url: '',
  enabled: true,
  revision: 0,
})
const jobs = ref<{ id: string; kind: string; status: string }[]>([])
const {
  job,
  error: jobError,
  busy,
  pendingKey,
  submit,
  reconcile,
  stop,
  load: loadJob,
  select: selectJob,
  review,
} = useProxyJob('test', () => void load(false))
const allSelected = computed(
  () => page.value.items.length > 0 && page.value.items.every((p) => selected.value.includes(p.id)),
)
const button =
  'rounded border border-line bg-raised px-3 py-2 text-sm text-gray-300 transition hover:bg-line disabled:opacity-40'
const input = 'rounded border border-line bg-canvas px-3 py-2 text-sm text-white'
function problem(e: unknown) {
  error.value = e instanceof ApiError ? e.code : 'NETWORK_ERROR'
}
async function load(includeSources = true) {
  if (!props.consolePasswordSession) return
  loading.value = true
  try {
    const [rows, numbers] = await Promise.all([proxyAPI.list({ ...query }), proxyAPI.stats()])
    page.value = rows
    stats.value = numbers
    if (includeSources) {
      sources.value = (await proxyAPI.sources()).items
      jobs.value = (await proxyAPI.jobs()).items.filter((j) => j.kind === 'test')
    }
    error.value = ''
  } catch (e) {
    problem(e)
  } finally {
    loading.value = false
  }
}
function edit(source?: ProxySource) {
  sourceID.value = source?.id ?? ''
  Object.assign(draft, {
    name: source?.name ?? '',
    kind: source?.kind ?? 'webshare',
    protocol: source?.protocol ?? 'socks5',
    enabled: source?.enabled ?? true,
    revision: source?.revision ?? 0,
    url: '',
  })
  showSource.value = true
}
function closeSource() {
  showSource.value = false
  draft.url = ''
}
async function save() {
  working.value = 'source'
  try {
    await proxyAPI.saveSource(sourceID.value, { ...draft })
    closeSource()
    await load()
  } catch (e) {
    problem(e)
  } finally {
    working.value = ''
  }
}
async function refresh(source: ProxySource) {
  working.value = source.id
  notice.value = ''
  try {
    const result = await proxyAPI.refresh(source.id)
    notice.value = `${text('updated')}：${result.count}；${text('failed')} ${result.invalid}`
    selected.value = []
    await load()
  } catch (e) {
    problem(e)
  } finally {
    working.value = ''
  }
}
async function remove(source: ProxySource) {
  if (!confirm(`${text('remove')}：${source.name}？`)) return
  try {
    await proxyAPI.deleteSource(source)
    await load()
  } catch (e) {
    problem(e)
  }
}
function filter() {
  query.offset = 0
  selected.value = []
  void load(false)
}
function movePage(offset: number) {
  query.offset = Math.max(0, offset)
  void load(false)
}
function toggle(id: string) {
  selected.value = selected.value.includes(id)
    ? selected.value.filter((v) => v !== id)
    : [...selected.value, id]
}
function togglePage() {
  selected.value = allSelected.value
    ? selected.value.filter((id) => !page.value.items.some((p) => p.id === id))
    : [...new Set([...selected.value, ...page.value.items.map((p) => p.id)])]
}
async function test(all = false, ids = selected.value) {
  await submit({
    kind: 'test',
    all,
    ids: all ? [] : ids,
    query: { ...query },
    concurrency: concurrency.value,
  })
}
onMounted(() => void load())
</script>
<template>
  <section class="min-h-0 flex-1 overflow-auto p-4 md:p-8">
    <header class="mb-6">
      <h2 class="text-xl font-semibold text-white">{{ text('title') }}</h2>
      <p class="mt-1 text-sm text-gray-500">{{ text('description') }}</p>
    </header>
    <p v-if="!consolePasswordSession" class="rounded border border-amber-500/40 p-4 text-amber-400">
      {{ text('passwordRequired') }}
    </p>
    <template v-else>
      <p
        v-if="error || jobError"
        role="alert"
        class="mb-4 rounded border border-red-500/40 p-3 text-red-400"
      >
        {{ text(error || jobError) }}
      </p>
      <p v-if="notice" role="status" class="mb-3 text-sm text-green-400">{{ notice }}</p>
      <div v-if="stats" class="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4 xl:grid-cols-7">
        <div
          v-for="key in [
            'total',
            'passed',
            'failed',
            'restricted',
            'untested',
            'eligible',
            'average',
          ] as const"
          :key="key"
          class="rounded-lg border border-line bg-panel p-4"
        >
          <p class="text-xs text-gray-500">{{ text(key) }}</p>
          <p class="mt-2 text-2xl font-semibold text-white">
            {{ key === 'average' ? Math.round(stats.average_ms) + ' ms' : stats[key] }}
          </p>
        </div>
      </div>
      <section class="mb-6 rounded-lg border border-line bg-panel p-4">
        <div class="mb-3 flex items-center justify-between">
          <h3 class="font-semibold text-white">{{ text('sources') }}</h3>
          <button :class="button" :disabled="!!working" @click="edit()">+ {{ text('add') }}</button>
        </div>
        <div
          v-for="source in sources"
          :key="source.id"
          class="flex flex-wrap items-center gap-3 border-t border-line py-3"
        >
          <div class="min-w-40 flex-1">
            <span class="font-medium text-white">{{ source.name }}</span
            ><span class="ml-2 text-xs text-gray-500"
              >{{ source.protocol.toUpperCase() }} ·
              {{ text(source.enabled ? 'enabled' : 'disabled') }} ·
              {{ text(source.configured ? 'configured' : 'notConfigured') }}</span
            >
            <p class="mt-1 text-xs text-gray-500">
              {{
                source.refreshed_at
                  ? new Date(source.refreshed_at).toLocaleString()
                  : text('manualRefresh')
              }}
              <span class="text-red-400">{{ source.error ? text(source.error) : '' }}</span>
            </p>
          </div>
          <button
            :class="button"
            :disabled="!!working || !source.enabled || !source.configured"
            @click="refresh(source)"
          >
            {{ working === source.id ? text('busy') : text('refresh') }}</button
          ><button :class="button" :disabled="!!working" @click="edit(source)">
            {{ text('edit') }}</button
          ><button
            :class="button"
            :disabled="!!working || !source.configured"
            @click="remove(source)"
          >
            {{ text('remove') }}
          </button>
        </div>
      </section>
      <div class="mb-3 flex flex-wrap items-center gap-2">
        <input
          v-model="query.search"
          :class="input"
          :placeholder="text('search')"
          @keyup.enter="filter"
        /><select v-model="query.source_id" :class="input" @change="filter">
          <option value="">{{ text('sourceAll') }}</option>
          <option v-for="source in sources" :key="source.id" :value="source.id">
            {{ source.name }}
          </option></select
        ><select v-model="query.protocol" :class="input" @change="filter">
          <option value="">{{ text('protocol') }} · {{ text('all') }}</option>
          <option value="socks5">SOCKS5</option>
          <option value="http">HTTP</option>
          <option value="https">HTTPS</option></select
        ><select v-model="query.state" :class="input" @change="filter">
          <option value="">{{ text('state') }} · {{ text('all') }}</option>
          <option
            v-for="value in ['passed', 'failed', 'restricted', 'untested', 'removed', 'disabled']"
            :key="value"
            :value="value"
          >
            {{ text(value) }}
          </option></select
        ><button :class="button" :disabled="loading" @click="filter">
          {{ text('refreshData') }}
        </button>
      </div>
      <div class="mb-3 flex flex-wrap items-center gap-3">
        <button
          :class="button"
          :disabled="!selected.length || busy || jobActive(job) || !!pendingKey"
          @click="test()"
        >
          {{ text('testSelected') }} ({{ selected.length }})</button
        ><button
          :class="button"
          :disabled="!page.total || busy || jobActive(job) || !!pendingKey"
          @click="test(true)"
        >
          {{ text('testAll') }} ({{ page.total }})</button
        ><label class="text-sm text-gray-500"
          >{{ text('concurrency') }}
          <input
            v-model.number="concurrency"
            type="number"
            min="1"
            max="50"
            :class="[input, 'w-20']" /></label
        ><button v-if="pendingKey" :class="button" :disabled="busy" @click="reconcile">
          {{ text('reconcile') }}
        </button>
        <p class="text-xs text-gray-500">{{ text('probeHelp') }}</p>
      </div>
      <p v-if="pendingKey" class="mb-3 text-sm text-amber-400">{{ text('submitUnknown') }}</p>
      <div class="overflow-x-auto rounded-lg border border-line bg-panel">
        <table class="w-full text-left text-sm">
          <thead class="border-b border-line text-xs text-gray-500">
            <tr>
              <th class="p-3">
                <input
                  type="checkbox"
                  :aria-label="text('select')"
                  :checked="allSelected"
                  @change="togglePage"
                />
              </th>
              <th>{{ text('endpoint') }}</th>
              <th>{{ text('sources') }}</th>
              <th>{{ text('state') }}</th>
              <th>{{ text('exitIP') }}</th>
              <th>{{ text('latency') }}</th>
              <th>{{ text('studioLatency') }}</th>
              <th>{{ text('checked') }}</th>
              <th>{{ text('accounts') }}</th>
              <th class="p-3">{{ text('action') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="proxy in page.items" :key="proxy.id" class="border-b border-line/60">
              <td class="p-3">
                <input
                  type="checkbox"
                  :aria-label="proxy.endpoint"
                  :checked="selected.includes(proxy.id)"
                  @change="toggle(proxy.id)"
                />
              </td>
              <td class="font-mono text-xs">
                {{ proxy.endpoint
                }}<span class="block text-gray-500">{{ proxy.protocol.toUpperCase() }}</span>
              </td>
              <td class="max-w-40 truncate pr-3 text-xs" :title="proxy.sources">
                {{ proxy.sources || '—' }}
              </td>
              <td class="py-3 text-xs">
                <span
                  :class="
                    proxy.eligible
                      ? 'text-green-400'
                      : proxy.state === 'failed'
                        ? 'text-red-400'
                        : 'text-gray-400'
                  "
                  >{{
                    text(!proxy.present ? 'removed' : !proxy.enabled ? 'disabled' : proxy.state)
                  }}</span
                ><span v-if="proxy.error" class="block text-gray-500">{{ text(proxy.error) }}</span>
              </td>
              <td class="font-mono text-xs">{{ proxy.exit_ip || '—' }}</td>
              <td>{{ proxy.checked_at ? Math.round(proxy.latency_ms) + ' ms' : '—' }}</td>
              <td>
                {{ proxy.studio_latency_ms ? Math.round(proxy.studio_latency_ms) + ' ms' : '—' }}
              </td>
              <td class="pr-3 text-xs text-gray-500">
                {{ proxy.checked_at ? new Date(proxy.checked_at).toLocaleString() : '—' }}
              </td>
              <td>{{ proxy.accounts }}</td>
              <td class="flex gap-2 p-3">
                <button
                  :class="button"
                  :disabled="busy || jobActive(job) || !proxy.enabled || !!pendingKey"
                  @click="test(false, [proxy.id])"
                >
                  {{ text('test') }}</button
                ><button
                  :class="button"
                  @click="
                    proxyAPI
                      .enabled(proxy)
                      .then(() => load(false))
                      .catch(problem)
                  "
                >
                  {{ text(proxy.enabled ? 'disabled' : 'enabled') }}
                </button>
              </td>
            </tr>
            <tr v-if="!page.items.length">
              <td colspan="10" class="p-8 text-center text-gray-500">
                {{ loading ? text('loading') : text('empty') }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="mt-3 flex justify-end gap-4 text-sm">
        <button
          :class="button"
          :disabled="page.offset === 0 || loading"
          @click="movePage(page.offset - 50)"
        >
          {{ text('previous') }}</button
        ><span class="py-2 text-gray-500"
          >{{ page.total ? page.offset + 1 : 0 }}–{{ Math.min(page.offset + 50, page.total) }} /
          {{ page.total }}</span
        ><button
          :class="button"
          :disabled="page.offset + 50 >= page.total || loading"
          @click="movePage(page.offset + 50)"
        >
          {{ text('next') }}
        </button>
      </div>
      <label v-if="jobs.length" class="mt-4 block text-sm text-gray-500"
        >{{ text('recover') }}
        <select
          :class="input"
          :value="job?.id ?? ''"
          @change="selectJob(($event.target as HTMLSelectElement).value)"
        >
          <option value="" disabled>—</option>
          <option v-for="item in jobs" :key="item.id" :value="item.id">
            {{ item.id.slice(0, 8) }} · {{ text(item.status) }}
          </option>
        </select></label
      >
      <ProxyJobResults v-if="job" :job="job" @page="loadJob" @stop="stop" @reconcile="review" />
      <p class="mt-4 text-xs text-gray-500">{{ text('changeHelp') }}</p>
      <DetailDialog :open="showSource" :title="text(sourceID ? 'edit' : 'add')" @close="closeSource"
        ><form class="space-y-4" @submit.prevent="save">
          <label class="block"
            >{{ text('name')
            }}<input
              v-model.trim="draft.name"
              required
              maxlength="120"
              :class="[input, 'mt-1 w-full']" /></label
          ><label class="block"
            >{{ text('url')
            }}<input
              v-model.trim="draft.url"
              type="password"
              autocomplete="off"
              :required="!sourceID"
              :placeholder="sourceID ? text('keepURL') : 'https://…'"
              :class="[input, 'mt-1 w-full']"
          /></label>
          <div class="flex flex-wrap items-center gap-3">
            <label
              >{{ text('protocol') }}
              <select v-model="draft.protocol" :class="input">
                <option value="socks5">SOCKS5</option>
                <option value="http">HTTP</option>
                <option value="https">HTTPS</option>
              </select></label
            ><label><input v-model="draft.enabled" type="checkbox" /> {{ text('enabled') }}</label>
          </div>
          <div class="flex justify-end gap-2">
            <button type="button" :class="button" @click="closeSource">{{ text('cancel') }}</button
            ><button type="submit" :class="button" :disabled="!!working">{{ text('save') }}</button>
          </div>
        </form></DetailDialog
      >
    </template>
  </section>
</template>
