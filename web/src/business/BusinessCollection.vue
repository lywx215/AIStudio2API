<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { requestJSON } from '@/api'
import { businessClient } from './client'
import type { BusinessAccounts } from './types'

type Task = { task_id: string; state: string; missing_fields: string[]; observations_count: number; expires_at: string }
type Entry = { id: string; account_id: string; credential_epoch: number; config_id: string; display_name: string; upstream_id: string; auto_form?: string; source: string; source_url: string; observed_at: string; expires_at: string; state: string }
type Catalog = { revision: number; entries: Entry[] }
const emit = defineEmits<{ changed: [] }>()
const root = '/api/addons/business'
const accounts = ref<BusinessAccounts | null>(null)
const target = ref('')
const label = ref('')
const executable = ref('')
const testedVersions = ref<string[]>([])
const proxy = ref('')
const overrideProxy = ref(false)
const task = ref<Task | null>(null)
const catalog = ref<Catalog>({ revision: 0, entries: [] })
const frame = ref('')
const text = ref('')
const key = ref('Enter')
const busy = ref(false)
const error = ref('')
const notice = ref('')
const controller = new AbortController()
let active = true
let timer: ReturnType<typeof setTimeout> | undefined
let polling = false

async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  return requestJSON<T>(`${root}${path}`, { method, signal: controller.signal, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
}
function clearFrame(): void { if (frame.value) URL.revokeObjectURL(frame.value); frame.value = '' }
async function refresh(): Promise<void> {
  try {
    const [a, c, info] = await Promise.all([businessClient.accounts(controller.signal), api<Catalog>('/observations'), api<{ executable_path: string; tested_versions?: string[] }>('/browser-info')])
    if (!active) return
    accounts.value = a; catalog.value = c
    testedVersions.value = info.tested_versions ?? []
    if (!executable.value) executable.value = info.executable_path
  } catch { if (active) error.value = '无法读取采集配置，请检查控制台登录状态。' }
}
function schedule(): void {
  if (active && task.value) timer = setTimeout(() => { void poll() }, 1500)
}
async function poll(): Promise<void> {
  if (!active || !task.value) return
  if (busy.value || polling) { schedule(); return }
  polling = true
  const id = task.value.task_id
  try {
    const next = await api<Task>(`/browser/${id}`)
    if (!active || task.value?.task_id !== id) return
    task.value = next
    if (['failed', 'cancelled', 'expired', 'cleanup_failed'].includes(next.state)) throw new Error('ended')
    if (next.state !== 'starting') {
      const response = await fetch(`${root}/browser/${id}/frame`, { credentials: 'same-origin', cache: 'no-store', signal: controller.signal })
      if (!response.ok || !response.headers.get('content-type')?.startsWith('image/png')) throw new Error('frame')
      const blob = await response.blob()
      if (blob.size > 4 * 1024 * 1024) throw new Error('frame limit')
      if (active && task.value?.task_id === id) { clearFrame(); frame.value = URL.createObjectURL(blob) }
    }
  } catch {
    if (active) { error.value = '采集已结束或画面不可用。请检查浏览器版本、会话与网络后重新启动。'; await cancelTask() }
  } finally { polling = false; schedule() }
}
async function start(): Promise<void> {
  if (busy.value || task.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const current = await businessClient.accounts(controller.signal)
    const next = await api<Task>('/browser', 'POST', { revision: current.revision, id: target.value || undefined, label: label.value, executable_path: executable.value, headless: true, ...(overrideProxy.value ? { proxy_url: proxy.value } : {}) })
    if (!active) return
    accounts.value = current; task.value = next; schedule()
  } catch { if (active) error.value = '无法启动采集。请确认账户版本、浏览器绝对路径、代理设置及是否已有任务。' }
  finally { busy.value = false }
}
async function cancelTask(): Promise<void> {
  const id = task.value?.task_id
  task.value = null; text.value = ''; clearFrame()
  if (timer) clearTimeout(timer)
  if (id) { try { await fetch(`${root}/browser/${id}`, { method: 'DELETE', credentials: 'same-origin', keepalive: true }) } catch { /* The server also enforces its idle timeout. */ } }
}
async function input(body: unknown): Promise<void> {
  if (!task.value || busy.value) return
  busy.value = true; error.value = ''
  try { await api(`/browser/${task.value.task_id}/input`, 'POST', body) }
  catch { if (active) error.value = '输入未完成，请确认画面和会话状态。' }
  finally { busy.value = false }
}
function clickFrame(event: MouseEvent): void {
  const image = event.currentTarget as HTMLImageElement
  const box = image.getBoundingClientRect()
  void input({ type: 'click', x: Math.floor((event.clientX - box.left) * image.naturalWidth / box.width), y: Math.floor((event.clientY - box.top) * image.naturalHeight / box.height) })
}
function sendText(): void { const value = text.value; text.value = ''; if (value) void input({ type: 'text', text: value }) }
async function save(): Promise<void> {
  if (!task.value || task.value.state !== 'ready' || busy.value) return
  busy.value = true; error.value = ''
  try {
    await api(`/browser/${task.value.task_id}/save`, 'POST', {})
    await cancelTask(); await refresh(); emit('changed')
    notice.value = '账号已加密保存，初始为停用状态。请在账号列表中检查并启用。'
  } catch { if (active) error.value = '保存未完成：账户可能已变化、会话已结束或清理失败。请刷新后重新采集。'; await cancelTask() }
  finally { busy.value = false }
}
async function confirm(entry: Entry): Promise<void> {
  if (busy.value) return
  busy.value = true; error.value = ''
  try { catalog.value = await api<Catalog>('/observations/confirm', 'POST', { revision: catalog.value.revision, id: entry.id }); notice.value = '已确认观察记录；模型绑定和参数能力未自动启用。' }
  catch { if (active) error.value = '观察记录已变化、过期或冲突，请刷新。' }
  finally { busy.value = false }
}
const states: Record<string, string> = { starting: '启动中', waiting: '等待手工登录和字段', ready: '字段完整，可保存', saving: '保存中', pending: '待确认', confirmed: '已确认观察', expired: '已过期', conflict: '映射冲突', stale: '账户版本已变化' }
onMounted(() => { void refresh() })
onUnmounted(() => { active = false; controller.abort(); void cancelTask() })
</script>

<template>
  <section class="business-collection">
    <h3>远程浏览器采集</h3>
    <p>在此画面手工登录或完成验证码。采集不会自动发送聊天或模型探测。任务最长 10 分钟；关闭面板或退出登录会取消。</p>
    <p v-if="testedVersions.length">已验证浏览器：{{ testedVersions.join('、') }}</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <form v-if="!task" @submit.prevent="start">
      <label>目标账号<select v-model="target"><option value="">新增账号</option><option v-for="a in accounts?.accounts ?? []" :key="a.id" :value="a.id">{{ a.label }}</option></select></label>
      <label>账号名称<input v-model="label" required maxlength="160" autocomplete="off" /></label>
      <label>服务器浏览器绝对路径<input v-model="executable" required autocomplete="off" /></label>
      <label><input v-model="overrideProxy" type="checkbox" /> 更改此账号的 Business 代理</label>
      <label v-if="overrideProxy">HTTP / SOCKS5 代理（需端口，无认证；留空为直连）<input v-model="proxy" autocomplete="off" placeholder="http://127.0.0.1:7890" /></label>
      <button type="submit" :disabled="busy">启动无桌面采集</button>
    </form>
    <div v-else>
      <p>状态：{{ states[task.state] ?? task.state }} · 观察记录 {{ task.observations_count }} · 截止 {{ new Date(task.expires_at).toLocaleTimeString() }}</p>
      <p v-if="task.missing_fields.length">待观察：{{ task.missing_fields.join('、') }}</p>
      <img v-if="frame" :src="frame" alt="Business 登录浏览器画面，点击即可操作" draggable="false" @click="clickFrame" />
      <form @submit.prevent="sendText"><label>向当前焦点输入（发送后清空）<input v-model="text" type="password" autocomplete="off" maxlength="2048" /></label><button :disabled="busy || !text">输入</button></form>
      <select v-model="key" aria-label="按键"><option v-for="k in ['Enter', 'Tab', 'Backspace', 'Escape', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight']" :key="k">{{ k }}</option></select>
      <button :disabled="busy" @click="input({ type: 'key', key })">发送按键</button>
      <button :disabled="busy || task.state !== 'ready'" @click="save">关闭浏览器并保存账号</button>
      <button @click="cancelTask">取消采集</button>
    </div>
    <h3>模型观察目录</h3>
    <p>仅记录网页真实返回或用户操作中的模型 ID。确认观察不等于调用验证，也不会自动启用映射。缺失 modelId 的请求仍为未判定，不能直接视为 Auto。</p>
    <button :disabled="busy" @click="refresh">刷新目录</button>
    <p v-if="!catalog.entries.length">尚无观察记录。登录后操作网页模型菜单，再保存采集账号。</p>
    <table v-else><thead><tr><th>显示名 / ID</th><th>来源和范围</th><th>状态</th><th>操作</th></tr></thead><tbody>
      <tr v-for="e in catalog.entries" :key="e.id"><td>{{ e.display_name || '未提供显示名' }}<br /><code>{{ e.upstream_id || e.auto_form }}</code></td><td>{{ e.source }}<br />{{ e.account_id }} · 版本 {{ e.credential_epoch }}<br />{{ new Date(e.observed_at).toLocaleString() }}</td><td>{{ states[e.state] ?? e.state }}</td><td><button :disabled="busy || e.state !== 'pending' || !e.upstream_id" @click="confirm(e)">确认观察</button></td></tr>
    </tbody></table>
  </section>
</template>

<style scoped>
.business-collection { border: 1px solid var(--border-color, #8885); border-radius: 12px; padding: 20px; margin: 20px 0; }
form { display: flex; flex-wrap: wrap; align-items: end; gap: 12px; margin: 12px 0; }
label { display: flex; flex-direction: column; gap: 4px; }
input:not([type='checkbox']) { min-width: 260px; }
input, select, button { padding: 7px 10px; border: 1px solid #8886; border-radius: 5px; background: transparent; color: inherit; }
button { margin: 4px; cursor: pointer; }
button:disabled { opacity: .5; cursor: default; }
img { display: block; max-width: 100%; cursor: crosshair; border: 1px solid #8886; }
table { width: 100%; text-align: left; border-collapse: collapse; margin-top: 12px; overflow-wrap: anywhere; }
th, td { padding: 8px; border-bottom: 1px solid #8884; }
[role='alert'] { color: #d45555; }
</style>
