import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const keys = (v, prefix = '') => Object.entries(v).flatMap(([key, value]) => typeof value === 'object' ? keys(value, `${prefix}${key}.`) : `${prefix}${key}`).sort()
test('smart operations use shared controls and all three complete locale namespaces', () => {
  const baseline = JSON.parse(read('../locales/en.json')).smartOps
  for (const lang of ['zh', 'zh-TW']) assert.deepEqual(keys(JSON.parse(read(`../locales/${lang}.json`)).smartOps), keys(baseline))
  for (const name of ['AutoConfig', 'PriorityScheduling', 'PelicanTests']) {
    const page = read(`../pages/${name}.tsx`)
    assert.match(page, /useTranslation/)
    assert.doesNotMatch(page, /<(select|button|input)\b/)
    assert.match(page, /SmartOpsNumber/)
  }
  assert.match(read('../components/SmartOpsFields.tsx'), /DraftNumberInput/)
  assert.match(read('../pages/PelicanTests.tsx'), /getPelicanTest/)
  assert.match(read('../pages/PelicanTests.tsx'), /sandbox="allow-scripts"/)
  assert.doesNotMatch(read('../pages/PelicanTests.tsx'), /allow-same-origin|dangerouslySetInnerHTML/)
})
