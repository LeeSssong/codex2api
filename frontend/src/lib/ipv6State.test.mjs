import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { ipv6StateDisplayStatus, isIPv6StateReady, parseIPv6States, parseStateLengths, serializeIPv6States } from './ipv6State.ts'

test('capture lengths support multiple values, localized separators and deduplication', () => {
  assert.deepEqual(parseStateLengths('292, 332'), [292, 332])
  assert.deepEqual(parseStateLengths(' 332，292、332；312\n356 '), [332, 292, 312, 356])
  for (const value of ['', '292,', '0', '-292', '292.5', '3e2', 'abc', '8193', Array.from({ length: 33 }, (_, i) => i + 1).join(',')]) {
    assert.throws(() => parseStateLengths(value))
  }
})

test('capture settings use shared controls and translated labels', () => {
  const source = readFileSync(new URL('../components/IPv6StatePlugin.tsx', import.meta.url), 'utf8')
  assert.match(source, /<Input[^>]*id="ipv6-state-lengths"/)
  assert.match(source, /<DraftNumberInput[^>]*id="ipv6-state-concurrency"[^>]*max=\{20\}/)
  assert.match(source, /api\.configureIPv6State\(next\)/)
  for (const locale of ['zh', 'zh-TW', 'en']) {
    const { ipv6State } = JSON.parse(readFileSync(new URL(`../locales/${locale}.json`, import.meta.url), 'utf8'))
    for (const key of ['acceptedLengths', 'lengthsHelp', 'invalidLengths', 'concurrency', 'concurrencyHelp', 'accountLimit']) assert.equal(typeof ipv6State[key], 'string')
  }
})

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
