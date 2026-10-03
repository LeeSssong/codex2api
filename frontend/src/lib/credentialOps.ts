import { getAdminKey } from '../api'
import i18n from '../i18n'

export interface CredentialConfig { account_id: number; email: string; mode: string; engine: string; proxy_source: string; password_configured: boolean; totp_configured: boolean; otp_url_configured: boolean }
export interface CredentialAccount { account_id: number; name: string; enabled: boolean; auto_relogin: boolean; probe_state: string; probe_detail: string; fail_streak: number; next_probe_at: string; cooldown_until: string | null; interval_seconds: number; failure_threshold: number; cooldown_seconds: number }
export interface CredentialJob { ID: number; AccountID: number; Status: string; Stage: string; Attempt: number; Error: string; CreatedAt: string }
export interface CredentialRules { retry_seconds: number; interval_seconds: number; failure_threshold: number; cooldown_seconds: number }
export interface CredentialOverview { rules: CredentialRules; login_configured: Record<string, boolean>; enabled: boolean; accounts: CredentialAccount[]; tasks: CredentialJob[]; session_studio_configured: boolean }

export async function credentialRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch('/api/admin' + path, { method, headers: { 'Content-Type': 'application/json', 'X-Admin-Key': getAdminKey() }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) { const payload = await response.json().catch(() => ({})); throw new Error(payload.error || i18n.t('credentialOps.requestFailed', { status: response.status })) }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
