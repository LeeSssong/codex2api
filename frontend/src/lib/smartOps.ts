export type ModelMapping = { from: string; to: string }
export type ModelBillingRule = { model: string; multiplier: number }
export type BPSDefaults = {
  ws_sse_acceleration: boolean; auto_enable_on_degradation: boolean; all_models: boolean;
  models: string[]; omit_unsupported_tools: boolean; ignore_encrypted_content: boolean;
  auto_disable_on_403: boolean; auto_recover_on_403: boolean; recovery_interval_minutes: number;
  auto_move_on_403: boolean; target_group_id: number; session_proxy: boolean; proxy_source: string;
  cache_creation_as_input: boolean;
}
export type Quality5xxConfig = { enabled: boolean; floor: number; cooldown_seconds: number; models: string[] }
export type OAuthAutoConfig = {
  quality_5xx?: Quality5xxConfig;
  enabled: boolean; platform: string; priority: number; load_factor: number; concurrency: number;
  group_ids: number[]; model_mappings: ModelMapping[]; upgrade_enabled: boolean; upgrade_group_ids: number[];
  successes_per_step: number; upgrade_step: number; max_concurrency: number; cooldown_seconds: number;
  revision: string; updated_at: string; bps: BPSDefaults;
  model_billing: { enabled: boolean; rules: ModelBillingRule[] };
}
export type PriorityConfig = {
  group_ids: number[]; models: string[]; quality_max_age_hours: number; balance_protocols: boolean;
  enabled: boolean; mode: string; window_minutes: number; min_samples: number; target_ttft_ms: number;
  max_load_percent: number; min_quality_percent: number; quality_weight: number; latency_weight: number;
  load_weight: number; cost_weight: number;
}
export type SmartOpsConfig = { oauth_auto_config: OAuthAutoConfig; priority_scheduling: PriorityConfig; plugins: Record<string, boolean> }
export type PelicanJob = {
  id?: number; account_id: number; group_ids: number[]; model: string; prompt: string; reasoning_effort: string;
  samples: number; parallel: number; retries: number; max_history: number;
}
export type PelicanResult = {
  job_id: number; account_id: number; sample: number; status: string; output: string; error: string;
  latency: number; first_content_ms?: number; input_tokens?: number; output_tokens?: number;
  started_at: string; finished_at: string;
  cost_usd?: number; cost_incomplete: boolean; attempts: { account_id: number; error: string; cost_usd?: number }[];
}
export type PelicanRecord = PelicanJob & { id: number; plan_id: number; status: string; results: PelicanResult[]; created_at: string; error?: string }
export type PelicanPlan = { id: number; name: string; enabled: boolean; interval_minutes: number; cron_expression: string; next_run_at: string; job: PelicanJob }
