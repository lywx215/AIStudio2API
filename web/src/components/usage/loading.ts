// One bounded cache per mounted usage shell; never shared between sessions.
export interface CachedUsage<T> {
  value: T
  updatedAt: number
}
export class UsageCache {
  private entries = new Map<string, CachedUsage<unknown>>()
  private capacity: number
  constructor(capacity = 32) {
    this.capacity = capacity
  }
  get<T>(key: string, ttl: number, now = Date.now()): CachedUsage<T> | undefined {
    const entry = this.entries.get(key)
    if (!entry || now - entry.updatedAt >= ttl) return undefined
    this.entries.delete(key)
    this.entries.set(key, entry)
    return entry as CachedUsage<T>
  }
  set<T>(key: string, entry: CachedUsage<T>): void {
    this.entries.delete(key)
    this.entries.set(key, entry)
    while (this.entries.size > this.capacity) this.entries.delete(this.entries.keys().next().value!)
  }
  clear(): void {
    this.entries.clear()
  }
}

// Cancellation also guards against transports which complete after abort().
export class UsageRequest<T> {
  private controller?: AbortController
  private version = 0
  private cache: UsageCache
  constructor(cache: UsageCache) {
    this.cache = cache
  }
  cancel(): void {
    this.version++
    this.controller?.abort()
  }
  async load(
    key: string,
    fetcher: (signal: AbortSignal) => Promise<T>,
    ttl: number,
    force = false,
  ): Promise<CachedUsage<T> | undefined> {
    this.cancel()
    const version = this.version
    const cached = force ? undefined : this.cache.get<T>(key, ttl)
    if (cached) return cached
    const controller = new AbortController()
    this.controller = controller
    try {
      const value = await fetcher(controller.signal)
      if (controller.signal.aborted || version !== this.version) return
      const entry = { value, updatedAt: Date.now() }
      this.cache.set(key, entry)
      return entry
    } catch (error) {
      if (!controller.signal.aborted && version === this.version) throw error
    }
  }
}

export function usageRefreshDue(options: {
  interval: number
  live: boolean
  visible: boolean
  loading: boolean
  page: number
  lastAttempt: number
  now: number
}): boolean {
  return (
    options.interval > 0 &&
    options.live &&
    options.visible &&
    !options.loading &&
    options.page === 0 &&
    options.lastAttempt > 0 &&
    options.now - options.lastAttempt >= options.interval * 1000
  )
}
