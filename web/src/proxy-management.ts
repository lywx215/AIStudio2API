import { requestCommand, requestJSON } from '@/api'
import { useI18n } from '@/i18n'

export interface ProxySource {
  id: string
  name: string
  kind: string
  protocol: string
  enabled: boolean
  revision: number
  configured: boolean
  refreshed_at: string
  error: string
}
export interface SourceDraft {
  name: string
  kind: string
  protocol: string
  enabled: boolean
  revision: number
  url: string
}
export interface ManagedProxy {
  id: string
  protocol: string
  endpoint: string
  enabled: boolean
  revision: number
  state: string
  exit_ip: string
  latency_ms: number
  studio_latency_ms: number
  checked_at: string
  error: string
  present: boolean
  eligible: boolean
  sources: string
  accounts: number
}
export interface ProxyPage {
  items: ManagedProxy[]
  total: number
  offset: number
  limit: number
}
export interface ProxyQuery {
  search?: string
  source_id?: string
  protocol?: string
  state?: string
  eligible?: boolean
  offset?: number
  limit?: number
}
export interface ProxyAccount {
  id: string
  account_id: string
  endpoint: string
  credential_version: number
  state_version: number
  archived: boolean
  deleted: boolean
  enabled: boolean
}
export interface ProxyItem {
  index: number
  proxy_id: string
  endpoint: string
  account_id: string
  status: string
  code: string
  receipt?: { credential_version: number; state_version: number }
  probe?: {
    state: string
    latency_ms: number
    studio_latency_ms: number
    exit_ip: string
    error: string
  }
}
export interface ProxyJob {
  id: string
  kind: string
  status: string
  total: number
  done: number
  cancel_requested: boolean
  items: ProxyItem[]
  offset: number
  limit: number
}
export interface ProxyStats {
  total: number
  passed: number
  failed: number
  restricted: number
  untested: number
  eligible: number
  average_ms: number
}
export interface ProxyJobInput {
  kind: 'test' | 'assign'
  ids?: string[]
  all?: boolean
  query?: ProxyQuery
  concurrency?: number
  targets?: { id: string; credential_version: number; state_version: number }[]
  mode?: string
  proxy_id?: string
  source_id?: string
}
function query(value: ProxyQuery): string {
  const result = new URLSearchParams()
  for (const [key, v] of Object.entries(value))
    if (v !== undefined && v !== '') result.set(key, String(v))
  return result.toString()
}
export const proxyAPI = {
  review: (id: string) =>
    requestJSON<ProxyJob>(`/api/proxies/jobs/${id}/reconcile`, { method: 'POST' }),
  sources: () => requestJSON<{ items: ProxySource[] }>('/api/proxies/sources'),
  saveSource: (id: string, draft: SourceDraft) =>
    requestJSON<ProxySource>(`/api/proxies/sources${id ? '/' + encodeURIComponent(id) : ''}`, {
      method: id ? 'PUT' : 'POST',
      body: JSON.stringify(draft),
    }),
  deleteSource: (source: ProxySource) =>
    requestCommand(`/api/proxies/sources/${source.id}`, {
      method: 'DELETE',
      body: JSON.stringify({ revision: source.revision }),
    }),
  refresh: (id: string) =>
    requestJSON<{ count: number; invalid: number }>(`/api/proxies/sources/${id}/refresh`, {
      method: 'POST',
    }),
  list: (q: ProxyQuery) => requestJSON<ProxyPage>('/api/proxies?' + query(q)),
  stats: () => requestJSON<ProxyStats>('/api/proxies/stats'),
  enabled: (value: ManagedProxy) =>
    requestCommand(`/api/proxies/${value.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled: !value.enabled, revision: value.revision }),
    }),
  contexts: (ids: string[]) =>
    requestJSON<{ items: ProxyAccount[] }>('/api/proxies/account-contexts', {
      method: 'POST',
      body: JSON.stringify({ ids }),
    }),
  submit: (input: ProxyJobInput, key: string) =>
    requestJSON<ProxyJob>('/api/proxies/jobs', {
      method: 'POST',
      headers: { 'Idempotency-Key': key },
      body: JSON.stringify(input),
    }),
  jobs: () => requestJSON<{ items: ProxyJob[] }>('/api/proxies/jobs'),
  job: (id: string, offset = 0) =>
    requestJSON<ProxyJob>(`/api/proxies/jobs/${id}?offset=${offset}&limit=50`),
  find: (key: string) =>
    requestJSON<ProxyJob>(`/api/proxies/jobs/by-key/${encodeURIComponent(key)}`),
  cancel: (id: string) => requestCommand(`/api/proxies/jobs/${id}/cancel`, { method: 'POST' }),
}
const zh = {
  title: '代理管理',
  description: '维护代理来源，检测网络状态，手动设置账户出口',
  sources: '代理来源',
  add: '添加来源',
  name: '来源名称',
  url: 'HTTPS 下载地址',
  keepURL: '已配置；留空保留原地址',
  protocol: '代理类型',
  save: '保存',
  cancel: '取消',
  edit: '编辑',
  remove: '移除来源',
  refresh: '更新列表',
  enabled: '启用',
  disabled: '停用',
  configured: '已配置',
  notConfigured: '未配置',
  total: '总数',
  passed: '通过',
  failed: '失败',
  restricted: '受限',
  untested: '未测试',
  eligible: '可分配',
  average: '平均请求延迟',
  search: '搜索主机或端口',
  all: '全部',
  removed: '来源已移除',
  testSelected: '测试所选',
  testAll: '测试当前筛选全部',
  concurrency: '并发',
  endpoint: '主机端口',
  exitIP: '出口 IP',
  latency: '请求延迟',
  studioLatency: 'AI Studio 延迟',
  checked: '检测时间',
  accounts: '账户数',
  state: '状态',
  action: '操作',
  test: '测试',
  previous: '上一页',
  next: '下一页',
  empty: '暂无代理，请添加来源并更新列表',
  job: '后台任务',
  stop: '停止任务',
  select: '全选本页',
  results: '任务结果',
  queued: '排队中',
  running: '处理中',
  completed: '已结束',
  cancelled: '已停止',
  interrupted: '已中断',
  succeeded: '成功',
  skipped: '跳过',
  needs_reconcile: '待核对',
  switch: '切换代理',
  batch: '批量随机代理',
  selectAccounts: '勾选账户',
  current: '当前出口',
  direct: '默认出口 / 直连',
  random: '随机选择',
  specified: '指定代理',
  apply: '设置代理',
  healthyHelp: '仅使用 24 小时内测试通过、启用且来源有效的代理，排除账户当前出口。',
  probeHelp: '测试仅检查出口 IP 和 AI Studio 网络可达性，不登录或调用模型。',
  submitUnknown: '提交结果未知，请只读核对原任务；不会重新抽取或重发。',
  reconcile: '核对提交',
  updated: '列表已更新',
  loading: '正在加载',
  passwordRequired: '请使用管理员密码登录后管理代理',
  choose: '选择代理',
  sourceAll: '全部来源',
  reason: '原因',
  noEligible: '暂无符合条件的代理，请先在代理管理中更新并测试',
  refreshData: '刷新数据',
  changeHelp: '更新列表和检测不会改变账户出口；停止任务保留已提交修改。',
  busy: '操作进行中',
  recover: '最近任务',
  selectLimit: '一次最多选择 200 个账户',
  manualRefresh: '来源仅手动更新',
  probe: '网络检测',
  assign: '账户设置',
  close: '关闭',
  pending: '待核对',
  PROXY_APPLIED: '代理已设置',
  NO_ELIGIBLE_PROXY: '没有可用候选代理',
  VERSION_CONFLICT: '版本已变化，请刷新后操作',
  ACCOUNT_CHANGED_AFTER_COMMIT: '已提交，账户随后发生变化',
  ACCOUNT_ARCHIVED_OR_DELETED: '账户已归档或删除',
  COMMIT_RESULT_UNKNOWN: '提交结果未知，仅核对原操作',
  RUNTIME_APPLY_UNCONFIRMED: '配置已提交，运行时生效尚未确认',
  PROXY_NO_LONGER_ELIGIBLE: '代理已失效、停用或移出来源',
  PROCESS_RESTARTED: '进程重启，任务未自动重跑',
  CANCELLED: '已停止',
  EMPTY_PROXY_LIST: '来源未返回有效代理，已保留旧列表',
  PRIVATE_NETWORK_ADDRESS: '拒绝连接非公网地址',
  PRIVATE_DOWNLOAD_ADDRESS: '下载地址必须为公网 HTTPS',
  DOWNLOAD_REDIRECT_REJECTED: '下载地址重定向已拒绝',
  PROXY_AUTH_FAILED: '代理认证失败',
  CONNECT_FAILED: '连接失败',
  TIMEOUT: '请求超时',
  DNS_FAILED: '域名解析失败',
  TLS_FAILED: 'TLS 校验失败',
  INVALID_EXIT_IP_RESPONSE: '出口 IP 响应无效',
  AISTUDIO_HTTP_RESTRICTED: 'AI Studio 返回受限状态',
  IP_HTTP_RESTRICTED: '出口 IP 服务返回受限状态',
  TEST_JOB_ALREADY_RUNNING: '已有检测任务正在运行',
  JOB_NOT_FOUND: '尚未查到原任务，未重新提交',
  PROXY_STORAGE_UNAVAILABLE: '代理库不可用，请检查服务端存储',
}
type Key = keyof typeof zh
const en: Record<string, string> = {
  title: 'Proxy management',
  description: 'Maintain sources, test network health and choose account exits',
  sources: 'Sources',
  add: 'Add source',
  name: 'Source name',
  url: 'HTTPS download URL',
  keepURL: 'Configured; leave blank to retain',
  protocol: 'Protocol',
  save: 'Save',
  cancel: 'Cancel',
  edit: 'Edit',
  remove: 'Remove source',
  refresh: 'Update list',
  enabled: 'Enable',
  disabled: 'Disable',
  configured: 'Configured',
  notConfigured: 'Not configured',
  total: 'Total',
  passed: 'Passed',
  failed: 'Failed',
  restricted: 'Restricted',
  untested: 'Untested',
  eligible: 'Eligible',
  average: 'Average request latency',
  search: 'Search host or port',
  all: 'All',
  removed: 'Removed from source',
  testSelected: 'Test selected',
  testAll: 'Test all matching',
  concurrency: 'Concurrency',
  endpoint: 'Host and port',
  exitIP: 'Exit IP',
  latency: 'Request latency',
  studioLatency: 'AI Studio latency',
  checked: 'Checked at',
  accounts: 'Accounts',
  state: 'Status',
  action: 'Actions',
  test: 'Test',
  previous: 'Previous',
  next: 'Next',
  empty: 'No proxies. Add a source and update its list.',
  job: 'Background job',
  stop: 'Stop job',
  select: 'Select page',
  results: 'Results',
  queued: 'Queued',
  running: 'Running',
  completed: 'Finished',
  cancelled: 'Stopped',
  interrupted: 'Interrupted',
  succeeded: 'Succeeded',
  skipped: 'Skipped',
  needs_reconcile: 'Reconciliation required',
  switch: 'Switch proxy',
  batch: 'Randomize selected',
  selectAccounts: 'Select accounts',
  current: 'Current exit',
  direct: 'Default exit / direct',
  random: 'Random selection',
  specified: 'Choose proxy',
  apply: 'Set proxy',
  healthyHelp:
    'Only enabled proxies that passed within 24 hours and remain in an active source; excludes the current exit.',
  probeHelp: 'Network checks only. No account login or model calls.',
  submitUnknown: 'Submission is unknown. Look up the original job; no redraw or resubmission.',
  reconcile: 'Look up submission',
  updated: 'List updated',
  loading: 'Loading',
  passwordRequired: 'Sign in with an administrator password to manage proxies.',
  choose: 'Choose proxy',
  sourceAll: 'All sources',
  reason: 'Reason',
  noEligible: 'No eligible proxies. Update and test proxies first.',
  refreshData: 'Refresh',
  changeHelp: 'Updating and testing do not change account exits. Stopping keeps committed changes.',
  busy: 'Working',
  recover: 'Recent jobs',
  selectLimit: 'Select at most 200 accounts',
  manualRefresh: 'Manual source updates only',
  probe: 'Network test',
  assign: 'Account proxy',
  close: 'Close',
  pending: 'Pending',
}
export function useProxyText() {
  const { locale } = useI18n()
  return (key: string): string =>
    locale.value.startsWith('zh') ? (zh[key as Key] ?? key) : (en[key] ?? key)
}
export function jobActive(job: ProxyJob | null): boolean {
  return job?.status === 'queued' || job?.status === 'running'
}
