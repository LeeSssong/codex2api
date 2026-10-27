import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { createStateFixture } from './state-management-fixture.mjs'

const base = process.env.QUOTA_UI_URL || 'http://127.0.0.1:41527'
const output = new URL('../../.dev/quota-ui/', import.meta.url)
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
try {
  for (const device of ['desktop', 'mobile']) for (const theme of ['light', 'dark']) {
    console.log('fixture', device, theme)
    const context = await browser.newContext({ viewport: device === 'desktop' ? { width: 1440, height: 1050 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: 'reduce' })
    const fixture = createStateFixture()
    const now = Math.floor(Date.now() / 1000) * 1000
    const iso = offset => new Date(now + offset * 1000).toISOString()
    let reset = iso(7200), dataReset = reset, mode = 'loading', quotaMode = 'ready', calls = 0, release
    const snapshot = (id, slot, state = 'ready') => {
      const monthly = false
      const seconds = slot === '5h' ? 18000 : monthly ? 2592000 : 604800
      // Match production: list/live timestamps omit fractions, quota stats retain them.
      const end = new Date(Date.parse(slot === '5h' ? dataReset : iso(3 * 86400)) + 477).toISOString()
      return { label: slot === '5h' ? '5h' : monthly ? '30d' : '7d', state: quotaMode === 'entry-error' ? 'error' : quotaMode === 'cycle' ? 'no_usage' : state, window_id: String(id) + ':' + seconds + ':' + Math.floor(Date.parse(end) / 1000), start_at: new Date(Date.parse(end) - seconds * 1000).toISOString(), reset_at: end, as_of: iso(-60), observed_at: iso(-120), used_percent: quotaMode === 'cycle' ? 0 : id === 2 ? .1 : id === 3 ? 0 : 30, used_usd: quotaMode === 'cycle' ? 0 : id === 2 ? .0002 : id === 3 ? 0 : 12, estimated_total_usd: id === 2 ? .2 : id === 3 ? undefined : 40, estimated_remaining_usd: id === 3 ? undefined : 28, requests: id === 3 ? 0 : 25, tokens: id === 3 ? 0 : 123456, user_usd: id === 3 ? 0 : 14 }
    }
    const accounts = () => [1, 2, 3, 4, 5].map(id => ({ id, name: 'quota-' + id + '@example.test', email: 'quota-' + id + '@example.test', enabled: true, plan_type: id === 5 ? 'pro' : id === 4 ? 'free' : 'plus', status: 'active', account_type: 'oauth', usage_percent_5h: id === 2 ? 5 : id === 3 ? 0 : 30, usage_percent_7d: id === 2 ? 5 : id === 3 ? 0 : 30, reset_5h_at: reset, reset_7d_at: iso(3 * 86400), usage_window_7d_kind: id === 4 ? 'monthly' : 'weekly', usage_window_7d_seconds: id === 4 ? 2592000 : 604800, usage_percent_spark: id === 5 ? 22 : undefined, reset_spark_at: iso(3600), created_at: iso(-86400), updated_at: iso(-60), codex_usage_updated_at: iso(-120), active_requests: 0, occupied_requests: 0, total_requests: 25, success_requests: 25, tags: [], group_ids: [], proxy_url: '', health_tier: 'healthy' }))
    const stats = () => Object.fromEntries(accounts().map(a => [a.id, { quota_5h: snapshot(a.id, '5h', a.id === 3 ? 'no_usage' : 'ready'), quota_7d: snapshot(a.id, '7d', a.id === 3 ? 'no_usage' : 'ready'), official_usd: 99, official_usage_synced: true }]))
    await context.addInitScript(({ theme }) => {
      localStorage.setItem('lang', 'zh'); localStorage.setItem('theme', theme)
      localStorage.setItem('codex2api:accounts:analysis-visible', 'false')
      if (!localStorage.getItem('codex2api:accounts:page-mode')) localStorage.setItem('codex2api:accounts:page-mode', 'personal')
    }, { theme })
    const pageErrors = []
    await context.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.origin !== base) return route.abort()
      if (!url.pathname.startsWith('/api/')) return route.continue()
      const path = url.pathname.replace('/api/admin', '')
      if (path === '/accounts/page-stats') {
        calls++
        const requestMode = mode
        const response = quotaMode === 'null' ? {} : stats()
        if (requestMode === 'loading') await new Promise(resolve => setTimeout(resolve, 1200))
        if (requestMode === 'delay') await new Promise(resolve => { release = resolve })
        if (requestMode === 'error') return route.fulfill({ status: 500, json: { error: 'synthetic stats failure' } })
        return route.fulfill({ json: { stats: response } })
      }
      if (path.startsWith('/accounts/') && Number.isInteger(Number(path.split('/').pop()))) return route.fulfill({ json: { ...accounts()[Number(path.split('/').pop()) - 1], detail_loaded: true } })
      if (path === '/accounts/live') return route.fulfill({ json: { accounts: Object.fromEntries(accounts().map(a => [a.id, { active_requests: 0, occupied_requests: 0, usage_percent_5h: reset === dataReset ? a.usage_percent_5h : 0, reset_5h_at: reset, reset_7d_at: a.reset_7d_at }])), session_slot_buffer_enabled: false } })
      if (path === '/accounts') return route.fulfill({ json: { accounts: accounts(), page: 1, page_size: 20, total: 5, summary: { total: 5, normal: 5, active: 5, healthy: 5, oauth: 5 }, facets: { tags: [], email_domains: [] }, stats_state: 'ready', snapshot_at: iso(0) } })
      const body = route.request().postData()
      return route.fulfill({ json: await fixture(url, route.request().method(), body ? JSON.parse(body) : {}) })
    })
    const page = await context.newPage()
    page.on('pageerror', error => pageErrors.push(error.message))
    await page.goto(base + '/admin/accounts')
    const windows = page.getByRole('region', { name: '5h 额度窗口', exact: true })
    await windows.first().getByText('正在加载窗口费用', { exact: true }).waitFor()
    await windows.first().scrollIntoViewIfNeeded()
    await windows.first().getByText('$40.00', { exact: true }).waitFor()
    assert.ok((await windows.first().innerText()).includes('已用 $12.00'))
    mode = 'ready'
    for (const index of [1, 2]) {
      await windows.nth(index).scrollIntoViewIfNeeded()
      await windows.nth(index).getByText('$0.00', { exact: true }).first().waitFor()
    }
    assert.ok(!(await windows.nth(1).innerText()).includes('样本较少'))
    assert.equal(await windows.nth(1).getByText('0%', { exact: true }).count(), 1)
    assert.match(await windows.nth(2).innerText(), /本窗口暂无消耗/)
    assert.equal(await windows.nth(2).getByText('$0.00', { exact: true }).count(), 1)
    assert.equal(await page.getByRole('region', { name: '30d 额度窗口', exact: true }).count(), 0)
    assert.equal(await page.locator('[role=progressbar][aria-label=spark]').count(), 1)
    const capture = async name => {
      await page.evaluate(async () => { await document.fonts.ready; await Promise.allSettled(document.getAnimations().filter(a => a.effect?.getComputedTiming().iterations !== Infinity).map(a => a.finished)) })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, name + ': page overflow')
      await page.screenshot({ path: fileURLToPath(new URL(name + '-' + device + '-' + theme + '.png', output)), fullPage: name !== 'detail', animations: 'disabled' })
    }
    await capture('list')
    await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('region', { name: '5h 额度窗口', exact: true }).getByText('$40.00', { exact: true }).waitFor()
    await dialog.getByRole('region', { name: '5h 额度窗口', exact: true }).scrollIntoViewIfNeeded()
    await capture('detail')
    await page.keyboard.press('Escape')
    let previousCalls = calls
    mode = 'delay'; quotaMode = 'cycle'; reset = iso(14400)
    for (let i = 0; i < 100 && calls === previousCalls; i++) await new Promise(resolve => setTimeout(resolve, 50))
    assert.ok(calls > previousCalls, 'live key change forced quota fetch')
    assert.equal(await windows.first().getByText('30%', { exact: true }).count(), 1)
    assert.equal(await windows.first().getByText('$12.00', { exact: true }).count(), 1)
    assert.equal(await windows.first().getByText('$40.00', { exact: true }).count(), 1)
    await capture('refreshing')
    release(); mode = 'ready'
    await windows.first().getByText('0%', { exact: true }).waitFor()
    assert.equal(await windows.first().getByText('$12.00', { exact: true }).count(), 0)
    mode = 'error'; previousCalls = calls; reset = iso(18000)
    for (let i = 0; i < 100 && calls === previousCalls; i++) await new Promise(resolve => setTimeout(resolve, 50))
    assert.ok(calls > previousCalls)
    await new Promise(resolve => setTimeout(resolve, 250))
    assert.equal(await windows.first().getByText('$0.00', { exact: true }).count(), 1)
    assert.equal(await windows.first().getByText('窗口费用查询失败', { exact: true }).count(), 0)
    await capture('transport-failure')
    mode = 'ready'; quotaMode = 'null'; reset = iso(21600)
    await windows.first().getByText('窗口信息不足，暂无法预测', { exact: true }).waitFor()
    assert.equal(await windows.first().getByText('$0.00', { exact: true }).count(), 0)
    quotaMode = 'entry-error'; reset = iso(25200)
    await windows.first().getByText('窗口费用查询失败', { exact: true }).waitFor()
    assert.equal(await windows.first().getByText('$12.00', { exact: true }).count(), 0)
    if (device === 'desktop') {
      mode = 'ready'; quotaMode = 'ready'; dataReset = reset
      await page.evaluate(() => { localStorage.setItem('codex2api:accounts:page-mode', 'pool'); localStorage.setItem('codex2api:accounts:view-mode', 'table') })
      await page.reload()
      await page.getByRole('table').waitFor()
      await windows.first().getByText('$40.00', { exact: true }).waitFor()
      await windows.first().scrollIntoViewIfNeeded()
      await capture('table')
    }
    assert.deepEqual(pageErrors, [], 'browser runtime errors')
    await context.close()
  }
} finally { await browser.close() }
console.log('Quota browser checks passed: desktop/mobile light/dark, source precision, list/detail cache, live reset background refresh, coherent in-flight display, atomic new cycle, retained transport failures, successful null/error entries, monthly scope, Spark.')
