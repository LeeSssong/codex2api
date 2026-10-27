import type { AccountQuotaWindow, AccountRow } from '../types'

type Slot = '5h' | '7d'
type StatsStatus = 'loading' | 'ready' | 'error'
type QuotaAccount = Pick<AccountRow, 'id'>
export type QuotaWindowView = Omit<Partial<AccountQuotaWindow>, 'label' | 'state'> & {
  label: AccountQuotaWindow['label']
  state: AccountQuotaWindow['state'] | 'loading'
}
function validAmount(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}
export function formatQuotaUSD(value?: number): string {
  return validAmount(value) ? `$${value.toFixed(2)}` : '—'
}
/** The successful response owns every display field until replaced atomically. */
export function quotaWindowView(_account: QuotaAccount, slot: Slot, snapshot?: AccountQuotaWindow, status: StatsStatus = 'loading', _now = Date.now()): QuotaWindowView {
  if (!snapshot) return { label: slot, state: status === 'ready' ? 'unavailable' : status }
  if (snapshot.state === 'error') return { label: snapshot.label, state: 'error', reason: snapshot.reason }
  const view: QuotaWindowView = { ...snapshot }
  for (const key of ['used_usd', 'estimated_total_usd', 'estimated_remaining_usd', 'user_usd', 'standard_usd', 'legacy_requests', 'requests', 'tokens', 'used_percent'] as const) {
    if (!validAmount(view[key])) delete view[key]
  }
  if (!(validAmount(view.used_percent) && view.used_percent > 0 && validAmount(view.used_usd) && view.used_usd > 0 && view.state === 'ready')) {
    delete view.estimated_total_usd
    delete view.estimated_remaining_usd
  }
  return view
}

/** Countdown changes never invalidate the response that owns the quota values. */
export function quotaResetText(resetAt: string | undefined, percent: number | undefined, now = Date.now()): string | undefined {
  const reset = Date.parse(resetAt ?? '')
  if (!Number.isFinite(reset)) return undefined
  const diff = reset - now
  if (diff <= 0) return percent !== undefined && percent > 0 ? 'pending' : 'now'
  const hours = Math.floor(diff / 3600000)
  const minutes = Math.floor(diff % 3600000 / 60000)
  if (hours >= 24) return `${Math.floor(hours / 24)}d ${hours % 24}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}
