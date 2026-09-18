export type IPv6StateConfig = {
  enabled: boolean; account_ids: number[]; models: string[]; source_ips: string[]; interval_seconds: number
  capture_mode: 'proxy' | 'local_ipv6'; proxy_ids: number[]; forward_proxy_id: number; new_session: boolean
}
export type IPv6StateEntry = {
  account_id: number; account_name: string; model: string; status: string; error?: string
  issued_at: number; expires_at: number; captured_at: number; source_ip?: string
  attempts: number; last_length: number; http_status: number; retry_at: number; fingerprint?: string
  proxy_id?: number; proxy_name?: string; session_id?: string
}
export type IPv6StateStatus = {
  config: IPv6StateConfig; entries: IPv6StateEntry[]; local_ips: string[]; running: boolean; error?: string; server_time: number
}
export type IPv6StatePackage = {
  format: string; member_hash: string; workspace_hash: string; model: string; value: string
}
