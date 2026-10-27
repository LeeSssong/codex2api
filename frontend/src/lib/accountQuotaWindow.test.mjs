import assert from 'node:assert/strict'
import test from 'node:test'
import { formatQuotaUSD, quotaWindowView, quotaResetText } from './accountQuotaWindow.ts'

const now = Date.parse('2026-10-27T10:00:00Z')
const account = { id: 42, reset_5h_at: '2026-10-27T12:00:00Z', reset_7d_at: '2026-11-01T12:00:00Z', usage_percent_5h: 30, usage_percent_7d: 30 }
const window = { label: '5h', state: 'ready', window_id: `42:18000:${Date.parse(account.reset_5h_at) / 1000}`, start_at: '2026-10-27T07:00:00Z', reset_at: account.reset_5h_at, as_of: '2026-10-27T09:59:00Z', observed_at: '2026-10-27T09:59:00Z', used_percent: 30, used_usd: 12, estimated_total_usd: 40, estimated_remaining_usd: 28, requests: 25, tokens: 123456, user_usd: 14 }

test('source precision distinguishes missing and zero without inventing sub-cent notation', () => {
  for (const value of [undefined, NaN, Infinity, -1]) assert.equal(formatQuotaUSD(value), '—')
  assert.equal(formatQuotaUSD(0), '$0.00')
  assert.equal(formatQuotaUSD(0.0002), '$0.00')
  assert.equal(formatQuotaUSD(12.345), '$12.35')
})
test('successful quota remains coherent past ten minutes, expired resets, live changes and transport errors', () => {
  for (const status of ['loading', 'ready', 'error']) {
    const changed = { ...account, reset_5h_at: '2026-10-27T17:00:00Z', usage_percent_5h: 0 }
    const view = quotaWindowView(changed, '5h', window, status, now + 8 * 3600_000)
    assert.equal(view.state, 'ready')
    assert.equal(view.used_percent, 30)
    assert.equal(view.used_usd, 12)
    assert.equal(view.estimated_total_usd, 40)
    assert.equal(view.reset_at, window.reset_at)
  }
})
test('there is no observation or low-percent display gate', () => {
  const view = quotaWindowView(account, '5h', { ...window, observed_at: undefined, used_percent: 0.1, estimated_total_usd: 12000 }, 'ready', now)
  assert.equal(view.state, 'ready')
  assert.equal(view.estimated_total_usd, 12000)
})
test('fees remain at zero percent and absent percentage with no estimate', () => {
  for (const used_percent of [0, undefined, NaN]) {
    const view = quotaWindowView(account, '5h', { ...window, used_percent }, 'ready', now)
    assert.equal(view.used_usd, 12)
    assert.equal(view.estimated_total_usd, undefined)
  }
})
test('first missing data stays missing without live percentage becoming a forecast', () => {
  for (const [status, expected] of [['loading', 'loading'], ['error', 'error'], ['ready', 'unavailable']]) {
    const view = quotaWindowView(account, '5h', undefined, status, now)
    assert.equal(view.state, expected)
    assert.equal(view.used_percent, undefined)
    assert.equal(view.used_usd, undefined)
  }
})
test('completed new-cycle, no-usage and error snapshots replace all fields', () => {
  const cycle = { ...window, reset_at: '2026-10-27T17:00:00Z', used_percent: 0, used_usd: 0, state: 'no_usage' }
  const view = quotaWindowView(account, '5h', cycle, 'ready', now)
  assert.equal(view.state, 'no_usage')
  assert.equal(view.used_usd, 0)
  assert.equal(view.reset_at, cycle.reset_at)
  assert.equal(view.estimated_total_usd, undefined)
  const failedEntry = quotaWindowView(account, '5h', { ...window, state: 'error' }, 'ready', now)
  assert.equal(failedEntry.state, 'error')
  assert.equal(failedEntry.used_usd, undefined)
})
test('invalid fee values cannot predict', () => {
  for (const used_usd of [undefined, NaN, Infinity, -1]) assert.equal(quotaWindowView(account, '5h', { ...window, used_usd }, 'ready', now).estimated_total_usd, undefined)
})

test('source reset countdown reports pending versus now without hiding old quota', () => {
  assert.equal(quotaResetText(undefined, 30, now), undefined)
  assert.equal(quotaResetText('invalid', 30, now), undefined)
  assert.equal(quotaResetText(new Date(now - 1).toISOString(), 30, now), 'pending')
  assert.equal(quotaResetText(new Date(now - 1).toISOString(), 0, now), 'now')
  assert.equal(quotaResetText(new Date(now + 30000).toISOString(), 30, now), '0m')
  assert.equal(quotaResetText(new Date(now + 90 * 60000).toISOString(), 30, now), '1h 30m')
  assert.equal(quotaResetText(new Date(now + 49 * 3600000).toISOString(), 30, now), '2d 1h')
})
