import { request } from "../api";

export type CredentialMode = "password_totp" | "email_otp_url";
export type CredentialEngine = "inherit" | "local_worker" | "session_studio";
export type CredentialProfile = "codex" | "excel";
export interface CredentialConfig {
  account_id: number;
  email: string;
  mode: CredentialMode;
  engine: CredentialEngine;
  proxy_source: string;
  proxy_id?: number | null;
  password_configured: boolean;
  totp_configured: boolean;
  otp_url_configured: boolean;
  revision?: number;
}
export interface CredentialConfigInput {
  import_key?: string;
  name?: string;
  email?: string;
  mode?: CredentialMode;
  engine?: CredentialEngine;
  proxy_source?: string;
  proxy_id?: number | null;
  password?: string;
  totp_secret?: string;
  otp_url?: string;
  clear_password?: boolean;
  clear_totp?: boolean;
  clear_otp_url?: boolean;
  revision?: number;
  group_ids?: number[];
  oauth_profile?: CredentialProfile;
}
export interface CredentialAccount {
  account_id: number;
  name: string;
  email?: string;
  enabled: boolean;
  auto_relogin: boolean;
  probe_state: string;
  probe_detail: string;
  fail_streak: number;
  last_probe_at?: string | null;
  last_reauth_at?: string | null;
  next_probe_at: string;
  cooldown_until?: string | null;
  blocked_reason?: string;
  config?: CredentialConfig | null;
  excel_configured?: boolean;
  effective_engine?: string;
  effective_egress?: string;
}
export interface CredentialJob {
  ID: number;
  AccountID: number;
  Status: string;
  Stage: string;
  Attempt: number;
  Error: string;
  CreatedAt: string;
  OAuthProfile?: CredentialProfile;
  Kind?: string;
}
export interface CredentialRules {
  interval_seconds: number;
  retry_seconds: number;
  failure_threshold: number;
  cooldown_seconds: number;
}
export interface CredentialRuntimeSettings {
  enabled?: boolean;
  engine: CredentialEngine | "";
  worker_concurrency: number;
  session_studio_endpoint?: string;
  session_studio_configured?: boolean;
  session_studio_headers_configured?: boolean;
  session_studio_headers?: Record<string, string>;
}
export interface CredentialWorker {
  mode: string;
  state: string;
  reason?: string;
  version?: string;
  capabilities?: string[];
}
export interface CredentialEncryption {
  configured: boolean;
  source: string;
}
export interface CredentialOverview {
  enabled: boolean;
  accounts: CredentialAccount[];
  tasks: CredentialJob[];
  rules: CredentialRules;
  login_configured: Record<string, boolean>;
  session_studio_configured: boolean;
  runtime_settings: CredentialRuntimeSettings;
  worker: CredentialWorker;
  encryption: CredentialEncryption;
}
export interface CredentialRotation {
  task_id?: number;
  status: string;
  phase?: string;
  error_code?: string;
  candidate_available?: boolean;
  previous_available?: boolean;
  updated_at?: string;
}
export interface CredentialExport {
  email: string;
  password?: string;
  totp_secret: string;
  otpauth_uri?: string;
}
export function credentialRequest<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  return request<T>(path, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
}
export const credentialAPI = {
  overview: (signal?: AbortSignal) =>
    credentialRequest<CredentialOverview>(
      "/credential-ops",
      "GET",
      undefined,
      signal,
    ),
  encryption: () =>
    credentialRequest<CredentialEncryption>("/credential-ops/encryption"),
  initialize: () =>
    credentialRequest<CredentialEncryption>(
      "/credential-ops/encryption/initialize",
      "POST",
    ),
  import: (input: CredentialConfigInput) =>
    credentialRequest<CredentialJob>("/credential-ops/import", "POST", input),
  config: (id: number) =>
    credentialRequest<CredentialConfig>(
      `/accounts/${id}/credential-ops/config`,
    ),
  saveConfig: (id: number, input: CredentialConfigInput) =>
    credentialRequest<CredentialConfig>(
      `/accounts/${id}/credential-ops/config`,
      "PUT",
      input,
    ),
  login: (id: number, input: CredentialConfigInput = {}) =>
    credentialRequest<CredentialJob>(
      `/accounts/${id}/credential-ops/login`,
      "POST",
      input,
    ),
  job: (id: number) =>
    credentialRequest<CredentialJob>(`/credential-ops/login/${id}`),
  cancel: (id: number) =>
    credentialRequest<void>(`/credential-ops/login/${id}`, "DELETE"),
  retry: (id: number) =>
    credentialRequest<CredentialJob>(
      `/credential-ops/login/${id}/retry`,
      "POST",
    ),
  rules: (input: CredentialRules) =>
    credentialRequest<CredentialRules>("/credential-ops/rules", "PUT", input),
  runtime: (input: CredentialRuntimeSettings) =>
    credentialRequest<CredentialRuntimeSettings>(
      "/credential-ops/runtime",
      "PUT",
      input,
    ),
  monitor: (id: number, input: { enabled: boolean; auto_relogin: boolean }) =>
    credentialRequest<void>(
      `/accounts/${id}/credential-ops/monitor`,
      "PUT",
      input,
    ),
  remove: (id: number) =>
    credentialRequest<void>(`/accounts/${id}/credential-ops/monitor`, "DELETE"),
  probe: (id: number) =>
    credentialRequest<void>(`/accounts/${id}/credential-ops/probe`, "POST"),
  rotation: (id: number) =>
    credentialRequest<CredentialRotation>(
      `/accounts/${id}/credential-ops/totp`,
    ),
  rotate: (id: number) =>
    credentialRequest<CredentialRotation>(
      `/accounts/${id}/credential-ops/totp`,
      "POST",
    ),
  verify: (id: number, action: "verify_new" | "verify_old") =>
    credentialRequest<CredentialRotation>(
      `/accounts/${id}/credential-ops/totp/verify`,
      "POST",
      { action },
    ),
  export: (id: number, source: string, include_password: boolean) =>
    credentialRequest<CredentialExport>(
      `/accounts/${id}/credential-ops/totp/export`,
      "POST",
      { source, include_password },
    ),
};
