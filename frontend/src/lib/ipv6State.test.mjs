import assert from 'node:assert/strict'
import test from 'node:test'
import { ipv6StateDisplayStatus, isIPv6StateReady, parseIPv6States, serializeIPv6States } from './ipv6State.ts'

const pack = { format: 'codex2api-ipv6-292-v1', member_hash: 'test-member', workspace_hash: 'test-workspace', model: 'gpt-5.6-sol', value: 'test-only-value' }

test('clipboard bundles preserve account, workspace, model, and exact state without local account IDs', () => {
  const states = [pack, { ...pack, model: 'gpt-5.6-terra' }, { ...pack, member_hash: 'other-member' }]
  assert.deepEqual(parseIPv6States(serializeIPv6States(states)), states)
  assert.deepEqual(parseIPv6States(JSON.stringify(pack)), [pack])
})

test('invalid or ambiguous imports are rejected before sending any state', () => {
  for (const text of ['gAAAAA', 'null', '[]', '{}', serializeIPv6States([]), serializeIPv6States([pack, pack]),
    JSON.stringify({ ...pack, format: 'codex2api-state-v1' }), JSON.stringify({ ...pack, member_hash: '' }),
    JSON.stringify({ ...pack, value: 292 }), serializeIPv6States(Array.from({ length: 513 }, (_, i) => ({ ...pack, member_hash: String(i) })))]) {
    assert.throws(() => parseIPv6States(text))
  }
})

test('expiry disables copy immediately, including the exact boundary', () => {
  const entry = { status: 'ready', expires_at: 200 }
  assert.equal(isIPv6StateReady(entry, 199), true)
  assert.equal(isIPv6StateReady(entry, 200), false)
  assert.equal(ipv6StateDisplayStatus(entry, 200, true), 'expired')
  assert.equal(ipv6StateDisplayStatus(entry, 200, false), 'expired')
  assert.equal(isIPv6StateReady({ ...entry, status: 'account_unavailable' }, 199), false)
})

test('disabling capture retains saved values without implying that pending requests will run', () => {
  assert.equal(ipv6StateDisplayStatus({ status: 'ready', expires_at: 200 }, 100, false), 'ready')
  assert.equal(ipv6StateDisplayStatus({ status: 'collecting', expires_at: 0 }, 100, false), 'paused')
  assert.equal(ipv6StateDisplayStatus({ status: 'waiting', expires_at: 0 }, 100, false), 'paused')
  assert.equal(ipv6StateDisplayStatus({ status: 'account_unavailable', expires_at: 0 }, 100, false), 'account_unavailable')
})
