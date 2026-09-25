import assert from 'node:assert/strict'
import test from 'node:test'
import { updateState } from './managedVersion.ts'

test('managed commit checks trust the backend rather than semver order', () => {
  assert.deepEqual(updateState({latest_version:'abcdef123456',has_update:false,mode:'source_image',check_status:'checked'}), {latestVersion:'abcdef123456',hasUpdate:false})
  assert.equal(updateState({latest_version:'000000000001',has_update:true,mode:'source_image',check_status:'checked'}).hasUpdate,true)
})
test('unknown checks never claim a confirmed latest version', () => {
  assert.deepEqual(updateState({latest_version:'',has_update:false,mode:'source_image',check_status:'unknown'}), {latestVersion:null,hasUpdate:false})
})
test('semantic release labels retain the version prefix', () => {
  assert.deepEqual(updateState({latest_version:'2.4.5',has_update:true,mode:'binary'}), {latestVersion:'v2.4.5',hasUpdate:true})
})
