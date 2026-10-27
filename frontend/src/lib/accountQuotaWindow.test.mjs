import assert from 'node:assert/strict'
import test from 'node:test'
import { formatQuotaUSD, quotaWindowView, selectAccountQuotaWindow } from './accountQuotaWindow.ts'

const now = Date.parse('2026-10-27T10:00:00Z')
const account = { id: 42, reset_5h_at: '2026-10-27T12:00:00Z', reset_7d_at: '2026-11-01T12:00:00Z', usage_percent_5h: 30, usage_percent_7d: 30 }
const window = { label: '5h', state: 'ready', window_id: `42:18000:${Date.parse(account.reset_5h_at) / 1000}`, start_at: '2026-10-27T07:00:00Z', reset_at: account.reset_5h_at, as_of: '2026-10-27T09:59:00Z', used_percent: 30, used_usd: 12, estimated_total_usd: 40, estimated_remaining_usd: 28, requests: 25, tokens: 123456, user_usd: 14 }

test('quota amounts distinguish missing, zero and positive sub-cent USD', () => {
  assert.equal(formatQuotaUSD(undefined), '—')
  assert.equal(formatQuotaUSD(NaN), '—')
  assert.equal(formatQuotaUSD(Infinity), '—')
  assert.equal(formatQuotaUSD(-1), '—')
  assert.equal(formatQuotaUSD(0), '$0.00')
  assert.equal(formatQuotaUSD(0.00001), '<$0.01')
  assert.equal(formatQuotaUSD(12), '$12.00')
})
test('ready window preserves same-window account fees and server estimate', () => {
  const view = quotaWindowView(account, '5h', window, 'ready', now)
  assert.equal(view.state, 'ready')
  assert.equal(view.used_usd, 12)
  assert.equal(view.estimated_total_usd, 40)
  assert.equal(view.estimated_remaining_usd, 28)
})
test('low sample remains explicit and zero percent cannot estimate', () => {
  assert.equal(quotaWindowView(account, '5h', { ...window, used_percent: 9 }, 'ready', now).state, 'low_sample')
  const zero = quotaWindowView(account, '5h', { ...window, state: 'no_usage', used_percent: 0, used_usd: 0 }, 'ready', now)
  assert.equal(zero.state, 'no_usage')
  assert.equal(zero.used_usd, 0)
  assert.equal(zero.estimated_total_usd, undefined)
  assert.equal(quotaWindowView(account, '5h', { ...window, used_percent: NaN }, 'ready', now).estimated_total_usd, undefined)
})
test('missing, loading and failed statistics do not become zero usage', () => {
  for (const [status, expected] of [['loading', 'loading'], ['error', 'error'], ['ready', 'unavailable']]) {
    const view = quotaWindowView(account, '5h', undefined, status, now)
    assert.equal(view.state, expected)
    assert.equal(view.used_usd, undefined)
  }
})
test('changed reset or expired window discards fees and forecasts', () => {
  for (const candidate of [{ ...window, reset_at: '2026-10-27T11:00:00Z' }, { ...window, start_at: undefined }, { ...window, window_id: '' }]) {
    const view = quotaWindowView(account, '5h', candidate, 'ready', now)
    assert.equal(view.state, 'stale')
    assert.equal(view.used_usd, undefined)
    assert.equal(view.estimated_total_usd, undefined)
  }
  assert.equal(quotaWindowView(account, '5h', window, 'ready', Date.parse(account.reset_5h_at)).state, 'stale')
})
test('equivalent timestamp spellings identify the same window', () => {
  assert.equal(quotaWindowView(account, '5h', { ...window, reset_at: '2026-10-27T08:00:00-04:00' }, 'ready', now).state, 'ready')
})
test('monthly duration cannot be filled by a weekly snapshot', () => {
  const monthly = { ...account, usage_window_7d_kind: 'monthly', usage_window_7d_seconds: 2592000 }
  const weekly = { ...window, window_id: `42:604800:${Date.parse(account.reset_7d_at) / 1000}`, label: '7d', reset_at: account.reset_7d_at, start_at: '2026-10-25T12:00:00Z' }
  assert.equal(quotaWindowView(monthly, '7d', weekly, 'ready', now).state, 'stale')
  const month = { ...weekly, window_id: `42:2592000:${Date.parse(account.reset_7d_at) / 1000}`, label: '30d', start_at: '2026-10-02T12:00:00Z' }
  assert.equal(quotaWindowView(monthly, '7d', month, 'ready', now).state, 'ready')
})
test('latest compatible snapshot wins and changed reset rejects late page stats', () => {
  const fresh = { ...window, as_of: '2026-10-27T10:00:00Z', used_usd: 15 }
  assert.equal(selectAccountQuotaWindow({ ...account, quota_5h: fresh }, '5h', window, now), fresh)
  const changed = { ...account, reset_5h_at: '2026-10-27T17:00:00Z' }
  assert.equal(selectAccountQuotaWindow(changed, '5h', window, now), undefined)
  assert.equal(quotaWindowView(changed, '5h', window, 'ready', now).state, 'stale')
})
test('non-finite fees and error snapshots never predict', () => {
  for (const used_usd of [undefined, NaN, Infinity, -1]) {
    assert.equal(quotaWindowView(account, '5h', { ...window, used_usd }, 'ready', now).estimated_total_usd, undefined)
  }
  const error = quotaWindowView(account, '5h', { ...window, state: 'error' }, 'ready', now)
  assert.equal(error.state, 'error')
  assert.equal(error.used_usd, undefined)
})

test('failed page statistics do not silently retain a ready snapshot', () => {
  const view = quotaWindowView(account, '5h', window, 'error', now)
  assert.equal(view.state, 'error')
  assert.equal(view.used_usd, undefined)
})

test('future statistics and observations from another window cannot predict', () => {
  for (const candidate of [
    { ...window, as_of: '2026-10-27T12:01:00Z' },
    { ...window, as_of: '2026-10-27T06:59:00Z' },
    { ...window, observed_at: '2026-10-27T06:59:00Z' },
  ]) {
    assert.equal(quotaWindowView(account, '5h', candidate, 'ready', now).estimated_total_usd, undefined)
  }
})

test('valid window fees remain visible when the official percentage is unavailable', () => {
  const view = quotaWindowView(account, '5h', { ...window, state: 'unavailable', used_percent: undefined, reason: 'missing_percent' }, 'ready', now)
  assert.equal(view.state, 'unavailable')
  assert.equal(view.used_usd, 12)
  assert.equal(view.estimated_total_usd, undefined)
})

test('non-monthly custom durations retain the server long-window label', () => {
  const custom = { ...account, usage_window_7d_seconds: 14 * 86400 }
  const snapshot = { ...window, label: '7d', window_id: '42:1209600:' + Date.parse(account.reset_7d_at) / 1000, reset_at: account.reset_7d_at, start_at: '2026-10-18T12:00:00Z' }
  assert.equal(quotaWindowView(custom, '7d', snapshot, 'ready', now).state, 'ready')
})
