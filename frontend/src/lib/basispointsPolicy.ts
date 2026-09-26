export type BpsScope = "inherit" | "all" | "selected";
export interface BasispointsPolicy {
  model_scope: BpsScope;
  models: string[];
  auto_disable_on_403: boolean;
  cache_creation_as_input: boolean;
  revision?: number;
  disabled_by?: string;
  disabled_reason?: string;
  disabled_at?: number;
}
export interface BasispointsSettings {
  enabled: boolean;
  model_scope: "all" | "selected";
  models: string[];
  image_relay_enabled: boolean;
  image_relay_public_origin: string;
  image_relay_epoch: number;
  image_relay_usage?: ImageRelayUsage | null;
  image_relay_runtime?: ImageRelayRuntime | null;
}
export function parseBpsModels(value: string): string[] {
  return [
    ...new Set(
      value
        .split(/[\s,]+/)
        .map((x) => x.trim())
        .filter(Boolean),
    ),
  ];
}
export function bpsPolicyPatch(
  model_scope: BpsScope,
  models: string,
  auto_disable_on_403: boolean,
  cache_creation_as_input: boolean,
): BasispointsPolicy {
  return {
    model_scope,
    models: parseBpsModels(models),
    auto_disable_on_403,
    cache_creation_as_input,
  };
}
function normalizedModels(models: string[]): string[] {
  return [...new Set(models.map((model) => model.trim().toLowerCase()).filter(Boolean))];
}
function modelPatternAllows(pattern: string, model: string): boolean {
  if (pattern === model) return true;
  if (!model.startsWith(pattern + "-")) return false;
  const suffix = model.slice(pattern.length + 1);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(suffix)) return false;
  const date = new Date(suffix + "T00:00:00.000Z");
  return !Number.isNaN(date.getTime()) && date.toISOString().slice(0, 10) === suffix;
}
export function bpsEffectiveModels(
  policy: Pick<BasispointsPolicy, "model_scope" | "models">,
  global: Pick<BasispointsSettings, "model_scope" | "models">,
): string[] | null {
  const globalModels = normalizedModels(global.models);
  if (policy.model_scope !== "selected")
    return global.model_scope === "all" ? null : globalModels;
  const accountModels = normalizedModels(policy.models);
  if (global.model_scope === "all") return accountModels;
  const intersection = new Set<string>();
  for (const account of accountModels) {
    for (const globalModel of globalModels) {
      if (modelPatternAllows(globalModel, account)) intersection.add(account);
      else if (modelPatternAllows(account, globalModel)) intersection.add(globalModel);
    }
  }
  return [...intersection];
}
export type BasispointsSettingsInput = Pick<
  BasispointsSettings,
  | "enabled"
  | "model_scope"
  | "models"
  | "image_relay_enabled"
  | "image_relay_public_origin"
>;
export function bpsSettingsUpdate(
  latest: BasispointsSettings,
  patch: Partial<BasispointsSettingsInput>,
): BasispointsSettingsInput {
  const {
    enabled,
    model_scope,
    models,
    image_relay_enabled,
    image_relay_public_origin,
  } = latest;
  return {
    enabled,
    model_scope,
    models,
    image_relay_enabled,
    image_relay_public_origin,
    ...patch,
  };
}
export interface ImageRelayUsage {
  bytes: number;
  assets: number;
  reserved_bytes: number;
  reserved_assets: number;
  cleanup_pending: number;
  rejections: number;
  cleanup_errors: number;
  max_bytes: number;
  max_assets: number;
}
export interface ImageRelayRuntime {
  signing_key_configured: boolean;
  backend: string;
  max_image_bytes: number;
  max_request_bytes: number;
  max_images: number;
  max_pixels: number;
  ttl_seconds: number;
  max_slots: number;
  active_slots: number;
  request_memory_limit_bytes: number;
  request_body_limit_bytes: number;
  source: "persisted" | "environment";
  validation_status:
    "disabled" | "configuration_incomplete" | "configured_unverified";
}
