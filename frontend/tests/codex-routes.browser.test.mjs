import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { createStateFixture } from './state-management-fixture.mjs'

// Synthetic admin responses only; no upstream, tokens or production profiles.
const base = process.env.CODEX_ROUTES_UI_URL || 'http://127.0.0.1:5179'
const output = new URL('../../.dev/codex-routes-ui/', import.meta.url)
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, executablePath: process.env.CODEX_ROUTES_BROWSER_PATH || undefined })
try {
  for (const device of ['desktop', 'mobile']) {
    const context = await browser.newContext({ viewport: device === 'desktop' ? { width: 1440, height: 1050 } : { width: 390, height: 844 }, reducedMotion: 'reduce' })
    const fixture = createStateFixture()
    const paths = Object.fromEntries([1, 2, 3].map(id => [id, ['codex', 'basispoints'].map(upstream => ({ upstream, model: 'gpt-6-astra', allowed: true, capability: id === 1 ? 'supported' : id === 2 ? 'unsupported' : 'unknown', health: id === 3 && upstream === 'basispoints' ? 'cooldown' : 'ready', observed_at: id === 3 ? 0 : 1750000000000000000, source: id === 3 ? '' : 'upstream_completed', reason: id === 3 ? '' : 'success', health_reason: id === 3 ? 'upstream_access' : '' }))]))
    let key = { id: 1, name: 'Synthetic local key', enabled: true, allowed_group_ids: [10], limits: { upstream_channel: 'codex', codex_route_policy: 'inherit', codex_capability_filter: 'any' } }
    const requests = [], writes = [], errors = []
    await context.addInitScript(() => {
      localStorage.setItem('lang', 'zh')
      localStorage.setItem('theme', 'light')
      localStorage.setItem('codex2api:accounts:analysis-visible', 'false')
      localStorage.setItem('codex2api:accounts:page-mode', 'pool')
    })
    await context.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url())
      if (url.origin !== base) return route.abort()
      if (!url.pathname.startsWith('/api/')) return route.continue()
      const body = request.postData() ? JSON.parse(request.postData()) : {}
      const path = url.pathname.replace('/api/admin', '')
      requests.push({ path, params: Object.fromEntries(url.searchParams) })
      if (path === '/keys') return route.fulfill({ json: { keys: [key] } })
      if (path === '/keys/1' && request.method() === 'PATCH') {
        writes.push({ path, body }); key = { ...key, ...body }; return route.fulfill({ json: key })
      }
      if ((path === '/accounts/groups' || path === '/account-groups')) return route.fulfill({ json: { groups: [{ id: 10, name: 'Authorized dual upstream', channel: 'codex', account_count: 3, enabled: true }] } })
      if (/^\/accounts\/\d+\/codex-routes$/.test(path)) return route.fulfill({ json: { paths: paths[Number(path.split('/')[2])] } })
      if (path === '/accounts/codex/routes') {
        writes.push({ path, body })
        for (const id of body.ids) for (const item of paths[id]) if (item.upstream === body.upstream) {
          if ('allowed' in body) item.allowed = body.allowed
          if (body.reset_observations) Object.assign(item, { capability: 'unknown', health: 'ready', reason: 'admin_reset', source: 'admin_reset', observed_at: Date.now() * 1e6 })
        }
        return route.fulfill({ json: { updated: body.ids.length } })
      }
      const result = await fixture(url, request.method(), body)
      if (path === '/accounts') {
        result.accounts = result.accounts.map(a => ({ ...a, codex_paths: paths[a.id] }))
        if (url.searchParams.get('capability') === 'bps_supported') result.accounts = result.accounts.filter(a => a.id === 1)
        result.total = result.accounts.length
      }
      return route.fulfill({ json: result })
    })
    const page = await context.newPage()
    page.on('pageerror', error => errors.push(error.message))
    page.setDefaultTimeout(10000)
    const check = async name => {
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, name + ': horizontal overflow')
      await page.screenshot({ path: fileURLToPath(new URL(`${name}-${device}.png`, output)), fullPage: true, animations: 'disabled' })
      assert.deepEqual(errors, [])
    }
    try {
      await page.goto(base + '/admin/accounts')
      const model = page.getByRole('textbox', { name: '能力对应的精确模型' })
      await model.fill('gpt-6-astra')
      await page.getByRole('button', { name: '能力筛选', exact: true }).click()
      await Promise.all([page.waitForResponse(r => r.url().includes('capability=bps_supported')), page.getByRole('option', { name: 'BPS 已支持', exact: true }).click()])
      assert.ok(requests.some(r => r.params.capability === 'bps_supported' && r.params.capability_model === 'gpt-6-astra'))
      await page.getByText('demo-1@example.test', { exact: true }).first().waitFor()
      await check('accounts-filter')
      await page.getByRole('checkbox').first().check()
      await page.getByRole('button', { name: '管理上游路径（1 个）' }).first().click()
      await page.getByRole('button', { name: '管理操作' }).click()
      await page.getByRole('option', { name: '禁用此路径' }).click()
      await page.getByRole('button', { name: '应用', exact: true }).click()
      await page.getByRole('status').filter({ hasText: '已更新 1 个账号' }).waitFor()
      assert.deepEqual(writes.at(-1).body, { ids: [1], upstream: 'basispoints', allowed: false })
      await page.getByRole('button', { name: '管理操作' }).click()
      await page.getByRole('option', { name: '清除能力观察' }).click()
      await page.getByRole('button', { name: '应用', exact: true }).click()
      await page.getByRole('status').filter({ hasText: '已更新 1 个账号' }).waitFor()
      assert.equal(paths[1][1].allowed, false)
      assert.equal(paths[1][1].capability, 'unknown')
      await check('accounts-manage')
      await page.goto(base + '/admin/api-keys')
      await page.getByRole('button', { name: '编辑', exact: true }).first().click()
      await page.getByRole('tab', { name: '高级限额', exact: true }).click()
      await page.locator('#codex-route-policy').click()
      await page.getByRole('option', { name: '仅 BPS', exact: true }).click()
      await page.locator('#codex-capability-filter').click()
      await page.getByRole('option', { name: 'BPS 已确认支持', exact: true }).click()
      await check('key-routing')
      await page.getByRole('button', { name: '保存', exact: true }).click()
      await page.getByRole('dialog').waitFor({ state: 'hidden' })
      assert.equal(key.limits.codex_route_policy, 'basispoints_only')
      assert.equal(key.limits.codex_capability_filter, 'basispoints_supported')
      assert.deepEqual(key.allowed_group_ids, [10])
      await page.getByRole('button', { name: '编辑', exact: true }).first().click()
      await page.getByRole('tab', { name: '高级限额', exact: true }).click()
      assert.match(await page.locator('#codex-route-policy').innerText(), /仅 BPS/)
      assert.match(await page.locator('#codex-capability-filter').innerText(), /BPS 已确认支持/)
      await check('key-reopened')
      console.log('Codex routes browser interactions passed: ' + device)
    } catch (error) {
      console.error((await page.locator('body').innerText()).slice(-14000))
      throw error
    } finally { await context.close() }
  }
} finally { await browser.close() }
