export type IPv6StateConfig = {
  enabled: boolean; account_ids: number[]; models: string[]; source_ips: string[]; interval_seconds: number
  capture_mode: 'proxy' | 'local_ipv6'; proxy_ids: number[]; forward_proxy_id: number; new_session: boolean
  accepted_lengths: number[]; concurrency: number
}
export type IPv6StateEntry = {
  account_id: number; account_name: string; model: string; status: string; error?: string
  issued_at: number; expires_at: number; captured_at: number; source_ip?: string
  attempts: number; last_length: number; http_status: number; retry_at: number; fingerprint?: string
  proxy_id?: number; proxy_name?: string; session_id?: string
}
export type IPv6StateStatus = {
  config: IPv6StateConfig; entries: IPv6StateEntry[]; local_ips: string[]; running: boolean; error?: string; server_time: number
  active_requests: number; account_concurrency: number
}
export type IPv6StatePackage = {
  format: string; member_hash: string; workspace_hash: string; model: string; value: string
}

export function parseStateLengths(text: string): number[] {
  const parts = text.trim().split(/[\s,，、;；]+/)
  if (!parts.length || parts.some(part => !/^\d+$/.test(part))) throw new Error('invalid_lengths')
  const lengths = [...new Set(parts.map(Number))]
  if (lengths.length > 32 || lengths.some(length => !Number.isSafeInteger(length) || length < 1 || length > 8192)) throw new Error('invalid_lengths')
  return lengths
}

export function isIPv6StateReady(entry: IPv6StateEntry, now: number): boolean {
  return entry.status === 'ready' && entry.expires_at > now
}

export function ipv6StateDisplayStatus(entry: IPv6StateEntry, now: number, enabled: boolean): string {
  if (isIPv6StateReady(entry, now)) return 'ready'
  if (entry.status === 'account_unavailable') return entry.status
  if (entry.status === 'collecting' && enabled) return entry.status
  if (entry.expires_at > 0 && entry.expires_at <= now) return 'expired'
  if (!enabled) return 'paused'
  return entry.status
}

export function serializeIPv6States(states: IPv6StatePackage[]): string {
  return JSON.stringify({ format: 'codex2api-ipv6-292-bundle-v1', states })
}

// Accept existing single-state packages as well as the multi-account clipboard format.
export function parseIPv6States(text: string): IPv6StatePackage[] {
  const input: unknown = JSON.parse(text)
  if (!input || typeof input !== 'object') throw new Error('invalid_package')
  const pack = input as Record<string, unknown>
  const items = pack.format === 'codex2api-ipv6-292-bundle-v1' ? pack.states : [pack]
  if (!Array.isArray(items) || !items.length || items.length > 512) throw new Error('invalid_package')
  const seen = new Set<string>()
  for (const item of items) {
    if (!item || typeof item !== 'object' || item.format !== 'codex2api-ipv6-292-v1' ||
      !['member_hash', 'workspace_hash', 'model', 'value'].every(key => typeof item[key] === 'string' && item[key].trim())) {
      throw new Error('invalid_package')
    }
    const key = JSON.stringify([item.member_hash, item.workspace_hash, item.model])
    if (seen.has(key)) throw new Error('duplicate_package')
    seen.add(key)
  }
  return items as IPv6StatePackage[]
}
