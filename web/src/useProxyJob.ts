import { onMounted, onUnmounted, ref } from 'vue'
import { ApiError } from '@/api'
import { proxyAPI, jobActive, type ProxyJob, type ProxyJobInput } from '@/proxy-management'

function stored(key: string, value?: string): string {
  try {
    if (value !== undefined) {
      if (value) sessionStorage.setItem(key, value)
      else sessionStorage.removeItem(key)
    }
    return sessionStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}
export function useProxyJob(kind: 'test' | 'assign', changed: () => void) {
  const job = ref<ProxyJob | null>(null),
    error = ref(''),
    busy = ref(false),
    pendingKey = ref(stored(`proxy.pending.${kind}`))
  let mounted = true,
    timer: ReturnType<typeof setTimeout> | undefined
  const remember = (key: string) => {
    pendingKey.value = key
    stored(`proxy.pending.${kind}`, key)
  }
  function schedule() {
    if (timer) clearTimeout(timer)
    if (mounted && jobActive(job.value)) timer = setTimeout(() => void load(), 2000)
  }
  async function load(offset = job.value?.offset ?? 0) {
    if (!job.value) return
    try {
      const previous = job.value
      const value = await proxyAPI.job(previous.id, offset)
      if (!mounted || job.value?.id !== previous.id) return
      job.value = value
      if (previous.done !== value.done || previous.status !== value.status) changed()
      error.value = ''
    } catch (e) {
      if (mounted) error.value = e instanceof ApiError ? e.code : 'NETWORK_ERROR'
    } finally {
      schedule()
    }
  }
  async function attach(value: ProxyJob) {
    if (!mounted) return
    job.value = value
    stored(`proxy.job.${kind}`, value.id)
    remember('')
    changed()
    schedule()
  }
  async function reconcile() {
    if (!pendingKey.value) return
    busy.value = true
    try {
      await attach(await proxyAPI.find(pendingKey.value))
      error.value = ''
    } catch (e) {
      error.value = e instanceof ApiError ? e.code : 'NETWORK_ERROR'
    } finally {
      busy.value = false
    }
  }
  async function submit(input: ProxyJobInput) {
    if (busy.value) return
    if (pendingKey.value) {
      await reconcile()
      return
    }
    busy.value = true
    error.value = ''
    const bytes = crypto.getRandomValues(new Uint8Array(16))
    const key = Array.from(bytes, (v) => v.toString(16).padStart(2, '0')).join('')
    remember(key)
    try {
      await attach(await proxyAPI.submit(input, key))
    } catch (e) {
      error.value = e instanceof ApiError ? e.code : 'COMMIT_RESULT_UNKNOWN'
      if (e instanceof ApiError && e.status >= 400 && e.status < 500 && e.status !== 408)
        remember('')
    } finally {
      busy.value = false
    }
  }
  async function stop() {
    if (!job.value) return
    try {
      await proxyAPI.cancel(job.value.id)
      await load()
    } catch (e) {
      error.value = e instanceof ApiError ? e.code : 'NETWORK_ERROR'
    }
  }
  async function select(id: string) {
    if (busy.value) return
    busy.value = true
    try {
      await attach(await proxyAPI.job(id))
    } catch (e) {
      error.value = e instanceof ApiError ? e.code : 'NETWORK_ERROR'
    } finally {
      busy.value = false
    }
  }
  async function review() {
    if (!job.value) return
    busy.value = true
    try {
      await attach(await proxyAPI.review(job.value.id))
      error.value = ''
    } catch (e) {
      error.value = e instanceof ApiError ? e.code : 'NETWORK_ERROR'
    } finally {
      busy.value = false
    }
  }
  onMounted(() => {
    if (pendingKey.value) void reconcile()
    else {
      const id = stored(`proxy.job.${kind}`)
      if (id) void select(id)
    }
  })
  onUnmounted(() => {
    mounted = false
    if (timer) clearTimeout(timer)
  })
  return { job, error, busy, pendingKey, submit, reconcile, stop, load, select, review }
}
