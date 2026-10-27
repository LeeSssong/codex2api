import assert from 'node:assert/strict'
import { chromium } from 'playwright'
import { createStateFixture } from './state-management-fixture.mjs'

const base = process.env.QUOTA_UI_URL || 'http://127.0.0.1:41527'
const browser = await chromium.launch({ headless: true })
try {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, reducedMotion: 'reduce' })
  const fixture = createStateFixture()
  const requests = [], errors = []
  const now = new Date().toISOString()
  const accounts = Array.from({ length: 12 }, (_, index) => ({ id: index + 1, name: `synthetic-${index + 1}@example.test`, email: `synthetic-${index + 1}@example.test`, enabled: true, status: 'active', plan_type: 'plus', account_type: 'oauth', openai_responses_api: index === 0, reset_5h_at: index === 0 ? undefined : new Date(Date.now() + 3600000).toISOString(), usage_percent_5h: index === 0 ? undefined : 30, usage_percent_7d: index === 0 ? undefined : 40, created_at: now, updated_at: now, tags: [], group_ids: [], proxy_url: '', health_tier: 'healthy' }))
  await context.addInitScript(() => {
    localStorage.setItem('lang', 'zh'); localStorage.setItem('codex2api:accounts:page-mode', 'personal')
    localStorage.setItem('codex2api:accounts:analysis-visible', 'false')
  })
  await context.route('**/*', async route => {
    const url = new URL(route.request().url())
    if (url.origin !== base) return route.abort()
    if (!url.pathname.startsWith('/api/')) return route.continue()
    const path = url.pathname.replace('/api/admin', '')
    if (path === '/accounts') return route.fulfill({ json: { accounts, page: 1, page_size: 20, total: accounts.length, summary: { total: accounts.length, normal: accounts.length, active: accounts.length, oauth: accounts.length }, facets: { tags: [], email_domains: [] }, stats_state: 'ready', snapshot_at: now } })
    if (path === '/accounts/live') return route.fulfill({ json: { accounts: {}, session_slot_buffer_enabled: false } })
    if (path === '/accounts/page-stats') {
      const ids = url.searchParams.get('ids').split(',').map(Number); requests.push(ids)
      return route.fulfill({ json: { stats: Object.fromEntries(ids.map(id => [id, { usage_5h_detail: { requests: 25, tokens: 12345, account_billed: 12.3456, user_billed: 20 }, usage_7d_detail: { requests: 50, tokens: 23456, account_billed: 24, user_billed: 40 }, usage_today_detail: { requests: 25, tokens: 12345, account_billed: 12.3456, user_billed: 20 }, official_usage_synced: true }])) } })
    }
    const body = route.request().postData()
    return route.fulfill({ json: await fixture(url, route.request().method(), body ? JSON.parse(body) : {}) })
  })
  const page = await context.newPage(); page.on('pageerror', e => errors.push(e.message))
  await page.goto(base + '/admin/accounts')
  const first = page.locator('.codex-account-card').first()
  await first.scrollIntoViewIfNeeded()
  await first.getByText('$24.0000', { exact: false }).first().waitFor({ timeout: 5000 })
  assert.ok(requests.some(ids => ids.includes(1)), 'visible Responses row requests its native page statistics')
  assert.ok(!requests.some(ids => ids.includes(12)), 'mobile does not burst-fetch the offscreen Codex rows')
  assert.deepEqual(errors, [])
  console.log('Mobile Responses statistics loaded; offscreen Codex accounts remained lazy.')
  await context.close()
} finally { await browser.close() }
