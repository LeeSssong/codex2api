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
export function bpsEffectiveModels(
  policy: Pick<BasispointsPolicy, "model_scope" | "models">,
  global: Pick<BasispointsSettings, "model_scope" | "models">,
): string[] | null {
  if (policy.model_scope !== "selected")
    return global.model_scope === "all" ? null : global.models;
  return global.model_scope === "all"
    ? policy.models
    : policy.models.filter((model) => global.models.includes(model));
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
