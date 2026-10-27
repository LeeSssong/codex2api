import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { createStateFixture } from './state-management-fixture.mjs'

const base = process.env.FIXED_EGRESS_UI_URL || 'http://127.0.0.1:41486'
const output = new URL('../../.impeccable/fixed-egress-review/', import.meta.url)
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
try {
  for (const mobile of [false, true]) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({ viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 1050 }, colorScheme: theme, reducedMotion: 'reduce' })
    const fixture = createStateFixture(); let fail = false
    let snapshot = { config: { enabled: false, revision: 1 }, pools: [{ id: 'pool-a', name: 'ISP A', enabled: true, strategy: 'stable', cooldown_seconds: 600, max_attempts: 3, rotate_429: false, retry_5xx: false }], providers: [{ id: 'provider-a', name: 'Provider A', base_url: 'https://ipapi.cliproxy.com', protocol: 'https', auth_format: '1', country: 'US', city: 'NY', whitelist_ips: ['203.0.113.2'], port_start: 1000, port_end: 1002, capacity: 2, refill_interval_seconds: 600, enabled: true, has_key: true }], resources: [{ id: 'r1', pool_id: 'pool-a', provider_id: 'provider-a', provider_port: 1000, endpoint: 'http://203.0.113.2:1000', observed_exit_ip: '198.51.100.1', state: 'healthy', owned: true, changed_at: new Date().toISOString() }, { id: 'r2', pool_id: 'pool-a', provider_id: 'provider-a', provider_port: 1001, endpoint: 'http://203.0.113.2:1001', observed_exit_ip: '198.51.100.2', state: 'healthy', owned: false, changed_at: new Date().toISOString() }], bindings: [{ account_id: 1, pool_id: 'pool-a', resource_id: 'r1', generation: 2, state: 'healthy', changed_reason: 'assigned', change_count: 0, changed_at: new Date().toISOString() }], enrollments: [{ account_id: 1, pool_id: 'pool-a' }], events: [], tasks: [], accounts: [{ id: 1, name: 'demo@example.test' }], resin_enabled: false, ipv6_enabled: false }
    await context.addInitScript(({ theme }) => { localStorage.setItem('lang', 'zh'); localStorage.setItem('theme', theme); localStorage.setItem('admin_key', 'fixture-admin-key'); localStorage.setItem('codex2api:first_setup_review_done_v1', '1') }, { theme })
    await context.route('**/*', async route => { const url = new URL(route.request().url()); if (url.origin !== base) return route.abort(); if (!url.pathname.startsWith('/api/')) return route.continue(); const body = route.request().postDataJSON() || {}; if (url.pathname === '/api/admin/bootstrap-status') return route.fulfill({ json: { needs_bootstrap: false } }); if (url.pathname === '/api/admin/health') return route.fulfill({ json: { status: 'ok' } }); if (url.pathname === '/api/admin/ipv6-egress') return route.fulfill({ json: { config: { enabled: false, source_ips: [], cooldown_seconds: 600, max_attempts: 3, retry_5xx: false, revision: 1 }, local_ips: [], bindings: [], cooldowns: [], account_names: {}, resin_enabled: false } }); if (url.pathname.startsWith('/api/admin/fixed-egress')) { if (fail && route.request().method() !== 'GET') return route.fulfill({ status: 409, json: { error: '固定池保存失败' } }); if (route.request().method() === 'PUT' && url.pathname.endsWith('/config')) snapshot = { ...snapshot, config: { ...body, revision: snapshot.config.revision + 1 } }; if (route.request().method() === 'POST' && url.pathname.includes('/rotate')) snapshot = { ...snapshot, bindings: [{ ...snapshot.bindings[0], resource_id: body.resource_id, generation: body.generation + 1 }] }; return route.fulfill({ json: snapshot }) } if (url.pathname === '/api/admin/proxies') return route.fulfill({ json: { proxies: [] } }); if (url.pathname.includes('/proxy-risk')) return route.fulfill({ json: { profiles: [], scores: [], jobs: [] } }); return route.fulfill({ json: await fixture(url, route.request().method(), body) }) })
    const page = await context.newPage(); const errors = []; page.on('pageerror', e => errors.push(e.message)); await page.goto(`${base}/admin/proxies`)
    const panel = page.getByRole('region', { name: '固定 ISP 出口池' }); try { await panel.waitFor({ timeout: 5000 }); await panel.getByLabel('提供商名称').first().waitFor({ timeout: 5000 }); await panel.getByText('198.51.100.1', { exact: true }).waitFor({ timeout: 5000 }) } catch (e) { await page.screenshot({ path: fileURLToPath(new URL(`debug-${mobile ? 'mobile' : 'desktop'}-${theme}.png`, output)) }); console.error('DEBUG URL', page.url(), 'TITLE', await page.title(), 'TEXT', (await page.locator('body').innerText()).slice(0, 1000)); throw e }
    await panel.getByRole('button', { name: '轮换' }).click(); await page.waitForTimeout(50); assert.equal(snapshot.bindings[0].resource_id, 'r2')
    await panel.getByRole('button', { name: '停止' }).click(); await page.locator('[role=dialog] button').last().click();
    await panel.getByRole('button', { name: '新建提供商' }).click(); await panel.getByLabel('提供商名称').last().waitFor();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true)
    await page.screenshot({ path: fileURLToPath(new URL(`${mobile ? 'mobile' : 'desktop'}-${theme}.png`, output)), animations: 'disabled' })
    fail = true; await panel.getByRole('button', { name: '保存池' }).click(); await panel.getByRole('alert').waitFor(); assert.match(await panel.getByRole('alert').innerText(), /固定池保存失败/)
    assert.deepEqual(errors, []); await context.close()
  }
} finally { await browser.close() }
console.log('Fixed egress browser checks passed (desktop/mobile, light/dark, rotation, confirmation, provider creation, error).')
