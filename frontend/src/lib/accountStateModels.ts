export type AccountStateModel = { model: string; valid: boolean; expires_at: number; length: number; refreshing: boolean }

export function hasValidModelState(states: AccountStateModel[] | undefined, model: string, now: number): boolean {
  return states?.some(state => state.model === model && state.valid && state.expires_at > now) ?? false
}
