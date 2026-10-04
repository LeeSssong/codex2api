export type ConcurrencyProgress = {
  revision: string
  concurrency: number
  successes: number
  required: number
  maximum: number
  step: number
  paused_until: string
}

export type ConcurrencyProgressDisplay = {
  successes: number
  required: number
  next: number
  capped: boolean
}

export function chunkConcurrencyProgressIDs(ids: number[], size = 200): number[][] {
  if (!Number.isFinite(size) || size < 1) size = 200
  const chunks: number[][] = []
  for (let index = 0; index < ids.length; index += Math.floor(size)) {
    chunks.push(ids.slice(index, index + Math.floor(size)))
  }
  return chunks
}

export function projectConcurrencyProgress(
  enabled: boolean,
  paused: boolean,
  state: ConcurrencyProgress | undefined,
): ConcurrencyProgress | null {
  return enabled && !paused && state ? state : null
}

export function formatConcurrencyProgress(
  state: ConcurrencyProgress,
): ConcurrencyProgressDisplay {
  const required = Math.max(1, Math.floor(state.required))
  const successes = Math.min(required, Math.max(0, Math.floor(state.successes)))
  const capped = state.concurrency >= state.maximum
  return {
    successes,
    required,
    next: capped ? state.maximum : Math.min(state.maximum, state.concurrency + state.step),
    capped,
  }
}
