import type { AccountQuotaWindow, AccountRow } from '../types'

type Slot = '5h' | '7d'
type StatsStatus = 'loading' | 'ready' | 'error'
const QUOTA_OBSERVATION_MAX_AGE_MS = 10 * 60 * 1000
type QuotaAccount = Pick<AccountRow, 'id' | 'reset_5h_at' | 'reset_7d_at' | 'usage_window_7d_kind' | 'usage_window_7d_seconds' | 'quota_5h' | 'quota_7d'>
export type QuotaWindowView = Omit<Partial<AccountQuotaWindow>, 'label' | 'state'> & {
  label: AccountQuotaWindow['label']
  state: AccountQuotaWindow['state'] | 'loading'
}

function validAmount(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

export function formatQuotaUSD(value?: number): string {
  if (!validAmount(value)) return '—'
  if (value > 0 && value < 0.01) return '<$0.01'
  return `$${value.toFixed(2)}`
}

function windowShape(account: QuotaAccount, slot: Slot) {
  const duration = slot === '5h' ? 18000 : (account.usage_window_7d_seconds && account.usage_window_7d_seconds > 0
    ? account.usage_window_7d_seconds : account.usage_window_7d_kind === 'monthly' ? 2592000 : 604800)
  const label: AccountQuotaWindow['label'] = slot === '5h' ? '5h' : account.usage_window_7d_kind === 'monthly' || duration >= 28 * 86400 && duration <= 31 * 86400 ? '30d' : '7d'
  return { duration, label, reset: Date.parse((slot === '5h' ? account.reset_5h_at : account.reset_7d_at) ?? '') }
}

/** Match real duration and reset, never the current percentage alone. */
function sameWindow(account: QuotaAccount, slot: Slot, snapshot: AccountQuotaWindow, now: number): boolean {
  const { duration, label, reset } = windowShape(account, slot)
  const start = Date.parse(snapshot.start_at ?? '')
  const snapshotReset = Date.parse(snapshot.reset_at ?? '')
  const asOf = Date.parse(snapshot.as_of)
  const observed = snapshot.observed_at ? Date.parse(snapshot.observed_at) : undefined
  return snapshot.window_id === `${account.id}:${duration}:${Math.floor(reset / 1000)}` && snapshot.label === label && Number.isFinite(reset) && reset > now &&
    Math.floor(reset / 1000) === Math.floor(snapshotReset / 1000) && Number.isFinite(start) && Math.abs((snapshotReset - start) / 1000 - duration) < 1 &&
    Number.isFinite(asOf) && asOf >= start && asOf < reset &&
    (observed === undefined || Number.isFinite(observed) && observed >= start && observed <= asOf)
}

/** A late page-stats response must never fill a window that live state has reset. */
export function selectAccountQuotaWindow(account: QuotaAccount, slot: Slot, incoming?: AccountQuotaWindow, now = Date.now()): AccountQuotaWindow | undefined {
  const existing = slot === '5h' ? account.quota_5h : account.quota_7d
  return [existing, incoming].filter((value): value is AccountQuotaWindow => Boolean(value && sameWindow(account, slot, value, now)))
    .sort((a, b) => (Date.parse(b.as_of) || 0) - (Date.parse(a.as_of) || 0))[0]
}

export function quotaWindowView(account: QuotaAccount, slot: Slot, snapshot?: AccountQuotaWindow, status: StatsStatus = 'loading', now = Date.now()): QuotaWindowView {
  const { label } = windowShape(account, slot)
  if (status === 'error') return { label, state: 'error' }
  if (!snapshot) return { label, state: status === 'ready' ? 'unavailable' : status }
  if (snapshot.state === 'error') return { label, state: 'error', reason: snapshot.reason }
  if (snapshot.state === 'unavailable' && !validAmount(snapshot.used_usd)) return { label, state: 'unavailable', reason: snapshot.reason }
  if (!sameWindow(account, slot, snapshot, now)) return { label, state: 'stale' }
  const view: QuotaWindowView = { ...snapshot, label }
  for (const key of ['used_usd', 'estimated_total_usd', 'estimated_remaining_usd', 'user_usd', 'requests', 'tokens'] as const) {
    if (!validAmount(view[key])) delete view[key]
  }
  const percent = snapshot.used_percent
  if (!validAmount(percent)) delete view.used_percent
  const observed = Date.parse(snapshot.observed_at ?? '')
  if (!Number.isFinite(observed) && (view.state === 'ready' || view.state === 'low_sample')) {
    view.state = 'unavailable'
    view.reason = 'missing_observation'
  } else if (Number.isFinite(observed) && Math.max(now, Date.parse(snapshot.as_of)) - observed > QUOTA_OBSERVATION_MAX_AGE_MS) {
    view.state = 'stale'
    view.reason = 'observation_stale'
  }
  const canEstimate = validAmount(percent) && percent > 0 && validAmount(view.used_usd) && view.used_usd > 0 && (view.state === 'ready' || view.state === 'low_sample')
  if (!canEstimate) {
    delete view.estimated_total_usd
    delete view.estimated_remaining_usd
  }
  if (validAmount(percent) && percent > 0 && percent < 10 && canEstimate) view.state = 'low_sample'
  return view
}
