// Wire contract follows the native Sub2API TokenGuard configuration.
export interface TokenGuardReloginAccount {
  account_id: number;
  email: string;
  password: string;
  mfa_secret: string;
}
export interface TokenGuardConfig {
  enabled: boolean;
  group_ids: number[];
  interval_seconds: number;
  probe_endpoint: string;
  probe_model: string;
  probe_headers: Record<string, string>;
  probe_timeout_seconds: number;
  probe_concurrency: number;
  max_probe_per_cycle: number;
  auto_relogin: boolean;
  relogin_endpoint: string;
  relogin_headers: Record<string, string>;
  relogin_accounts: TokenGuardReloginAccount[];
  restore_schedulable: boolean;
  fail_streak_threshold: number;
  bark_key: string;
  notify_on_fix: boolean;
  notify_on_fail: boolean;
}
export interface TokenGuardAccountState {
  account_id: number;
  account_name: string;
  account_status: string;
  schedulable: boolean;
  probe_state: string;
  probe_detail: string;
  latency_ms: number;
  fail_streak: number;
  last_probe_at: string | null;
  last_fix_at: string | null;
  last_fix_action: string;
  last_fix_result: string;
  needs_relogin: boolean;
  updated_at: string;
}
export interface TokenGuardEvent {
  id: number;
  account_id: number;
  account_name: string;
  kind: string;
  detail: string;
  latency_ms: number;
  created_at: string;
}
export interface TokenGuardStats {
  probed: number;
  healthy: number;
  auth_failed: number;
  transient: number;
  repaired: number;
  state_fixed: number;
  failed: number;
  duration_ms: number;
  started_at: number;
}
export interface TokenGuardRuntime {
  running: boolean;
  last_run: string | null;
  last_message: string;
  stats: TokenGuardStats;
  job_id?: string;
  job_state?: string;
  cancellation?: boolean;
  cancel_requested?: boolean;
  cancellation_requested?: boolean;
  cancelled?: boolean;
}
export interface TokenGuardStatus {
  config: TokenGuardConfig;
  accounts: TokenGuardAccountState[];
  events: TokenGuardEvent[];
  runtime: TokenGuardRuntime;
  module_enabled: boolean;
}
export interface TokenGuardJob {
  job_id: string;
  state: string;
}
export class TokenGuardFormError extends Error {
  readonly field: "groups" | "headers" | "mappings";
  constructor(field: "groups" | "headers" | "mappings") {
    super(field);
    this.name = "TokenGuardFormError";
    this.field = field;
  }
}
export function parseTokenGuardGroupIDs(raw: string): number[] {
  const parts = raw.trim() ? raw.trim().split(/[,\s;]+/) : [];
  if (
    parts.some(
      (value) =>
        !/^\d+$/.test(value) ||
        !Number.isSafeInteger(Number(value)) ||
        Number(value) <= 0,
    )
  )
    throw new TokenGuardFormError("groups");
  return [...new Set(parts.map(Number))].sort((a, b) => a - b);
}
export function parseTokenGuardHeaders(raw: string): Record<string, string> {
  const result: Record<string, string> = {};
  const names = new Set<string>();
  for (const line of raw.split(/\r?\n/).filter((line) => line.trim())) {
    const colon = line.indexOf(":");
    const name = line.slice(0, colon).trim();
    if (
      colon <= 0 ||
      !/^[!#$%&'*+.^_a-z|~0-9-]+$/i.test(name) ||
      names.has(name.toLowerCase())
    )
      throw new TokenGuardFormError("headers");
    names.add(name.toLowerCase());
    Object.defineProperty(result, name, {
      value: line.slice(colon + 1).trim(),
      enumerable: true,
      configurable: true,
      writable: true,
    });
  }
  return result;
}
export function tokenGuardHeadersText(
  headers?: Record<string, string>,
): string {
  return Object.entries(headers ?? {})
    .map(([key, value]) => key + ": " + value)
    .join("\n");
}
export function collectTokenGuardConfig(
  config: TokenGuardConfig,
  groups: string,
  probeHeaders: string,
  reloginHeaders: string,
): TokenGuardConfig {
  const accountIDs = new Set<number>();
  for (const row of config.relogin_accounts) {
    if (
      !Number.isSafeInteger(row.account_id) ||
      row.account_id <= 0 ||
      !row.email.trim() ||
      accountIDs.has(row.account_id)
    )
      throw new TokenGuardFormError("mappings");
    accountIDs.add(row.account_id);
  }
  // Passwords, MFA values and masks are intentionally not trimmed or filtered.
  // Empty/masked/omitted secrets are preservation commands in the server contract.
  return {
    ...config,
    group_ids: parseTokenGuardGroupIDs(groups),
    probe_headers: parseTokenGuardHeaders(probeHeaders),
    relogin_headers: parseTokenGuardHeaders(reloginHeaders),
    relogin_accounts: config.relogin_accounts.map((row) => ({
      ...row,
      email: row.email.trim(),
    })),
  };
}
export function isTokenGuardBusy(
  runtime?: Pick<TokenGuardRuntime, "running" | "job_state">,
): boolean {
  return Boolean(
    runtime?.running ||
    ["queued", "running", "cancelling"].includes(runtime?.job_state ?? ""),
  );
}
