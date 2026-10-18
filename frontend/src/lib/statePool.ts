export type StateCheck = {
  phase: string; http_status: number; terminal?: string; passed: boolean; answer?: string
  error?: string; duration_ms: number; input_tokens: number; output_tokens: number
  proxy_id?: number; proxy_name?: string; last_test_ip?: string; session_id?: string; forward_proxy_name?: string
}
export type StateEntry = {
  id: string; account_id: number; account_name: string; model: string; effort: string
  fingerprint: string; captured_at: number; expires_at: number; verified_at: number
  enabled: boolean; strict: boolean; source: string; status: string; checks: StateCheck[]
}
export type StateJob = {
  id: string; batch_id: string; account_id: number; account_name: string; model: string
  effort: string; status: string; phase: string; error?: string; created_at: number
  updated_at: number; retry_at?: number; checks: StateCheck[]
  group_id?: string; candidate?: number; candidates?: number; strategy?: string
  capture_proxy?: { id: number; name: string; last_test_ip?: string; session_id?: string; rotate_session?: boolean }
  forward_proxy?: { id: number; name: string }
}
export type StateImportPreview = { index: number; model: string; account_id?: number; account_name?: string; status: string; reason?: string; expires_at: number }
export type StatePackage = {
  format: string; version: number; exported_at: number
  states: { model: string; effort: string; value: string; fingerprint: string; captured_at: number; expires_at: number; member_hash: string; workspace_hash: string }[]
}
export type StatePoolData = {
  entries: StateEntry[]; jobs: StateJob[]
  accounts: { id: number; name: string; plan: string; available: boolean }[]
  proxies: { id: number; name: string; enabled: boolean; last_test_ip: string; test_status: string; supports_session_rotation: boolean }[]
  resin_enabled: boolean
  models: string[]; limits: { concurrency: number; per_account: number; per_proxy: number }; server_time: number
}
export const STATE_MODEL_LABELS: Record<string, string> = {
  'gpt-5.6-sol': 'Sol', 'gpt-5.6-terra': 'Terra', 'gpt-5.6-luna': 'Luna', 'gpt-6-astra': 'GPT-6 Astra',
}
export type StateCapturePreferences = {
  accounts: number[]; models: string[]; proxyIDs: number[]; forwardProxyID: number
  routeMode: 'business' | 'proxies'; candidates: number; strategy: 'race' | 'sequential'
  distinctIPs: boolean; newSession: boolean; enable: boolean; strict: boolean
}

export function readStateCapturePreferences(raw: string | null): StateCapturePreferences | undefined {
  try {
    const value: unknown = JSON.parse(raw ?? 'null')
    if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
    const p = value as Record<string, unknown>
    const ids = (input: unknown, max: number) => Array.isArray(input) ? [...new Set(input.filter((id): id is number => Number.isSafeInteger(id) && Number(id) > 0))].slice(0, max) : []
    const forwardProxyID = Number.isSafeInteger(p.forwardProxyID) && Number(p.forwardProxyID) > 0 ? Number(p.forwardProxyID) : 0
    return {
      accounts: ids(p.accounts, 128),
      models: Array.isArray(p.models) ? [...new Set(p.models.filter((model): model is string => typeof model === 'string' && Object.prototype.hasOwnProperty.call(STATE_MODEL_LABELS, model)))] : Object.keys(STATE_MODEL_LABELS),
      proxyIDs: ids(p.proxyIDs, 12).filter(id => id !== forwardProxyID), forwardProxyID,
      routeMode: p.routeMode === 'proxies' ? 'proxies' : 'business',
      candidates: typeof p.candidates === 'number' && Number.isFinite(p.candidates) ? Math.min(12, Math.max(1, Math.trunc(p.candidates))) : 3,
      strategy: p.strategy === 'sequential' ? 'sequential' : 'race',
      distinctIPs: p.distinctIPs !== false, newSession: p.newSession !== false,
      enable: p.enable !== false, strict: p.strict !== false,
    }
  } catch { return undefined }
}
export const isStateJobActive = (job: StateJob) => ['queued', 'running', 'cancelling'].includes(job.status)
export const groupStateJobs = (jobs: StateJob[]) => {
  const groups = new Map<string, StateJob[]>()
  for (const job of jobs) {
    const id = job.group_id || job.id
    groups.set(id, [...(groups.get(id) ?? []), job])
  }
  return [...groups.entries()].map(([id, candidates]) => {
    candidates.sort((a, b) => (a.candidate ?? 1) - (b.candidate ?? 1))
    const winner = candidates.find(job => job.status === 'completed')
    const active = candidates.some(isStateJobActive)
    const representative = winner ?? candidates.find(job => job.status === 'running') ?? candidates.find(isStateJobActive) ?? candidates.find(job => job.status === 'failed') ?? candidates[0]
    return { id, candidates, winner, active, representative }
  })
}
export const stateRemaining = (expiresAt: number, now: number) => {
  const seconds = Math.max(0, Math.floor(expiresAt - now))
  return `${Math.floor(seconds / 60).toString().padStart(2, '0')}:${(seconds % 60).toString().padStart(2, '0')}`
}
