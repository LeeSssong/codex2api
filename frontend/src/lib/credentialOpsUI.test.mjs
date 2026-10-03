import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const form = read('../components/TwoFAImport.tsx')
const page = read('../pages/CredentialOps.tsx')

test('credential forms use shared controls and expose native account import', () => {
  for (const source of [form, page]) {
    assert.doesNotMatch(source, /<select\b|type="checkbox"|<Input type="number"/)

  }
  assert.match(form, /<Switch\b/)
  assert.match(form, /<Select\b/)
  assert.match(page, /<DraftNumberInput\b/)
  const accounts = read('../pages/Accounts.tsx')
  assert.match(accounts, /accounts\.addMethodTwoFA/)
  assert.match(accounts, /<TwoFAImport accountId=\{0\}/)
  assert.match(form, /credentialOps\.loginSucceeded/)
  assert.match(form, /onSaved\?\.\(\)/)
  assert.match(page, /credential-ops\/rules/)
  assert.doesNotMatch(page, /selected\.enabled|selected\.auto_relogin/)
})

test('credential operations translations have matching keys in three languages', () => {
  function flatten(value, prefix = '') { return Object.entries(value).flatMap(([key, entry]) => typeof entry === 'object' ? flatten(entry, `${prefix}${key}.`) : [`${prefix}${key}`]).sort() }
  const resources = ['zh', 'en', 'zh-TW'].map(lang => JSON.parse(read(`../locales/${lang}.json`)))
  const expected = flatten(resources[0].credentialOps)
  for (const resource of resources) {
    assert.deepEqual(flatten(resource.credentialOps), expected)
    assert.equal(typeof resource.accounts.addMethodTwoFA, 'string')
    for (const match of `${form}\n${page}`.matchAll(/'credentialOps\.([^']+)'/g)) {
      if (match[1].includes('${')) continue
      assert.ok(expected.includes(match[1]), `missing credentialOps.${match[1]}`)
    }
  }
})
