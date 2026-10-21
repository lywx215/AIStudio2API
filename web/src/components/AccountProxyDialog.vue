<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ApiError } from '@/api'
import {
  proxyAPI,
  jobActive,
  useProxyText,
  type ProxyAccount,
  type ProxySource,
  type ProxyPage,
} from '@/proxy-management'
import { useProxyJob } from '@/useProxyJob'
import DetailDialog from './DetailDialog.vue'
import ProxyJobResults from './ProxyJobResults.vue'
const props = defineProps<{ ids: string[] }>()
const emit = defineEmits<{ close: []; changed: [] }>()
const text = useProxyText()
const accounts = ref<ProxyAccount[]>([]),
  sources = ref<ProxySource[]>([]),
  sourceID = ref(''),
  mode = ref('random'),
  proxyID = ref(''),
  search = ref(''),
  page = ref<ProxyPage>({ items: [], total: 0, offset: 0, limit: 50 }),
  loading = ref(true),
  error = ref('')
const {
  job,
  error: jobError,
  busy,
  pendingKey,
  submit,
  reconcile,
  stop,
  load: loadJob,
  review,
} = useProxyJob('assign', () => emit('changed'))
const button =
  'rounded border border-line bg-raised px-3 py-2 text-sm text-gray-300 disabled:opacity-40'
const input = 'rounded border border-line bg-canvas px-3 py-2 text-sm text-white'
function problem(e: unknown) {
  error.value = e instanceof ApiError ? e.code : 'NETWORK_ERROR'
}
async function candidates(offset = 0) {
  try {
    page.value = await proxyAPI.list({
      eligible: true,
      source_id: sourceID.value,
      search: search.value,
      offset,
    })
    proxyID.value = ''
  } catch (e) {
    problem(e)
  }
}
async function apply() {
  if (loading.value || accounts.value.length !== props.ids.length) return
  await submit({
    kind: 'assign',
    mode: mode.value,
    proxy_id: mode.value === 'specified' ? proxyID.value : '',
    source_id: sourceID.value,
    concurrency: 3,
    targets: accounts.value.map((a) => ({
      id: a.id,
      credential_version: a.credential_version,
      state_version: a.state_version,
    })),
  })
}
onMounted(async () => {
  try {
    const [context, sourceList] = await Promise.all([
      proxyAPI.contexts(props.ids),
      proxyAPI.sources(),
    ])
    accounts.value = context.items
    sources.value = sourceList.items
    await candidates()
  } catch (e) {
    problem(e)
  } finally {
    loading.value = false
  }
})
</script>
<template>
  <DetailDialog
    :open="true"
    :title="text(ids.length > 1 ? 'batch' : 'switch')"
    :description="text('healthyHelp')"
    @close="$emit('close')"
  >
    <p v-if="error || jobError" role="alert" class="mb-3 text-red-400">
      {{ text(error || jobError) }}
    </p>
    <div class="mb-4 max-h-36 overflow-auto rounded border border-line">
      <div
        v-for="account in accounts"
        :key="account.id"
        class="flex flex-wrap justify-between gap-2 border-b border-line px-3 py-2 text-xs"
      >
        <span class="font-mono">{{ account.id }}</span
        ><span class="text-gray-500"
          >{{ text('current') }}：{{ account.endpoint || text('direct') }} ·
          {{ text(account.enabled ? 'enabled' : 'disabled') }}</span
        >
      </div>
    </div>
    <div class="flex flex-wrap gap-3">
      <label
        >{{ text('sources') }}
        <select
          v-model="sourceID"
          :class="input"
          :disabled="busy || jobActive(job) || !!pendingKey"
          @change="candidates()"
        >
          <option value="">{{ text('sourceAll') }}</option>
          <option
            v-for="source in sources.filter((s) => s.enabled)"
            :key="source.id"
            :value="source.id"
          >
            {{ source.name }}
          </option>
        </select></label
      ><label
        >{{ text('choose') }}
        <select v-model="mode" :class="input" :disabled="busy || jobActive(job) || !!pendingKey">
          <option value="random">{{ text('random') }}</option>
          <option value="specified" :disabled="ids.length > 1">{{ text('specified') }}</option>
        </select></label
      >
    </div>
    <template v-if="mode === 'specified'"
      ><div class="my-3 flex gap-2">
        <input
          v-model="search"
          :class="input"
          :placeholder="text('search')"
          @keyup.enter="candidates()"
        /><button :class="button" @click="candidates()">{{ text('refreshData') }}</button>
      </div>
      <select
        v-model="proxyID"
        size="6"
        :class="[input, 'w-full']"
        :disabled="busy || jobActive(job) || !!pendingKey"
      >
        <option v-for="proxy in page.items" :key="proxy.id" :value="proxy.id">
          {{ proxy.protocol.toUpperCase() }} · {{ proxy.endpoint }} ·
          {{ Math.round(proxy.latency_ms) }} ms · #{{ proxy.id.slice(0, 6) }}
        </option>
      </select>
      <div class="mt-2 flex justify-end gap-2">
        <button :class="button" :disabled="page.offset === 0" @click="candidates(page.offset - 50)">
          {{ text('previous') }}</button
        ><span class="py-2 text-gray-500">{{ page.total }}</span
        ><button
          :class="button"
          :disabled="page.offset + 50 >= page.total"
          @click="candidates(page.offset + 50)"
        >
          {{ text('next') }}
        </button>
      </div></template
    >
    <p v-if="!loading && !page.total" class="my-4 text-amber-400">{{ text('noEligible') }}</p>
    <p v-if="pendingKey" class="my-3 text-amber-400">{{ text('submitUnknown') }}</p>
    <div class="mt-5 flex justify-end gap-2">
      <button v-if="pendingKey" :class="button" :disabled="busy" @click="reconcile">
        {{ text('reconcile') }}</button
      ><button
        :class="button"
        :disabled="
          loading ||
          busy ||
          jobActive(job) ||
          !!pendingKey ||
          !page.total ||
          accounts.length !== ids.length ||
          (mode === 'specified' && !proxyID)
        "
        @click="apply"
      >
        {{ loading ? text('loading') : text('apply') }}
      </button>
    </div>
    <ProxyJobResults v-if="job" :job="job" @page="loadJob" @stop="stop" @reconcile="review" />
    <p class="mt-4 text-xs text-gray-500">{{ text('changeHelp') }}</p>
  </DetailDialog>
</template>
