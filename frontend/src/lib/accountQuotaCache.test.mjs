import assert from 'node:assert/strict'
import test from 'node:test'
import { AccountQuotaCache, accountQuotaRefreshKey } from './accountQuotaCache.ts'

const tick = () => new Promise(resolve => setTimeout(resolve, 5))
function fixture() {
  let now = 1000
  const requests = []
  const cache = new AccountQuotaCache(ids => new Promise((resolve, reject) => requests.push({ ids, resolve, reject })), () => now)
  return { cache, requests, advance: ms => { now += ms } }
}
const old = { quota_5h: { state: 'ready', used_percent: 30, used_usd: 12, estimated_total_usd: 40, reset_at: 'old' } }
const next = { quota_5h: { state: 'no_usage', used_percent: 0, used_usd: 0, reset_at: 'new' } }

test('desktop requests batch, dedupe, cache for five minutes and retain displayed data beyond TTL', async () => {
  const { cache, requests, advance } = fixture()
  cache.request(1, 'a'); cache.request(2, 'b'); cache.request(1, 'a')
  await tick(); assert.deepEqual(requests[0].ids, [1, 2]); assert.equal(requests.length, 1)
  cache.request(1, 'a'); await tick(); assert.equal(requests.length, 1)
  requests[0].resolve({ stats: { 1: old, 2: next } }); await tick()
  advance(299999); cache.request(1, 'a'); await tick(); assert.equal(requests.length, 1)
  advance(2); cache.request(1, 'a'); await tick(); assert.equal(requests.length, 2)
  assert.equal(cache.getSnapshot()['1'].data, old)
  advance(3600000); assert.equal(cache.getSnapshot()['1'].data, old)
  requests[1].resolve({ stats: { 1: next } }); await tick()
})
test('refresh keys bypass TTL, keep entire response in flight and after network failure', async () => {
  const { cache, requests } = fixture()
  cache.request(1, 'a'); await tick(); requests[0].resolve({ stats: { 1: old } }); await tick()
  cache.request(1, 'b'); await tick()
  assert.equal(cache.getSnapshot()['1'].data, old)
  assert.equal(cache.getSnapshot()['1'].status, 'loading')
  requests[1].reject(new Error('transport')); await tick()
  assert.equal(cache.getSnapshot()['1'].data, old)
  assert.equal(cache.getSnapshot()['1'].status, 'error')
  cache.request(1, 'b', true); await tick(); requests[2].resolve({ stats: { 1: next } }); await tick()
  assert.equal(cache.getSnapshot()['1'].data, next)
})
test('manual bypass fences late replies and success null or error entries remove previous fees atomically', async () => {
  const { cache, requests } = fixture()
  cache.request(1, 'a'); await tick(); requests[0].resolve({ stats: { 1: old } }); await tick()
  cache.request(1, 'b'); await tick(); cache.request(1, 'b', true); await tick()
  requests[2].resolve({ stats: { 1: next } }); await tick()
  requests[1].resolve({ stats: { 1: old } }); await tick()
  assert.equal(cache.getSnapshot()['1'].data, next)
  cache.request(1, 'b', true); await tick(); requests[3].resolve({ stats: {} }); await tick()
  assert.equal(cache.getSnapshot()['1'].data, null)
  assert.equal(cache.getSnapshot()['1'].status, 'ready')
  cache.request(1, 'b', true); await tick(); const error = { quota_5h: { state: 'error' } }
  requests[4].resolve({ stats: { 1: error } }); await tick()
  assert.equal(cache.getSnapshot()['1'].data, error)
})
test('an obsolete rejection cannot change a newer successful state', async () => {
  const { cache, requests } = fixture()
  cache.request(1, 'a'); await tick(); cache.request(1, 'b'); await tick()
  requests[1].resolve({ stats: { 1: next } }); await tick(); requests[0].reject(new Error('late')); await tick()
  assert.equal(cache.getSnapshot()['1'].status, 'ready')
  assert.equal(cache.getSnapshot()['1'].data, next)
})
test('native refresh key tracks source-equivalent updates but ignores live request counts', () => {
  const account = { id: 1, updated_at: 'one', last_used_at: 'two', usage_percent_5h: 30, reset_5h_at: 'old' }
  const key = accountQuotaRefreshKey(account)
  assert.equal(accountQuotaRefreshKey({ ...account, active_requests: 3 }), key)
  for (const patch of [{ updated_at: 'new' }, { last_used_at: 'new' }, { usage_percent_5h: 0 }, { reset_5h_at: 'new' }, { codex_usage_updated_at: 'new' }]) {
    assert.notEqual(accountQuotaRefreshKey({ ...account, ...patch }), key)
  }
})

test('manual official settlement patch stays separate from quota accounting and request age', async () => {
  const { cache, requests } = fixture()
  cache.request(1, 'a'); await tick(); requests[0].resolve({ stats: { 1: old } }); await tick()
  cache.patchOfficialCost(1, 99)
  assert.equal(cache.getSnapshot()['1'].data.quota_5h, old.quota_5h)
  assert.equal(cache.getSnapshot()['1'].data.official_usd, 99)
  cache.request(1, 'a'); await tick(); assert.equal(requests.length, 1)
  cache.patchOfficialCost(1, null)
  assert.equal(cache.getSnapshot()['1'].data.official_usd, undefined)
  assert.equal(cache.getSnapshot()['1'].data.official_usage_synced, true)
})
