import assert from 'node:assert/strict'
import test from 'node:test'
import { codexProbeOutcome, readCodexProbeEvents } from './codexProbe.ts'

const result = { account_id: 12, upstream: 'basispoints', model: 'gpt-6-astra', level: 'tools', outcome: 'supported', capability: 'supported', basic_outcome: 'supported', tools_outcome: 'supported', http_status: 200, reported_status: 200, started_at: '2026-09-25T12:00:00Z', finished_at: '2026-09-25T12:00:01Z', duration_ms: 1000, attempts: 3 }
const wire = events => events.map(event => `event: ${event.type}\r\ndata: ${JSON.stringify(event)}\r\n\r\n`).join('')
const streamOf = text => new ReadableStream({ start(controller) { const bytes = new TextEncoder().encode(text); for (let i = 0; i < bytes.length; i += 3) controller.enqueue(bytes.slice(i, i + 3)); controller.close() } })

test('strict success requires BPS and complete evidence for the requested test level', () => {
  assert.equal(codexProbeOutcome(result), 'supported')
  for (const change of [{ upstream: 'codex' }, { finished_at: '' }, { basic_outcome: 'not_run' }, { tools_outcome: 'protocol_error' }]) assert.equal(codexProbeOutcome({ ...result, ...change }), 'protocol_error')
  assert.equal(codexProbeOutcome({ ...result, outcome: 'blocked', capability: 'supported', http_status: 200, reported_status: 403 }), 'blocked')
  assert.equal(codexProbeOutcome({ ...result, level: 'basic', tools_outcome: 'not_run' }), 'supported')
})

test('probe stream preserves split UTF-8 and emits completed account results', async () => {
  const events = [{ type: 'start', total: 1, completed: 0 }, { type: 'testing', total: 1, completed: 0, account_id: 12 }, { type: 'result', total: 1, completed: 1, result: { ...result, message: '完整通过' } }, { type: 'done', total: 1, completed: 1 }]
  const received = []
  const batch = await readCodexProbeEvents(streamOf(': heartbeat\r\n\r\n' + wire(events)), event => received.push(event))
  assert.deepEqual(received, events)
  assert.equal(batch.results[0].message, '完整通过')
  assert.equal(batch.completed, 1)
})

test('an HTTP-success stream that ends early fails without losing already received results', async () => {
  const received = []
  await assert.rejects(readCodexProbeEvents(streamOf(wire([{ type: 'result', total: 2, completed: 1, result }])), event => received.push(event)), /before completion/)
  assert.deepEqual(received[0].result, result)
})

test('canceling a running stream rejects instead of synthesizing unsupported capability', async () => {
  let controller
  const stream = new ReadableStream({ start(value) { controller = value } })
  const read = readCodexProbeEvents(stream, () => assert.fail('no result expected'))
  controller.error(new DOMException('Canceled', 'AbortError'))
  await assert.rejects(read, { name: 'AbortError' })
  assert.equal(stream.locked, false)
})
