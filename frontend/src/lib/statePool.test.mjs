import assert from 'node:assert/strict'
import test from 'node:test'
import { readStateCapturePreferences, STATE_MODEL_LABELS } from './statePool.ts'

test('capture preferences preserve deliberate selections without importing secrets', () => {
  const p = readStateCapturePreferences(JSON.stringify({
    accounts: [23], models: Object.keys(STATE_MODEL_LABELS), proxyIDs: [2], forwardProxyID: 1,
    routeMode: 'proxies', candidates: 3, strategy: 'race', distinctIPs: true, newSession: true,
    enable: false, strict: false, access_token: 'never-save', value: 'never-save',
  }))
  assert.equal(p.forwardProxyID, 1)
  assert.deepEqual(p.proxyIDs, [2])
  assert.equal(p.enable, false)
  assert.equal(p.strict, false)
  assert.equal(JSON.stringify(p).includes('never-save'), false)
  assert.deepEqual(readStateCapturePreferences(JSON.stringify(p)), p)
})

test('invalid stored settings cannot create loops or unsupported model selections', () => {
  assert.equal(readStateCapturePreferences('{broken'), undefined)
  assert.equal(readStateCapturePreferences('[]'), undefined)
  const p = readStateCapturePreferences(JSON.stringify({
    accounts: [23, 23, -1, '7', null], proxyIDs: [1, 2, 2], forwardProxyID: 1,
    models: ['gpt-5.6-sol', 'unknown', 'toString'], candidates: 999, strategy: 'unknown',
  }))
  assert.deepEqual(p.accounts, [23])
  assert.deepEqual(p.proxyIDs, [2])
  assert.deepEqual(p.models, ['gpt-5.6-sol'])
  assert.equal(p.candidates, 12)
  assert.equal(p.strategy, 'race')
})

test('empty saved selections remain empty', () => {
  const p = readStateCapturePreferences(JSON.stringify({ accounts: [], models: [], proxyIDs: [], routeMode: 'business' }))
  assert.deepEqual(p.accounts, [])
  assert.deepEqual(p.models, [])
  assert.deepEqual(p.proxyIDs, [])
  assert.equal(p.routeMode, 'business')
})
