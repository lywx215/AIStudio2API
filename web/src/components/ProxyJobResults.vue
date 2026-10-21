<script setup lang="ts">
import { jobActive, useProxyText, type ProxyJob } from '@/proxy-management'
defineProps<{ job: ProxyJob }>()
defineEmits<{ page: [offset: number]; stop: []; reconcile: [] }>()
const text = useProxyText()
</script>
<template>
  <section class="mt-4 rounded-lg border border-line bg-panel p-4" aria-live="polite">
    <div class="flex flex-wrap items-center gap-3">
      <h3 class="font-semibold text-white">
        {{ text('job') }} · {{ text(job.kind === 'test' ? 'probe' : 'assign') }}
      </h3>
      <span class="text-sm text-gray-400"
        >{{ job.done }} / {{ job.total }} · {{ text(job.status) }}</span
      ><button
        v-if="jobActive(job)"
        class="ml-auto rounded border border-line px-3 py-1 text-sm text-gray-300 disabled:opacity-50"
        :disabled="job.cancel_requested"
        @click="$emit('stop')"
      >
        {{ text('stop') }}
      </button>
    </div>
    <button
      v-if="
        !jobActive(job) &&
        job.items.some((item) => item.status === 'needs_reconcile' || item.status === 'interrupted')
      "
      class="mt-2 rounded border border-line px-3 py-1 text-sm text-gray-300"
      @click="$emit('reconcile')"
    >
      {{ text('reconcile') }}
    </button>
    <progress
      class="mt-2 h-2 w-full accent-blue-500"
      :value="job.done"
      :max="Math.max(job.total, 1)"
    />
    <div class="mt-3 max-h-72 overflow-auto">
      <table class="w-full text-left text-xs">
        <thead class="text-gray-500">
          <tr>
            <th class="p-2">{{ text('accounts') }}</th>
            <th>{{ text('endpoint') }}</th>
            <th>{{ text('state') }}</th>
            <th>{{ text('reason') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in job.items" :key="item.index" class="border-t border-line">
            <td class="p-2 font-mono">{{ item.account_id || '—' }}</td>
            <td>{{ item.endpoint || '—' }}</td>
            <td
              :class="
                item.status === 'succeeded'
                  ? 'text-green-400'
                  : item.status === 'needs_reconcile'
                    ? 'text-amber-400'
                    : 'text-gray-400'
              "
            >
              {{ text(item.status) }}
            </td>
            <td class="py-2">
              {{ item.code ? text(item.code) : '—'
              }}<span v-if="item.probe?.exit_ip">
                · {{ item.probe.exit_ip }} · {{ Math.round(item.probe.latency_ms) }} ms</span
              >
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="mt-2 flex justify-end gap-3 text-sm">
      <button
        :disabled="job.offset === 0"
        class="disabled:opacity-40"
        @click="$emit('page', Math.max(0, job.offset - 50))"
      >
        {{ text('previous') }}</button
      ><span class="text-gray-500"
        >{{ job.offset + 1 }}–{{ Math.min(job.offset + 50, job.total) }} / {{ job.total }}</span
      ><button
        :disabled="job.offset + 50 >= job.total"
        class="disabled:opacity-40"
        @click="$emit('page', job.offset + 50)"
      >
        {{ text('next') }}
      </button>
    </div>
  </section>
</template>
