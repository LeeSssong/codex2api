import assert from 'node:assert/strict'
import test from 'node:test'
import { parseCredentialImport, credentialCombo, credentialImportProgress } from './credentialImport.ts'

test('first import preserves passwords and optional empty TOTP', () => {
  assert.deepEqual(parseCredentialImport('demo@example.invalid---- a password ----'), [
    { email: 'demo@example.invalid', password: ' a password ', totp_secret: '' },
  ])
})

test('first import accepts a normalized long-term TOTP secret', () => {
  assert.equal(parseCredentialImport(' demo@example.invalid ----password----jbsw y3dp ehpk 3pxp')[0].totp_secret, 'JBSWY3DPEHPK3PXP')
})

test('CSV import preserves quoted password delimiters and escaped quotes', () => {
  assert.equal(parseCredentialImport('demo@example.invalid,"word,""with quotes""",JBSWY3DPEHPK3PXP')[0].password, 'word,"with quotes"')
})

test('JSON allows passwords containing text separators or newlines', () => {
  const password = 'abc----def\nsecret'
  assert.equal(parseCredentialImport(JSON.stringify([{ email: 'demo@example.invalid', password, mfa_secret: 'JBSWY3DPEHPK3PXP' }]))[0].password, password)
})

test('duplicate addresses are rejected case-insensitively before login', () => {
  assert.throws(() => parseCredentialImport('demo@example.invalid----one\nDEMO@example.invalid----two'), /duplicate/)
})

test('six-digit verification codes are not accepted as TOTP secrets', () => {
  assert.throws(() => parseCredentialImport('demo@example.invalid----secret----123456'), /totp/)
})

test('invalid input errors do not contain account secrets', () => {
  assert.throws(() => parseCredentialImport('demo@example.invalid----very-private----extra----secret'), error => !error.message.includes('very-private') && !error.message.includes('demo@example.invalid'))
})

test('import enforces batch and input-size limits', () => {
  assert.throws(() => parseCredentialImport(Array.from({ length: 101 }, (_, i) => `demo${i}@example.invalid----pw`).join('\n')), /limit/)
  assert.throws(() => parseCredentialImport('x'.repeat(262145)), /limit/)
})

test('credential text export refuses ambiguous passwords', () => {
  assert.throws(() => credentialCombo({ email: 'demo@example.invalid', password: 'a----b', totp_secret: 'JBSWY3DPEHPK3PXP' }), /json/)
  assert.equal(credentialCombo({ email: 'demo@example.invalid', totp_secret: 'JBSWY3DPEHPK3PXP' }), 'demo@example.invalid--------JBSWY3DPEHPK3PXP')
})

test('import progress distinguishes login completion from queueing and failures', () => {
  assert.deepEqual(credentialImportProgress([{ Status: 'succeeded' }, { Status: 'queued' }, { Status: 'running' }, { Status: 'failed' }, { Status: 'cancelled' }]), { total: 5, succeeded: 1, active: 2, failed: 2 })
})
