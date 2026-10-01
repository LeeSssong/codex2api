import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { hasValidModelState } from './accountStateModels.ts'

test('green State requires an exact model and an unexpired valid saved token', () => {
  const states = [{ model: 'gpt-5.6-sol', valid: true, expires_at: 200, length: 332, refreshing: true }]
  assert.equal(hasValidModelState(states, 'gpt-5.6-sol', 199), true)
  assert.equal(hasValidModelState(states, 'gpt-5.6-sol', 200), false)
  assert.equal(hasValidModelState(states, 'gpt-5.6-terra', 100), false)
  assert.equal(hasValidModelState([{ ...states[0], valid: false }], 'gpt-5.6-sol', 100), false)
  assert.equal(hasValidModelState(undefined, 'gpt-5.6-sol', 100), false)
  assert.equal(hasValidModelState([{ ...states[0], in_scope: false }], 'gpt-5.6-sol', 100), false)
})

test('State filters reach paginated queries and bulk selectors; settings use a policy patch', () => {
  const accounts = readFileSync(new URL('../pages/Accounts.tsx', import.meta.url), 'utf8')
  assert.match(accounts, /state: stateFilter,/)
  assert.match(accounts, /stateModel: stateModelFilter,/)
  assert.match(accounts, /state_model: stateModelFilter \|\| undefined/)
  assert.match(accounts, /value=\{stateSummary\?\.reuse_accounts \?\? 0\}/)
  assert.match(accounts, /<StateCoverage summary=\{stateSummary\} target="state-pool" includeSaved/)
  const settings = readFileSync(new URL('../components/StatePolicySettings.tsx', import.meta.url), 'utf8')
  assert.match(settings, /<Switch/)
  assert.match(settings, /api\.configureStatePolicy\(checked\)/)
  for (const lang of ['zh', 'en', 'zh-TW']) {
    const { ipv6State, accounts: accountCopy } = JSON.parse(readFileSync(new URL(`../locales/${lang}.json`, import.meta.url), 'utf8'))
    for (const key of ['schedulableAccounts', 'filterSchedulable', 'usageSamplingIndependent']) assert.equal(typeof accountCopy[key], 'string')
    for (const key of ['requireValid', 'renewalHelp', 'stateFilter_valid', 'stateFilter_missing', 'modelValid', 'modelMissing']) assert.equal(typeof ipv6State[key], 'string')
  }
})
