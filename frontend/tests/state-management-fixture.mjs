import { createServer } from 'node:http'

// All values are synthetic. This fixture never contacts an upstream or reads a
// production configuration, credential, database, or saved browser profile.
export function createStateFixture({ scenario = 'renewal' } = {}) {
  const allHealthy = scenario === 'five-healthy'
  const start = Math.floor(Date.now() / 1000)
  const config = { enabled: true, require_valid_state: false, models: ['gpt-5.6-sol', 'gpt-5.6-luna', 'gpt-6-astra'], account_ids: [1, 2], proxy_ids: [1], source_ips: [], capture_mode: 'mixed', forward_proxy_id: 0, new_session: true, interval_seconds: 3, accepted_lengths: [292, 332], concurrency: 20, refresh_before_minutes: 30, staged_concurrency: true, urgent_before_minutes: 10, early_concurrency: 1, urgent_concurrency: 3, expired_concurrency: 10, urgent_business_concurrency: 1 }
  const raw = [1, 2, 3].map(id => ({ id, name: `demo-${id}@example.test`, email: `demo-${id}@example.test`, status: id === 2 ? 'unauthorized' : 'active', enabled: true, plan_type: 'plus', upstream_type: 'codex', active_requests: 0, occupied_requests: 0, request_count: 124 * id, today_requests: 28 * id, tags: [], group_ids: [], groups: [], created_at: new Date(start * 1000).toISOString(), usage_percent_5h: 12, usage_percent_5h_valid: true, usage_percent_7d: 24, usage_percent_7d_valid: true, health_tier: 'healthy', access_token: undefined }))
  if (allHealthy) {
    raw.push(...[4, 5].map(id => ({ ...raw[0], id, name: `demo-${id}@example.test`, email: `demo-${id}@example.test` })))
    for (const account of raw) {
      account.status = 'active'
      account.usage_percent_5h_valid = account.id === 1
      account.usage_percent_7d_valid = account.id === 1
      account.usage_percent_5h = account.id === 1 ? 12 : undefined
      account.usage_percent_7d = account.id === 1 ? 27 : undefined
    }
    config.account_ids = raw.map(account => account.id)
  }
  const entries = raw.slice(0, 2).flatMap(account => ['gpt-5.6-sol', 'gpt-5.6-luna', 'gpt-6-astra'].map((model, i) => ({ account_id: account.id, account_name: account.name, model, valid: i < 2, available: account.id === 1 && i < 2, expires_at: i < 2 ? start + (i === 0 ? 540 : 1080) : 0, issued_at: start - (i === 0 ? 3060 : 2520), captured_at: start - 600, status: i < 2 ? 'ready' : 'waiting', capture_phase: account.id === 2 ? 'account_unavailable' : i === 0 ? 'collecting' : 'retrying', capture_stage: i === 0 ? 'urgent' : 'early', refreshing: account.id === 1 && i === 0, attempts: i + 12, http_status: account.id === 2 ? 429 : 200, last_length: i === 0 ? 292 : 332, retry_at: start + 30, retry_source: 'local_backoff', cooldown_reason: account.id === 2 ? 'unauthorized' : '', cooldown_until: account.id === 2 ? start + 3600 : 0, error: account.id === 2 ? 'upstream_429' : i === 1 ? 'state_not_newer' : '', proxy_name: 'Demo pool', proxy_id: 1 })))
  if (allHealthy) {
    entries.splice(0, entries.length, ...raw.flatMap(account => config.models.map(model => ({ account_id: account.id, account_name: account.name, model, valid: true, available: true, expires_at: start + 2700, issued_at: start - 900, captured_at: start - 900, status: 'ready', capture_phase: 'idle', refreshing: false, attempts: 1, http_status: 200, last_length: 292 }))))
  }
  function snapshot() {
    const now = Math.floor(Date.now() / 1000)
    const selected = entries.filter(entry => config.models.includes(entry.model) && (!config.account_ids.length || config.account_ids.includes(entry.account_id))).map(entry => ({ ...entry, valid: entry.valid && entry.expires_at > now, available: entry.available && entry.expires_at > now, refreshing: entry.refreshing && config.enabled, capture_phase: !config.enabled ? 'paused' : entry.capture_phase }))
    const models = config.models.map(model => ({ model, reuse_accounts: selected.filter(entry => entry.model === model && entry.valid).length, available_accounts: selected.filter(entry => entry.model === model && entry.valid && entry.available).length }))
    const summary = { enabled: config.enabled, require_valid_state: config.require_valid_state, total_accounts: raw.length, reuse_accounts: new Set(selected.filter(entry => entry.valid).map(entry => entry.account_id)).size, available_accounts: new Set(selected.filter(entry => entry.valid && entry.available).map(entry => entry.account_id)).size, valid_combinations: selected.filter(entry => entry.valid).length, covered_models: models.filter(model => model.available_accounts).length, models, next_expiry: Math.min(...selected.filter(entry => entry.valid).map(entry => entry.expires_at)), revision: JSON.stringify([config, models]) }
    const accounts = raw.map(account => ({ ...account, state_models: config.models.map(model => { const entry = selected.find(entry => entry.account_id === account.id && entry.model === model); return { model, in_scope: !config.account_ids.length || config.account_ids.includes(account.id), valid: entry?.valid ?? false, available: entry?.available ?? false, refreshing: entry?.refreshing ?? false, capture_phase: entry?.capture_phase ?? 'out_of_scope', expires_at: entry?.expires_at ?? 0, length: entry?.valid ? 292 : 0, restriction: entry?.cooldown_reason } }) }))
    return { config, selected, summary, accounts, now }
  }
  return async function handle(url, method = 'GET', body = {}) {
    const path = url.pathname.replace('/api/admin', '')
    if (path === '/state-pool/ipv6' && method === 'PUT') Object.assign(config, body)
    if (path === '/state-pool/ipv6/policy' && method === 'PATCH') config.require_valid_state = body.require_valid_state
    const { selected, summary, accounts, now } = snapshot()
    if (path.startsWith('/state-pool/ipv6')) return { config, entries: selected, summary, local_ips: ['2001:db8::10'], active_requests: config.enabled ? 5 : 0, account_concurrency: 10, running: config.enabled, server_time: now }
    if (path === '/state-pool') return { accounts: accounts.map(a => ({ id: a.id, name: a.name, plan: 'plus', available: a.id !== 2 })), proxies: [{ id: 1, name: 'Demo pool', enabled: true, test_status: 'success', last_test_ip: '192.0.2.1', supports_session_rotation: true }], entries: [], jobs: [], models: config.models, limits: { concurrency: 4, per_account: 2, per_proxy: 2 }, server_time: now }
    if (path === '/accounts/live') return { accounts: Object.fromEntries(accounts.map(a => [a.id, { active_requests: 0, occupied_requests: 0, state_models: a.state_models }])), state_summary: summary, server_time: now, session_slot_buffer_enabled: false }
    if (path === '/accounts') {
      const state = url.searchParams.get('state'), model = url.searchParams.get('state_model')
      const status = url.searchParams.get('status')
      const filtered = accounts.filter(a => (!['normal', 'scheduling', 'unsampled'].includes(status) || a.status === 'active' && (status !== 'unsampled' || !a.usage_percent_7d_valid && !a.usage_percent_5h_valid)) && (!['valid', 'available', 'missing'].includes(state) || a.state_models.some(item => item.valid && (state !== 'available' || item.available) && (!model || !config.models.includes(model) || item.model === model)) === (state !== 'missing')))
      return { accounts: filtered, page: 1, page_size: 20, total: filtered.length, summary: { total: raw.length, normal: allHealthy ? 5 : 2, active: allHealthy ? 5 : 2, healthy: allHealthy ? 5 : 2, abnormal: allHealthy ? 0 : 1, banned: allHealthy ? 0 : 1, oauth: raw.length, disabled: 0, rate_limited: 0, unsampled: allHealthy ? 4 : 0 }, facets: { tags: [], email_domains: [] }, stats_state: 'ready', snapshot_at: new Date(start * 1000).toISOString(), state_summary: summary }
    }
    if (path === '/accounts/page-stats') return { stats: {} }
    if (path === '/accounts/analysis') return null
    if (path === '/accounts/health-bars') return { bars: {} }
    if (path === '/accounts/groups') return { groups: [] }
    if (path === '/stats') return { total: 3, available: 2, error: 1, rate_limited: 0, today_requests: 168, channels: { codex: { total: 3, available: 2, error: 1, rate_limited: 0, today_requests: 168 } }, state_summary: summary }
    if (path === '/settings/visible-channels') return { channels: ['codex', 'claude', 'grok', 'antigravity'] }
    if (path === '/settings') return { site_name: 'codex2api', max_concurrency: 10, timezone: 'Asia/Shanghai', show_full_usage_numbers: false, public_account_portal_page_enabled: false, proxy_pool_enabled: false }
    if (path === '/usage/chart-data') return { buckets: [], models: [], model_distribution: [], account_distribution: [], channel_distribution: [], status_distribution: [] }
    if (path === '/usage/stats') return null
    if (path === '/api/branding') return { site_name: 'codex2api · 模拟数据' }
    if (path.includes('bootstrap')) return { needs_bootstrap: false }
    if (path === '/keys') return { keys: [{ id: 1, name: 'Synthetic local key', enabled: true }] }
    if (path.includes('health')) return { status: 'ok' }
    return {}
  }
}

if (process.argv.includes('--serve')) {
  const handle = createStateFixture()
  createServer(async (req, res) => {
    try {
      let input = ''
      for await (const chunk of req) input += chunk
      const value = await handle(new URL(req.url, 'http://127.0.0.1:18129'), req.method, input ? JSON.parse(input) : {})
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify(value))
    } catch { res.writeHead(400); res.end('{}') }
  }).listen(18129, '127.0.0.1')
}
