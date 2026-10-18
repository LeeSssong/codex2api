export type CodexProbeLevel = 'basic' | 'tools'
export type CodexProbeOutcome = 'supported' | 'blocked' | 'unsupported' | 'rate_limited' | 'unauthorized' | 'workspace_deactivated' | 'network_error' | 'protocol_error' | 'canceled' | 'skipped' | 'not_run' | 'error'

export interface CodexProbeResult {
  account_id: number
  upstream: string
  model: string
  level: CodexProbeLevel
  outcome: CodexProbeOutcome
  capability: 'unknown' | 'supported' | 'unsupported'
  basic_outcome?: CodexProbeOutcome
  tools_outcome?: CodexProbeOutcome
  http_status?: number
  reported_status?: number
  error_code?: string
  message?: string
  started_at: string
  finished_at: string
  duration_ms: number
  attempts: number
}

export interface CodexProbeBatch {
  results: CodexProbeResult[]
  total: number
  completed: number
}

export interface CodexProbeEvent {
  type: 'start' | 'testing' | 'result' | 'done'
  total: number
  completed: number
  account_id?: number
  result?: CodexProbeResult
  results?: CodexProbeResult[]
}

export function codexProbeOutcome(result: CodexProbeResult): CodexProbeOutcome {
  // A successful HTTP exchange alone does not prove a completed BPS response.
  if (result.outcome === 'supported' && (result.upstream !== 'basispoints' || !result.finished_at || result.basic_outcome !== 'supported' || (result.level === 'tools' && result.tools_outcome !== 'supported'))) return 'protocol_error'
  return result.outcome
}

export async function readCodexProbeEvents(stream: ReadableStream<Uint8Array>, onEvent: (event: CodexProbeEvent) => void): Promise<CodexProbeBatch> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  const results: CodexProbeResult[] = []
  let buffer = ''
  let final: CodexProbeBatch | undefined
  const dispatch = (frame: string) => {
    const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n')
    if (!data) return
    const event = JSON.parse(data) as CodexProbeEvent
    if (!['start', 'testing', 'result', 'done'].includes(event.type)) return
    if (event.type === 'result') {
      if (!event.result) throw new Error('Missing probe result')
      results.push(event.result)
    }
    if (event.type === 'done') final = { results: event.results ?? results, total: event.total, completed: event.completed }
    onEvent(event)
  }
  try {
    while (!final) {
      const { value, done } = await reader.read()
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true })
      buffer = buffer.replace(/\r\n/g, '\n')
      let boundary = buffer.indexOf('\n\n')
      while (boundary !== -1) {
        dispatch(buffer.slice(0, boundary))
        buffer = buffer.slice(boundary + 2)
        boundary = buffer.indexOf('\n\n')
      }
      if (done) break
    }
    if (!final) throw new Error('Probe stream ended before completion')
    return final
  } finally {
    await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}
