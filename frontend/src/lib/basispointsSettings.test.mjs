import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

test('Basispoints uses the shared switch and autosaves the pool-wide setting', () => {
  const page = readFileSync(new URL('../pages/Settings.tsx', import.meta.url), 'utf8')
  assert.match(page, /codex_basispoints_enabled: false/)
  assert.match(page, /<Switch\s+aria-label=\{t\('settings.codexBasispointsEnabled'\)\}\s+checked=\{settingsForm.codex_basispoints_enabled\}/)
  assert.match(page, /autoSaveBooleanField\('codex_basispoints_enabled', checked\)/)
  for (const locale of ['zh', 'en', 'zh-TW']) {
    const { settings } = JSON.parse(readFileSync(new URL(`../locales/${locale}.json`, import.meta.url), 'utf8'))
    for (const key of ['codexBasispoints', 'codexBasispointsDesc', 'codexBasispointsEnabled', 'codexBasispointsEnabledDesc']) assert.ok(settings[key])
    assert.match(settings.codexBasispointsEnabledDesc, /max.*xhigh/)
  }
})
