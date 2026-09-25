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
    const probes = {}, probeRequests = []
    let probeMode = 'blocked'
    let key = { id: 1, name: 'Synthetic local key', enabled: true, allowed_group_ids: [10], limits: { upstream_channel: 'codex', codex_route_policy: 'inherit', codex_capability_filter: 'any' } }
    const requests = [], writes = [], errors = []
    await context.addInitScript(() => {
      localStorage.setItem('lang', 'zh')
      localStorage.setItem('theme', 'light')
      localStorage.setItem('codex2api:accounts:analysis-visible', 'false')
      localStorage.setItem('codex2api:accounts:page-mode', 'pool')
      const nativeFetch = window.fetch.bind(window)
      window.fetch = async (input, init) => {
        if (!window.__holdCodexProbe || !String(input).includes('/accounts/codex/probe?stream=true')) return nativeFetch(input, init)
        window.__canceledProbeBody = JSON.parse(init.body)
        return new Response(new ReadableStream({ start(controller) {
          controller.enqueue(new TextEncoder().encode('data: {"type":"start","total":1,"completed":0}\n\n'))
          init.signal.addEventListener('abort', () => { window.__probeWasAborted = true; controller.error(new DOMException('Canceled', 'AbortError')) }, { once: true })
        } }), { headers: { 'Content-Type': 'text/event-stream' } })
      }
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
      if (/^\/accounts\/\d+\/codex-routes$/.test(path)) {
        const id = Number(path.split('/')[2]), model = url.searchParams.get('model')
        return route.fulfill({ json: { paths: paths[id], probes: (probes[id] || []).filter(result => !model || result.model === model) } })
      }
      if (path === '/accounts/codex/probe') {
        probeRequests.push({ body, stream: url.searchParams.get('stream') })
        const results = body.ids.map(id => ({ account_id: id, upstream: 'basispoints', model: body.model, level: body.level, outcome: probeMode === 'blocked' ? 'blocked' : 'supported', capability: probeMode === 'blocked' ? 'unknown' : 'supported', basic_outcome: probeMode === 'blocked' ? 'blocked' : 'supported', tools_outcome: body.level === 'tools' ? 'supported' : 'not_run', http_status: probeMode === 'blocked' ? 403 : 200, reported_status: probeMode === 'blocked' ? 403 : 200, started_at: '2026-09-25T12:00:00Z', finished_at: probeMode === 'incomplete' ? '' : '2026-09-25T12:00:01Z', duration_ms: 1000, attempts: body.level === 'tools' ? 3 : 1, error_code: probeMode === 'blocked' ? 'ambiguous_usage_rejection' : '' }))
        for (const result of results) probes[result.account_id] = [...(probes[result.account_id] || []).filter(previous => previous.level !== result.level), result]
        const events = [{ type: 'start', total: results.length, completed: 0 }, ...results.map((result, index) => ({ type: 'result', total: results.length, completed: index + 1, result })), { type: 'done', total: results.length, completed: results.length }]
        return route.fulfill({ contentType: 'text/event-stream', body: events.map(event => `data: ${JSON.stringify(event)}\n\n`).join('') })
      }
      if (/^\/accounts\/\d+$/.test(path) && request.method() === 'GET') {
        const id = Number(path.split('/')[2]), accounts = (await fixture(new URL('/api/admin/accounts', base))).accounts
        return route.fulfill({ json: { ...accounts.find(account => account.id === id), codex_paths: paths[id], detail_loaded: true } })
      }
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
      if (!process.env.CODEX_ROUTES_SKIP_SCREENSHOTS) await page.screenshot({ path: fileURLToPath(new URL(`${name}-${device}.png`, output)), fullPage: true, animations: 'disabled' })
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
      const probe = page.getByRole('region', { name: 'BPS 强测试', exact: true })
      const runProbe = probe.getByRole('button', { name: '强测 BPS（1 个）' })
      assert.equal(await runProbe.isDisabled(), true)
      await probe.getByRole('textbox', { name: '强测试的精确模型' }).fill('gpt-6-astra')
      const writesBeforeProbe = writes.length
      await runProbe.click()
      await probe.locator('[data-probe-outcome="blocked"]').first().waitFor()
      assert.deepEqual(probeRequests.at(-1), { body: { ids: [1], model: 'gpt-6-astra', level: 'basic' }, stream: 'true' })
      assert.equal(await probe.locator('[data-probe-outcome="supported"]').count(), 0)
      assert.match(await probe.innerText(), /ambiguous_usage_rejection/)
      assert.equal(writes.length, writesBeforeProbe)
      assert.equal(paths[1][1].allowed, false)
      await probe.getByText('最近的基础 / 工具测试', { exact: true }).click()
      await probe.locator('details [data-probe-outcome="blocked"]').waitFor()
      assert.ok(requests.some(request => request.path === '/accounts/1/codex-routes' && request.params.model === 'gpt-6-astra'))
      await check('bps-blocked')
      probeMode = 'supported'
      await probe.getByRole('button', { name: '测试级别', exact: true }).click()
      await page.getByRole('option', { name: '工具回环测试', exact: true }).click()
      await runProbe.click()
      await probe.locator('[data-probe-outcome="supported"]').first().waitFor()
      assert.equal(probeRequests.at(-1).body.level, 'tools')
      assert.match(await probe.innerText(), /3 次请求/)
      if (!process.env.CODEX_ROUTES_SKIP_SCREENSHOTS) await probe.screenshot({ path: fileURLToPath(new URL(`bps-tools-${device}.png`, output)), animations: 'disabled' })
      probeMode = 'incomplete'
      await runProbe.click()
      await probe.locator('[data-probe-outcome="protocol_error"]').first().waitFor()
      assert.match(await probe.innerText(), /缺少完整成功的 BPS 结束证据/)
      await page.evaluate(() => { window.__holdCodexProbe = true })
      await runProbe.click()
      await probe.getByRole('button', { name: '取消测试', exact: true }).click()
      await probe.getByRole('status').filter({ hasText: '已取消测试' }).waitFor()
      assert.equal(await page.evaluate(() => window.__probeWasAborted), true)
      assert.deepEqual(await page.evaluate(() => window.__canceledProbeBody), { ids: [1], model: 'gpt-6-astra', level: 'tools' })
      assert.equal(writes.length, writesBeforeProbe)
      assert.equal(requests.some(request => /connection.test|\/test$/.test(request.path)), false)
      await check('bps-canceled')
      await page.evaluate(() => { window.__holdCodexProbe = false })
      probeMode = 'supported'
      await page.getByRole('button', { name: '能力筛选', exact: true }).click()
      await page.getByRole('option', { name: '全部能力', exact: true }).click()
      await page.getByText('demo-3@example.test', { exact: true }).first().waitFor()
      if (device === 'mobile') await page.getByRole('button', { name: '本页全选', exact: true }).click()
      else await page.getByRole('checkbox').first().check()
      await page.getByRole('button', { name: '管理上游路径（3 个）', exact: true }).waitFor()
      await probe.getByRole('button', { name: '强测 BPS（3 个）' }).click()
      await probe.getByRole('status').filter({ hasText: '本批测试结束：3 / 3' }).waitFor()
      assert.deepEqual(probeRequests.at(-1).body, { ids: [1, 2, 3], model: 'gpt-6-astra', level: 'tools' })
      assert.equal(writes.length, writesBeforeProbe)
      await page.getByTitle('查看详情', { exact: true }).first().click()
      const detail = page.getByRole('dialog')
      await detail.getByRole('button', { name: '管理上游路径（1 个）', exact: true }).click()
      await detail.getByRole('textbox', { name: '强测试的精确模型' }).fill('gpt-6-astra')
      await detail.getByRole('button', { name: '强测 BPS（1 个）' }).click()
      await detail.locator('[data-probe-outcome="supported"]').first().waitFor()
      assert.deepEqual(probeRequests.at(-1).body, { ids: [1], model: 'gpt-6-astra', level: 'basic' })
      await page.keyboard.press('Escape')
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
