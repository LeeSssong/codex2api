export type AccountStateModel = {
  model: string; valid: boolean; expires_at: number; length: number; refreshing: boolean
  in_scope?: boolean; available?: boolean; capture_phase?: string; retry_at?: number
  updated_at?: number; restriction?: string; restriction_until?: number
}

export type StateSummary = {
  enabled: boolean; require_valid_state: boolean; total_accounts: number
  reuse_accounts: number; available_accounts: number; valid_combinations: number
  covered_models: number; models: { model: string; reuse_accounts: number; available_accounts: number }[]
  revision: string; next_expiry: number
}

export function hasValidModelState(states: AccountStateModel[] | undefined, model: string, now: number): boolean {
  return states?.some(state => state.model === model && state.in_scope !== false && state.valid && state.expires_at > now) ?? false
}
