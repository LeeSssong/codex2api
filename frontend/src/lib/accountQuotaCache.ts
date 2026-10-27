import type { AccountPageStatsItem, AccountPageStatsResponse, AccountRow } from '../types'

export type QuotaCacheEntry = { data: AccountPageStatsItem | null; status: 'loading' | 'ready' | 'error' }
const CACHE_TTL = 5 * 60 * 1000
const REQUEST_TIMEOUT_MS = 30_000

/** Source-equivalent native metadata; request counters never invalidate usage. */
export function accountQuotaRefreshKey(account: Partial<AccountRow>): string {
  return [account.id, account.updated_at, account.last_used_at, account.last_rate_limited_at,
    account.codex_usage_updated_at, (account as Partial<AccountRow> & { codex_5h_usage_updated_at?: string }).codex_5h_usage_updated_at, account.usage_percent_5h, account.reset_5h_at,
    account.usage_percent_7d, account.reset_7d_at, account.usage_window_7d_kind,
    account.usage_window_7d_seconds].map(value => value == null ? '' : String(value)).join('|')
}

/** Memory-only, shared by list/detail. TTL governs requests, never display. */
export class AccountQuotaCache {
  private entries: Record<string, QuotaCacheEntry> = {}
  private listeners = new Set<() => void>()
  private keys = new Map<number, string>()
  private cachedAt = new Map<number, number>()
  private versions = new Map<number, number>()
  private inFlight = new Map<number, number>()
  private pending = new Set<number>()
  private timer: ReturnType<typeof setTimeout> | undefined
  private load: (ids: number[], signal?: AbortSignal) => Promise<AccountPageStatsResponse>
  private now: () => number
  constructor(load: (ids: number[], signal?: AbortSignal) => Promise<AccountPageStatsResponse>, now = Date.now) {
    this.load = load
    this.now = now
  }
  getSnapshot = () => this.entries
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  private emit() { for (const listener of this.listeners) listener() }
  request(id: number, key: string, force = false) {
    const changed = this.keys.has(id) && this.keys.get(id) !== key
    this.keys.set(id, key)
    if (!force && !changed) {
      if (this.inFlight.has(id) || this.pending.has(id)) return
      const cachedAt = this.cachedAt.get(id)
      if (cachedAt !== undefined && this.now() - cachedAt < CACHE_TTL) return
    }
    this.cachedAt.delete(id)
    this.versions.set(id, (this.versions.get(id) ?? 0) + 1)
    this.entries = { ...this.entries, [id]: { data: this.entries[id]?.data ?? null, status: 'loading' } }
    this.pending.add(id)
    this.emit()
    this.timer ??= setTimeout(() => { this.timer = undefined; void this.flush() }, 0)
  }
  /** Settlement refresh must not alter the quota response's account/user costs. */
  patchOfficialCost(id: number, officialUsd: number | null) {
    const entry = this.entries[id]
    if (!entry?.data) return
    this.entries = { ...this.entries, [id]: { ...entry, data: {
      ...entry.data, official_usd: officialUsd ?? undefined,
      official_usd_7d: officialUsd ?? undefined, official_usage_synced: true,
    } } }
    this.emit()
  }
  private async flush() {
    const ids = [...this.pending]
    this.pending.clear()
    const versions = new Map(ids.map(id => [id, this.versions.get(id)!]))
    for (const id of ids) this.inFlight.set(id, versions.get(id)!)
    const controller = new AbortController()
    let timeout: ReturnType<typeof setTimeout> | undefined
    const deadline = new Promise<never>((_resolve, reject) => {
      timeout = setTimeout(() => {
        controller.abort()
        reject(new Error('Account quota request timed out'))
      }, REQUEST_TIMEOUT_MS)
    })
    try {
      // Covers headers AND body; even a loader that ignores abort cannot lock
      // the per-account in-flight sentinel or overwrite a later successful retry.
      const result = await Promise.race([this.load(ids, controller.signal), deadline])
      const entries = { ...this.entries }
      for (const id of ids) {
        if (this.versions.get(id) !== versions.get(id)) continue
        const data = result.stats?.[String(id)] ?? null
        entries[id] = { data, status: 'ready' }
        // Source null entries clear the request cache as well as displayed data.
        if (data) this.cachedAt.set(id, this.now())
        else this.cachedAt.delete(id)
      }
      this.entries = entries
    } catch {
      const entries = { ...this.entries }
      for (const id of ids) {
        if (this.versions.get(id) === versions.get(id)) entries[id] = { data: entries[id]?.data ?? null, status: 'error' }
      }
      this.entries = entries
    } finally {
      if (timeout !== undefined) clearTimeout(timeout)
      for (const id of ids) if (this.inFlight.get(id) === versions.get(id)) this.inFlight.delete(id)
      this.emit()
    }
  }
}
